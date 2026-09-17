// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// These tests drive the real Cobra tree for `agent session input` and
// `agent session interrupt` against a controlled HTTP listener. They pin the
// thin-client contract: method, escaped path, body keys, credentials, one
// request, and no lifecycle fallback. They do not spawn a provider runtime.

type sessionControlProbe struct {
	*httptest.Server
	mu     sync.Mutex
	calls  atomic.Int64
	method atomic.Value
	path   atomic.Value
	auth   atomic.Value
	tenant atomic.Value
	ctype  atomic.Value
	body   atomic.Value
	paths  []string
}

func newSessionControlProbe(t *testing.T, status int, payload string) *sessionControlProbe {
	t.Helper()
	return newSessionControlProbeHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	})
}

func newSessionControlProbeHandler(t *testing.T, h http.HandlerFunc) *sessionControlProbe {
	t.Helper()
	p := &sessionControlProbe{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls.Add(1)
		p.method.Store(r.Method)
		p.path.Store(r.URL.EscapedPath())
		p.auth.Store(r.Header.Get("Authorization"))
		p.tenant.Store(r.Header.Get("X-Olivares-Tenant"))
		p.ctype.Store(r.Header.Get("Content-Type"))
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		p.body.Store(string(raw))
		p.mu.Lock()
		p.paths = append(p.paths, r.Method+" "+r.URL.EscapedPath())
		p.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *sessionControlProbe) lastBody() string {
	s, _ := p.body.Load().(string)
	return s
}
func (p *sessionControlProbe) lastAuth() string {
	s, _ := p.auth.Load().(string)
	return s
}
func (p *sessionControlProbe) lastTenant() string {
	s, _ := p.tenant.Load().(string)
	return s
}
func (p *sessionControlProbe) lastMethod() string {
	s, _ := p.method.Load().(string)
	return s
}
func (p *sessionControlProbe) lastPath() string {
	s, _ := p.path.Load().(string)
	return s
}
func (p *sessionControlProbe) lastType() string {
	s, _ := p.ctype.Load().(string)
	return s
}
func (p *sessionControlProbe) allHits() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.paths))
	copy(out, p.paths)
	return out
}

func execSessionCLI(t *testing.T, stdin io.Reader, args ...string) (string, string, error) {
	t.Helper()
	if os.Getenv(cliConfigOverrideEnv) == "" {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	root := newRootCmd()
	var out, errb strings.Builder
	root.SetOut(&out)
	root.SetErr(&errb)
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	root.SetIn(stdin)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

func sessionCreds(server string, extra ...string) []string {
	base := []string{"--server", server, "--token", "test-token", "--tenant", "tenant-a"}
	return append(base, extra...)
}

func decodeSessionBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var got map[string]any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, raw)
	}
	if dec.More() {
		t.Fatalf("body contained a second JSON value: %s", raw)
	}
	return got
}

func requireOneInputPOST(t *testing.T, p *sessionControlProbe, escaped string) {
	t.Helper()
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1 hits=%v", p.calls.Load(), p.allHits())
	}
	if p.lastMethod() != http.MethodPost {
		t.Fatalf("method=%q, want POST", p.lastMethod())
	}
	if p.lastPath() != escaped {
		t.Fatalf("path=%q, want %q", p.lastPath(), escaped)
	}
	if p.lastAuth() != "Bearer test-token" {
		t.Fatalf("Authorization=%q, want Bearer test-token", p.lastAuth())
	}
	if p.lastTenant() != "tenant-a" {
		t.Fatalf("X-Olivares-Tenant=%q, want tenant-a", p.lastTenant())
	}
	if !strings.HasPrefix(p.lastType(), "application/json") {
		t.Fatalf("Content-Type=%q, want application/json", p.lastType())
	}
}

const (
	acceptedJSON      = `{"accepted":true}`
	interruptedJSON   = `{"run_ref":"run-lab-1","state":"running","transport":"stream-json","isolation":"native"}`
	largeFenceInt     = "9007199254740993"
	escapedRunRef     = "run/a b"
	escapedRunSegment = "run%2Fa%20b"
)

func TestAgentSessionInterruptIsRegistered(t *testing.T) {
	cur := newRootCmd()
	for _, name := range []string{"agent", "session", "interrupt"} {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("command %q not registered under %s", name, cur.CommandPath())
		}
		cur = next
	}
	if cur.Name() != "interrupt" {
		t.Fatalf("registered verb = %q, want interrupt", cur.Name())
	}
	if !strings.Contains(cur.Short, "turn") {
		t.Fatalf("interrupt short help must distinguish the turn from stop, got %q", cur.Short)
	}
}

func TestAgentSessionInputInlineText(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusAccepted, acceptedJSON)
	out, errb, err := execSessionCLI(t, strings.NewReader("FROMSTDIN"),
		append([]string{"agent", "session", "input", "run-123", "--text", "review the remaining tests"},
			sessionCreds(p.URL)...)...)
	if err != nil {
		t.Fatalf("inline text: %v stderr=%s stdout=%s", err, errb, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("accepted input must print nothing on stdout, got %q", out)
	}
	requireOneInputPOST(t, p, "/v1/m/sessions/runs/run-123/input")
	got := decodeSessionBody(t, p.lastBody())
	if got["text"] != "review the remaining tests" {
		t.Fatalf("text=%v, want the inline string", got["text"])
	}
	if _, ok := got["line"]; ok {
		t.Fatalf("text mode must not send line: %v", got)
	}
	if _, ok := got["work_lease_fence"]; ok {
		t.Fatalf("omitted fence must not appear: %v", got)
	}
}

func TestAgentSessionInputTextStdinPreservesMultiline(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusAccepted, acceptedJSON)
	payload := "line one\nline two\n"
	out, errb, err := execSessionCLI(t, strings.NewReader(payload),
		append([]string{"agent", "session", "input", "run-123", "--text", "-"},
			sessionCreds(p.URL)...)...)
	if err != nil {
		t.Fatalf("text stdin: %v stderr=%s stdout=%s", err, errb, out)
	}
	requireOneInputPOST(t, p, "/v1/m/sessions/runs/run-123/input")
	got := decodeSessionBody(t, p.lastBody())
	if got["text"] != payload {
		t.Fatalf("text=%q, want exact multiline including trailing newline", got["text"])
	}
	if _, ok := got["line"]; ok {
		t.Fatalf("text mode must not send line: %v", got)
	}
}

func TestAgentSessionInputLegacyDefaultStdinLine(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusAccepted, acceptedJSON)
	_, _, err := execSessionCLI(t, strings.NewReader("{\"type\":\"user\"}\n\n"),
		append([]string{"agent", "session", "input", "run-123"}, sessionCreds(p.URL)...)...)
	if err != nil {
		t.Fatalf("legacy stdin line: %v", err)
	}
	requireOneInputPOST(t, p, "/v1/m/sessions/runs/run-123/input")
	got := decodeSessionBody(t, p.lastBody())
	if got["line"] != `{"type":"user"}` {
		t.Fatalf("line=%v, want trailing newlines trimmed", got["line"])
	}
	if _, ok := got["text"]; ok {
		t.Fatalf("legacy line mode must not send text: %v", got)
	}
}

func TestAgentSessionInputExplicitStdinLine(t *testing.T) {
	for _, flag := range [][]string{{"--line", ""}, {"--line", "-"}} {
		t.Run(strings.Join(flag, "="), func(t *testing.T) {
			p := newSessionControlProbe(t, http.StatusAccepted, acceptedJSON)
			args := append([]string{"agent", "session", "input", "run-123"}, flag...)
			args = append(args, sessionCreds(p.URL)...)
			_, _, err := execSessionCLI(t, strings.NewReader("continue please\n"), args...)
			if err != nil {
				t.Fatalf("explicit stdin line: %v", err)
			}
			requireOneInputPOST(t, p, "/v1/m/sessions/runs/run-123/input")
			got := decodeSessionBody(t, p.lastBody())
			if got["line"] != "continue please" {
				t.Fatalf("line=%v", got["line"])
			}
			if _, ok := got["text"]; ok {
				t.Fatalf("line mode must not send text: %v", got)
			}
		})
	}
}

func TestAgentSessionInputEscapedRunRefAndLargeFence(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusAccepted, acceptedJSON)
	_, _, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "input", escapedRunRef, "--text", "hello",
			"--work-lease-fence", largeFenceInt}, sessionCreds(p.URL)...)...)
	if err != nil {
		t.Fatalf("fenced text: %v", err)
	}
	requireOneInputPOST(t, p, "/v1/m/sessions/runs/"+escapedRunSegment+"/input")
	raw := p.lastBody()
	if strings.Contains(raw, ".") || strings.Contains(strings.ToLower(raw), "e+") {
		t.Fatalf("fence must be a JSON integer, got %s", raw)
	}
	got := decodeSessionBody(t, raw)
	num, ok := got["work_lease_fence"].(json.Number)
	if !ok || num.String() != largeFenceInt {
		t.Fatalf("work_lease_fence=%v (%T), want %s", got["work_lease_fence"], got["work_lease_fence"], largeFenceInt)
	}
	if got["text"] != "hello" {
		t.Fatalf("text=%v", got["text"])
	}
	if _, hasLine := got["line"]; hasLine {
		t.Fatalf("exactly one of line or text, got %v", got)
	}
}

func TestAgentSessionInputLocalRefusalsMakeZeroRequests(t *testing.T) {
	tooBig := strings.Repeat("a", sessionInputMaxBytes+1)
	invalidUTF8 := "ok" + string([]byte{0xff})
	if utf8.ValidString(invalidUTF8) {
		t.Fatal("test fixture must be invalid UTF-8")
	}
	cases := []struct {
		name  string
		stdin io.Reader
		extra []string
		want  string
	}{
		{"both flags empty", nil, []string{"--text", "", "--line", ""}, "mutually exclusive"},
		{"both flags set", nil, []string{"--text", "a", "--line", "b"}, "mutually exclusive"},
		{"empty text", nil, []string{"--text", ""}, "empty"},
		{"whitespace text", nil, []string{"--text", "  \n\t"}, "empty"},
		{"text dash empty stdin", strings.NewReader(""), []string{"--text", "-"}, "empty"},
		{"text dash whitespace stdin", strings.NewReader(" \n"), []string{"--text", "-"}, "empty"},
		{"fence zero", nil, []string{"--text", "hi", "--work-lease-fence", "0"}, "positive"},
		{"fence negative", nil, []string{"--text", "hi", "--work-lease-fence=-1"}, "positive"},
		{"inline text overflow", nil, []string{"--text", tooBig}, "1048576"},
		{"stdin text overflow", strings.NewReader(tooBig), []string{"--text", "-"}, "1048576"},
		{"inline line overflow", nil, []string{"--line", tooBig}, "1048576"},
		{"stdin line overflow", strings.NewReader(tooBig), []string{"--line", "-"}, "1048576"},
		{"default stdin overflow", strings.NewReader(tooBig), nil, "1048576"},
		{"inline invalid utf-8", nil, []string{"--text", invalidUTF8}, "UTF-8"},
		{"stdin invalid utf-8", strings.NewReader(invalidUTF8), []string{"--text", "-"}, "UTF-8"},
		{"line stdin invalid utf-8", strings.NewReader(string([]byte{0xff})), []string{"--line", "-"}, "UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newSessionControlProbe(t, http.StatusAccepted, acceptedJSON)
			args := append([]string{"agent", "session", "input", "run-123"}, tc.extra...)
			args = append(args, sessionCreds(p.URL)...)
			_, _, err := execSessionCLI(t, tc.stdin, args...)
			if err == nil {
				t.Fatal("local refusal must fail")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit=%d, want %d (usage): %v", got, exitcode.Usage, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want substring %q", err, tc.want)
			}
			if p.calls.Load() != 0 {
				t.Fatalf("invalid local input made %d request(s): %v", p.calls.Load(), p.allHits())
			}
		})
	}
}

type failReader struct{ err error }

func (f failReader) Read([]byte) (int, error) { return 0, f.err }

func TestAgentSessionInputPropagatesStdinReadFailure(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusAccepted, acceptedJSON)
	boom := errors.New("stdin exploded")
	_, _, err := execSessionCLI(t, failReader{err: boom},
		append([]string{"agent", "session", "input", "run-123", "--text", "-"},
			sessionCreds(p.URL)...)...)
	if err == nil {
		t.Fatal("stdin read failure must fail")
	}
	if !errors.Is(err, boom) && !strings.Contains(err.Error(), "stdin exploded") {
		t.Fatalf("read failure was swallowed: %v", err)
	}
	if p.calls.Load() != 0 {
		t.Fatalf("read failure made %d request(s)", p.calls.Load())
	}
}

func TestAgentSessionInputRefusalDoesNotRetryAsLine(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusConflict, `{"error":{"message":"turn already in flight"}}`)
	out, _, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "input", "run-123", "--text", "hello"},
			sessionCreds(p.URL)...)...)
	if err == nil {
		t.Fatal("HTTP 409 must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Fatalf("exit=%d, want %d (conflict): %v", got, exitcode.Conflict, err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("refused input must print nothing on stdout, got %q", out)
	}
	requireOneInputPOST(t, p, "/v1/m/sessions/runs/run-123/input")
	got := decodeSessionBody(t, p.lastBody())
	if _, ok := got["line"]; ok {
		t.Fatalf("refused text must not be retried as line: %v", got)
	}
	hits := p.allHits()
	for _, h := range hits {
		if strings.Contains(h, "/stop") || strings.Contains(h, "/resume") || strings.Contains(h, "/cleanup") {
			t.Fatalf("input refusal fell back to another lifecycle endpoint: %v", hits)
		}
	}
}

func TestAgentSessionInputRequiresAccepted(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusOK, acceptedJSON)
	_, _, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "input", "run-123", "--line", "hello"},
			sessionCreds(p.URL)...)...)
	if err == nil {
		t.Fatal("HTTP 200 must not satisfy input, which requires 202")
	}
	if !strings.Contains(err.Error(), "HTTP 200") {
		t.Fatalf("error must name HTTP 200, got %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", p.calls.Load())
	}
}

func TestAgentSessionInterruptSuccessWithoutBody(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusOK, interruptedJSON)
	out, errb, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "interrupt", "run-123"}, sessionCreds(p.URL)...)...)
	if err != nil {
		t.Fatalf("interrupt: %v stderr=%s stdout=%s", err, errb, out)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1 hits=%v", p.calls.Load(), p.allHits())
	}
	if p.lastMethod() != http.MethodPost {
		t.Fatalf("method=%q, want POST", p.lastMethod())
	}
	if p.lastPath() != "/v1/m/sessions/runs/run-123/interrupt" {
		t.Fatalf("path=%q", p.lastPath())
	}
	if p.lastAuth() != "Bearer test-token" || p.lastTenant() != "tenant-a" {
		t.Fatalf("credentials Authorization=%q tenant=%q", p.lastAuth(), p.lastTenant())
	}
	if p.lastBody() != "" {
		t.Fatalf("unfenced interrupt must send no body, got %q", p.lastBody())
	}
	if strings.Contains(strings.ToLower(p.lastType()), "json") {
		t.Fatalf("unfenced interrupt must not set JSON Content-Type, got %q", p.lastType())
	}
	if !strings.Contains(out, "run-lab-1") || !strings.Contains(out, `"state":"running"`) {
		t.Fatalf("stdout must print the updated record, got %q", out)
	}
}

func TestAgentSessionInterruptFencedEscapedRef(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusOK, interruptedJSON)
	out, _, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "interrupt", escapedRunRef,
			"--work-lease-fence", largeFenceInt}, sessionCreds(p.URL)...)...)
	if err != nil {
		t.Fatalf("fenced interrupt: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1 hits=%v", p.calls.Load(), p.allHits())
	}
	if p.lastPath() != "/v1/m/sessions/runs/"+escapedRunSegment+"/interrupt" {
		t.Fatalf("path=%q", p.lastPath())
	}
	raw := p.lastBody()
	if strings.Contains(raw, ".") || strings.Contains(strings.ToLower(raw), "e+") {
		t.Fatalf("fence must be a JSON integer, got %s", raw)
	}
	got := decodeSessionBody(t, raw)
	if len(got) != 1 {
		t.Fatalf("fenced interrupt body must contain only work_lease_fence, got %v", got)
	}
	num, ok := got["work_lease_fence"].(json.Number)
	if !ok || num.String() != largeFenceInt {
		t.Fatalf("work_lease_fence=%v, want %s", got["work_lease_fence"], largeFenceInt)
	}
	if !strings.Contains(out, "run-lab-1") {
		t.Fatalf("stdout must print the updated record, got %q", out)
	}
}

func TestAgentSessionInterruptLocalFenceRefusalsMakeZeroRequests(t *testing.T) {
	for _, flag := range []string{"--work-lease-fence=0", "--work-lease-fence=-1"} {
		t.Run(flag, func(t *testing.T) {
			p := newSessionControlProbe(t, http.StatusOK, interruptedJSON)
			_, _, err := execSessionCLI(t, nil,
				append([]string{"agent", "session", "interrupt", "run-123", flag},
					sessionCreds(p.URL)...)...)
			if err == nil {
				t.Fatal("non-positive fence must fail")
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit=%d, want usage: %v", got, err)
			}
			if p.calls.Load() != 0 {
				t.Fatalf("calls=%d, want 0", p.calls.Load())
			}
		})
	}
}

func TestAgentSessionInterruptConflictDoesNotFallBack(t *testing.T) {
	p := newSessionControlProbeHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.EscapedPath(), "/interrupt") {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"message":"turn already in flight"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"run_ref":"run-lab-1","state":"stopped"}`)
	})
	out, _, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "interrupt", "run-123"}, sessionCreds(p.URL)...)...)
	if err == nil {
		t.Fatal("HTTP 409 must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Conflict {
		t.Fatalf("exit=%d, want conflict: %v", got, err)
	}
	if strings.Contains(out, "stopped") {
		t.Fatalf("conflict must not print a stop record, got %q", out)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1 hits=%v", p.calls.Load(), p.allHits())
	}
	hits := p.allHits()
	if hits[0] != "POST /v1/m/sessions/runs/run-123/interrupt" {
		t.Fatalf("hits=%v", hits)
	}
	for _, h := range hits {
		if strings.Contains(h, "/stop") || strings.Contains(h, "/resume") || strings.Contains(h, "/cleanup") || strings.Contains(h, "/input") {
			t.Fatalf("interrupt conflict fell back: %v", hits)
		}
	}
}

func TestAgentSessionInterruptUnsupportedDoesNotFallBack(t *testing.T) {
	p := newSessionControlProbeHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.EscapedPath(), "/interrupt") {
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = io.WriteString(w, `{"error":{"message":"interrupt is not supported"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"run_ref":"run-lab-1","state":"stopped"}`)
	})
	_, _, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "interrupt", escapedRunRef}, sessionCreds(p.URL)...)...)
	if err == nil {
		t.Fatal("HTTP 501 must not exit 0")
	}
	if got := exitcode.From(err); got != exitcode.Server {
		t.Fatalf("exit=%d, want server: %v", got, err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d hits=%v", p.calls.Load(), p.allHits())
	}
	if p.lastPath() != "/v1/m/sessions/runs/"+escapedRunSegment+"/interrupt" {
		t.Fatalf("path=%q", p.lastPath())
	}
	for _, h := range p.allHits() {
		if strings.Contains(h, "/stop") {
			t.Fatalf("unsupported interrupt fell back to stop: %v", p.allHits())
		}
	}
}

func TestAgentSessionInterruptRequiresOK(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusAccepted, interruptedJSON)
	_, _, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "interrupt", "run-123"}, sessionCreds(p.URL)...)...)
	if err == nil {
		t.Fatal("HTTP 202 must not satisfy interrupt, which requires 200")
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", p.calls.Load())
	}
}

func TestAgentSessionInterruptArgvSendsOnlyWithCredentials(t *testing.T) {
	prepareBootstrapCLITest(t)
	p := newSessionControlProbe(t, http.StatusOK, interruptedJSON)
	if _, _, err := execSessionCLI(t, nil,
		"agent", "session", "interrupt", "run-123",
		"--server", p.URL, "--tenant", "tenant-a"); err == nil {
		t.Fatal("interrupt without a credential must fail")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("interrupt without a credential made %d request(s)", p.calls.Load())
	}
	out, _, err := execSessionCLI(t, nil,
		"agent", "session", "interrupt", "run-123",
		"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a")
	if err != nil {
		t.Fatalf("interrupt with a credential: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("credentialed interrupt made %d request(s)", p.calls.Load())
	}
	if !strings.Contains(out, "run-lab-1") {
		t.Fatalf("stdout=%q", out)
	}
}
