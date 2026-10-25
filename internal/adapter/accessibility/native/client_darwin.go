//go:build darwin

package native

import (
	"github.com/y3owk1n/neru/internal/adapter/accessibility/native/darwin"
	"github.com/y3owk1n/neru/internal/ports"
)

var _ ports.ScrollCanceler = (*Client)(nil)

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
