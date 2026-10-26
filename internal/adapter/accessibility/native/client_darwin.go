//go:build darwin

package native

import (
	"context"

	"github.com/y3owk1n/neru/internal/adapter/accessibility/ax"
	"github.com/y3owk1n/neru/internal/adapter/accessibility/native/darwin"
	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/ports"
)

var (
	_ ports.ScrollCanceler = (*Client)(nil)
	_ ax.AppWindowClient   = (*Client)(nil)
	_ ax.RetainableNode    = (*InfraNode)(nil)
)

// CancelScroll drops whatever smooth scroll has not posted yet
// (ports.ScrollCanceler).
func (c *Client) CancelScroll() {
	darwin.CancelScroll()
}

// HasPopup reports whether activating the node opens a menu or popup
// (AXHasPopup). Only the macOS walk reads it.
func (n *InfraNode) HasPopup() bool {
	if n.node == nil || n.node.Info() == nil {
		return false
	}

	return n.node.Info().HasPopup()
}

// FocusedAndPopoverWindowsOf is FrontmostAndPopoverWindows for app
// (ax.AppWindowClient).
func (c *Client) FocusedAndPopoverWindowsOf(_ context.Context, app ax.App) ([]ax.Window, error) {
	appElement, extractErr := c.extractElement(app)
	if extractErr != nil {
		return nil, extractErr
	}

	windows, windowsErr := appElement.FocusedAndPopoverWindows()
	if windowsErr != nil {
		return nil, windowsErr
	}

	result := make([]ax.Window, len(windows))
	for i, w := range windows {
		result[i] = &InfraWindow{element: w}
	}

	return result, nil
}

// FocusedWindowOf is FrontmostWindow for app (ax.AppWindowClient).
func (c *Client) FocusedWindowOf(_ context.Context, app ax.App) (ax.Window, error) {
	appElement, extractErr := c.extractElement(app)
	if extractErr != nil {
		return nil, extractErr
	}

	window := appElement.FocusedWindow()
	if window == nil {
		return nil, derrors.New(derrors.CodeAccessibilityFailed, "failed to get focused window")
	}

	return &InfraWindow{element: window}, nil
}

// Retain returns a second reference to the node's element, which the caller
// owns and releases once (ax.RetainableNode).
func (n *InfraNode) Retain() (ax.Handle, error) {
	if n.node == nil || n.node.Element() == nil {
		return nil, derrors.New(derrors.CodeInvalidInput, "node has no element")
	}

	clone, cloneErr := n.node.Element().Clone()
	if cloneErr != nil {
		return nil, cloneErr
	}

	return clone, nil
}
