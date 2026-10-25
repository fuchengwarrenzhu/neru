//go:build darwin

package textinput

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/y3owk1n/neru/internal/ports"
)

// bridgeWaitBudget bounds every wait on a callback the bridges started.
const bridgeWaitBudget = 2 * time.Second

// TestTextInput_TabBridge_HandsTheCallbackToAGoroutine pins the property the
// mode handler's locking rests on (internal/app/modes/AGENTS.md): the field
// calls each bridge on the main thread, sometimes inside a round trip the
// handler is waiting on with its lock held, and the callback takes that lock.
// So a bridge must return before its callback runs. The callback here blocks
// until released; a bridge that ran it inline would never return.
//
// The confirm and cancel bridges are held to the same rule, so they are pinned
// beside it.
func TestTextInput_TabBridge_HandsTheCallbackToAGoroutine(t *testing.T) {
	tests := []struct {
		name   string
		set    func(callbacks *ports.TextInputCallbacks, callback func())
		bridge func()
	}{
		{
			name:   "tab",
			set:    func(callbacks *ports.TextInputCallbacks, callback func()) { callbacks.OnTab = callback },
			bridge: func() { textInputTabBridge(nil) },
		},
		{
			name:   "confirm",
			set:    func(callbacks *ports.TextInputCallbacks, callback func()) { callbacks.OnConfirm = callback },
			bridge: func() { textInputConfirmBridge(nil) },
		},
		{
			name:   "cancel",
			set:    func(callbacks *ports.TextInputCallbacks, callback func()) { callbacks.OnCancel = callback },
			bridge: func() { textInputCancelBridge(nil) },
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			release := make(chan struct{})
			ran := make(chan struct{})

			var callbacks ports.TextInputCallbacks
			testCase.set(&callbacks, func() {
				<-release
				close(ran)
			})

			// The bridges find the field through the package global, as the
			// Objective-C side does.
			input := NewTextInput(zap.NewNop())
			input.mu.Lock()
			input.callbacks = callbacks
			input.mu.Unlock()

			returned := make(chan struct{})

			go func() {
				testCase.bridge()
				close(returned)
			}()

			select {
			case <-returned:
			case <-time.After(bridgeWaitBudget):
				close(release)
				t.Fatal("the bridge ran its callback inline; it must hand it to a goroutine")
			}

			close(release)

			select {
			case <-ran:
			case <-time.After(bridgeWaitBudget):
				t.Fatal("the bridge never ran its callback")
			}
		})
	}
}
