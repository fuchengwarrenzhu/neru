package accessibility

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/y3owk1n/neru/internal/adapter/accessibility/ax"
	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain/element"
	"github.com/y3owk1n/neru/internal/ports"
)

// LeaseClickableElements is ClickableElements over the focused window and
// popovers of app, keeping the platform reference of each element those
// windows yield (ports.ElementLeaser). The application is named rather than
// asked for, because while Neru's search field is open the focused
// application is Neru.
func (a *Adapter) LeaseClickableElements(
	ctx context.Context,
	app string,
	filter ports.ElementFilter,
) ([]*element.Element, ports.ElementLease, error) {
	windowsOf, ok := a.client.(ax.AppWindowClient)
	if !ok {
		return nil, nil, derrors.New(
			derrors.CodeNotSupported,
			"reading a named application's windows is not supported on this platform",
		)
	}

	lease := &elementLease{handles: make(map[*element.Element]ax.Handle)}

	elements, err := a.clickableElements(
		ctx,
		filter,
		func(ctx context.Context) ([]ax.Window, error) {
			return a.appWindowsToScan(ctx, windowsOf, app)
		},
		lease,
	)
	if err == nil {
		// A window scan the context cut short drops what it already held from
		// its result but not from the lease, so a scan whose context ended is
		// not kept even when every source answered in time.
		err = a.checkContext(ctx)
	}

	if err != nil {
		// A window scan the collection stopped waiting for can still be
		// running; whatever it holds from here on is given straight back.
		lease.Release()

		return nil, nil, err
	}

	a.logger.Debug("Leased clickable elements",
		zap.Int("count", len(elements)),
		zap.Int("held", len(lease.handles)))

	return elements, lease, nil
}

// appWindowsToScan is windowsToScan for the application with bundle ID app,
// whether or not it is the focused one.
func (a *Adapter) appWindowsToScan(
	ctx context.Context,
	windowsOf ax.AppWindowClient,
	app string,
) ([]ax.Window, error) {
	target, targetErr := a.client.ApplicationByBundleID(ctx, app)
	if targetErr != nil {
		return nil, targetErr
	}
	defer target.Release()

	windows, windowsErr := windowsOf.FocusedAndPopoverWindowsOf(ctx, target)
	if windowsErr != nil {
		return nil, windowsErr
	}

	if len(windows) > 0 {
		return windows, nil
	}

	focused, focusedErr := windowsOf.FocusedWindowOf(ctx, target)
	if focusedErr != nil {
		return nil, focusedErr
	}

	return []ax.Window{focused}, nil
}

// maxResolveBudget is the longest one Resolve may take. A healthy one is a few
// round trips to the element's application; a context with an earlier
// deadline shortens it.
const maxResolveBudget = time.Second

// elementLease holds the retained platform references of one leased scan's
// window elements, keyed by the element each became (ports.ElementLease).
type elementLease struct {
	// mu is held for reading across a Resolve's native read and for writing by
	// Release, so a reference is never given back while it is being read.
	mu       sync.RWMutex
	handles  map[*element.Element]ax.Handle
	released bool
}

// Holds reports whether the lease holds a reference for elem.
func (l *elementLease) Holds(elem *element.Element) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()

	_, ok := l.handles[elem]

	return ok
}

// Resolve reads where elem is now through the reference the lease holds for
// it, within the earlier of ctx's deadline and maxResolveBudget; running out
// fails it with CodeTimeout. The context cannot interrupt the native read once
// it has started, but each message the read sends waits at most the shorter of
// half a second and the time left, so it returns about when the budget ends:
// late by the timers' slack, and by however long it waited for the read lock.
// The read lock is held for the whole native read, so Release waits for it.
// Calls must not overlap: each bounds the same reference's own timeout.
func (l *elementLease) Resolve(
	ctx context.Context,
	elem *element.Element,
) (image.Rectangle, error) {
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return image.Rectangle{}, derrors.New(derrors.CodeTimeout, "element resolve timed out")
		}

		return image.Rectangle{}, derrors.WrapContextCanceled(ctx, "element resolve")
	default:
	}

	budget := maxResolveBudget
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline))
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.released {
		return image.Rectangle{}, derrors.New(
			derrors.CodeElementNotFound,
			"element lease is released",
		)
	}

	handle, ok := l.handles[elem]
	if !ok {
		return image.Rectangle{}, derrors.New(
			derrors.CodeElementNotFound,
			"element is not held by the lease",
		)
	}

	return handle.Resolve(budget)
}

// Release gives every reference back, once.
func (l *elementLease) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.released {
		return
	}

	l.released = true

	for _, handle := range l.handles {
		handle.Release()
	}

	l.handles = nil
}

// hold keeps node's reference for elem. A node that cannot be retained is not
// held, a released lease gives the reference straight back, and a nil lease
// (ClickableElements) holds nothing.
func (l *elementLease) hold(node ax.Node, elem *element.Element) {
	if l == nil {
		return
	}

	retainable, ok := node.(ax.RetainableNode)
	if !ok {
		return
	}

	handle, retainErr := retainable.Retain()
	if retainErr != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.released {
		handle.Release()

		return
	}

	l.handles[elem] = handle
}
