// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
)

// The witness secrets. Each is a TOY value that never existed anywhere: they are
// chosen so a single strings.Contains over the wire is an unambiguous verdict,
// and so no real credential is ever needed to exercise this.
const (
	witnessDSNPassword  = "S3cr3t-P4ss-nunca-real"
	witnessURLToken     = "AbCdEf0123456789xyzXYZ0123"
	witnessVendorKey    = "AKIAQQQWWWEEERRRTTTY" // AWS access-key SHAPE, not a key
	witnessLDAPPassword = "b1nd-P4ssw0rd-nunca-real"
	witnessInternalHost = "db.internal.corp"
	redactionMarkPrefix = "[REDACTED:"
)

// dsnConnectError is the encargo's first example: a driver error that embedded
// the whole connection string, password included. Nothing in the engine builds
// this — an upstream library does, and 287 sites log it whole.
func dsnConnectError() error {
	return fmt.Errorf("dial: %w", errors.New(
		"failed to connect to `user=alma database=alma`: "+
			"parse \"postgres://alma:"+witnessDSNPassword+"@"+witnessInternalHost+":5432/alma?sslmode=require\": "+
			"server error (FATAL: password authentication failed for user \"alma\" (SQLSTATE 28P01))"))
}

// httpConnectorError is what net/http hands EVERY connector in this tree:
// *url.Error, whose URL field carries the full request target — query string,
// and therefore any token in it, included.
func httpConnectorError() error {
	return &url.Error{
		Op:  "Get",
		URL: "https://idp." + witnessInternalHost + "/scim/v2/Users?access_token=" + witnessURLToken,
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
	}
}

// ldapBindError is the encargo's second example: the bind DN and the bind
// password in one upstream string.
func ldapBindError() error {
	return errors.New(`LDAP Result Code 49 "Invalid Credentials": bind ` +
		`bindDN=cn=svc-olivares,ou=service,dc=corp,dc=local ` +
		`bindPassword=` + witnessLDAPPassword + ` failed against ldaps://ldap.` + witnessInternalHost + `:636`)
}

// vendorKeyError carries a credential the CANONICAL catalog recognizes by its
// vendor prefix and the core-owned floor deliberately does not. It is the
// witness that tells the two redaction paths apart.
func vendorKeyError() error {
	return errors.New("upstream rejected credential " + witnessVendorKey + " for role sync")
}

// streamCapture opens /v1/console/logs/stream against a REAL listener, the way
// the console does, and returns everything the viewer received. It is
// deliberately not a ResponseRecorder: what this test judges is what crosses the
// wire.
func streamCapture(t *testing.T, h *harness, admin string, emit func()) string {
	t.Helper()

	srv := httptest.NewServer(h.srv.Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/console/logs/stream", nil)
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+admin)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream = %d %s", resp.StatusCode, body)
	}

	reader := bufio.NewReader(resp.Body)
	// The handler writes ": connected" only AFTER Subscribe, so reading that line
	// proves the subscription is live before we emit. Without it the emit can race
	// ahead of Subscribe and the assertions would pass for the wrong reason — a
	// viewer that received nothing is not a viewer that was protected.
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read stream preamble: %v", err)
	}

	emit()

	var got strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		line, rerr := reader.ReadString('\n')
		got.WriteString(line)
		if rerr != nil {
			break
		}
		if strings.Contains(got.String(), "event: log") && strings.HasSuffix(got.String(), "\n\n") {
			break
		}
	}
	return got.String()
}

// leakCase is one upstream error and the exact bytes that must not survive it,
// plus the diagnosis fragments that must.
type leakCase struct {
	name    string
	err     error
	secrets []string
	keep    []string
}

func upstreamLeakCases() []leakCase {
	return []leakCase{
		{
			name:    "postgres_dsn_password",
			err:     dsnConnectError(),
			secrets: []string{witnessDSNPassword},
			keep:    []string{witnessInternalHost, "28P01", "password authentication failed"},
		},
		{
			name:    "http_connector_url_token",
			err:     httpConnectorError(),
			secrets: []string{witnessURLToken},
			keep:    []string{"idp." + witnessInternalHost, "/scim/v2/Users", "connection refused"},
		},
		{
			name:    "ldap_bind_password",
			err:     ldapBindError(),
			secrets: []string{witnessLDAPPassword},
			keep:    []string{"Invalid Credentials", "cn=svc-olivares", "ldap." + witnessInternalHost},
		},
	}
}

// assertRedactedFrame is the shared verdict: no witness secret survived, the
// operator can still see WHAT/WHERE/WHY, and a marker says redaction happened.
func assertRedactedFrame(t *testing.T, where, frame string, c leakCase) {
	t.Helper()
	if frame == "" {
		t.Fatalf("%s/%s: nothing captured; there is no evidence either way", where, c.name)
	}
	for _, secret := range c.secrets {
		if strings.Contains(frame, secret) {
			t.Errorf("%s/%s: the secret %q crossed the console surface verbatim:\n%s", where, c.name, secret, frame)
		}
	}
	if !strings.Contains(frame, redactionMarkPrefix) {
		t.Errorf("%s/%s: no redaction marker — an operator cannot tell a scrubbed line from a clean one:\n%s",
			where, c.name, frame)
	}
	// Property 1 of the encargo, and the one that is load-bearing: redacting is
	// not silencing. Every fragment below is what an operator acts ON.
	for _, keep := range c.keep {
		if !strings.Contains(frame, keep) {
			t.Errorf("%s/%s: redaction destroyed the diagnosis — %q is gone:\n%s", where, c.name, keep, frame)
		}
	}
}

// TestConsoleLogStream_UpstreamSecretNeverReachesTheViewer is the encargo's
// reproduction, turned into the regression. Each case logs a raw upstream error
// exactly the way modules/governance/roster.go:626 does, and reads the result off
// the SSE wire as a system:admin.
func TestConsoleLogStream_UpstreamSecretNeverReachesTheViewer(t *testing.T) {
	for _, c := range upstreamLeakCases() {
		t.Run(c.name, func(t *testing.T) {
			h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
			frame := streamCapture(t, h, admin, func() {
				logger.Debug("governance: roster snapshot failed", "err", c.err)
			})
			t.Logf("wire capture:\n%s", frame)
			assertRedactedFrame(t, "stream", frame, c)
		})
	}
}

// TestConsoleLogBuffer_UpstreamSecretNeverReachesTheViewer covers the SECOND
// consumer of the same ring. The console seeds the viewer from /logs/buffer
// before it attaches the stream, so a seam that only protected the stream would
// leave the backfill leaking.
func TestConsoleLogBuffer_UpstreamSecretNeverReachesTheViewer(t *testing.T) {
	for _, c := range upstreamLeakCases() {
		t.Run(c.name, func(t *testing.T) {
			h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
			logger.Debug("governance: roster snapshot failed", "err", c.err)
			r := h.do("GET", "/v1/console/logs/buffer", admin, nil, nil)
			if r.code != http.StatusOK {
				t.Fatalf("buffer = %d %s", r.code, r.raw)
			}
			assertRedactedFrame(t, "buffer", r.raw, c)
		})
	}
}

// TestConsoleLogStream_ErrorAttrArrivesAsReadableText is the OTHER half of the
// same root cause, and it fails on main for the opposite reason.
//
// LogBroker.Handle stored the live Go value (`attrs[k] = a.Value.Any()`) and let
// encoding/json decide. errors.New and fmt.Errorf return types whose fields are
// UNEXPORTED, so json.Marshal renders them `{}` — the operator is handed an empty
// object where the diagnosis should be. Types with EXPORTED fields (*url.Error,
// *net.OpError, *os.PathError) marshal in full, secrets included. Same line, two
// opposite failures, decided by which concrete type the upstream happened to
// return.
//
// Rendering the value to text is what fixes the mute half — and on its own it
// would turn a type-dependent leak into a universal one. That is why the render
// and the redaction are one change and not two.
func TestConsoleLogStream_ErrorAttrArrivesAsReadableText(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)

	frame := streamCapture(t, h, admin, func() {
		logger.Debug("governance: roster snapshot failed", "err", dsnConnectError())
	})
	t.Logf("wire capture:\n%s", frame)

	if strings.Contains(frame, `"err":{}`) {
		t.Fatalf("the error attribute arrived as an empty object — the operator has NOTHING to act on:\n%s", frame)
	}
	payload := frameData(t, frame)
	attrs, ok := payload["attrs"].(map[string]any)
	if !ok {
		t.Fatalf("no attrs in the entry: %s", frame)
	}
	text, ok := attrs["err"].(string)
	if !ok {
		t.Fatalf("attrs.err is %T, want a rendered string: %s", attrs["err"], frame)
	}
	if !strings.Contains(text, "28P01") {
		t.Errorf("rendered error lost the driver's own code: %q", text)
	}
}

// TestLogBroker_CoreFloorRedactsWithNoSeamWired pins the fallback path. An
// embedder that never wires the canonical catalog still gets the structural
// credential shapes scrubbed — redaction is not something a missing wire can
// switch off. newLogHandlerHarness leaves the seam unwired, which is exactly the
// configuration under test.
func TestLogBroker_CoreFloorRedactsWithNoSeamWired(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 100)
	logger.Debug("connector: sync failed", "err", dsnConnectError())

	r := h.do("GET", "/v1/console/logs/buffer", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("buffer = %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, witnessDSNPassword) {
		t.Errorf("with no redactor seam wired the DSN password crossed the console surface:\n%s", r.raw)
	}
	// The floor is deliberately STRUCTURAL: it recognizes a secret by its frame
	// (userinfo in an authority, key=value, Bearer, PEM, JWT), never by a vendor
	// prefix. The vendor catalog is single-owner in modules/security, so this
	// witness must survive here — and must NOT survive once the seam is wired
	// (TestLogBroker_WiredCatalogRedactsVendorShapes).
	logger.Debug("connector: sync failed", "err", vendorKeyError())
	r = h.do("GET", "/v1/console/logs/buffer", admin, nil, nil)
	if !strings.Contains(r.raw, witnessVendorKey) {
		t.Errorf("the core floor claimed a vendor-prefix shape it does not own; "+
			"the two paths are then indistinguishable and neither can be seen to fail:\n%s", r.raw)
	}
}

// frameData pulls the JSON object out of an SSE `data:` line, or out of a plain
// JSON body, so an assertion can read fields instead of substrings.
func frameData(t *testing.T, frame string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		raw, found := strings.CutPrefix(line, "data: ")
		if !found {
			continue
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("unmarshal SSE data: %v (%q)", err, raw)
		}
		return out
	}
	t.Fatalf("no data: line in frame:\n%s", frame)
	return nil
}

// TestLogBroker_DerivedHandlersKeepTheRedactor is not an edge case: it is the
// MAIN path. core/runtime/lifecycle.go:115 hands every module
// `r.log.With("module", name)`, so every one of the ~148 module call sites this
// seam exists for logs through a handler produced by WithAttrs — never through
// the root one. A derivation that dropped the injected catalog would leave the
// composition root wired to a handler nothing logs through, the floor quietly
// doing all the work, and every test above still green.
func TestLogBroker_DerivedHandlersKeepTheRedactor(t *testing.T) {
	const canary = "canary-only-the-injected-redactor-knows"

	for _, derive := range []struct {
		name string
		of   func(*slog.Logger) *slog.Logger
	}{
		{"root", func(l *slog.Logger) *slog.Logger { return l }},
		{"WithAttrs", func(l *slog.Logger) *slog.Logger { return l.With("module", "governance") }},
		{"WithGroup", func(l *slog.Logger) *slog.Logger { return l.WithGroup("gov") }},
		{"WithAttrs twice", func(l *slog.Logger) *slog.Logger { return l.With("module", "gov").With("tenant", "t1") }},
		{"WithGroup then WithAttrs", func(l *slog.Logger) *slog.Logger { return l.WithGroup("gov").With("tenant", "t1") }},
	} {
		t.Run(derive.name, func(t *testing.T) {
			level := &slog.LevelVar{}
			level.Set(slog.LevelDebug)
			// A redactor with a shape NEITHER the core floor nor the canonical
			// catalog owns, so only the injected function can remove it. If the
			// derivation drops it, the canary survives and this goes red.
			broker := api.NewLogBroker(
				slog.NewTextHandler(io.Discard, nil), 32, level,
				api.WithLogRedactor(func(s string) (string, int) {
					if !strings.Contains(s, canary) {
						return s, 0
					}
					return strings.ReplaceAll(s, canary, "[REDACTED:canary]"), 1
				}),
			)
			derive.of(slog.New(broker)).Error("upstream failed", "err", "before "+canary+" after")

			entries, _ := broker.Buffer(api.LogFilter{}, 0)
			raw, err := json.Marshal(entries)
			if err != nil {
				t.Fatalf("marshal ring: %v", err)
			}
			// ABSENCE IS NOT ENOUGH, and this is the assertion the external
			// contrast was right to challenge: an empty ring contains no canary
			// either. A derivation that stopped capturing, or dropped attrs, would
			// have passed a bare "does not contain" check while proving nothing.
			// So: exactly one entry, and the benign text on BOTH sides of the
			// canary still present — which can only be true if this handler
			// captured the record AND redacted its value.
			if len(entries) != 1 {
				t.Fatalf("%s handler captured %d entries, want 1 — absence here would prove nothing: %s",
					derive.name, len(entries), raw)
			}
			for _, keep := range []string{"upstream failed", "before ", " after"} {
				if !strings.Contains(string(raw), keep) {
					t.Fatalf("%s handler lost %q, so this entry is not the one under test: %s",
						derive.name, keep, raw)
				}
			}
			if !strings.Contains(string(raw), "[REDACTED:canary]") {
				t.Errorf("the injected redactor did not run on a %s handler (no marker): %s", derive.name, raw)
			}
			if strings.Contains(string(raw), canary) {
				t.Errorf("the injected redactor did not run on a %s handler: %s", derive.name, raw)
			}
		})
	}
}

// TestLogBroker_FloorCoversEveryShapeItDocuments closes what the external
// contrast named: the floor's doc comment claims five structural shapes, and only
// two of them (URL userinfo, key=value) were exercised through the broker. Killing
// the Bearer, PEM or JWT rule was a SURVIVING mutant — the catalog's own unit test
// covers them, but nothing proved the FLOOR still offered them once the seam is
// unwired, which is the configuration where the floor is all there is.
//
// It also covers the MESSAGE path. Every other test here puts the secret in an
// attribute, so making redactLogMessage the identity function stayed green.
func TestLogBroker_FloorCoversEveryShapeItDocuments(t *testing.T) {
	shapes := []struct {
		rule    string
		text    string
		secret  string
		survive string // the diagnosis around it that must NOT be eaten
	}{
		{"url-userinfo", `parse "postgres://alma:` + witnessDSNPassword + `@` + witnessInternalHost + `:5432/x"`,
			witnessDSNPassword, witnessInternalHost},
		{"key-value-secret", "reject: access_token=" + witnessURLToken + " on /scim/v2/Users",
			witnessURLToken, "/scim/v2/Users"},
		{"bearer-token", "proxy denied Authorization: Bearer bEaReRvALue0123456789 upstream 401",
			"bEaReRvALue0123456789", "upstream 401"},
		{"jwt", "assertion eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3OCJ9.dBjftJeZ4CVPmB92K27uhb rejected",
			"dBjftJeZ4CVPmB92K27uhb", "rejected"},
		{"private-key", "loaded -----BEGIN PRIVATE KEY-----\nQUJDREVGR0hJSktMTU5PUFFS\n-----END PRIVATE KEY-----\n from disk",
			"QUJDREVGR0hJSktMTU5PUFFS", "from disk"},
	}

	for _, s := range shapes {
		for _, where := range []string{"message", "attr"} {
			t.Run(s.rule+"/"+where, func(t *testing.T) {
				// UNWIRED on purpose: newLogHandlerHarness passes no redactor, so
				// the core floor is the only thing that can answer here.
				h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 32)
				if where == "message" {
					logger.Error(s.text)
				} else {
					logger.Error("connector: upstream refused", "err", s.text)
				}

				r := h.do("GET", "/v1/console/logs/buffer", admin, nil, nil)
				if r.code != http.StatusOK {
					t.Fatalf("buffer = %d %s", r.code, r.raw)
				}
				if !strings.Contains(r.raw, redactionMarkPrefix+s.rule+"]") {
					t.Errorf("the floor did not emit [REDACTED:%s] for the %s path:\n%s", s.rule, where, r.raw)
				}
				if strings.Contains(r.raw, s.secret) {
					t.Errorf("the %s secret survived the floor on the %s path:\n%s", s.rule, where, r.raw)
				}
				if !strings.Contains(r.raw, s.survive) {
					t.Errorf("the floor ate the diagnosis %q on the %s path:\n%s", s.survive, where, r.raw)
				}
			})
		}
	}
}

// TestLogBroker_ModuleAndAttrKeysAreScrubbedToo covers the fields the contrast
// found traveling around the seam: entry.Module (which WithGroup sets from its
// argument verbatim) and the attribute KEYS, both of which the console prints.
func TestLogBroker_ModuleAndAttrKeysAreScrubbedToo(t *testing.T) {
	const dsn = "postgres://svc:" + witnessDSNPassword + "@" + witnessInternalHost + "/db"

	for _, c := range []struct {
		name string
		emit func(*slog.Logger)
	}{
		{"module attr on the record", func(l *slog.Logger) { l.Error("boom", "module", dsn) }},
		{"module attr via With", func(l *slog.Logger) { l.With("module", dsn).Error("boom") }},
		{"WithGroup name", func(l *slog.Logger) { l.WithGroup(dsn).Error("boom") }},
		{"attribute key", func(l *slog.Logger) { l.Error("boom", dsn, "value") }},
		{"key inside a group", func(l *slog.Logger) { l.Error("boom", slog.Group("g", dsn, "value")) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 32)
			c.emit(logger)
			r := h.do("GET", "/v1/console/logs/buffer", admin, nil, nil)
			if r.code != http.StatusOK {
				t.Fatalf("buffer = %d %s", r.code, r.raw)
			}
			if !strings.Contains(r.raw, `"message":"boom"`) {
				t.Fatalf("the entry never arrived, so its absence proves nothing:\n%s", r.raw)
			}
			if strings.Contains(r.raw, witnessDSNPassword) {
				t.Errorf("a secret reached the console through the %s:\n%s", c.name, r.raw)
			}
		})
	}
}

// TestLogBroker_NonFiniteFloatAndPanickingValueStayServable covers the
// availability half. encoding/json REFUSES NaN and ±Inf, and the SSE handler
// drops a frame whose marshal fails — the entry would silently never arrive.
// renderAny now calls Error()/String() where nothing did before, so a typed-nil
// receiver or a broken method could panic inside a log handler and take down the
// goroutine that logged.
func TestLogBroker_NonFiniteFloatAndPanickingValueStayServable(t *testing.T) {
	h, logger, admin := newLogHandlerHarness(t, slog.LevelDebug, 32)

	logger.Error("telemetry", "ratio", math.NaN(), "ceiling", math.Inf(1), "floor", math.Inf(-1))
	logger.Error("upstream", "err", (*panickyError)(nil))
	logger.Error("upstream", "val", panickyStringer{})

	r := h.do("GET", "/v1/console/logs/buffer", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("buffer = %d %s", r.code, r.raw)
	}
	var envelope struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.raw), &envelope); err != nil {
		t.Fatalf("the buffer response is not valid JSON, so a non-finite float broke the surface: %v\n%s", err, r.raw)
	}
	if len(envelope.Items) != 3 {
		t.Fatalf("items = %d, want 3 — an entry was dropped rather than served: %s", len(envelope.Items), r.raw)
	}
	if !strings.Contains(r.raw, "NaN") {
		t.Errorf("the non-finite float lost its value instead of being rendered: %s", r.raw)
	}
	if !strings.Contains(r.raw, "panicked in its own String/Error method") {
		t.Errorf("a panicking value did not produce a diagnostic placeholder: %s", r.raw)
	}
}

// panickyError is the classic typed-nil: a *T with a value-dereferencing Error().
type panickyError struct{ msg string }

func (e *panickyError) Error() string { return "boom: " + e.msg }

type panickyStringer struct{}

func (panickyStringer) String() string { panic("half-built value") }
