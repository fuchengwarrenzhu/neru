package ports

import (
	"context"
	"image"

	"github.com/y3owk1n/neru/internal/domain/action"
	"github.com/y3owk1n/neru/internal/domain/element"
)

// AccessibilityPort is the interface for interacting with the platform
// accessibility API (AXUIElement on macOS, AT-SPI on Linux, UIA on Windows).
// Implementations handle all platform-specific bridge complexity and live in
// internal/adapter/accessibility/.
type AccessibilityPort interface {
	// Health returns nil if the component is healthy, or an error if it is not.
	Health(ctx context.Context) error

	// ClickableElements retrieves all clickable UI elements matching the filter.
	ClickableElements(ctx context.Context, filter ElementFilter) ([]*element.Element, error)

	// UpdateClickableRoles replaces the set of roles treated as clickable.
	// Config reload calls it when hints.clickable_roles changes.
	//
	// roles are native platform role names (AXButton, push-button,
	// ControlType.Button), already resolved from the semantic vocabulary by
	// element.RoleVocabulary. Backends that do not filter by role — where the
	// tree walk decides clickability another way — accept and ignore them.
	UpdateClickableRoles(roles []string)

	// UpdateMissionControlDetection turns hints' Mission Control check on or
	// off. Config reload calls it when hints.detect_mission_control changes.
	// Backends with no Mission Control accept and ignore it.
	UpdateMissionControlDetection(enabled bool)

	// PerformAction executes an action on the specified element.
	PerformAction(ctx context.Context, elem *element.Element, actionType action.Type) error

	// PerformActionAtPoint executes an action at the specified point.
	PerformActionAtPoint(
		ctx context.Context,
		actionType action.Type,
		point image.Point,
		modifiers action.Modifiers,
	) error

	// Scroll performs a scroll action at the current cursor position,
	// presenting modifiers as held while the scroll is emitted — a ctrl-modified
	// scroll zooms where an unmodified one pans.
	//
	// A backend that cannot present a non-zero modifier set returns
	// derrors.CodeNotSupported rather than scrolling without it: a scroll that
	// silently drops its ctrl looks to the user like a broken binding.
	Scroll(ctx context.Context, deltaX, deltaY int, modifiers action.Modifiers) error

	// ReleaseHeldButtons releases any mouse button this process is still
	// holding down.
	//
	// Neru can leave a button held when a drag-style action is interrupted —
	// the mode exits, the daemon is paused, or a chain bails between the
	// press and the release. Without this the button stays down at the OS
	// level and the desktop behaves as if the user were mid-drag.
	//
	// Implementations must be idempotent and safe to call when nothing is
	// held, because it runs on every mode-exit path.
	ReleaseHeldButtons(ctx context.Context) error

	// FocusedAppBundleID returns the platform application identifier of the
	// currently focused application. On macOS this is a bundle ID
	// (e.g. "com.apple.Safari"). On Linux this will be a desktop ID or
	// executable name; on Windows an AppUserModelID or executable path.
	FocusedAppBundleID(ctx context.Context) (string, error)

	// PrimeApplication reports whether bundleID has an accessibility tree to
	// hint against, waiting briefly for one. Exists for macOS, where Electron,
	// Chromium and Gecko build their tree asynchronously; eager backends
	// report true immediately (a genuine "nothing to do", never
	// CodeNotSupported). Safe to retry, off the event-tap thread.
	PrimeApplication(ctx context.Context, bundleID string) (bool, error)
}

// ScrollCanceler is an optional AccessibilityPort extension for backends that
// play a scroll out over time (smooth_scroll). CancelScroll drops whatever an
// in-flight scroll has not posted yet, so a held scroll key stops scrolling
// when it is let go rather than when the animation catches up with it. Treat
// a missing implementation as nothing in flight: a backend that posts each
// scroll whole has nothing to drop.
type ScrollCanceler interface {
	CancelScroll()
}

// ElementLeaser is an optional AccessibilityPort extension for a caller that
// acts on a scanned element some time after the scan returned, and has to know
// then that it is still the element it was shown. ClickableElements copies
// each element and gives its platform reference back at once, so nothing it
// returns can be read again; a lease keeps those references until its owner
// releases it. Treat a missing implementation as nothing that can be
// confirmed: the caller does not act.
type ElementLeaser interface {
	// LeaseClickableElements is ClickableElements over the focused window and
	// popovers of app, the application with that identifier, rather than over
	// whichever application is in front. Only the elements of those windows
	// are leased; the other surfaces the filter asks for are collected as
	// ClickableElements collects them, and are not. An app that is not
	// running is an error. On an error the lease is nil and nothing is held.
	LeaseClickableElements(
		ctx context.Context,
		app string,
		filter ElementFilter,
	) ([]*element.Element, ElementLease, error)
}

// ElementLease holds the platform references of the elements one leased scan
// returned. It has one owner, which releases it once it is done with them.
type ElementLease interface {
	// Holds reports whether the lease holds a reference for elem. It answers
	// from what the lease holds and asks the platform nothing.
	Holds(elem *element.Element) bool

	// Resolve reads where elem is now. It fails with CodeElementNotFound when
	// the lease holds no reference for elem, when the element is gone or
	// hidden, and when the element elem's own application reports at the
	// center of the live frame is neither elem nor inside it. It looks at no
	// other application's windows. A read its deadline cut short fails with
	// CodeTimeout. What it returns is a read, not a guarantee: the element
	// can move as soon as it has.
	Resolve(ctx context.Context, elem *element.Element) (image.Rectangle, error)

	// Release gives every reference back. A Resolve in progress finishes
	// first, and a second call does nothing.
	Release()
}

// ElementFilter defines criteria for filtering UI elements.
type ElementFilter struct {
	// Roles specifies which accessibility roles to include.
	Roles []element.Role

	// SkipWindowElements skips querying the frontmost window for elements.
	// When true, only supplementary elements (menubar, dock, NC, etc.) are
	// collected. Used by the vision strategy where the frontmost window
	// is detected via Vision Framework instead of the AX tree.
	SkipWindowElements bool

	// MinSize specifies the minimum element size to include.
	MinSize image.Point

	// ExcludeRoles specifies roles to exclude.
	ExcludeRoles []element.Role

	// IncludeMenubar includes menubar elements.
	IncludeMenubar bool

	// AdditionalMenubarTargets specifies additional bundle IDs to scan for menubar elements.
	AdditionalMenubarTargets []string

	// IncludeDock includes dock/taskbar elements.
	// On macOS this queries com.apple.dock.
	// Platform equivalents on Linux/Windows are not yet mapped.
	IncludeDock bool

	// IncludeNotificationCenter includes notification center elements.
	// On macOS this queries com.apple.notificationcenterui.
	// Platform equivalents on Linux/Windows are not yet mapped.
	IncludeNotificationCenter bool

	// IncludeStageManager includes stage manager / window manager elements.
	// On macOS this queries com.apple.WindowManager.
	// Platform equivalents on Linux/Windows are not yet mapped.
	IncludeStageManager bool

	// IncludePIP includes Picture in Picture controls.
	// On macOS this queries com.apple.PIPAgent.
	// Platform equivalents on Linux/Windows are not yet mapped.
	IncludePIP bool

	// IncludeScreenCapture includes screen capture controls.
	// On macOS this queries com.apple.screencaptureui.
	// Platform equivalents on Linux/Windows are not yet mapped.
	IncludeScreenCapture bool

	// TitleContains filters elements whose title contains this substring (case-insensitive).
	TitleContains string

	// DescriptionContains filters elements whose description contains this substring (case-insensitive).
	DescriptionContains string

	// ValueContains filters elements whose value contains this substring (case-insensitive).
	ValueContains string

	// ReadElementState asks for each element's identifier and focused state,
	// which only `neru query hints` reports. A backend that would make extra
	// reads for them skips those reads when it is false, so a Linux or Windows
	// element scanned without it reports unfocused and no identifier whatever
	// its real state.
	ReadElementState bool

	// TextContainsList is a list of additional text substrings to match against (OR logic).
	// This is used when multiple text matches are needed - elements matching any of these
	// will be included. The first string should be set in TitleContains, DescriptionContains,
	// and ValueContains, with additional strings in this list.
	TextContainsList []string
}

// DefaultElementFilter returns a filter with sensible defaults.
func DefaultElementFilter() ElementFilter {
	return ElementFilter{
		MinSize:                   image.Point{X: 1, Y: 1},
		IncludeMenubar:            false,
		AdditionalMenubarTargets:  []string{},
		IncludeDock:               false,
		IncludeNotificationCenter: false,
		IncludeStageManager:       false,
		IncludePIP:                false,
		IncludeScreenCapture:      false,
	}
}
