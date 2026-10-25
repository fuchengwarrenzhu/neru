package modes

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/y3owk1n/neru/internal/app/heldmotion"
	configpkg "github.com/y3owk1n/neru/internal/config"
	"github.com/y3owk1n/neru/internal/domain"
	"github.com/y3owk1n/neru/internal/domain/keyvocab"
	"github.com/y3owk1n/neru/internal/domain/motion"
	portmocks "github.com/y3owk1n/neru/internal/ports/mocks"
)

const (
	passingMode    = "warren"
	otherPassMode  = "reader"
	passingToggle  = "Ctrl+Alt+X"
	sequenceGG     = "gg"
	sequenceGT     = "gt"
	stepGoTop      = "action go_top"
	stepMoveLeft   = "action move_mouse_relative --dx=-10 --dy=0"
	stepScrollDown = "action scroll_down"
)

// passingConfig declares a mode with h, the "gg" and "gt" sequences (t bound by
// nothing else), n and the default Escape, a second declared mode that also
// passes unbound keys, and a global chord that toggles the first. unboundKeys
// is what the first declaration writes for unbound_keys; general chord
// passthrough is off, so anything the tap is told to pass comes from the
// declarations alone.
func passingConfig(unboundKeys string) *configpkg.Config {
	table := configpkg.DefaultCustomModeHotkeys()
	table["h"] = configpkg.StringOrStringArray{stepMoveLeft}
	table[sequenceGG] = configpkg.StringOrStringArray{stepGoTop}
	table[sequenceGT] = configpkg.StringOrStringArray{stepGoTop}
	table["n"] = configpkg.StringOrStringArray{stepScrollDown}

	other := configpkg.DefaultCustomModeHotkeys()
	other["j"] = configpkg.StringOrStringArray{stepScrollDown}

	return &configpkg.Config{
		General: configpkg.GeneralConfig{ShouldExitAfterPassthrough: true},
		Hotkeys: configpkg.HotkeysConfig{
			Bindings: map[string][]string{passingToggle: {"mode " + passingMode + " --toggle"}},
		},
		Modes: map[string]configpkg.CustomModeConfig{
			passingMode:   {UnboundKeys: unboundKeys, Hotkeys: table},
			otherPassMode: {UnboundKeys: configpkg.UnboundKeysPass, Hotkeys: other},
		},
	}
}

func enterPassingMode(handler *Handler) {
	handler.ActivateMode(activate(passingMode, false))
}

// The labels the mock tap logs for the three pushes whose order matters.
const (
	logPassOff = "consume_unbound_keys"
	logList    = "set_modifier_passthrough"
	logPassOn  = "pass_unbound_keys"
)

// callLog records the mock tap's call labels.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) record(label string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls = append(l.calls, label)
}

// pushes returns the logged calls that are one of the three ordered pushes.
func (l *callLog) pushes() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []string

	for _, label := range l.calls {
		if label == logPassOff || label == logList || label == logPassOn {
			out = append(out, label)
		}
	}

	return out
}

// everyListIsGuarded reports whether each list push in pushes comes right after
// an off push, so no list ever lands while bare keys are being passed.
func everyListIsGuarded(pushes []string) bool {
	for idx, label := range pushes {
		if label == logList && (idx == 0 || pushes[idx-1] != logPassOff) {
			return false
		}
	}

	return true
}

// TestSyncModifierPassthrough_PassModeHandsTheTapItsWholeBoundSet pins what a
// declared mode with unbound_keys = "pass" tells the tap: pass what is not
// bound, and the bound set is every key dispatch could answer — the mode's own
// keys, the global chord it falls back to, and both letters of each sequence.
// Without t, "gt" could never complete: t would go to the application and end
// the sequence g started. The switch to pass bare keys goes on only right after
// the list it reads, each list guarded by an off push before it.
func TestSyncModifierPassthrough_PassModeHandsTheTapItsWholeBoundSet(t *testing.T) {
	t.Parallel()

	var log callLog

	tap := &portmocks.MockEventTapPort{OnCall: log.record}

	handler, _ := newCustomModeHandler(passingConfig(configpkg.UnboundKeysPass))
	handler.SetEventTap(tap)
	enterPassingMode(handler)

	enabled, blacklist := tap.ModifierPassthrough()
	if !enabled {
		t.Error("chord passthrough is off in a mode that passes unbound keys")
	}

	canonical := configpkg.CanonicalHotkeyForPlatform
	for _, want := range []string{"h", "n", configpkg.KeyNameEscape, "g", "t", passingToggle} {
		if !slices.Contains(blacklist, canonical(want)) {
			t.Errorf(
				"blacklist is %v, want %q among the keys the tap keeps",
				blacklist,
				canonical(want),
			)
		}
	}

	if !tap.UnboundKeyPassthrough() {
		t.Fatal("the tap still swallows unbound bare keys")
	}

	pushes := log.pushes()
	if len(pushes) < 3 ||
		!slices.Equal(pushes[len(pushes)-3:], []string{logPassOff, logList, logPassOn}) ||
		!everyListIsGuarded(pushes) {
		t.Errorf("pushes %v: want every list guarded by an off push, ending off, list, on", pushes)
	}
}

// TestSyncModifierPassthrough_PassToPassSwallowsUntilTheNewListLands pins the
// handover between two declared modes that both pass unbound keys: the tap
// stops passing as the switch begins and resumes only on the new mode's list,
// so a key typed during the switch is consumed and delivered to the mode coming
// up rather than passed on the old mode's list.
func TestSyncModifierPassthrough_PassToPassSwallowsUntilTheNewListLands(t *testing.T) {
	t.Parallel()

	var log callLog

	handler, _ := newCustomModeHandler(passingConfig(configpkg.UnboundKeysPass))
	tap := &portmocks.MockEventTapPort{}
	handler.SetEventTap(tap)
	enterPassingMode(handler)

	tap.SetOnCall(log.record)
	handler.ActivateMode(activate(otherPassMode, false))

	pushes := log.pushes()
	if len(pushes) == 0 || pushes[0] != logPassOff || pushes[len(pushes)-1] != logPassOn ||
		!everyListIsGuarded(pushes) {
		t.Errorf("pushes during the switch %v: want off first, every list guarded, on last", pushes)
	}

	_, blacklist := tap.ModifierPassthrough()
	if !slices.Contains(blacklist, "j") || slices.Contains(blacklist, "h") {
		t.Errorf(
			"blacklist after the switch is %v, want the new mode's j and not the old mode's h",
			blacklist,
		)
	}
}

// TestSyncModifierPassthrough_ConsumeModeNeverPassesBareKeys pins the default:
// a declaration that leaves unbound_keys out, or writes "consume", swallows
// bare keys exactly as before, and passes unbound chords only when [general]
// says so — the declared option widens nothing for a mode that does not set it.
func TestSyncModifierPassthrough_ConsumeModeNeverPassesBareKeys(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", configpkg.UnboundKeysConsume} {
		for _, generalChords := range []bool{false, true} {
			name := fmt.Sprintf("unbound_keys=%q/general_chords=%v", value, generalChords)

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				cfg := passingConfig(value)
				cfg.General.PassthroughUnboundedKeys = generalChords

				handler, _ := newCustomModeHandler(cfg)
				tap := &portmocks.MockEventTapPort{}
				handler.SetEventTap(tap)
				enterPassingMode(handler)

				if tap.UnboundKeyPassthrough() {
					t.Error("a consume mode told the tap to pass unbound bare keys")
				}

				if enabled, _ := tap.ModifierPassthrough(); enabled != generalChords {
					t.Errorf(
						"chord passthrough = %v, want %v as [general] says",
						enabled,
						generalChords,
					)
				}
			})
		}
	}
}

// TestSyncStickyModifierToggle_PassModeLeavesModifiersToTheApp pins that a mode
// passing unbound keys does not arm sticky modifiers: the modifiers belong to
// the typing it lets through, so a lone Shift tap reaches the application
// instead of toggling. A consume mode keeps them.
func TestSyncStickyModifierToggle_PassModeLeavesModifiersToTheApp(t *testing.T) {
	t.Parallel()

	for _, value := range []string{configpkg.UnboundKeysPass, configpkg.UnboundKeysConsume} {
		t.Run("unbound_keys="+value, func(t *testing.T) {
			t.Parallel()

			cfg := passingConfig(value)
			cfg.StickyModifiers.Enabled = true

			handler, _ := newCustomModeHandler(cfg)
			tap := &portmocks.MockEventTapPort{}
			handler.SetEventTap(tap)
			enterPassingMode(handler)

			want := value != configpkg.UnboundKeysPass
			if got := tap.StickyModifierToggle(); got != want {
				t.Errorf("sticky modifier detection = %v, want %v", got, want)
			}
		})
	}
}

// TestSyncModifierPassthrough_LeavingAPassModeSwallowsBeforeTheListChanges pins
// the other half of the push order: going from a pass mode to one that is not,
// the tap stops passing bare keys before it is handed the next mode's list, so
// a hint label typed in that instant is not handed to the application.
func TestSyncModifierPassthrough_LeavingAPassModeSwallowsBeforeTheListChanges(t *testing.T) {
	t.Parallel()

	var log callLog

	handler, _ := newCustomModeHandler(passingConfig(configpkg.UnboundKeysPass))
	tap := &portmocks.MockEventTapPort{}
	handler.SetEventTap(tap)
	enterPassingMode(handler)

	tap.SetOnCall(log.record)
	handler.ExitMode()

	if tap.UnboundKeyPassthrough() {
		t.Fatal("the tap still passes unbound bare keys after the mode closed")
	}

	if pushes := log.pushes(); len(pushes) == 0 || !everyListIsGuarded(pushes) ||
		slices.Contains(pushes, logPassOn) {
		t.Errorf("pushes on exit %v: want every list guarded and nothing passed again", pushes)
	}
}

// TestPassthroughTick_PassModeStaysOpen pins that general.should_exit_after_
// passthrough is for modes that let the odd chord through: a declared mode that
// passes unbound keys does so on every keystroke, and leaving on the first one
// would make it useless.
func TestPassthroughTick_PassModeStaysOpen(t *testing.T) {
	t.Parallel()

	handler, _ := newCustomModeHandler(passingConfig(configpkg.UnboundKeysPass))
	tap := &portmocks.MockEventTapPort{}
	handler.SetEventTap(tap)
	enterPassingMode(handler)

	tap.TriggerPassthrough()

	if mode := handler.appState.CurrentMode(); mode != domain.ModeCustom {
		t.Errorf("mode after a passthrough = %v, want the declared mode still open", mode)
	}
}

// TestHandleKeyPress_SequenceAcrossAPassedKey pins the outcome a user sees: "g",
// a key the tap handed to the application, then "g" is not "gg", and "g", "g"
// still is. It does not pin how: any key the handler sees ends a pending
// sequence, so this holds with or without the marker's own branch, which
// TestHandleKeyPress_PassedKeyNeverReachesTheModesInput is what pins.
func TestHandleKeyPress_SequenceAcrossAPassedKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		keys    []string
		wantRan bool
	}{
		{"g then g completes the sequence", []string{"g", "g"}, true},
		{"g, a passed key, then g does not", []string{"g", keyvocab.PassthroughEvent, "g"}, false},
	}

	for _, scenario := range tests {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			handler, ran := newCustomModeHandler(passingConfig(configpkg.UnboundKeysPass))
			enterPassingMode(handler)

			for _, key := range scenario.keys {
				handler.HandleKeyPress(key)
			}

			select {
			case step := <-ran:
				if !scenario.wantRan {
					t.Errorf(
						"ran %q; a passed key between the two g presses must end the sequence",
						step,
					)
				}
			case <-time.After(200 * time.Millisecond):
				if scenario.wantRan {
					t.Error("gg never ran")
				}
			}
		})
	}
}

// TestHandleKeyPress_PassedKeyLeavesAHeldRepeatRunning pins that the marker is
// not a release: a key the user types while holding n goes to the application
// and n keeps repeating.
func TestHandleKeyPress_PassedKeyLeavesAHeldRepeatRunning(t *testing.T) {
	t.Parallel()

	handler := newHeldRepeatTestHandler()

	handler.HandleKeyPress("j")
	handler.HandleKeyPress(keyvocab.PassthroughEvent)

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if handler.heldRepeatingKey != "j" || handler.heldRepeatingCancel == nil {
		t.Errorf("held repeat after a passed key: key %q, running %v; want j still repeating",
			handler.heldRepeatingKey, handler.heldRepeatingCancel != nil)
	}
}

// TestHandleKeyPress_PassedKeyLeavesAHeldGlideRunning is the glide's half: h
// held in a pass mode keeps steering the cursor while other keys go to the
// application.
func TestHandleKeyPress_PassedKeyLeavesAHeldGlideRunning(t *testing.T) {
	t.Parallel()

	cfg := passingConfig(configpkg.UnboundKeysPass)
	cfg.HeldRepeat = configpkg.HeldRepeatConfig{Enabled: true, Interval: 50}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	controller := heldmotion.New(
		ctx,
		&portmocks.MockSystemPort{},
		func() motion.Ramp { return motion.Ramp{Interval: 50 * time.Millisecond, Multiplier: 1} },
		nil,
	)

	handler, _ := newCustomModeHandler(cfg)
	handler.motion = controller.Group("modes")
	enterPassingMode(handler)

	handler.HandleKeyPress("h")
	handler.HandleKeyPress(keyvocab.PassthroughEvent)

	if !handler.motion.IsHeld("h") {
		t.Error("a passed key released the held glide")
	}
}

// TestRefreshPassthroughForFocusedAppChange_MovesAPassModesList pins that a pass
// mode is retargeted on a focus change even with [general] chord passthrough
// off: an [[app_configs]] binding is part of what the tap keeps, and a stale
// list would hand the newly focused application's binding to it.
func TestRefreshPassthroughForFocusedAppChange_MovesAPassModesList(t *testing.T) {
	t.Parallel()

	cfg := passingConfig(configpkg.UnboundKeysPass)
	mode := cfg.Modes[passingMode]
	mode.AppConfigs = []configpkg.AppConfig{
		{
			BundleID: passthroughFirstApp,
			Hotkeys:  map[string]configpkg.StringOrStringArray{"u": {stepLeftClick}},
		},
		{
			BundleID: passthroughSecondApp,
			Hotkeys:  map[string]configpkg.StringOrStringArray{"o": {stepRightClick}},
		},
	}
	cfg.Modes[passingMode] = mode

	handler, _ := newCustomModeHandler(cfg)
	tap := &portmocks.MockEventTapPort{}
	handler.SetEventTap(tap)
	handler.PublishFocusedApp(passthroughFirstApp)
	enterPassingMode(handler)

	handler.PublishFocusedApp(passthroughSecondApp)
	handler.RefreshPassthroughForFocusedAppChange()

	_, blacklist := tap.ModifierPassthrough()
	if !slices.Contains(blacklist, "o") || slices.Contains(blacklist, "u") {
		t.Errorf(
			"blacklist after the switch is %v, want the second app's o and not the first app's u",
			blacklist,
		)
	}
}

// TestHandleKeyPress_PassedKeyNeverReachesTheModesInput pins that the marker is
// bookkeeping, not input: it never reaches a mode's own key handling, so it
// cannot be read as a hint label, a grid cell or search text.
func TestHandleKeyPress_PassedKeyNeverReachesTheModesInput(t *testing.T) {
	t.Parallel()

	handler := newHeldRepeatTestHandler()

	mode, ok := handler.modes[domain.ModeRecursiveGrid].(*recordingMode)
	if !ok {
		t.Fatal("the held-repeat fixture no longer records its mode's keys")
	}

	handler.HandleKeyPress(keyvocab.PassthroughEvent)

	select {
	case key := <-mode.keys:
		t.Errorf("the mode received %q as input", key)
	default:
	}
}
