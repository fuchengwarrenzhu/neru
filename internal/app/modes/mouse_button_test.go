package modes

// A physical mouse button reaches the handler as a pseudo-key from the macOS
// mode tap, after the click itself has gone on to the application. These pin
// what the handler then does with it: run the active mode's binding, and
// otherwise nothing at all.

import (
	"image"
	"strings"
	"testing"

	"github.com/y3owk1n/neru/internal/app/heldmotion"
	configpkg "github.com/y3owk1n/neru/internal/config"
	"github.com/y3owk1n/neru/internal/domain"
	"github.com/y3owk1n/neru/internal/domain/keyvocab"
	"github.com/y3owk1n/neru/internal/domain/motion"
	"github.com/y3owk1n/neru/internal/domain/state"
	"github.com/y3owk1n/neru/internal/ports/mocks"
)

// mouseButtonMode is the declared mode the handler is in for these tests.
const mouseButtonMode = "cursor"

// mouseButtonHandler builds a handler in the declared mode with hotkeys as its
// table, recording what each dispatch runs and what reaches the mode itself.
func mouseButtonHandler(
	t *testing.T,
	hotkeys map[string]configpkg.StringOrStringArray,
	heldRepeat configpkg.HeldRepeatConfig,
) (*Handler, chan string, *recordingMode) {
	t.Helper()

	appState := state.NewAppState()
	appState.SetCustomModeName(mouseButtonMode)
	appState.SetMode(domain.ModeCustom)

	mode := &recordingMode{keys: make(chan string, 1)}
	dispatched := make(chan string, 1)

	cfg := &configpkg.Config{
		Modes:      map[string]configpkg.CustomModeConfig{mouseButtonMode: {Hotkeys: hotkeys}},
		HeldRepeat: heldRepeat,
	}

	handler := newHandlerWithState(handlerState{
		config:        cfg,
		appState:      appState,
		modifierState: state.NewModifierState(),
		modes:         map[domain.Mode]Mode{domain.ModeCustom: mode},
		screenBounds:  image.Rect(0, 0, 100, 100),
		motion: heldmotion.New(
			t.Context(),
			&mocks.MockSystemPort{},
			func() motion.Ramp { return heldRepeat.Ramp() },
			nil,
		).Group("modes"),
		executeActionSequence: func(_ string, steps []string, _ []string) {
			dispatched <- strings.Join(steps, ",")
		},
	})

	return handler, dispatched, mode
}

func TestHandleKeyPress_MouseButtonRunsTheModesBinding(t *testing.T) {
	t.Parallel()

	for _, button := range keyvocab.MouseButtons() {
		t.Run(button, func(t *testing.T) {
			t.Parallel()

			handler, dispatched, _ := mouseButtonHandler(t,
				map[string]configpkg.StringOrStringArray{button: {"idle"}},
				configpkg.HeldRepeatConfig{},
			)

			handler.HandleKeyPress(button)

			if steps := waitForDispatch(t, dispatched); steps != "idle" {
				t.Errorf("%s ran %q, want the mode's binding %q", button, steps, "idle")
			}
		})
	}
}

// An unbound button is not input: it must never reach the mode as a key,
// where hints would read it as a label and grid as a cell, and it must not end
// a sequence the user is half way through typing.
func TestHandleKeyPress_UnboundMouseButtonIsNoModesInput(t *testing.T) {
	t.Parallel()

	handler, dispatched, mode := mouseButtonHandler(t,
		map[string]configpkg.StringOrStringArray{"gg": {"action go_top"}},
		configpkg.HeldRepeatConfig{},
	)

	handler.HandleKeyPress("g")
	handler.HandleKeyPress(keyvocab.MouseLeft)
	handler.HandleKeyPress("g")

	if steps := waitForDispatch(t, dispatched); steps != "action go_top" {
		t.Errorf("g, click, g ran %q, want the sequence %q", steps, "action go_top")
	}

	select {
	case key := <-mode.keys:
		t.Errorf("the mode received %q; an unbound button is no mode's input", key)
	default:
	}
}

// A button reports no release, so a binding that would glide or repeat while
// a key is held has to run once instead: a glide started by a click would
// never be told to stop.
func TestHandleKeyPress_MouseButtonNeverStartsAGlide(t *testing.T) {
	t.Parallel()

	const glide = "action move_mouse_relative --dx=10 --dy=0"

	handler, dispatched, _ := mouseButtonHandler(t,
		map[string]configpkg.StringOrStringArray{keyvocab.MouseLeft: {glide}},
		configpkg.HeldRepeatConfig{Enabled: true, InitialDelay: 50, Interval: 50},
	)

	handler.HandleKeyPress(keyvocab.MouseLeft)

	if steps := waitForDispatch(t, dispatched); steps != glide {
		t.Errorf("the click ran %q, want one discrete %q", steps, glide)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if handler.motion.IsHeld(motionKeyOf(keyvocab.MouseLeft)) {
		t.Error("the click is held in the glide, which nothing will ever release")
	}

	if handler.heldRepeatingKey != "" {
		t.Errorf("the click started a held repeat for %q", handler.heldRepeatingKey)
	}
}
