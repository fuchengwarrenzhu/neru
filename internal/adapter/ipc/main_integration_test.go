//go:build integration && !windows

package ipc_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/y3owk1n/neru/internal/ipcendpoint/endpointtest"
)

// TestMain keeps every test here off the live daemon's endpoint: these start
// real IPC servers, and one bound at the user's endpoint deletes the running
// daemon's socket when it stops.
func TestMain(m *testing.M) {
	cleanup, err := endpointtest.Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "refusing to run against the live IPC endpoint:", err)
		os.Exit(1)
	}

	code := m.Run()

	cleanup()
	os.Exit(code)
}
