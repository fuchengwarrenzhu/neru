package accessibility_test

import (
	"context"
	"image"
	"slices"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/y3owk1n/neru/internal/adapter/accessibility"
	"github.com/y3owk1n/neru/internal/adapter/accessibility/ax"
	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain/element"
	"github.com/y3owk1n/neru/internal/ports"
)

// leaseApp is the application the lease tests scan, and leaseWindow its
// window.
const (
	leaseApp    = "com.example.Session"
	leaseWindow = "app"
)

// namedWindow is a window no other compares equal to, so ClickableNodes can
// answer for each one separately.
type namedWindow struct{ name string }

func (w *namedWindow) Release()     {}
func (w *namedWindow) Role() string { return "AXWindow" }

// leaseNode is a clickable button whose Retain records into handles.
func leaseNode(
	id string,
	bounds image.Rectangle,
	handles *accessibility.MockHandles,
) *accessibility.MockNode {
	return &accessibility.MockNode{
		MockID:        id,
		MockBounds:    bounds,
		MockRole:      axButtonRole,
		MockClickable: true,
		MockHandles:   handles,
	}
}

// newLeaseAdapter builds an adapter whose leaseApp has one window holding
// nodes.
func newLeaseAdapter(nodes ...ax.Node) *accessibility.Adapter {
	window := &namedWindow{name: leaseWindow}
	client := &accessibility.MockAXClient{
		MockApps: map[string]ax.App{
			leaseApp: &accessibility.MockApp{MockBundleID: leaseApp},
		},
		MockAppWindows:  []ax.Window{window},
		MockWindowNodes: map[ax.Element][]ax.Node{window: nodes},
	}

	return accessibility.NewAdapter(zap.NewNop(), client, false)
}

// waitFor fails the test when ch is not closed in time; a bare receive would
// turn the regression under test into a hang of the whole binary.
func waitFor(t *testing.T, ch <-chan struct{}, failure string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal(failure)
	}
}

// TestAdapter_LeaseClickableElements_HoldsOnlyTheAppsWindowElements pins what a
// lease holds: the elements of the named application's windows that pass the
// filter. Not the focused application's window, which is Neru's own search
// panel while it is open; not a supplementary surface, which is returned but
// not held; and not an element the filter dropped, whose reference is never
// taken.
func TestAdapter_LeaseClickableElements_HoldsOnlyTheAppsWindowElements(t *testing.T) {
	ctx := context.Background()
	handles := &accessibility.MockHandles{
		// A live frame unlike the scanned one shows Resolve answers from the
		// reference rather than from the element's copy.
		ResolveFunc: func(node *accessibility.MockNode, _ time.Duration) (image.Rectangle, error) {
			return node.MockBounds.Add(image.Pt(5, 7)), nil
		},
	}

	focusedWindow := &namedWindow{name: "focused"}
	appWindow := &namedWindow{name: leaseWindow}
	client := &accessibility.MockAXClient{
		MockFrontmostWindow: focusedWindow,
		MockApps: map[string]ax.App{
			leaseApp: &accessibility.MockApp{MockBundleID: leaseApp},
		},
		MockAppWindows: []ax.Window{appWindow},
		MockWindowNodes: map[ax.Element][]ax.Node{
			focusedWindow: {leaseNode("search-field", image.Rect(0, 0, 200, 30), handles)},
			appWindow: {
				leaseNode("save", image.Rect(10, 10, 110, 40), handles),
				leaseNode("tiny", image.Rect(0, 0, 4, 4), handles),
			},
		},
		MockMenuBarNodes: []ax.Node{leaseNode("menu", image.Rect(0, 0, 60, 22), handles)},
	}
	adapter := accessibility.NewAdapter(zap.NewNop(), client, false)

	filter := ports.DefaultElementFilter()
	filter.MinSize = image.Pt(10, 10)
	filter.IncludeMenubar = true

	elements, lease, err := adapter.LeaseClickableElements(ctx, leaseApp, filter)
	if err != nil {
		t.Fatalf("LeaseClickableElements() error = %v", err)
	}

	t.Cleanup(lease.Release)

	if got, want := idsOf(elements), []string{"menu", "save"}; !slices.Equal(got, want) {
		t.Fatalf("LeaseClickableElements() returned %v, want %v", got, want)
	}

	byID := make(map[string]*element.Element, len(elements))
	for _, elem := range elements {
		byID[string(elem.ID())] = elem
	}

	if !lease.Holds(byID["save"]) {
		t.Error("lease does not hold the app window's element")
	}

	if lease.Holds(byID["menu"]) {
		t.Error("lease holds a menu bar element, which is not a window element")
	}

	retained := handles.Handles()
	if len(retained) != 1 || retained[0].Node.MockID != "save" {
		ids := make([]string, 0, len(retained))
		for _, handle := range retained {
			ids = append(ids, handle.Node.MockID)
		}

		t.Fatalf("references retained for %v, want only [save]", ids)
	}

	frame, err := lease.Resolve(ctx, byID["save"])
	if err != nil {
		t.Fatalf("Resolve(held) error = %v", err)
	}

	if want := image.Rect(15, 17, 115, 47); frame != want {
		t.Errorf("Resolve(held) = %v, want the reference's live frame %v", frame, want)
	}

	_, err = lease.Resolve(ctx, byID["menu"])
	if !derrors.IsCode(err, derrors.CodeElementNotFound) {
		t.Errorf("Resolve(not held) error = %v, want %s", err, derrors.CodeElementNotFound)
	}
}

// TestAdapter_LeaseClickableElements_FailsWithoutALease pins the refusals: a
// client that cannot read a named application's windows says so with
// CodeNotSupported, so a caller can tell it from a failure, and an application
// that is not running fails the scan. Neither returns a lease.
func TestAdapter_LeaseClickableElements_FailsWithoutALease(t *testing.T) {
	tests := []struct {
		name     string
		client   func(*accessibility.MockAXClient) ax.Client
		app      string
		wantCode derrors.Code
	}{
		{
			name: "client cannot read a named application's windows",
			client: func(mock *accessibility.MockAXClient) ax.Client {
				return struct{ ax.Client }{mock}
			},
			app:      leaseApp,
			wantCode: derrors.CodeNotSupported,
		},
		{
			name:     "application is not running",
			client:   func(mock *accessibility.MockAXClient) ax.Client { return mock },
			app:      "com.example.NotRunning",
			wantCode: derrors.CodeAccessibilityFailed,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			window := &namedWindow{name: leaseWindow}
			mock := &accessibility.MockAXClient{
				MockApps: map[string]ax.App{
					leaseApp: &accessibility.MockApp{MockBundleID: leaseApp},
				},
				MockAppWindows: []ax.Window{window},
			}
			adapter := accessibility.NewAdapter(zap.NewNop(), testCase.client(mock), false)

			elements, lease, err := adapter.LeaseClickableElements(
				context.Background(),
				testCase.app,
				ports.DefaultElementFilter(),
			)
			if !derrors.IsCode(err, testCase.wantCode) {
				t.Fatalf("LeaseClickableElements() error = %v, want %s", err, testCase.wantCode)
			}

			if elements != nil || lease != nil {
				t.Errorf("LeaseClickableElements() = %v, %v alongside its error, want nil, nil",
					elements, lease)
			}
		})
	}
}

// blockingNode holds the window scan inside its conversion until resumed, so a
// test can end the scan's context while the window is still being read.
type blockingNode struct {
	*accessibility.MockNode

	reading  chan struct{} // closed when the scan reaches the node
	resume   chan struct{} // close to let the scan go on
	released chan struct{} // closed when the scan releases the node
}

func (n *blockingNode) Bounds() image.Rectangle {
	close(n.reading)
	<-n.resume

	return n.MockNode.Bounds()
}

func (n *blockingNode) Release() { close(n.released) }

// TestAdapter_LeaseClickableElements_GivesBackWhatACanceledScanHeld pins the
// cancel path: the scan returns no lease, the reference it already held is
// released, and a window scan still running after the collection gave up
// releases what it goes on to hold at once instead of keeping it in the lease
// nobody owns.
func TestAdapter_LeaseClickableElements_GivesBackWhatACanceledScanHeld(t *testing.T) {
	handles := &accessibility.MockHandles{}
	late := &blockingNode{
		MockNode: leaseNode("late", image.Rect(0, 40, 100, 70), handles),
		reading:  make(chan struct{}),
		resume:   make(chan struct{}),
		released: make(chan struct{}),
	}
	adapter := newLeaseAdapter(leaseNode("first", image.Rect(0, 0, 100, 30), handles), late)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		elements []*element.Element
		lease    ports.ElementLease
		err      error
	}

	done := make(chan result, 1)

	go func() {
		elements, lease, err := adapter.LeaseClickableElements(
			ctx,
			leaseApp,
			ports.DefaultElementFilter(),
		)
		done <- result{elements: elements, lease: lease, err: err}
	}()

	waitFor(t, late.reading, "the window scan never reached the second node")
	cancel()

	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("LeaseClickableElements did not return after its context was canceled")
	}

	if res.err == nil || res.lease != nil || res.elements != nil {
		t.Fatalf("LeaseClickableElements() = %v, %v, %v after cancel, want nil, nil and an error",
			res.elements, res.lease, res.err)
	}

	if held := handles.Handles(); len(held) != 1 || held[0].Releases() != 1 {
		t.Fatalf(
			"before the scan finished: %d references retained, want 1 released once",
			len(held),
		)
	}

	close(late.resume)
	waitFor(t, late.released, "the window scan never finished the second node")

	held := handles.Handles()
	if len(held) != 2 {
		t.Fatalf("%d references retained, want 2: the late node was never held", len(held))
	}

	for _, handle := range held {
		if handle.Releases() != 1 {
			t.Errorf(
				"reference for %s released %d times, want 1",
				handle.Node.MockID,
				handle.Releases(),
			)
		}
	}
}

// TestElementLease_Release_GivesEachReferenceBackOnce pins the end of a lease:
// every reference is released exactly once however often Release is called,
// and the lease holds and resolves nothing afterwards.
func TestElementLease_Release_GivesEachReferenceBackOnce(t *testing.T) {
	ctx := context.Background()
	handles := &accessibility.MockHandles{}
	adapter := newLeaseAdapter(
		leaseNode("save", image.Rect(0, 0, 100, 30), handles),
		leaseNode("cancel", image.Rect(0, 40, 100, 70), handles),
	)

	elements, lease, err := adapter.LeaseClickableElements(
		ctx,
		leaseApp,
		ports.DefaultElementFilter(),
	)
	if err != nil {
		t.Fatalf("LeaseClickableElements() error = %v", err)
	}

	lease.Release()
	lease.Release()

	held := handles.Handles()
	if len(held) != len(elements) || len(held) == 0 {
		t.Fatalf("%d references retained for %d elements", len(held), len(elements))
	}

	for _, handle := range held {
		if handle.Releases() != 1 {
			t.Errorf(
				"reference for %s released %d times, want 1",
				handle.Node.MockID,
				handle.Releases(),
			)
		}
	}

	for _, elem := range elements {
		if lease.Holds(elem) {
			t.Errorf("released lease still holds %s", elem.ID())
		}

		_, resolveErr := lease.Resolve(ctx, elem)
		if !derrors.IsCode(resolveErr, derrors.CodeElementNotFound) {
			t.Errorf("Resolve(%s) after Release error = %v, want %s",
				elem.ID(), resolveErr, derrors.CodeElementNotFound)
		}
	}
}

// TestElementLease_Release_WaitsForAResolveInProgress pins what keeps a
// reference alive under a read of it: Release gives nothing back until a
// Resolve in progress has finished.
func TestElementLease_Release_WaitsForAResolveInProgress(t *testing.T) {
	ctx := context.Background()
	resolving := make(chan struct{})
	finish := make(chan struct{})
	handles := &accessibility.MockHandles{
		ResolveFunc: func(node *accessibility.MockNode, _ time.Duration) (image.Rectangle, error) {
			close(resolving)
			<-finish

			return node.MockBounds, nil
		},
	}
	adapter := newLeaseAdapter(leaseNode("save", image.Rect(0, 0, 100, 30), handles))

	elements, lease, err := adapter.LeaseClickableElements(
		ctx,
		leaseApp,
		ports.DefaultElementFilter(),
	)
	if err != nil || len(elements) != 1 {
		t.Fatalf(
			"LeaseClickableElements() = %d elements, error %v; want 1 element",
			len(elements),
			err,
		)
	}

	resolved := make(chan error, 1)

	go func() {
		_, resolveErr := lease.Resolve(ctx, elements[0])
		resolved <- resolveErr
	}()

	waitFor(t, resolving, "Resolve never reached the reference")

	released := make(chan struct{})

	go func() {
		lease.Release()
		close(released)
	}()

	select {
	case <-released:
		t.Fatal("Release returned while a Resolve was still reading the reference")
	case <-time.After(50 * time.Millisecond):
	}

	handle := handles.Handles()[0]
	if handle.Releases() != 0 {
		t.Fatal("Release gave the reference back while a Resolve was still reading it")
	}

	close(finish)

	select {
	case resolveErr := <-resolved:
		if resolveErr != nil {
			t.Fatalf("Resolve() error = %v", resolveErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Resolve never returned")
	}

	waitFor(t, released, "Release never returned after the Resolve finished")

	if handle.Releases() != 1 {
		t.Errorf("reference released %d times, want 1", handle.Releases())
	}
}

// TestElementLease_Resolve_BoundsTheReadByTheContext pins the budget Resolve
// hands the native read, which nothing can interrupt once it starts: what is
// left of the context, and never more than the lease's one-second ceiling.
func TestElementLease_Resolve_BoundsTheReadByTheContext(t *testing.T) {
	const ceiling = time.Second

	tests := []struct {
		name    string
		timeout time.Duration // zero for a context with no deadline
		wantMin time.Duration
		wantMax time.Duration
	}{
		{name: "no deadline", wantMin: ceiling, wantMax: ceiling},
		{
			name:    "a deadline past the ceiling",
			timeout: time.Minute,
			wantMin: ceiling,
			wantMax: ceiling,
		},
		{
			name:    "a deadline before the ceiling",
			timeout: 200 * time.Millisecond,
			wantMin: 100 * time.Millisecond,
			wantMax: 200 * time.Millisecond,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var budget time.Duration

			handles := &accessibility.MockHandles{
				ResolveFunc: func(node *accessibility.MockNode, b time.Duration) (image.Rectangle, error) {
					budget = b

					return node.MockBounds, nil
				},
			}
			adapter := newLeaseAdapter(leaseNode("save", image.Rect(0, 0, 100, 30), handles))

			elements, lease, err := adapter.LeaseClickableElements(
				context.Background(),
				leaseApp,
				ports.DefaultElementFilter(),
			)
			if err != nil || len(elements) != 1 {
				t.Fatalf("LeaseClickableElements() = %d elements, error %v; want 1 element",
					len(elements), err)
			}

			t.Cleanup(lease.Release)

			ctx := context.Background()
			if testCase.timeout > 0 {
				var cancel context.CancelFunc

				ctx, cancel = context.WithTimeout(ctx, testCase.timeout)
				defer cancel()
			}

			_, err = lease.Resolve(ctx, elements[0])
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}

			if budget < testCase.wantMin || budget > testCase.wantMax {
				t.Errorf("Resolve() gave the read %v, want between %v and %v",
					budget, testCase.wantMin, testCase.wantMax)
			}
		})
	}
}
