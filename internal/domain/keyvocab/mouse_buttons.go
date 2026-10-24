package keyvocab

import (
	"slices"
	"strings"
)

// The mouse buttons, in the spelling a mode's hotkey table writes and the
// macOS mode tap emits when a physical button goes down while a mode is
// active. They are not named keys: a button has no keycode, reports no
// release, takes no modifiers, cannot be fed to the OS and cannot be a global
// hotkey. A binding in a mode's own table is the only thing that reads one,
// and the click itself always reaches the application under the cursor.
const (
	MouseLeft   = "MouseLeft"
	MouseRight  = "MouseRight"
	MouseMiddle = "MouseMiddle"
)

var mouseButtonDisplay = lowercaseIndex([]string{MouseLeft, MouseRight, MouseMiddle})

// IsMouseButton reports whether name is one of the mouse buttons,
// case-insensitively.
func IsMouseButton(name string) bool {
	_, ok := mouseButtonDisplay[strings.ToLower(name)]

	return ok
}

// MouseButtons returns every mouse button in display form, sorted, so the set
// can be pinned against the native emitter rather than sampled.
func MouseButtons() []string {
	buttons := make([]string, 0, len(mouseButtonDisplay))
	for _, display := range mouseButtonDisplay {
		buttons = append(buttons, display)
	}

	slices.Sort(buttons)

	return buttons
}
