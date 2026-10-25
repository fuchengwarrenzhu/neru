// Package endpointtest keeps a test binary off the endpoint of the daemon the
// user is running. It redirects the Unix socket endpoint; a Windows pipe name
// is fixed per user and cannot be moved this way, so Isolate refuses there.
package endpointtest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/y3owk1n/neru/internal/ipcendpoint"
)

// errEndpointNotPrivate is Isolate's refusal: the endpoint still resolves to
// a path outside the directory it made.
var errEndpointNotPrivate = errors.New(
	"IPC endpoint still resolves outside the private test directory",
)

// Isolate points this process's IPC endpoint at a fresh private directory, so
// a test that starts a real server or dials a client reaches only its own.
// Without it a server binds the user's live endpoint, and stopping it removes
// the socket out from under the running daemon. It refuses, rather than
// returning a cleanup, when the endpoint still resolves outside that
// directory, so a test binary that calls it from TestMain never gets as far as
// binding anything. The returned function removes the directory.
func Isolate() (func(), error) {
	// The default temp directory on macOS is too long for a socket path once
	// the endpoint's own directory and name are added (sun_path is 104 bytes).
	dir, err := os.MkdirTemp("/tmp", "neru-test-")
	if err != nil {
		return nil, err
	}

	cleanup := func() { _ = os.RemoveAll(dir) }

	// Both, because the endpoint falls back to the temp directory when the
	// runtime directory is unusable.
	for _, name := range []string{"XDG_RUNTIME_DIR", "TMPDIR"} {
		err = os.Setenv(name, dir)
		if err != nil {
			cleanup()

			return nil, err
		}
	}

	if !strings.HasPrefix(ipcendpoint.DaemonPath(), dir+string(filepath.Separator)) {
		cleanup()

		return nil, errEndpointNotPrivate
	}

	return cleanup, nil
}
