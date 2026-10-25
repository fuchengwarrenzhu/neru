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
