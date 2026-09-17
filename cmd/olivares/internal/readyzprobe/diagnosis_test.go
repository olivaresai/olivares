// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package readyzprobe

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bodyServer answers every request with one status and one body over real
// loopback TCP, which is the only transport this probe is allowed to use.
func bodyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func checkBody(t *testing.T, status int, body string) Result {
	t.Helper()
	server := bodyServer(t, status, body)
	got, err := Check(t.Context(), Config{Origin: server.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return got
}

func TestDiagnoseRecognizedFirstBootStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		body          string
		wantDiagnosis string
		wantRemedy    string
	}{
		{
			name:          "missing administrative enumeration",
			body:          `{"status":"setup_blocked","store":"up","leader":true,"setup_required":true,"code":"cross_tenant_admin_pool_not_configured"}`,
			wantDiagnosis: "cross-tenant admin database pool",
			wantRemedy:    "deploy/postgres/README.md",
		},
		{
			name:          "setup probe failure",
			body:          `{"status":"setup_unavailable","store":"up","leader":true,"setup_required":true,"code":"setup_probe_unavailable"}`,
			wantDiagnosis: "its probe of the capability that setup needs failed",
			wantRemedy:    "Read the engine log",
		},
		{
			name:          "unknown setup state",
			body:          `{"status":"setup_unavailable","store":"up","leader":true,"code":"setup_state_unavailable"}`,
			wantDiagnosis: "cannot read whether first-boot",
			wantRemedy:    "Read the engine log",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkBody(t, http.StatusServiceUnavailable, tc.body)
			if got.Outcome != NotReady || got.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("result = %+v, want not-ready HTTP 503", got)
			}
			if !strings.Contains(got.Diagnosis, tc.wantDiagnosis) {
				t.Fatalf("diagnosis = %q, want it to contain %q", got.Diagnosis, tc.wantDiagnosis)
			}
			if !strings.Contains(got.Remedy, tc.wantRemedy) {
				t.Fatalf("remedy = %q, want it to contain %q", got.Remedy, tc.wantRemedy)
			}
		})
	}
}

// TestDiagnoseIgnoresUnrelatedFields proves the ignore-and-continue rule: a
// field this build has never seen must not cost a live install its diagnosis.
func TestDiagnoseIgnoresUnrelatedFields(t *testing.T) {
	t.Parallel()
	got := checkBody(t, http.StatusServiceUnavailable,
		`{"schema":"v9","status":"setup_blocked","future":{"nested":[1,2,{"deep":true}]},`+
			`"code":"cross_tenant_admin_pool_not_configured","trailing_number":3}`)
	if got.Outcome != NotReady || got.Diagnosis == "" {
		t.Fatalf("result = %+v, want not-ready with a diagnosis", got)
	}
}

// TestDiagnoseWithholdsHintOnUnusableBody is the containment witness. Every case
// still yields the status-driven verdict; none of them yields a sentence.
func TestDiagnoseWithholdsHintOnUnusableBody(t *testing.T) {
	t.Parallel()
	recognized := `"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured"`
	// The oversize fixture is a fixed byte count on purpose. Sizing it from
	// maxDiagnosticBody would grow with the constant, so raising the bound would
	// keep this case green while the probe buffered megabytes of a body it does
	// not control. The guard below is what makes the bound the discriminator.
	const oversizeBytes = 32 << 10
	oversize := fmt.Sprintf(`{%s,"pad":%q}`, recognized, strings.Repeat("a", oversizeBytes))
	if len(oversize) <= maxDiagnosticBody {
		t.Fatalf("oversize fixture is %d bytes, no longer past the %d-byte bound", len(oversize), maxDiagnosticBody)
	}
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "not json", body: "service unavailable"},
		{name: "not an object", body: `["setup_blocked"]`},
		{name: "unknown code", body: `{"status":"setup_blocked","code":"something_new"}`},
		{name: "unknown status", body: `{"status":"wedged","code":"cross_tenant_admin_pool_not_configured"}`},
		{name: "status without code", body: `{"status":"setup_blocked"}`},
		{name: "code of the wrong type", body: `{"status":"setup_blocked","code":404}`},
		{name: "code as an object", body: `{"status":"setup_blocked","code":{}}`},
		{name: "status as an array", body: `{"status":["setup_blocked"],"code":"cross_tenant_admin_pool_not_configured"}`},
		{name: "duplicate code", body: `{"status":"setup_blocked","code":"setup_probe_unavailable","code":"cross_tenant_admin_pool_not_configured"}`},
		{name: "duplicate status", body: `{"status":"ok","status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured"}`},
		{name: "malformed", body: `{"status":"setup_blocked","code":`},
		{name: "trailing document", body: `{` + recognized + `}{"status":"ok"}`},
		{name: "trailing garbage", body: `{` + recognized + `} not-json`},
		{name: "oversized", body: oversize},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkBody(t, http.StatusServiceUnavailable, tc.body)
			if got.Outcome != NotReady || got.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("result = %+v, want the status-driven not-ready verdict", got)
			}
			if got.Diagnosis != "" || got.Remedy != "" {
				t.Fatalf("result = %+v, want no diagnosis for an unusable body", got)
			}
		})
	}
}

// TestDiagnoseBoundIsTheDiscriminator pairs the oversized case above with the
// same document just inside the bound. Without it, "oversized yields no hint"
// would also pass if the padding field alone had broken the parse.
func TestDiagnoseBoundIsTheDiscriminator(t *testing.T) {
	t.Parallel()
	envelope := len(`{"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured","pad":""}`)
	body := fmt.Sprintf(`{"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured","pad":%q}`,
		strings.Repeat("a", maxDiagnosticBody-envelope))
	if len(body) != maxDiagnosticBody {
		t.Fatalf("fixture is %d bytes, want exactly the %d-byte bound", len(body), maxDiagnosticBody)
	}
	got := checkBody(t, http.StatusServiceUnavailable, body)
	if got.Diagnosis == "" {
		t.Fatalf("result = %+v, want a diagnosis for a body exactly at the bound", got)
	}
}

// TestDiagnoseRefusesABodyOneBytePastTheBound holds the overflow byte that
// decides between refusing an oversized body and silently truncating it into one
// that parses. The excess here is trailing whitespace after a complete recognized
// object, so a read of only maxDiagnosticBody bytes would leave a parseable,
// recognized document behind and answer with a diagnosis for a body that never
// fit. Independent review found the +1 unpinned: the oversized case above pads
// INSIDE the object, where truncation breaks the JSON and hides the difference.
func TestDiagnoseRefusesABodyOneBytePastTheBound(t *testing.T) {
	t.Parallel()
	const object = `{"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured"}`
	atBound := object + strings.Repeat(" ", maxDiagnosticBody-len(object))
	pastBound := object + strings.Repeat(" ", maxDiagnosticBody+1-len(object))
	if len(atBound) != maxDiagnosticBody || len(pastBound) != maxDiagnosticBody+1 {
		t.Fatalf("fixtures are %d and %d bytes, want %d and %d",
			len(atBound), len(pastBound), maxDiagnosticBody, maxDiagnosticBody+1)
	}
	if control := checkBody(t, http.StatusServiceUnavailable, atBound); control.Diagnosis == "" {
		t.Fatalf("body of exactly %d bytes = %+v, want the recognized diagnosis", maxDiagnosticBody, control)
	}
	got := checkBody(t, http.StatusServiceUnavailable, pastBound)
	if got.Outcome != NotReady || got.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("result = %+v, want the received not-ready status", got)
	}
	if got.Diagnosis != "" || got.Remedy != "" {
		t.Fatalf("body of %d bytes = %+v, want no diagnosis one byte past the bound", len(pastBound), got)
	}
}

// TestDiagnoseNeverCrossesTheVerdict keeps the status the sole authority: a body
// cannot talk a 503 into readiness, and a 200 needs no body to be ready.
func TestDiagnoseNeverCrossesTheVerdict(t *testing.T) {
	t.Parallel()
	notReady := checkBody(t, http.StatusServiceUnavailable, `{"status":"ok","store":"up","leader":true,"setup_required":false}`)
	if notReady.Outcome != NotReady || notReady.Diagnosis != "" {
		t.Fatalf("503 claiming ok = %+v, want not-ready with no diagnosis", notReady)
	}
	ready := checkBody(t, http.StatusOK, `{"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured"}`)
	if ready.Outcome != Ready || ready.Diagnosis != "" || ready.Remedy != "" {
		t.Fatalf("200 with a blocked body = %+v, want ready with no diagnosis", ready)
	}
}

// TestDiagnoseContainsSecretsAndControlCharacters answers the one question that
// makes body reading risky at all: a readiness body is written by a process this
// probe does not control, and its text must never reach an operator's terminal.
func TestDiagnoseContainsSecretsAndControlCharacters(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-do-not-print"
	hostile := fmt.Sprintf("{\"status\":\"setup_blocked\",\"code\":\"%s\","+
		"\"remedy\":\"PGPASSWORD=%s \\u001b[2J\\u0007 rm -rf /\",\"detail\":\"dsn=postgres://admin:%s@db\"}",
		"%s", secret, secret)
	for _, tc := range []struct {
		name      string
		code      string
		wantHint  bool
		wantPhase string
	}{
		{name: "recognized", code: "cross_tenant_admin_pool_not_configured", wantHint: true, wantPhase: "cross-tenant admin database pool"},
		{name: "unrecognized", code: "brand_new_code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkBody(t, http.StatusServiceUnavailable, fmt.Sprintf(hostile, tc.code))
			if strings.Contains(got.Endpoint+got.Diagnosis+got.Remedy, secret) {
				t.Fatalf("result carried body text: %+v", got)
			}
			for _, r := range got.Diagnosis + got.Remedy {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("result carried control character %q: %+v", r, got)
				}
			}
			if tc.wantHint != (got.Diagnosis != "") {
				t.Fatalf("result = %+v, want hint=%v", got, tc.wantHint)
			}
			if tc.wantHint && !strings.Contains(got.Diagnosis, tc.wantPhase) {
				t.Fatalf("diagnosis = %q, want the sentence held in this binary", got.Diagnosis)
			}
		})
	}
}

// TestDiagnoseTruncatedBodyIsNotInterpreted cuts the connection mid-body, which
// is what a crashing engine does. The prefix goes as far as it goes and must
// still yield nothing: a half-read answer is not an answer.
func TestDiagnoseTruncatedBodyIsNotInterpreted(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\n" +
			"Content-Length: 4096\r\nConnection: close\r\n\r\n")
		_, _ = buf.WriteString(`{"status":"setup_blocked","code":"cross_tenant_admin_pool_not_configured"}`)
		_ = buf.Flush()
	}))
	defer server.Close()

	got, err := Check(t.Context(), Config{Origin: server.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.Outcome != NotReady || got.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("result = %+v, want not-ready HTTP 503", got)
	}
	if got.Diagnosis != "" {
		t.Fatalf("result = %+v, want no diagnosis from a truncated body", got)
	}
}

// TestDiagnoseStallingBodyStaysInsideTheProbeDeadline is why the body read has
// no deadline of its own: it inherits the one the operator already asked for.
func TestDiagnoseStallingBodyStaysInsideTheProbeDeadline(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"setup_blocked",`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer server.Close()
	defer close(release)

	const budget = 400 * time.Millisecond
	start := time.Now()
	got, err := Check(t.Context(), Config{Origin: server.URL, Timeout: budget})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.Outcome != NotReady || got.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("result = %+v, want the received not-ready status", got)
	}
	if got.Diagnosis != "" {
		t.Fatalf("result = %+v, want no diagnosis from a body that never arrived", got)
	}
	if elapsed > 10*budget {
		t.Fatalf("probe took %s with a %s deadline; the body read escaped the bound", elapsed, budget)
	}
}

// TestDiagnoseStopsReadingAnEndlessBody separates the two bounds that look like
// one. Refusing a body once it turns out to be too large still buffers all of
// it; the probe has to stop pulling bytes at the bound. An engine that streams
// without end is exactly what a first-boot installer must not be held open by,
// so the witness is the clock: the read returns long before the deadline it was
// given, which it cannot do if it is draining the whole stream first.
func TestDiagnoseStopsReadingAnEndlessBody(t *testing.T) {
	t.Parallel()
	chunk := bytes.Repeat([]byte("a"), 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"setup_blocked","pad":"`))
		flusher, _ := w.(http.Flusher)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	defer server.Close()

	const budget = 5 * time.Second
	start := time.Now()
	got, err := Check(t.Context(), Config{Origin: server.URL, Timeout: budget})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.Outcome != NotReady || got.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("result = %+v, want the received not-ready status", got)
	}
	if got.Diagnosis != "" {
		t.Fatalf("result = %+v, want no diagnosis from an endless body", got)
	}
	if elapsed > budget/2 {
		t.Fatalf("probe spent %s of a %s deadline; it drained the body instead of stopping at the bound",
			elapsed, budget)
	}
}

// TestCheckRefusedConnectionIsUnmeasurable keeps the third answer distinct: a
// closed port is not a not-ready service, and reporting it as one would let the
// installer blame the engine for a port nobody is listening on.
func TestCheckRefusedConnectionIsUnmeasurable(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot reserve a loopback port: %v", err)
	}
	origin := "http://" + listener.Addr().String()
	if cerr := listener.Close(); cerr != nil {
		t.Fatalf("close listener: %v", cerr)
	}

	got, err := Check(t.Context(), Config{Origin: origin, Timeout: 2 * time.Second})
	if err == nil {
		t.Fatalf("Check on a closed port = %+v, want an unmeasurable error", got)
	}
	if got.Outcome != Unmeasurable || got.StatusCode != 0 || got.Diagnosis != "" {
		t.Fatalf("result = %+v, want unmeasurable with no status and no diagnosis", got)
	}
}
