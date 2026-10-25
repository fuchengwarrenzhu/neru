package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/y3owk1n/neru/internal/derrors"
	"github.com/y3owk1n/neru/internal/domain/semantic"
)

const (
	// testKey is a stand-in key; no test reads the real one.
	testKey   = "ts-test-key-not-real"
	testQuery = "open the account settings"
	testApp   = "Safari"
	// bodySentinel is response text that must never reach an error or a log.
	bodySentinel = "response-body-sentinel"
)

func testCandidates() []semantic.Candidate {
	return []semantic.Candidate{
		{
			Role:        "AXButton",
			Title:       "Settings",
			Description: "Open account settings",
			ParentRole:  "AXToolbar",
			ParentTitle: "Main toolbar",
			AppName:     testApp,
		},
		{
			Role:     "AXPopUpButton",
			Title:    strings.Repeat("é", 250),
			HasPopup: true,
			AppName:  testApp,
		},
		{
			Role:    "AXLink",
			Subrole: "AXTextLink",
			Title:   "  Help  ",
			AppName: testApp,
		},
	}
}

// writeKey writes key to a key file whose directory and file carry the given
// permissions, and returns its path.
func writeKey(t *testing.T, key string, dirPerm, filePerm os.FileMode) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "typesafe-ai")

	err := os.Mkdir(dir, 0o700)
	if err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	path := filepath.Join(dir, "apikey")

	err = os.WriteFile(path, []byte(key+"\n"), 0o600)
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	for target, perm := range map[string]os.FileMode{path: filePerm, dir: dirPerm} {
		err = os.Chmod(target, perm)
		if err != nil {
			t.Fatalf("Chmod(%s) error = %v", target, err)
		}
	}

	return path
}

// answer is a System One response choosing choice under model.
func answer(model, choice string) string {
	return fmt.Sprintf(
		`{"model":%q,"answers":{"target":{"type":"choice","choice":%q,"confidence":0.4,`+
			`"probabilities":{"c0000":0.4}}},"usage":{"input_tokens":1,"output_tokens":1}}`,
		model, choice,
	)
}

// recordingServer answers every request with status and body, and keeps the
// last request's body and headers.
type recordingServer struct {
	*httptest.Server

	requests atomic.Int32
	body     atomic.Value
	header   atomic.Value
}

func newRecordingServer(t *testing.T, status int, body string) *recordingServer {
	t.Helper()

	server := &recordingServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			server.requests.Add(1)

			received, _ := io.ReadAll(request.Body)
			server.body.Store(received)
			server.header.Store(request.Header.Clone())

			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, body)
		},
	))
	t.Cleanup(server.Close)

	return server
}

func TestAdapter_Select_SendsTheGoldenRequest(t *testing.T) {
	server := newRecordingServer(t, http.StatusOK, answer(Model, "c0000"))
	adapter := newAdapter(server.URL, writeKey(t, testKey, 0o700, 0o600), nil)

	selection, err := adapter.Select(context.Background(), testQuery, testCandidates())
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}

	if !selection.Matched || selection.Index != 0 {
		t.Fatalf("Select() = %+v, want candidate 0", selection)
	}

	golden, err := os.ReadFile(filepath.Join("testdata", "request.golden.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var want bytes.Buffer

	err = json.Compact(&want, golden)
	if err != nil {
		t.Fatalf("Compact(golden) error = %v", err)
	}

	got, _ := server.body.Load().([]byte)
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("request body =\n%s\nwant\n%s", got, want.Bytes())
	}

	header, _ := server.header.Load().(http.Header)
	if got := header.Get("Authorization"); got != "Bearer "+testKey {
		t.Fatalf("Authorization header does not carry the key file's key")
	}

	if got := header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestAdapter_Select_ChecksTheAnswer(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantCode    derrors.Code
		wantMatched bool
	}{
		{name: "choice in the set", body: answer(Model, "c0002"), wantMatched: true},
		{name: "no single match", body: answer(Model, semantic.NoneID)},
		{
			name:     "another model",
			body:     answer("jev-1.13.0", "c0000"),
			wantCode: derrors.CodeRemoteFailed,
		},
		{
			name:     "choice outside the set",
			body:     answer(Model, "c0003"),
			wantCode: derrors.CodeRemoteFailed,
		},
		{
			name:     "no answer to the question",
			body:     `{"model":"` + Model + `","answers":{}}`,
			wantCode: derrors.CodeRemoteFailed,
		},
		{
			name:     "answer of another type",
			body:     `{"model":"` + Model + `","answers":{"target":{"type":"noul","noul":0.9}}}`,
			wantCode: derrors.CodeRemoteFailed,
		},
		{
			name:     "not json",
			body:     "<html>" + bodySentinel + "</html>",
			wantCode: derrors.CodeRemoteFailed,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := newRecordingServer(t, http.StatusOK, testCase.body)
			adapter := newAdapter(server.URL, writeKey(t, testKey, 0o700, 0o600), nil)

			selection, err := adapter.Select(context.Background(), testQuery, testCandidates())

			if testCase.wantCode == "" {
				if err != nil {
					t.Fatalf("Select() error = %v", err)
				}
			} else if !derrors.IsCode(err, testCase.wantCode) {
				t.Fatalf("Select() error = %v, want code %s", err, testCase.wantCode)
			}

			if selection.Matched != testCase.wantMatched {
				t.Fatalf("Select() = %+v, want matched %v", selection, testCase.wantMatched)
			}

			assertNoSecretIn(t, err)
		})
	}
}

func TestAdapter_Select_MapsHTTPFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   derrors.Code
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: derrors.CodeUnauthorized},
		{name: "rate limited", status: http.StatusTooManyRequests, want: derrors.CodeRateLimited},
		{
			name:   "server error",
			status: http.StatusInternalServerError,
			want:   derrors.CodeRemoteFailed,
		},
		{
			name:   "invalid request",
			status: http.StatusUnprocessableEntity,
			want:   derrors.CodeRemoteFailed,
		},
		{name: "overloaded", status: 529, want: derrors.CodeRemoteFailed},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := newRecordingServer(t, testCase.status, `{"detail":"`+bodySentinel+`"}`)
			adapter := newAdapter(server.URL, writeKey(t, testKey, 0o700, 0o600), nil)

			selection, err := adapter.Select(context.Background(), testQuery, testCandidates())
			if !derrors.IsCode(err, testCase.want) {
				t.Fatalf("Select() error = %v, want code %s", err, testCase.want)
			}

			if selection.Matched {
				t.Fatalf("Select() matched %d despite HTTP %d", selection.Index, testCase.status)
			}

			assertNoSecretIn(t, err)
		})
	}
}

func TestAdapter_Select_RefusesARedirect(t *testing.T) {
	elsewhere := newRecordingServer(t, http.StatusOK, answer(Model, "c0000"))
	redirecting := httptest.NewServer(
		http.RedirectHandler(elsewhere.URL, http.StatusTemporaryRedirect),
	)
	t.Cleanup(redirecting.Close)

	adapter := newAdapter(redirecting.URL, writeKey(t, testKey, 0o700, 0o600), nil)

	_, err := adapter.Select(context.Background(), testQuery, testCandidates())
	if !derrors.IsCode(err, derrors.CodeRemoteFailed) {
		t.Fatalf("Select() error = %v, want code %s", err, derrors.CodeRemoteFailed)
	}

	if got := elsewhere.requests.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests, want 0", got)
	}
}

func TestAdapter_Select_TimesOutWithinTheCallersBudget(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, request *http.Request) {
			_, _ = io.Copy(io.Discard, request.Body)

			select {
			case <-request.Context().Done():
			case <-release:
			}
		},
	))
	// Cleanups run last first: release the handler, then close the server.
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	adapter := newAdapter(server.URL, writeKey(t, testKey, 0o700, 0o600), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()

	_, err := adapter.Select(ctx, testQuery, testCandidates())
	if !derrors.IsCode(err, derrors.CodeTimeout) {
		t.Fatalf("Select() error = %v, want code %s", err, derrors.CodeTimeout)
	}

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Select() took %v, want it to stop at the context deadline", elapsed)
	}
}

func TestAdapter_Select_RefusesAnUnusableKeyFile(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		dirPerm  os.FileMode
		filePerm os.FileMode
		remove   bool
		want     derrors.Code
	}{
		{
			name: "world readable file", key: testKey, dirPerm: 0o700, filePerm: 0o644,
			want: derrors.CodeCredentialInsecure,
		},
		{
			name: "group readable file", key: testKey, dirPerm: 0o700, filePerm: 0o640,
			want: derrors.CodeCredentialInsecure,
		},
		{
			name: "world searchable directory", key: testKey, dirPerm: 0o755, filePerm: 0o600,
			want: derrors.CodeCredentialInsecure,
		},
		{
			name: "missing file", key: testKey, dirPerm: 0o700, filePerm: 0o600, remove: true,
			want: derrors.CodeCredentialMissing,
		},
		{
			name: "empty file", key: "", dirPerm: 0o700, filePerm: 0o600,
			want: derrors.CodeCredentialMissing,
		},
		{
			name: "not a header-safe key", key: "two words", dirPerm: 0o700, filePerm: 0o600,
			want: derrors.CodeCredentialMissing,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := newRecordingServer(t, http.StatusOK, answer(Model, "c0000"))
			path := writeKey(t, testCase.key, testCase.dirPerm, testCase.filePerm)

			if testCase.remove {
				err := os.Remove(path)
				if err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			}

			_, err := newAdapter(server.URL, path, nil).
				Select(context.Background(), testQuery, testCandidates())
			if !derrors.IsCode(err, testCase.want) {
				t.Fatalf("Select() error = %v, want code %s", err, testCase.want)
			}

			if got := server.requests.Load(); got != 0 {
				t.Fatalf("server received %d requests, want none without a usable key", got)
			}

			if testCase.key != "" && strings.Contains(err.Error(), testCase.key) {
				t.Fatal("the error quotes the key file's content")
			}
		})
	}
}

func TestAdapter_Select_ReadsTheKeyAtCallTime(t *testing.T) {
	server := newRecordingServer(t, http.StatusOK, answer(Model, "c0000"))
	path := filepath.Join(t.TempDir(), "typesafe-ai", "apikey")
	adapter := newAdapter(server.URL, path, nil)

	_, err := adapter.Select(context.Background(), testQuery, testCandidates())
	if !derrors.IsCode(err, derrors.CodeCredentialMissing) {
		t.Fatalf("Select() before the key exists: error = %v, want code %s",
			err, derrors.CodeCredentialMissing)
	}

	err = os.Mkdir(filepath.Dir(path), 0o700)
	if err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	err = os.WriteFile(path, []byte(testKey+"\n"), 0o600)
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = adapter.Select(context.Background(), testQuery, testCandidates())
	if err != nil {
		t.Fatalf("Select() after the key was written: error = %v", err)
	}

	header, _ := server.header.Load().(http.Header)
	if header.Get("Authorization") != "Bearer "+testKey {
		t.Fatal("Authorization header does not carry the key written after construction")
	}
}

func TestAdapter_Select_LogsOnlyCountsAndCodes(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantOutcome string
		wantModel   string
		wantCode    string
	}{
		{
			name: "match", status: http.StatusOK, body: answer(Model, "c0001"),
			wantOutcome: outcomeMatch, wantModel: Model,
		},
		{
			name: "none", status: http.StatusOK, body: answer(Model, semantic.NoneID),
			wantOutcome: outcomeNone, wantModel: Model,
		},
		{
			name: "error", status: http.StatusUnauthorized, body: bodySentinel,
			wantOutcome: outcomeError, wantCode: string(derrors.CodeUnauthorized),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			server := newRecordingServer(t, testCase.status, testCase.body)
			adapter := newAdapter(server.URL, writeKey(t, testKey, 0o700, 0o600), zap.New(core))

			candidates := testCandidates()
			_, _ = adapter.Select(context.Background(), testQuery, candidates)

			entries := logs.AllUntimed()
			if len(entries) != 1 {
				t.Fatalf("logged %d entries, want one outcome line", len(entries))
			}

			entry := entries[0]
			if entry.Level != zapcore.DebugLevel {
				t.Fatalf("logged at %v, want debug", entry.Level)
			}

			fields := entry.ContextMap()
			printed := entry.Message + fmt.Sprint(fields)

			forbidden := []string{
				testQuery,
				testKey,
				bodySentinel,
				"Settings",
				"Help",
				testApp,
				"é",
			}
			for _, text := range forbidden {
				if strings.Contains(printed, text) {
					t.Fatalf("log line %q carries %q", printed, text)
				}
			}

			want := map[string]any{
				"candidates":    int64(len(candidates)),
				"leaves":        int64(1),
				"outcome":       testCase.wantOutcome,
				"model":         testCase.wantModel,
				"endpoint_host": strings.TrimPrefix(server.URL, "http://"),
			}
			if testCase.wantCode != "" {
				want["error_code"] = testCase.wantCode
			}

			for key, value := range want {
				if fields[key] != value {
					t.Fatalf("field %s = %v, want %v", key, fields[key], value)
				}
			}

			if _, ok := fields["latency_ms"]; !ok {
				t.Fatal("log line has no latency_ms")
			}
		})
	}
}

// TestAdapter_Select_ReadsTheSettingsAtEachSelection pins what makes the
// endpoint reloadable: the endpoint and key file in force at a submit are the
// ones that submit uses, so a gateway named by a config reload answers the next
// question with its own key.
func TestAdapter_Select_ReadsTheSettingsAtEachSelection(t *testing.T) {
	direct := newRecordingServer(t, http.StatusOK, answer(Model, "c0000"))
	gateway := newRecordingServer(t, http.StatusOK, answer(Model, "c0000"))

	const gatewayKey = "gateway-key-0123456789"

	settings := Settings{Endpoint: direct.URL, KeyFile: writeKey(t, testKey, 0o700, 0o600)}
	adapter := NewAdapter(nil, func() Settings { return settings })

	_, err := adapter.Select(context.Background(), testQuery, testCandidates())
	if err != nil {
		t.Fatalf("Select() to the first endpoint error = %v", err)
	}

	settings = Settings{Endpoint: gateway.URL, KeyFile: writeKey(t, gatewayKey, 0o700, 0o600)}

	_, err = adapter.Select(context.Background(), testQuery, testCandidates())
	if err != nil {
		t.Fatalf("Select() to the second endpoint error = %v", err)
	}

	if direct.requests.Load() != 1 || gateway.requests.Load() != 1 {
		t.Fatalf("requests = %d to the first endpoint and %d to the second, want 1 each",
			direct.requests.Load(), gateway.requests.Load())
	}

	header, _ := gateway.header.Load().(http.Header)
	if got := header.Get("Authorization"); got != "Bearer "+gatewayKey {
		t.Fatal("the second endpoint did not get its own key")
	}
}

// assertNoSecretIn fails when err carries the key or response body text.
func assertNoSecretIn(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		return
	}

	for _, text := range []string{testKey, bodySentinel} {
		if strings.Contains(err.Error(), text) {
			t.Fatalf("error %q carries %q", err, text)
		}
	}
}
