//go:build integration && darwin

package accessibility_test

import (
	"context"
	"image"
	"testing"

	"github.com/y3owk1n/neru/internal/adapter/accessibility"
	"github.com/y3owk1n/neru/internal/adapter/accessibility/native"
	nativedarwin "github.com/y3owk1n/neru/internal/adapter/accessibility/native/darwin"
	"github.com/y3owk1n/neru/internal/adapter/logger"
	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain/element"
	"github.com/y3owk1n/neru/internal/ports"
)

// leaseResolveSample is how many held elements the lease leak test resolves.
// Resolving reads each element and hit-tests it in its application, so the
// sample stays small.
const leaseResolveSample = 20

// leakWalkCount is how many full clickable-element scans the regression test
// runs. The leak class this test pins (an element enqueued by a traversal and
// never released) leaks per walk, so a handful of walks separates a real leak
// from noise without slowing the suite.
const leakWalkCount = 5

// TestTreeWalk_ReleasesEveryElement pins the AX element ownership discipline:
// every Element a tree walk constructs must be balanced by exactly one
// Release, including elements a traversal enqueues but abandons. The live
// wrapper count must return to its baseline after every walk — the leaks
// fixed in #1150/#1152 (elements retained during priming and never released)
// would fail this immediately.
//
// Requires Accessibility permission and a real focused application; skipped
// in short mode per the CI test contract.
func TestTreeWalk_ReleasesEveryElement(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping input/permission-dependent integration test in short mode")
	}

	if !nativedarwin.CheckAccessibilityPermissions() {
		t.Skip("Accessibility permission not granted; cannot walk a real tree")
	}

	log := logger.Get()
	client := native.New(log, nil)
	adapter := accessibility.NewAdapter(log, client, false)

	// Each walk gets its own context matching the per-call budget: a shared
	// deadline would expire cumulatively across six healthy scans and fail a
	// later walk that never hung.
	walkOnce := func(what string) error {
		ctx, cancel := context.WithTimeout(context.Background(), integrationScanBudget)
		defer cancel()

		var err error

		runWithinBudget(t, what, func() {
			_, err = adapter.ClickableElements(ctx, ports.DefaultElementFilter())
		})

		return err
	}

	// One warmup walk: lazily-initialized singletons (system-wide element,
	// cached application handles) may legitimately outlive the first walk.
	// runWithinBudget (inside walkOnce) is the hang guard — the AX client
	// discards its context, so only a watchdog can catch a wedged native query.
	warmupErr := walkOnce("warmup ClickableElements walk")
	if warmupErr != nil {
		t.Skipf("warmup walk failed (no scannable frontmost app?): %v", warmupErr)
	}

	baseline := nativedarwin.LiveElementCount()

	for walk := range leakWalkCount {
		// The adapter returns domain elements, not live AX wrappers; the walk
		// owns and must have already released every wrapper it made.
		walkErr := walkOnce("measured ClickableElements walk")
		if walkErr != nil {
			t.Fatalf("walk %d: ClickableElements() error = %v", walk, walkErr)
		}

		if live := nativedarwin.LiveElementCount(); live != baseline {
			t.Fatalf(
				"walk %d leaked AX element wrappers: live count %d, baseline %d — "+
					"some traversal constructed Elements it never released "+
					"(see the ownership rule in internal/adapter/platform/darwin/accessibility.h)",
				walk, live, baseline,
			)
		}
	}
}

// TestPrimeApplication_ReleasesEveryElement pins the same ownership rule for
// the tree warmup that runs on every application switch. Whether the warmup
// finds a ready tree, gives up, or stops at its node budget, it must release
// every Element it queued before it returns.
func TestPrimeApplication_ReleasesEveryElement(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping input/permission-dependent integration test in short mode")
	}

	if !nativedarwin.CheckAccessibilityPermissions() {
		t.Skip("Accessibility permission not granted; cannot walk a real tree")
	}

	log := logger.Get()
	adapter := accessibility.NewAdapter(log, native.New(log, nil), false)

	ctx, cancel := context.WithTimeout(context.Background(), integrationScanBudget)
	defer cancel()

	bundleID, err := adapter.FocusedAppBundleID(ctx)
	if err != nil || bundleID == "" {
		t.Skipf("no focused application to prime: %v", err)
	}

	baseline := nativedarwin.LiveElementCount()

	runWithinBudget(t, "PrimeApplication", func() {
		_, err = adapter.PrimeApplication(ctx, bundleID)
	})

	if err != nil {
		t.Fatalf("PrimeApplication() error = %v", err)
	}

	if live := nativedarwin.LiveElementCount(); live != baseline {
		t.Fatalf("priming leaked AX element wrappers: live count %d, baseline %d", live, baseline)
	}
}

// TestLeaseClickableElements_ReleasesEveryElement pins the ownership rule for
// a leased scan: while the lease is held the live count is the baseline plus
// one element for each the lease holds, and Release returns it to the
// baseline. The first walk also resolves a sample of what it holds
// (assertLeasedElementsResolveInPlace). It reads the focused application's
// tree and hit-tests it; nothing is clicked, typed or focused.
func TestLeaseClickableElements_ReleasesEveryElement(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping input/permission-dependent integration test in short mode")
	}

	if !nativedarwin.CheckAccessibilityPermissions() {
		t.Skip("Accessibility permission not granted; cannot walk a real tree")
	}

	log := logger.Get()
	adapter := accessibility.NewAdapter(log, native.New(log, nil), false)

	// A client with no config has no clickable roles, and a walk that keeps
	// nothing leases nothing. The roles are process-wide, so they go back.
	adapter.UpdateClickableRoles([]string{
		string(element.RoleButton),
		string(element.RoleLink),
		string(element.RoleCheckBox),
		string(element.RoleRadioButton),
		string(element.RolePopUpButton),
		string(element.RoleMenuButton),
		string(element.RoleTabButton),
	})
	t.Cleanup(func() { adapter.UpdateClickableRoles(nil) })

	appCtx, appCancel := context.WithTimeout(context.Background(), integrationScanBudget)
	defer appCancel()

	bundleID, err := adapter.FocusedAppBundleID(appCtx)
	if err != nil || bundleID == "" {
		t.Skipf("no focused application to lease: %v", err)
	}

	leaseOnce := func(what string) ([]*element.Element, ports.ElementLease, error) {
		ctx, cancel := context.WithTimeout(context.Background(), integrationScanBudget)
		defer cancel()

		var (
			elements []*element.Element
			lease    ports.ElementLease
			leaseErr error
		)

		runWithinBudget(t, what, func() {
			elements, lease, leaseErr = adapter.LeaseClickableElements(
				ctx,
				bundleID,
				ports.DefaultElementFilter(),
			)
		})

		return elements, lease, leaseErr
	}

	// Warmup, as in TestTreeWalk_ReleasesEveryElement.
	_, warmup, warmupErr := leaseOnce("warmup LeaseClickableElements scan")
	if warmupErr != nil {
		t.Skipf("warmup lease failed (no scannable focused window?): %v", warmupErr)
	}

	warmup.Release()

	baseline := nativedarwin.LiveElementCount()

	for walk := range leakWalkCount {
		elements, lease, leaseErr := leaseOnce("measured LeaseClickableElements scan")
		if leaseErr != nil {
			t.Fatalf("walk %d: LeaseClickableElements() error = %v", walk, leaseErr)
		}

		var held []*element.Element

		for _, elem := range elements {
			if lease.Holds(elem) {
				held = append(held, elem)
			}
		}

		if len(held) == 0 {
			lease.Release()
			t.Skip("the focused window yielded no element to lease")
		}

		if live, want := nativedarwin.LiveElementCount(), baseline+int64(len(held)); live != want {
			lease.Release()
			t.Fatalf("walk %d: live count %d while %d elements are held, want %d",
				walk, live, len(held), want)
		}

		if walk == 0 {
			assertLeasedElementsResolveInPlace(t, lease, held)
		}

		lease.Release()

		if live := nativedarwin.LiveElementCount(); live != baseline {
			t.Fatalf(
				"walk %d leaked AX element wrappers through the lease: live count %d, baseline %d",
				walk, live, baseline,
			)
		}
	}
}

// assertLeasedElementsResolveInPlace resolves up to leaseResolveSample held
// elements of a window that has not moved since its scan. At least half must
// resolve, which a hit test that misjudged the element its application reports
// would not reach, and each that does must come back at exactly its scanned
// bounds, which a frame read in the wrong coordinates would not.
func assertLeasedElementsResolveInPlace(
	t *testing.T,
	lease ports.ElementLease,
	held []*element.Element,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), integrationScanBudget)
	defer cancel()

	sample := held[:min(len(held), leaseResolveSample)]
	failures := map[derrors.Code]int{}
	resolved := 0

	for _, elem := range sample {
		var (
			frame      image.Rectangle
			resolveErr error
		)

		runWithinBudget(t, "Resolve", func() {
			frame, resolveErr = lease.Resolve(ctx, elem)
		})

		if resolveErr != nil {
			failures[derrors.GetCode(resolveErr)]++

			continue
		}

		resolved++

		if frame != elem.Bounds() {
			t.Errorf(
				"Resolve(%s) = %v, want its scanned bounds %v",
				elem.ID(),
				frame,
				elem.Bounds(),
			)
		}
	}

	if resolved*2 < len(sample) {
		t.Errorf("%d of %d sampled leased elements resolved, want at least half; failures %v",
			resolved, len(sample), failures)
	}
}
