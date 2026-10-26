package modes

import (
	"context"
	"image"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/y3owk1n/neru/internal/app/components"
	hintscomponent "github.com/y3owk1n/neru/internal/app/components/hints"
	"github.com/y3owk1n/neru/internal/app/components/scroll"
	"github.com/y3owk1n/neru/internal/app/services"
	"github.com/y3owk1n/neru/internal/config"
	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain"
	"github.com/y3owk1n/neru/internal/domain/element"
	domainhint "github.com/y3owk1n/neru/internal/domain/hint"
	"github.com/y3owk1n/neru/internal/domain/modecmd"
	"github.com/y3owk1n/neru/internal/domain/semantic"
	"github.com/y3owk1n/neru/internal/domain/state"
	"github.com/y3owk1n/neru/internal/ports"
	portmocks "github.com/y3owk1n/neru/internal/ports/mocks"
)

// semanticQuery is what the user types; semanticWaitBudget bounds every wait
// for the answer goroutine to come back through the lock.
const (
	semanticQuery      = "open the code menu"
	semanticWaitBudget = 5 * time.Second
	semanticAppID      = "com.example.Editor"
	otherAppID         = "com.example.Mail"
	// codeTitle is the title of the hint the tests describe.
	codeTitle = "Code"
)

// The two input paths a search can take, and the log line a failed selection
// writes.
const (
	pathSearchField = "search field"
	pathKeyStream   = "key stream"
	logFailed       = "Semantic selection failed"
)

// selectorAnswer is what the gated selector returns for one question.
type selectorAnswer struct {
	selection semantic.Selection
	err       error
}

// semanticFixture is a handler sitting in hints mode with three hints and a
// --semantic search open, over a selector that answers only when the test
// says so.
type semanticFixture struct {
	handler  *Handler
	overlay  *portmocks.MockOverlayPort
	field    *portmocks.MockTextInputPort
	selector *portmocks.MockTargetSelector
	logs     *observer.ObservedLogs
	hints    []*domainhint.Interface

	asked   chan struct{}
	answers chan selectorAnswer

	movesMu sync.Mutex
	moves   []image.Point

	// pageMu guards what the next scan reads and what each scan leased.
	pageMu sync.Mutex
	// unheld are the IDs of the elements a scan returns but does not lease,
	// the way the Dock or the menu bar comes back.
	unheld map[element.ID]bool
	// page is what the next scan reads, when it differs from the hints on
	// screen; nil reads the elements of the hints on screen.
	page []*element.Element
	// scanErr fails the next scan.
	scanErr error
	// resolve says where a leased element is read again; nil reads it where
	// it was scanned. resolveCtx does the same with the read's context.
	resolve    func(*element.Element) (image.Rectangle, error)
	resolveCtx func(context.Context, *element.Element) (image.Rectangle, error)
	// gate, when set, holds every scan until the test sends on it, after
	// announcing it on scanning.
	gate     chan struct{}
	scanning chan struct{}
	leases   []*portmocks.MockElementLease
	scans    []string
}

func semanticElement(t *testing.T, id, title string, bounds image.Rectangle) *element.Element {
	t.Helper()

	elem, err := element.NewElement(
		element.ID(id),
		bounds,
		element.RoleButton,
		element.WithTitle(title),
		element.WithDescription(title+" description"),
		element.WithValue("draft the user typed"),
		element.WithSubrole("AXToolbarButton"),
		element.WithContext("AXToolbar", "Repository"),
		element.WithHasPopup(true),
	)
	if err != nil {
		t.Fatalf("NewElement() error = %v", err)
	}

	return elem
}

// newSemanticFixture opens a --semantic search. field says whether the
// platform's search field takes the keys; without one they arrive through the
// key stream.
func newSemanticFixture(t *testing.T, field bool) *semanticFixture {
	t.Helper()

	core, logs := observer.New(zapcore.DebugLevel)

	fixture := &semanticFixture{
		overlay: &portmocks.MockOverlayPort{
			HintSearchBoundsFunc: func(image.Rectangle) image.Rectangle {
				return image.Rect(400, 740, 600, 780)
			},
		},
		field:    &portmocks.MockTextInputPort{Started: field},
		logs:     logs,
		asked:    make(chan struct{}, 8),
		answers:  make(chan selectorAnswer),
		unheld:   map[element.ID]bool{},
		scanning: make(chan struct{}, 8),
	}

	generator, generatorErr := domainhint.NewAlphabetGenerator(
		"asdf",
		domainhint.LabelDirectionNormal,
	)
	if generatorErr != nil {
		t.Fatalf("NewAlphabetGenerator() error = %v", generatorErr)
	}

	accessibility := &portmocks.MockAccessibilityPort{
		LeaseClickableElementsFunc: fixture.leaseScan,
	}

	fixture.selector = &portmocks.MockTargetSelector{
		SelectFunc: func(
			_ context.Context,
			_ string,
			_ []semantic.Candidate,
		) (semantic.Selection, error) {
			fixture.asked <- struct{}{}

			answer, open := <-fixture.answers
			if !open {
				return semantic.Selection{}, derrors.New(derrors.CodeContextCanceled, "test over")
			}

			return answer.selection, answer.err
		},
	}

	t.Cleanup(func() { close(fixture.answers) })

	appState := state.NewAppState()
	appState.SetMode(domain.ModeHints)

	fixture.handler = newHandlerWithState(handlerState{
		ctx:         context.Background(),
		config:      config.DefaultConfig(),
		logger:      zap.New(core),
		appState:    appState,
		cursorState: state.NewCursorState(),
		scroll:      &components.ScrollComponent{Context: &scroll.Context{}},
		hints: &components.HintsComponent{
			Context: &hintscomponent.Context{},
		},
		overlayPort:           fixture.overlay,
		textInput:             fixture.field,
		targetSelector:        fixture.selector,
		targetSelectionBudget: semanticWaitBudget,
		focusedApp:            &focusedAppCell{},
		hintService: services.NewHintService(
			accessibility,
			fixture.overlay,
			&portmocks.MockSystemPort{},
			generator,
			config.DefaultConfig().Hints,
			zap.NewNop(),
			nil,
		),
		actionService: services.NewActionService(
			&portmocks.MockAccessibilityPort{},
			fixture.overlay,
			&portmocks.MockSystemPort{
				MoveCursorToPointFunc: func(_ context.Context, point image.Point, _ bool) error {
					fixture.movesMu.Lock()
					fixture.moves = append(fixture.moves, point)
					fixture.movesMu.Unlock()

					return nil
				},
			},
			zap.NewNop(),
		),
		screenBounds: image.Rect(0, 0, 1000, 800),
	})

	fixture.hints = []*domainhint.Interface{
		mustNewModeHint("A", semanticElement(t, "save", "Save", image.Rect(10, 10, 90, 40))),
		mustNewModeHint("S", semanticElement(t, "code", codeTitle, image.Rect(110, 10, 190, 40))),
		mustNewModeHint("D", semanticElement(t, "help", "Help", image.Rect(210, 10, 290, 40))),
	}

	fixture.locked(func(locked *handlerState) {
		manager := domainhint.NewManager(locked.logger, &fixture.handler.mu)
		manager.SetUpdateCallback(locked.drawHints)
		locked.hints.Context.SetManager(manager)

		setErr := locked.hints.Context.SetHints(domainhint.NewCollection(fixture.hints))
		if setErr != nil {
			t.Fatalf("SetHints() error = %v", setErr)
		}

		locked.hints.Context.SetSemantic(true)
		locked.hints.Context.SetScannedApp(semanticAppID)

		startErr := locked.startHintSearch(false)
		if startErr != nil {
			t.Fatalf("startHintSearch() error = %v", startErr)
		}
	})

	return fixture
}

// leaseScan is the accessibility port's leased scan: the elements of the
// hints the fixture holds now, under a fresh lease.
func (f *semanticFixture) leaseScan(
	_ context.Context,
	app string,
	_ ports.ElementFilter,
) ([]*element.Element, ports.ElementLease, error) {
	f.pageMu.Lock()
	gate := f.gate
	f.scans = append(f.scans, app)
	f.pageMu.Unlock()

	if gate != nil {
		f.scanning <- struct{}{}

		<-gate
	}

	f.pageMu.Lock()
	defer f.pageMu.Unlock()

	if f.scanErr != nil {
		return nil, nil, f.scanErr
	}

	elements := f.page
	if elements == nil {
		elements = make([]*element.Element, 0, len(f.hints))
		for _, hint := range f.hints {
			elements = append(elements, hint.Element())
		}
	}

	unheld := maps.Clone(f.unheld)
	resolve := f.resolve

	lease := &portmocks.MockElementLease{
		HoldsFunc:   func(elem *element.Element) bool { return !unheld[elem.ID()] },
		ResolveFunc: f.resolveCtx,
	}
	if resolve != nil {
		lease.ResolveFunc = func(_ context.Context, elem *element.Element) (image.Rectangle, error) {
			return resolve(elem)
		}
	}

	f.leases = append(f.leases, lease)

	return elements, lease, nil
}

// changePage makes what the next scan reads differ from the hints on screen,
// as a dialog that opened after they were read does.
func (f *semanticFixture) changePage(page ...*element.Element) {
	f.pageMu.Lock()
	defer f.pageMu.Unlock()

	f.page = page
	if f.page == nil {
		f.page = []*element.Element{}
	}
}

// scannedApps is the application each scan was asked to read.
func (f *semanticFixture) scannedApps() []string {
	f.pageMu.Lock()
	defer f.pageMu.Unlock()

	return slices.Clone(f.scans)
}

// holdScans makes every scan from now on wait for releaseScan.
func (f *semanticFixture) holdScans(t *testing.T) {
	t.Helper()

	f.pageMu.Lock()
	f.gate = make(chan struct{})
	f.pageMu.Unlock()

	t.Cleanup(func() { close(f.gate) })
}

// waitForScan waits until a held scan has started.
func (f *semanticFixture) waitForScan(t *testing.T) {
	t.Helper()

	select {
	case <-f.scanning:
	case <-time.After(semanticWaitBudget):
		t.Fatal("Return did not start a scan")
	}
}

// releaseScan lets one held scan finish.
func (f *semanticFixture) releaseScan(t *testing.T) {
	t.Helper()

	select {
	case f.gate <- struct{}{}:
	case <-time.After(semanticWaitBudget):
		t.Fatal("no scan was waiting to finish")
	}
}

// pressReturn submits without waiting for the selector, for a Return that
// may never reach it.
func (f *semanticFixture) pressReturn() {
	if f.field.Started {
		f.field.EmitConfirm()

		return
	}

	f.press(config.KeyNameReturn)
}

// waitForReleases waits until every lease a scan took has been released once.
func (f *semanticFixture) waitForReleases(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(semanticWaitBudget)

	for time.Now().Before(deadline) {
		f.pageMu.Lock()

		released := len(f.leases) > 0
		for _, lease := range f.leases {
			released = released && lease.Releases() == 1
		}
		f.pageMu.Unlock()

		if released {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("a scan's lease was not released exactly once")
}

// locked runs fn holding the handler's lock, the way every entry point does.
func (f *semanticFixture) locked(fn func(h *handlerState)) {
	f.handler.mu.Lock()
	defer f.handler.mu.Unlock()

	fn(&f.handler.handlerState)
}

// typeQuery puts text in the search the way the active input path does.
func (f *semanticFixture) typeQuery(text string) {
	if f.field.Started {
		f.field.EmitQueryChanged(text)

		return
	}

	for key := range strings.SplitSeq(text, "") {
		if key == " " {
			key = config.KeyNameSpace
		}

		f.press(key)
	}
}

// press hands one key to the key-stream search path.
func (f *semanticFixture) press(key string) {
	f.locked(func(h *handlerState) { h.handleSearchInputKey(key) })
}

// submit presses Return on the active input path and waits until the
// selector has been asked.
func (f *semanticFixture) submit(t *testing.T) {
	t.Helper()

	f.pressReturn()

	select {
	case <-f.asked:
	case <-time.After(semanticWaitBudget):
		t.Fatal("Return did not ask the target selector")
	}
}

// answer hands the selector's answer to the question in flight.
func (f *semanticFixture) answer(t *testing.T, answer selectorAnswer) {
	t.Helper()

	select {
	case f.answers <- answer:
	case <-time.After(semanticWaitBudget):
		t.Fatal("no question was waiting for an answer")
	}
}

// waitForLog waits for a debug line with the given message, and returns it.
func (f *semanticFixture) waitForLog(t *testing.T, message string) observer.LoggedEntry {
	t.Helper()

	deadline := time.Now().Add(semanticWaitBudget)

	for time.Now().Before(deadline) {
		entries := f.logs.FilterMessage(message).All()
		if len(entries) > 0 {
			return entries[len(entries)-1]
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("timed out waiting for the log line %q", message)

	return observer.LoggedEntry{}
}

func (f *semanticFixture) recordedMoves() []image.Point {
	f.movesMu.Lock()
	defer f.movesMu.Unlock()

	return append([]image.Point(nil), f.moves...)
}

func (f *semanticFixture) status() string {
	search, _ := f.overlay.LastHintSearch()

	return search.Status
}

// lastHintsFrame is the most recent hints frame handed to the overlay.
func (f *semanticFixture) lastHintsFrame(t *testing.T) ports.HintsFrame {
	t.Helper()

	frames := f.overlay.Frames()
	for _, frame := range slices.Backward(frames) {
		if frame, ok := frame.(ports.HintsFrame); ok {
			return frame
		}
	}

	t.Fatal("no hints frame was drawn")

	return ports.HintsFrame{}
}

func TestHandler_SubmitSemanticSearch_AsksOnceOnReturnAndShowsPending(t *testing.T) {
	for _, path := range []struct {
		name  string
		field bool
	}{
		{name: pathSearchField, field: true},
		{name: pathKeyStream, field: false},
	} {
		t.Run(path.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, path.field)

			fixture.typeQuery(semanticQuery)

			if asked := len(fixture.selector.Queries()); asked != 0 {
				t.Fatalf("typing asked the selector %d times, want 0", asked)
			}

			fixture.locked(func(h *handlerState) {
				if got := h.hints.Context.Hints().Count(); got != len(fixture.hints) {
					t.Errorf("typing narrowed the hints to %d, want all %d in view",
						got, len(fixture.hints))
				}
			})

			fixture.submit(t)

			queries := fixture.selector.Queries()
			if len(queries) != 1 || queries[0] != semanticQuery {
				t.Fatalf("selector asked %q, want exactly %q once", queries, semanticQuery)
			}

			candidates := fixture.selector.Candidates()[0]
			if len(candidates) != len(fixture.hints) {
				t.Fatalf("candidates = %d, want one per hint (%d)",
					len(candidates), len(fixture.hints))
			}

			want := semantic.Candidate{
				Role:        string(element.RoleButton),
				Subrole:     "AXToolbarButton",
				Title:       codeTitle,
				Description: "Code description",
				ParentRole:  "AXToolbar",
				ParentTitle: "Repository",
				AppName:     semanticAppID,
				HasPopup:    true,
			}
			if candidates[1] != want {
				t.Errorf("candidate = %+v, want %+v", candidates[1], want)
			}

			if got := fixture.status(); got != semanticStatusPending {
				t.Errorf("status while asking = %q, want %q", got, semanticStatusPending)
			}

			fixture.locked(func(h *handlerState) {
				if !h.hints.Context.SearchActive() {
					t.Error("the search closed while the selector was asked")
				}
			})

			fixture.answer(t, selectorAnswer{})
			fixture.waitForLog(t, "Semantic selection found no single match")
		})
	}
}

func TestHandler_ApplySemanticSelection_NoMatchKeepsTheSearchOpen(t *testing.T) {
	tests := []struct {
		name   string
		answer selectorAnswer
		log    string
		status string
	}{
		{
			name:   "none",
			answer: selectorAnswer{},
			log:    "Semantic selection found no single match",
			status: semanticStatusNoMatch,
		},
		{
			name: "no API key",
			answer: selectorAnswer{
				err: derrors.New(derrors.CodeCredentialMissing, "no key"),
			},
			log:    logFailed,
			status: semanticStatusNoKey,
		},
		{
			name:   "timeout",
			answer: selectorAnswer{err: derrors.New(derrors.CodeTimeout, "slow")},
			log:    logFailed,
			status: semanticStatusTimedOut,
		},
		{
			name:   "any other failure",
			answer: selectorAnswer{err: derrors.New(derrors.CodeRemoteFailed, "broken")},
			log:    logFailed,
			status: semanticStatusFailed,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)

			fixture.typeQuery(semanticQuery)
			fixture.submit(t)
			fixture.answer(t, testCase.answer)
			fixture.waitForLog(t, testCase.log)

			if got := fixture.status(); got != testCase.status {
				t.Errorf("status = %q, want %q", got, testCase.status)
			}

			if moves := fixture.recordedMoves(); len(moves) != 0 {
				t.Errorf("cursor moved to %v, want nothing selected", moves)
			}

			if stops := fixture.field.StopCount(); stops != 0 {
				t.Errorf("search field stopped %d times, want it left open", stops)
			}

			fixture.locked(func(h *handlerState) {
				ctx := h.hints.Context
				if !ctx.SearchActive() || ctx.SearchQuery() != semanticQuery {
					t.Errorf("search active = %v with query %q, want it open with %q",
						ctx.SearchActive(), ctx.SearchQuery(), semanticQuery)
				}

				if got := ctx.Hints().Count(); got != len(fixture.hints) {
					t.Errorf(
						"visible hints = %d, want every boundary (%d)",
						got,
						len(fixture.hints),
					)
				}
			})

			if frame := fixture.lastHintsFrame(t); !frame.LabelsHidden {
				t.Error("the labels came back; the search should still show only boundaries")
			}

			// Editing the query leaves the status that described the old one.
			fixture.typeQuery(semanticQuery + " please")

			if got := fixture.status(); got != "" {
				t.Errorf("status after an edit = %q, want it cleared", got)
			}
		})
	}
}

func TestHandler_ApplySemanticSelection_MatchSelectsThroughTheConfirmPath(t *testing.T) {
	fixture := newSemanticFixture(t, true)

	fixture.typeQuery(semanticQuery)
	fixture.submit(t)
	fixture.answer(t, selectorAnswer{selection: semantic.Selection{Index: 1, Matched: true}})
	fixture.waitForLog(t, "Semantic selection matched")

	chosen := fixture.hints[1].Element().Center()

	moves := fixture.recordedMoves()
	if len(moves) != 1 || moves[0] != chosen {
		t.Fatalf("cursor moves = %v, want one to the chosen hint's center %v", moves, chosen)
	}

	fixture.locked(func(h *handlerState) {
		ctx := h.hints.Context
		if ctx.SearchActive() {
			t.Error("the search is still open after a match")
		}

		visible := ctx.Hints().All()
		if len(visible) != 1 || visible[0].Element().ID() != fixture.hints[1].Element().ID() {
			t.Errorf("visible hints = %d, want only the chosen one", len(visible))
		}
	})

	if stops := fixture.field.StopCount(); stops != 1 {
		t.Errorf("search field stopped %d times, want once by the confirm", stops)
	}
}

func TestHandler_ApplySemanticSelection_DropsAStaleAnswer(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		// change is what happens while the selector is still thinking.
		change func(t *testing.T, fixture *semanticFixture)
	}{
		{
			name:   "a newer submit",
			reason: staleSuperseded,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				fixture.submit(t)
			},
		},
		{
			name:   "Tab to the labels",
			reason: staleSearchClosed,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				if !fixture.field.EmitTab() {
					t.Fatal("the semantic search registered no Tab handler")
				}
			},
		},
		{
			name:   "hints scanned again",
			reason: staleRescanned,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				fixture.locked(func(h *handlerState) {
					rescanned := domainhint.NewCollection(fixture.hints)

					setErr := h.hints.Context.SetHints(rescanned)
					if setErr != nil {
						t.Fatalf("SetHints() error = %v", setErr)
					}
				})
			},
		},
		{
			name:   "Escape out of hints",
			reason: staleModeChanged,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				fixture.field.EmitCancel()
			},
		},
		{
			name:   "another app took the focus",
			reason: staleFocusMoved,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				fixture.handler.PublishFocusedApp(otherAppID)
			},
		},
		{
			// The warp to the other display runs unlocked between the clear
			// and the refresh, so the clear is where the pick is withdrawn.
			name:   "hints moving to another display",
			reason: staleSuperseded,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				if _, cleared := fixture.handler.clearFrameForMonitorMove(); !cleared {
					t.Fatal("the monitor move cleared nothing")
				}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)

			fixture.typeQuery(semanticQuery)
			fixture.submit(t)
			testCase.change(t, fixture)

			// The first question's answer names a hint; it must not be acted on.
			fixture.answer(
				t,
				selectorAnswer{selection: semantic.Selection{Index: 0, Matched: true}},
			)

			entry := fixture.waitForLog(t, "Semantic selection dropped")
			if got := entry.ContextMap()["reason"]; got != testCase.reason {
				t.Errorf("drop reason = %v, want %q", got, testCase.reason)
			}

			if moves := fixture.recordedMoves(); len(moves) != 0 {
				t.Errorf("a stale answer moved the cursor to %v", moves)
			}
		})
	}
}

func TestHandler_HintSearchTab_BringsTheLabelsBackAndClosesTheField(t *testing.T) {
	for _, path := range []struct {
		name  string
		field bool
		tab   func(t *testing.T, fixture *semanticFixture)
	}{
		{
			name:  pathSearchField,
			field: true,
			tab: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				if !fixture.field.EmitTab() {
					t.Fatal("the semantic search registered no Tab handler")
				}
			},
		},
		{
			name:  pathKeyStream,
			field: false,
			tab: func(_ *testing.T, fixture *semanticFixture) {
				fixture.press(config.KeyNameTab)
			},
		},
	} {
		t.Run(path.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, path.field)

			if frame := fixture.lastHintsFrame(t); !frame.LabelsHidden {
				t.Fatal("the semantic search drew the labels; want boundaries only")
			}

			fixture.typeQuery("code")
			path.tab(t, fixture)

			frame := fixture.lastHintsFrame(t)
			if frame.LabelsHidden {
				t.Error("labels still hidden after Tab")
			}

			if len(frame.Hints) != len(fixture.hints) {
				t.Errorf("Tab drew %d hints, want every labeled hint (%d)",
					len(frame.Hints), len(fixture.hints))
			}

			fixture.locked(func(h *handlerState) {
				if h.hints.Context.SearchActive() {
					t.Error("the search is still open after Tab")
				}

				if mode := h.appState.CurrentMode(); mode != domain.ModeHints {
					t.Errorf("mode after Tab = %s, want hints", domain.ModeString(mode))
				}
			})

			if path.field && fixture.field.StopCount() != 1 {
				t.Errorf("search field stopped %d times, want once", fixture.field.StopCount())
			}
		})
	}
}

func TestHandler_HintSearchEscape_LeavesHintsInASemanticSearch(t *testing.T) {
	for _, path := range []struct {
		name  string
		field bool
	}{
		{name: pathSearchField, field: true},
		{name: pathKeyStream, field: false},
	} {
		t.Run(path.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, path.field)

			if path.field {
				fixture.field.EmitCancel()
			} else {
				fixture.press(config.KeyNameEscape)
			}

			fixture.locked(func(h *handlerState) {
				if mode := h.appState.CurrentMode(); mode != domain.ModeIdle {
					t.Errorf("mode after Escape = %s, want idle", domain.ModeString(mode))
				}
			})
		})
	}
}

// TestHandler_StartHintSearch_PlainSearchKeepsUpstreamKeys pins that a search
// without --semantic is untouched: the field keeps Tab, and Escape steps back
// to the labels rather than leaving hints.
func TestHandler_StartHintSearch_PlainSearchKeepsUpstreamKeys(t *testing.T) {
	overlay := &portmocks.MockOverlayPort{
		HintSearchBoundsFunc: func(image.Rectangle) image.Rectangle {
			return image.Rect(400, 740, 600, 780)
		},
	}
	field := &portmocks.MockTextInputPort{Started: true}

	handler := newHintSearchTestHandler(t, overlay, field)

	handler.mu.Lock()
	startErr := handler.startHintSearch(false)
	handler.mu.Unlock()

	if startErr != nil {
		t.Fatalf("startHintSearch() error = %v", startErr)
	}

	if field.EmitTab() {
		t.Error("a plain search took Tab from the field")
	}

	field.EmitCancel()

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if handler.hints.Context.SearchActive() {
		t.Error("Escape left the plain search open")
	}

	if mode := handler.appState.CurrentMode(); mode != domain.ModeHints {
		t.Errorf("mode after Escape = %s, want hints with the labels back", domain.ModeString(mode))
	}
}

// TestHandler_SubmitSemanticSearch_LogsNoQueryOrUIText pins the privacy rule
// for the one feature that sends UI text off the machine: the log says how
// many candidates, how long and what came of it, never what was asked or what
// the controls are called.
func TestHandler_SubmitSemanticSearch_LogsNoQueryOrUIText(t *testing.T) {
	fixture := newSemanticFixture(t, true)

	fixture.typeQuery(semanticQuery)
	fixture.submit(t)
	fixture.answer(t, selectorAnswer{})
	fixture.waitForLog(t, "Semantic selection found no single match")

	forbidden := []string{
		semanticQuery,
		"Save",
		codeTitle,
		"Help",
		"Repository",
		"draft the user typed",
	}

	for _, entry := range fixture.logs.All() {
		texts := []string{entry.Message}

		for _, value := range entry.ContextMap() {
			if text, ok := value.(string); ok {
				texts = append(texts, text)
			}
		}

		for _, text := range texts {
			for _, secret := range forbidden {
				if strings.Contains(text, secret) {
					t.Errorf("log line %q carries %q", entry.Message, secret)
				}
			}
		}
	}
}

// visionElement is a hint target read off the screen by OCR: its title is
// whatever text was there.
func visionElement(t *testing.T, id, title string, bounds image.Rectangle) *element.Element {
	t.Helper()

	elem, err := element.NewElement(
		element.ID(id),
		bounds,
		element.RoleButton,
		element.WithTitle(title),
		element.WithSource(element.SourceVision),
	)
	if err != nil {
		t.Fatalf("NewElement() error = %v", err)
	}

	return elem
}

// replaceHints swaps the session's hints for the given ones, the way a rescan
// does, without closing the search.
func (f *semanticFixture) replaceHints(t *testing.T, hints []*domainhint.Interface) {
	t.Helper()

	f.locked(func(locked *handlerState) {
		setErr := locked.hints.Context.SetHints(domainhint.NewCollection(hints))
		if setErr != nil {
			t.Fatalf("SetHints() error = %v", setErr)
		}
	})

	f.pageMu.Lock()
	f.hints = hints
	f.pageMu.Unlock()
}

func TestSemanticCandidates_KeepsAWindowOrPageTitleOffTheMachine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		parentRole string
		wantTitle  string
	}{
		{parentRole: "AXWindow", wantTitle: ""},
		{parentRole: "AXSheet", wantTitle: ""},
		{parentRole: "AXDrawer", wantTitle: ""},
		{parentRole: "AXApplication", wantTitle: ""},
		{parentRole: "AXWebArea", wantTitle: ""},
		{parentRole: "AXToolbar", wantTitle: "Re: Offer letter"},
	}

	for _, testCase := range tests {
		t.Run(testCase.parentRole, func(t *testing.T) {
			t.Parallel()

			elem, err := element.NewElement(
				"close",
				image.Rect(0, 0, 14, 14),
				element.RoleButton,
				element.WithTitle("Close"),
				element.WithContext(testCase.parentRole, "Re: Offer letter"),
			)
			if err != nil {
				t.Fatalf("NewElement() error = %v", err)
			}

			_, candidates := semanticCandidates(
				[]*domainhint.Interface{mustNewModeHint("A", elem)},
				semanticAppID,
			)

			if len(candidates) != 1 {
				t.Fatalf("candidates = %d, want 1", len(candidates))
			}

			if got := candidates[0].ParentRole; got != testCase.parentRole {
				t.Errorf("ParentRole = %q, want the role kept as %q", got, testCase.parentRole)
			}

			if got := candidates[0].ParentTitle; got != testCase.wantTitle {
				t.Errorf("ParentTitle = %q, want %q", got, testCase.wantTitle)
			}
		})
	}
}

func TestSemanticCandidates_LeavesOutHintsReadOffTheScreen(t *testing.T) {
	t.Parallel()

	read := mustNewModeHint(
		"A",
		visionElement(t, "ocr", "Balance: $4,210", image.Rect(0, 0, 90, 20)),
	)
	walked := mustNewModeHint(
		"S",
		semanticElement(t, "code", codeTitle, image.Rect(100, 0, 190, 20)),
	)

	asked, candidates := semanticCandidates([]*domainhint.Interface{read, walked}, semanticAppID)

	if len(candidates) != 1 || candidates[0].Title != codeTitle {
		t.Fatalf("candidates = %+v, want only the accessibility element", candidates)
	}

	if len(asked) != 1 || asked[0] != walked {
		t.Errorf("asked = %d hints, want only the one described", len(asked))
	}
}

// TestHandler_SubmitSemanticSearch_RefusesWhatItCannotConfirm pins the
// sessions Return does not scan: nothing they hold can be read again before a
// click, so nothing is sent and the search says why.
func TestHandler_SubmitSemanticSearch_RefusesWhatItCannotConfirm(t *testing.T) {
	tests := []struct {
		name    string
		session func(t *testing.T, fixture *semanticFixture)
		status  string
	}{
		{
			name: "every hint read off the screen",
			session: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				fixture.replaceHints(t, []*domainhint.Interface{
					mustNewModeHint(
						"A",
						visionElement(t, "ocr-1", "Inbox", image.Rect(10, 10, 90, 40)),
					),
					mustNewModeHint(
						"S",
						visionElement(t, "ocr-2", "Archive", image.Rect(110, 10, 190, 40)),
					),
				})
			},
			status: semanticStatusVisionOnly,
		},
		{
			name: "a session scanned off the screen",
			session: func(_ *testing.T, fixture *semanticFixture) {
				fixture.locked(func(h *handlerState) {
					h.hints.Context.SetActiveScan(domain.StrategyVision, domain.CaptureScopeWindow)
				})
			},
			status: semanticStatusVisionOnly,
		},
		{
			name: "supplied targets",
			session: func(_ *testing.T, fixture *semanticFixture) {
				fixture.locked(func(h *handlerState) {
					h.hints.Context.SetTargets(
						[]modecmd.Target{{X: 10, Y: 10, Width: 80, Height: 30}},
					)
				})
			},
			status: semanticStatusTargetsOnly,
		},
		{
			name: "another app took the focus after the hints were read",
			session: func(_ *testing.T, fixture *semanticFixture) {
				fixture.handler.PublishFocusedApp(otherAppID)
			},
			status: semanticStatusFocusMoved,
		},
		{
			name: "no application recorded at activation",
			session: func(_ *testing.T, fixture *semanticFixture) {
				fixture.locked(func(h *handlerState) { h.hints.Context.SetScannedApp("") })
			},
			status: semanticStatusScanFailed,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)
			testCase.session(t, fixture)

			fixture.typeQuery(semanticQuery)
			fixture.pressReturn()

			if scans := fixture.scannedApps(); len(scans) != 0 {
				t.Errorf("Return scanned %d times, want none", len(scans))
			}

			if asked := len(fixture.selector.Queries()); asked != 0 {
				t.Errorf("selector asked %d times, want 0", asked)
			}

			if got := fixture.status(); got != testCase.status {
				t.Errorf("status = %q, want %q", got, testCase.status)
			}
		})
	}
}

// TestHandler_ApplySemanticSelection_MapsTheIndexPastHintsLeftOut pins that a
// selection names a hint by its place among the candidates sent, which skips
// the ones left out, rather than by its place among every hint on screen.
func TestHandler_ApplySemanticSelection_MapsTheIndexPastHintsLeftOut(t *testing.T) {
	fixture := newSemanticFixture(t, true)

	code := mustNewModeHint(
		"S",
		semanticElement(t, "code", codeTitle, image.Rect(110, 10, 190, 40)),
	)
	fixture.replaceHints(t, []*domainhint.Interface{
		mustNewModeHint("A", visionElement(t, "ocr", "Inbox", image.Rect(10, 10, 90, 40))),
		code,
	})

	fixture.typeQuery(semanticQuery)
	fixture.submit(t)
	fixture.answer(t, selectorAnswer{selection: semantic.Selection{Index: 0, Matched: true}})
	fixture.waitForLog(t, "Semantic selection matched")

	moves := fixture.recordedMoves()
	if want := code.Element().Center(); len(moves) != 1 || moves[0] != want {
		t.Fatalf("cursor moves = %v, want one to the first candidate sent, at %v", moves, want)
	}
}

func TestHandler_EditSemanticQuery_WithdrawsTheQuestionInFlight(t *testing.T) {
	tests := []struct {
		name string
		// change is what the user does while the selector is thinking.
		change func(fixture *semanticFixture)
	}{
		{
			name:   "retyped",
			change: func(fixture *semanticFixture) { fixture.typeQuery("save draft") },
		},
		{
			// The field holds only spaces with no edit reported, so Return
			// itself has to withdraw the question.
			name: "Return on an empty field",
			change: func(fixture *semanticFixture) {
				fixture.locked(func(locked *handlerState) {
					locked.hints.Context.SetSearchQuery("   ")
				})
				fixture.field.EmitConfirm()
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)

			fixture.typeQuery("delete draft")
			fixture.submit(t)
			testCase.change(fixture)

			if got := fixture.status(); got != "" {
				t.Errorf("status after withdrawing = %q, want it cleared", got)
			}

			fixture.answer(
				t,
				selectorAnswer{selection: semantic.Selection{Index: 0, Matched: true}},
			)

			entry := fixture.waitForLog(t, "Semantic selection dropped")
			if got := entry.ContextMap()["reason"]; got != staleSuperseded {
				t.Errorf("drop reason = %v, want %q", got, staleSuperseded)
			}

			if moves := fixture.recordedMoves(); len(moves) != 0 {
				t.Errorf("an answer to withdrawn text moved the cursor to %v", moves)
			}

			if asked := len(fixture.selector.Queries()); asked != 1 {
				t.Errorf("selector asked %d times, want only the withdrawn question", asked)
			}
		})
	}
}

// TestHandler_ApplySemanticSelection_ClearsThePendingStatusItDrops covers an
// answer dropped while it was still the question in flight: the hints were
// rescanned under it. Nothing more is coming, so "…" must not stay.
func TestHandler_ApplySemanticSelection_ClearsThePendingStatusItDrops(t *testing.T) {
	fixture := newSemanticFixture(t, true)

	fixture.typeQuery(semanticQuery)
	fixture.submit(t)
	fixture.replaceHints(t, append([]*domainhint.Interface(nil), fixture.hints...))
	fixture.answer(t, selectorAnswer{selection: semantic.Selection{Index: 0, Matched: true}})

	entry := fixture.waitForLog(t, "Semantic selection dropped")
	if got := entry.ContextMap()["reason"]; got != staleRescanned {
		t.Fatalf("drop reason = %v, want %q", got, staleRescanned)
	}

	fixture.locked(func(locked *handlerState) {
		if locked.semanticCancel != nil {
			t.Error("the dropped request is still held as the one in flight")
		}

		if !locked.hints.Context.SearchActive() {
			t.Error("the search closed; a rescan should leave it open")
		}
	})

	if got := fixture.status(); got != "" {
		t.Errorf("status after the drop = %q, want the pending status cleared", got)
	}

	if moves := fixture.recordedMoves(); len(moves) != 0 {
		t.Errorf("a dropped answer moved the cursor to %v", moves)
	}
}

// TestHandler_SubmitSemanticSearch_AsksAboutAFreshScanOfTheSessionApp pins
// what Return asks about: the application the session was activated over, read
// again, so a dialog that opened after the hints were drawn is what is offered
// and what is outlined. Elements the scan could not lease are drawn but never
// offered.
func TestHandler_SubmitSemanticSearch_AsksAboutAFreshScanOfTheSessionApp(t *testing.T) {
	fixture := newSemanticFixture(t, true)

	cancel := semanticElement(t, "dialog-cancel", "Cancel", image.Rect(300, 300, 380, 330))
	create := semanticElement(t, "dialog-create", "Create key", image.Rect(400, 300, 520, 330))
	dock := semanticElement(t, "dock-mail", "Mail", image.Rect(500, 760, 540, 790))

	fixture.pageMu.Lock()
	fixture.unheld[dock.ID()] = true
	fixture.pageMu.Unlock()
	fixture.changePage(cancel, create, dock)

	fixture.typeQuery(semanticQuery)
	fixture.submit(t)

	if scans := fixture.scannedApps(); len(scans) != 1 || scans[0] != semanticAppID {
		t.Errorf("scanned %q, want the session's application %q once", scans, semanticAppID)
	}

	candidates := fixture.selector.Candidates()[0]

	titles := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		titles = append(titles, candidate.Title)
	}

	if want := []string{"Cancel", "Create key"}; !slices.Equal(titles, want) {
		t.Errorf("asked about %q, want the fresh window's leased controls %q", titles, want)
	}

	fixture.locked(func(h *handlerState) {
		if got := h.hints.Context.SourceHints().Count(); got != 3 {
			t.Errorf("hints on screen = %d, want the 3 the scan found", got)
		}
	})

	if frame := fixture.lastHintsFrame(t); !frame.LabelsHidden || len(frame.Hints) != 3 {
		t.Errorf("drew %d hints with labels hidden = %v, want the 3 fresh boundaries",
			len(frame.Hints), frame.LabelsHidden)
	}

	fixture.answer(t, selectorAnswer{})
	fixture.waitForLog(t, "Semantic selection found no single match")
	fixture.waitForReleases(t)
}

// TestHandler_SubmitSemanticSearch_DropsAScanOvertakenBeforeAsking pins that
// whatever the user does while Return's scan runs wins before anything is
// sent: the scan is dropped and the selector never asked about it.
func TestHandler_SubmitSemanticSearch_DropsAScanOvertakenBeforeAsking(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		change func(t *testing.T, fixture *semanticFixture)
		// status is what the box says once the scan is dropped, when the
		// search is still open to say it.
		status string
	}{
		{
			name:   "retyped",
			reason: staleSuperseded,
			change: func(_ *testing.T, fixture *semanticFixture) { fixture.typeQuery("save draft") },
		},
		{
			name:   "Tab to the labels",
			reason: staleSearchClosed,
			change: func(t *testing.T, fixture *semanticFixture) {
				t.Helper()

				if !fixture.field.EmitTab() {
					t.Fatal("the semantic search registered no Tab handler")
				}
			},
		},
		{
			name:   "Escape out of hints",
			reason: staleModeChanged,
			change: func(_ *testing.T, fixture *semanticFixture) { fixture.field.EmitCancel() },
		},
		{
			name:   "another app took the focus",
			reason: staleFocusMoved,
			change: func(_ *testing.T, fixture *semanticFixture) {
				fixture.handler.PublishFocusedApp(otherAppID)
			},
			status: semanticStatusFocusMoved,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)
			fixture.holdScans(t)

			fixture.typeQuery(semanticQuery)
			fixture.pressReturn()
			fixture.waitForScan(t)
			testCase.change(t, fixture)
			fixture.releaseScan(t)

			entry := fixture.waitForLog(t, "Semantic scan dropped")
			if got := entry.ContextMap()["reason"]; got != testCase.reason {
				t.Errorf("drop reason = %v, want %q", got, testCase.reason)
			}

			if asked := len(fixture.selector.Queries()); asked != 0 {
				t.Errorf("selector asked %d times after the scan was overtaken, want 0", asked)
			}

			if got := fixture.status(); testCase.status != "" && got != testCase.status {
				t.Errorf("status = %q, want %q", got, testCase.status)
			}

			fixture.waitForReleases(t)
		})
	}
}

// TestHandler_SubmitSemanticSearch_ASecondReturnReplacesTheScanInFlight pins
// that pressing Return again while the first scan runs asks once, about the
// second.
func TestHandler_SubmitSemanticSearch_ASecondReturnReplacesTheScanInFlight(t *testing.T) {
	fixture := newSemanticFixture(t, true)
	fixture.holdScans(t)

	fixture.typeQuery(semanticQuery)
	fixture.pressReturn()
	fixture.waitForScan(t)

	fixture.pressReturn()
	fixture.waitForScan(t)

	fixture.releaseScan(t)
	fixture.releaseScan(t)

	select {
	case <-fixture.asked:
	case <-time.After(semanticWaitBudget):
		t.Fatal("the second Return did not ask the selector")
	}

	entry := fixture.waitForLog(t, "Semantic scan dropped")
	if got := entry.ContextMap()["reason"]; got != staleSuperseded {
		t.Errorf("drop reason = %v, want %q", got, staleSuperseded)
	}

	if asked := len(fixture.selector.Queries()); asked != 1 {
		t.Errorf("selector asked %d times, want once for the second Return", asked)
	}

	fixture.answer(t, selectorAnswer{})
	fixture.waitForLog(t, "Semantic selection found no single match")
	fixture.waitForReleases(t)
}

// TestHandler_ApplySemanticScan_KeepsTheSearchWhenThereIsNothingToAsk pins
// the scans that leave nothing to offer: the search stays open with its
// query, the hints on screen stay as they were, and the box says why.
// Nothing is asked about the old hints instead.
func TestHandler_ApplySemanticScan_KeepsTheSearchWhenThereIsNothingToAsk(t *testing.T) {
	tests := []struct {
		name   string
		page   func(t *testing.T, fixture *semanticFixture)
		status string
		log    string
		// leased says the scan returned a lease, which must come back.
		leased bool
	}{
		{
			name: "the scan failed",
			page: func(_ *testing.T, fixture *semanticFixture) {
				fixture.pageMu.Lock()
				fixture.scanErr = derrors.New(derrors.CodeAccessibilityFailed, "window gone")
				fixture.pageMu.Unlock()
			},
			status: semanticStatusScanFailed,
			log:    "Semantic scan failed",
		},
		{
			name:   "the window is empty",
			page:   func(_ *testing.T, fixture *semanticFixture) { fixture.changePage() },
			status: semanticStatusNothing,
			log:    "Semantic scan found nothing to select",
			leased: true,
		},
		{
			name: "nothing the scan found could be leased",
			page: func(_ *testing.T, fixture *semanticFixture) {
				fixture.pageMu.Lock()
				for _, hint := range fixture.hints {
					fixture.unheld[hint.Element().ID()] = true
				}
				fixture.pageMu.Unlock()
			},
			status: semanticStatusNothing,
			log:    "Semantic scan found nothing to select",
			leased: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)
			testCase.page(t, fixture)

			var before *domainhint.Collection

			fixture.locked(func(h *handlerState) { before = h.hints.Context.SourceHints() })

			fixture.typeQuery(semanticQuery)
			fixture.pressReturn()
			fixture.waitForLog(t, testCase.log)

			if asked := len(fixture.selector.Queries()); asked != 0 {
				t.Errorf("selector asked %d times, want 0", asked)
			}

			if got := fixture.status(); got != testCase.status {
				t.Errorf("status = %q, want %q", got, testCase.status)
			}

			fixture.locked(func(locked *handlerState) {
				ctx := locked.hints.Context
				if !ctx.SearchActive() || ctx.SearchQuery() != semanticQuery {
					t.Errorf("search active = %v with query %q, want it open with %q",
						ctx.SearchActive(), ctx.SearchQuery(), semanticQuery)
				}

				if ctx.SourceHints() != before {
					t.Error("the hints on screen were replaced by a scan that offered nothing")
				}

				if locked.semanticCancel != nil {
					t.Error("the finished request is still held as the one in flight")
				}
			})

			if stops := fixture.field.StopCount(); stops != 0 {
				t.Errorf("search field stopped %d times, want it left open", stops)
			}

			if testCase.leased {
				fixture.waitForReleases(t)
			}
		})
	}
}

// TestHandler_ApplySemanticSelection_ClicksTheTargetWhereItIsNow pins the
// second read: the chosen element is clicked where it was read after the
// answer, not where the scan found it.
func TestHandler_ApplySemanticSelection_ClicksTheTargetWhereItIsNow(t *testing.T) {
	fixture := newSemanticFixture(t, true)

	scanned := fixture.hints[1].Element()
	settled := scanned.Bounds().Add(image.Pt(40, 6))

	fixture.pageMu.Lock()
	fixture.resolve = func(elem *element.Element) (image.Rectangle, error) {
		if elem != scanned {
			return image.Rectangle{}, derrors.New(derrors.CodeElementNotFound, "not this one")
		}

		return settled, nil
	}
	fixture.pageMu.Unlock()

	fixture.typeQuery(semanticQuery)
	fixture.submit(t)
	fixture.answer(t, selectorAnswer{selection: semantic.Selection{Index: 1, Matched: true}})
	fixture.waitForLog(t, "Semantic selection matched")

	want := image.Pt(settled.Min.X+settled.Dx()/2, settled.Min.Y+settled.Dy()/2)
	if moves := fixture.recordedMoves(); len(moves) != 1 || moves[0] != want {
		t.Fatalf("cursor moves = %v, want one to the center it was read at, %v", moves, want)
	}

	fixture.waitForReleases(t)
}

// TestHandler_ApplySemanticSelection_LeavesATargetThatChangedUnclicked pins
// that a chosen element the second read cannot confirm is not clicked where
// it used to be: the search stays open and says so.
func TestHandler_ApplySemanticSelection_LeavesATargetThatChangedUnclicked(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(*element.Element) (image.Rectangle, error)
		// resolveCtx, when set, reads the element again with the context the
		// read was given.
		resolveCtx func(context.Context, *element.Element) (image.Rectangle, error)
		status     string
	}{
		{
			name: "gone, hidden or covered",
			resolve: func(*element.Element) (image.Rectangle, error) {
				return image.Rectangle{}, derrors.New(derrors.CodeElementNotFound, "covered")
			},
			status: semanticStatusMoved,
		},
		{
			name: "moved off the screen",
			resolve: func(*element.Element) (image.Rectangle, error) {
				return image.Rect(1200, 900, 1280, 930), nil
			},
			status: semanticStatusMoved,
		},
		{
			name: "too slow to read again",
			resolve: func(*element.Element) (image.Rectangle, error) {
				return image.Rectangle{}, derrors.New(derrors.CodeTimeout, "hung")
			},
			status: semanticStatusTimedOut,
		},
		{
			// The native read cannot be interrupted, so it can come back
			// with a good frame after its deadline; that frame is not acted on.
			name: "read again successfully, but after its deadline",
			resolveCtx: func(ctx context.Context, elem *element.Element) (image.Rectangle, error) {
				<-ctx.Done()

				return elem.Bounds(), nil
			},
			status: semanticStatusTimedOut,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSemanticFixture(t, true)

			fixture.pageMu.Lock()
			fixture.resolve = testCase.resolve
			fixture.resolveCtx = testCase.resolveCtx
			fixture.pageMu.Unlock()

			fixture.typeQuery(semanticQuery)
			fixture.submit(t)
			fixture.answer(
				t,
				selectorAnswer{selection: semantic.Selection{Index: 1, Matched: true}},
			)
			fixture.waitForLog(t, "Semantic selection named a target that changed")

			if moves := fixture.recordedMoves(); len(moves) != 0 {
				t.Errorf("cursor moved to %v, want nothing clicked", moves)
			}

			if got := fixture.status(); got != testCase.status {
				t.Errorf("status = %q, want %q", got, testCase.status)
			}

			fixture.locked(func(locked *handlerState) {
				ctx := locked.hints.Context
				if !ctx.SearchActive() || ctx.SearchQuery() != semanticQuery {
					t.Errorf("search active = %v with query %q, want it open with %q",
						ctx.SearchActive(), ctx.SearchQuery(), semanticQuery)
				}
			})

			fixture.waitForReleases(t)
		})
	}
}
