package ax

import (
	"context"
	"image"
	"time"

	"github.com/y3owk1n/neru/internal/domain/action"
)

// Element is a generic accessibility element.
type Element interface {
	Release()
}

// Window is a window element.
type Window interface {
	Element
	Role() string
}

// Client is the interface for accessibility operations.
type Client interface {
	// Window and App operations
	FrontmostWindow(ctx context.Context) (Window, error)
	AllWindows(ctx context.Context) ([]Window, error)
	FrontmostAndPopoverWindows(ctx context.Context) ([]Window, error)
	FocusedApplication(ctx context.Context) (App, error)
	ApplicationByBundleID(ctx context.Context, bundleID string) (App, error)
	// ClickableNodes returns the clickable nodes under root. readState asks
	// for each node's identifier and focused state (see
	// ports.ElementFilter.ReadElementState).
	ClickableNodes(
		ctx context.Context,
		root Element,
		roles []string,
		maxDepth int,
		readState bool,
	) ([]Node, error)
	MenuBarClickableElements(ctx context.Context, maxDepth int) ([]Node, error)
	// MissionControlClickableNodes returns what Mission Control draws that a
	// hint can land on while it is up: the current desktop's windows, and the
	// Spaces bar while it is expanded. CodeNotSupported off macOS.
	MissionControlClickableNodes(ctx context.Context, maxDepth int) ([]Node, error)
	ClickableElementsFromBundleID(
		ctx context.Context,
		bundleID string,
		roles []string,
		maxDepth int,
	) ([]Node, error)
	ActiveScreenBounds() image.Rectangle

	// Actions
	PerformAction(
		actionType action.Type,
		p image.Point,
		restoreCursor bool,
		modifiers action.Modifiers,
	) error
	Scroll(deltaX, deltaY int, modifiers action.Modifiers) error
	MoveMouse(p image.Point, bypassSmooth bool)
	CursorPosition() image.Point

	// System
	CheckPermissions() bool
	SetClickableRoles(roles []string)
	ClickableRoles() []string
	IsMissionControlActive() bool
	// SupportsSupplementaryElements reports whether the platform exposes the
	// macOS-specific auxiliary surfaces hints can also scan (Dock, menu bar,
	// Notification Center, Stage Manager, Picture-in-Picture, screen-capture
	// UI). False on Linux/Windows so those collectors are skipped.
	SupportsSupplementaryElements() bool

	// Close releases any resources held by the client (e.g. D-Bus connections
	// or AT-SPI accessibility status).
	Close() error
}

// AppWindowClient is a Client that reads the windows of an application it is
// handed, rather than of whichever one is focused. Only the macOS client is
// one.
type AppWindowClient interface {
	// FocusedAndPopoverWindowsOf is FrontmostAndPopoverWindows for app.
	FocusedAndPopoverWindowsOf(ctx context.Context, app App) ([]Window, error)
	// FocusedWindowOf is FrontmostWindow for app.
	FocusedWindowOf(ctx context.Context, app App) (Window, error)
}

// AppInfo contains information about an application.
type AppInfo struct {
	Role  string
	Title string
}

// App is an application element.
type App interface {
	Element
	BundleIdentifier() string
	Info() (*AppInfo, error)
}

// Node is a node in the accessibility tree.
type Node interface {
	ID() string
	Bounds() image.Rectangle
	Role() string
	Title() string
	Description() string
	Value() string
	IsFocused() bool
	Identifier() string
	IsClickable() bool
	Release()
}

// RetainableNode is a Node whose platform reference can outlive the node's
// own Release, for an element lease. A node that is not one is never leased.
type RetainableNode interface {
	// Retain returns a second reference to the node's element. It must come
	// before the node's Release.
	Retain() (Handle, error)
}

// Handle is a retained platform reference to one element, which a lease reads
// again long after the scan that found it.
type Handle interface {
	Element
	// Resolve reads the element's live frame and checks that its own
	// application still reports it at the frame's center
	// (ports.ElementLease.Resolve), giving up with CodeTimeout once budget
	// has run out.
	Resolve(budget time.Duration) (image.Rectangle, error)
}
