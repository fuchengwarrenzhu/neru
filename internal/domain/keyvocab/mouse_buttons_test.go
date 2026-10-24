package keyvocab_test

import (
	"slices"
	"testing"

	"github.com/y3owk1n/neru/internal/domain/keyvocab"
)

// TestMouseButtons_IsExactlyTheDocumentedSet pins the buttons to the list in
// docs/reference/configuration.md, written out rather than read from the table.
func TestMouseButtons_IsExactlyTheDocumentedSet(t *testing.T) {
	t.Parallel()

	want := []string{"MouseLeft", "MouseMiddle", "MouseRight"}
	if got := keyvocab.MouseButtons(); !slices.Equal(got, want) {
		t.Errorf("MouseButtons() = %v, want %v", got, want)
	}
}

// TestIsMouseButton_IsNeverANamedKey keeps the two sets apart: a named key is
// a keyboard key every tap names and every hotkey table accepts, and a button
// is neither, so a name in both would reach backends that cannot press it.
func TestIsMouseButton_IsNeverANamedKey(t *testing.T) {
	t.Parallel()

	for _, button := range keyvocab.MouseButtons() {
		if keyvocab.IsNamedKey(button) {
			t.Errorf("%q is both a mouse button and a named key", button)
		}
	}

	for _, key := range keyvocab.NamedKeys() {
		if keyvocab.IsMouseButton(key) {
			t.Errorf("named key %q reads as a mouse button", key)
		}
	}
}

func TestIsMouseButton_IgnoresCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want bool
	}{
		{in: "MouseLeft", want: true},
		{in: "mouseleft", want: true},
		{in: "MOUSEMIDDLE", want: true},
		{in: "MouseRight", want: true},
		{in: "Mouse4", want: false},
		{in: "Shift+MouseLeft", want: false},
		{in: "", want: false},
	}

	for _, testCase := range tests {
		if got := keyvocab.IsMouseButton(testCase.in); got != testCase.want {
			t.Errorf("IsMouseButton(%q) = %t, want %t", testCase.in, got, testCase.want)
		}
	}
}
