// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package updatecheck

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/release"
)

// Status.Error is serialized to the console as `update.error`. These tests pin what may
// appear there: owned stage/reason text, a display-only scheme://host, and a numeric HTTP
// status. They are written against the ACTUAL Status and its ACTUAL JSON, because the DTO
// is what the operator reads.
//
// Every credential below is fabricated and every server is loopback.

const (
	contUser     = "mirroruser"
	contPassword = "s3cr3t-FIXTURE-PASSWORD"
	contQuery    = "decoy-query-TOKEN"
	contSegment  = "decoy-path-SEGMENT"
	contField    = "decoy-body-FIELD"
	contLocation = "decoy-location-HEADER"
)

// contDisclosures are the strings that must never reach the operator-facing field, each
// standing for one disclosure class the correction closes.
var contDisclosures = map[string]string{
	"endpoint password":      contPassword,
	"endpoint username":      contUser,
	"endpoint query token":   contQuery,
	"endpoint path segment":  contSegment,
	"remote manifest field":  contField,
	"remote Location header": contLocation,
}

// contChannel serves one signed channel pair behind HTTP Basic authentication and records
// every request line it answers. The 401 matters: a containment change that also disarmed
// the credential would fail these tests instead of passing quietly.
type contServer struct {
	*httptest.Server
	seen []string
	auth []bool
}

func contChannelServer(t *testing.T, channel string, manifest []byte, sig string) *contServer {
	t.Helper()
	cs := &contServer{}
	base := "/" + contSegment + "/" + channel
	mux := http.NewServeMux()
	guard := func(body func() []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			cs.seen = append(cs.seen, r.RequestURI)
			u, p, ok := r.BasicAuth()
			cs.auth = append(cs.auth, ok && u == contUser && p == contPassword)
			if !ok || u != contUser || p != contPassword {
				w.Header().Set("WWW-Authenticate", `Basic realm="channel"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write(body())
		}
	}
	mux.HandleFunc(base+"/manifest.json", guard(func() []byte { return manifest }))
	mux.HandleFunc(base+"/manifest.json.sig", guard(func() []byte { return []byte(sig) }))
	cs.Server = httptest.NewServer(mux)
	t.Cleanup(cs.Close)
	return cs
}

// contSigned builds a validly signed manifest for a channel.
func contSigned(t *testing.T, channel, version string, expires *time.Time) ([]byte, string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	sum := sha256.Sum256([]byte("artifact"))
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, Channel: channel, Version: version,
		ReleasedAt: time.Now().UTC().Add(-time.Hour), Expires: expires,
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Filename: "a.tgz", SHA256: hex.EncodeToString(sum[:])}},
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return mb, base64.StdEncoding.EncodeToString(release.SignManifest(mb, priv)), pub
}

// contCredentialed puts the fabricated credential and the decoy path into the endpoint the
// operator would configure.
func contCredentialed(base string) string {
	return strings.Replace(base, "http://", "http://"+contUser+":"+contPassword+"@", 1) + "/" + contSegment
}

// contLoopbackClient refuses any dial that is not loopback, so a fixture mistake cannot
// become an outbound request. It is also the configured-transport path (Config.Client).
func contLoopbackClient(timeout time.Duration) *http.Client {
	var d net.Dialer
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("test: unparsable address %q", addr)
			}
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("test: refusing a non-loopback dial to %q", addr)
			}
			return d.DialContext(ctx, network, addr)
		},
	}}
}

// requestEcho is what Status repeats back from the request rather than from a diagnostic:
// the configured channel and the running version, each in its own JSON key. The contract
// preserves both, and a caller that configures a decoy as its channel name gets that decoy
// back in `channel` — correctly. So assertContained proves those two keys EQUAL to what the
// caller configured, which is airtight (a field equal to a known value hides nothing), and
// scans every OTHER key for decoys. Scanning the echo would only test the fixture.
type requestEcho struct {
	channel        string
	currentVersion string
}

// assertContained is the whole contract in one place: the operator-facing field, AND the
// JSON the console actually receives, carry nothing but owned text and the request echo.
func assertContained(t *testing.T, name string, st Status, echo requestEcho) {
	t.Helper()
	if st.Channel != echo.channel {
		t.Errorf("%s: channel = %q, want the configured %q", name, st.Channel, echo.channel)
	}
	if st.CurrentVersion != echo.currentVersion {
		t.Errorf("%s: current_version = %q, want the running %q", name, st.CurrentVersion, echo.currentVersion)
	}
	scanned := st
	scanned.Channel, scanned.CurrentVersion = "", ""
	blob, err := json.Marshal(scanned)
	if err != nil {
		t.Fatalf("%s: marshal status: %v", name, err)
	}
	for class, decoy := range contDisclosures {
		if strings.Contains(st.Error, decoy) {
			t.Errorf("%s: Status.Error discloses the %s: %q", name, class, st.Error)
		}
		if strings.Contains(string(blob), decoy) {
			t.Errorf("%s: the Status JSON discloses the %s: %s", name, class, blob)
		}
	}
	if i := strings.IndexFunc(st.Error, func(r rune) bool { return r < 0x20 || r == 0x7f }); i >= 0 {
		t.Errorf("%s: Status.Error carries a control byte at %d: %q", name, i, st.Error)
	}
	if len(st.Error) > maxStatusError {
		t.Errorf("%s: Status.Error is %d bytes, over the %d bound: %q", name, len(st.Error), maxStatusError, st.Error)
	}
	if st.Error != "" && !strings.HasPrefix(st.Error, "update check: ") {
		t.Errorf("%s: a failure must come from the owned funnel, got %q", name, st.Error)
	}
}

// TestCheckContainsEveryFailureStage drives every stage that can set Status.Error against a
// loopback channel whose endpoint carries a fabricated credential, and pins BOTH halves of
// the contract: nothing configured or remote reaches the DTO, and the stages stay apart.
func TestCheckContainsEveryFailureStage(t *testing.T) {
	ctx := context.Background()
	anyKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	client := contLoopbackClient(10 * time.Second)

	// A healthy authenticated channel, reused by the cases that must reach it.
	goodManifest, goodSig, goodKey := contSigned(t, release.ChannelStable, "26.9.0", nil)
	good := contChannelServer(t, release.ChannelStable, goodManifest, goodSig)

	// A 404 channel, a 502 signature, a dead listener and a remote-controlled redirect.
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)

	sigBroken := http.NewServeMux()
	sigBroken.HandleFunc("/"+contSegment+"/stable/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(goodManifest)
	})
	sigBroken.HandleFunc("/"+contSegment+"/stable/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream failure", http.StatusBadGateway)
	})
	sigSrv := httptest.NewServer(sigBroken)
	t.Cleanup(sigSrv.Close)

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/"+contLocation+"?token="+contQuery, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	shortBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write([]byte("{"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(shortBody.Close)

	oversize := append([]byte(`{"schema_version":1,"padding":"`), []byte(strings.Repeat("A", 2<<20))...)
	tooBig := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oversize)
	}))
	t.Cleanup(tooBig.Close)

	// A manifest whose fields are remote text, under a VALID signature: the parser's message
	// is the disclosure, and a valid signature is what gets it past verification.
	hostileBody := []byte(`{"schema_version":1,"` + contField + `":"` + strings.Repeat("Z", 200) + `"}`)
	hostileKey, hostilePriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	hostile := contChannelServer(t, release.ChannelStable, hostileBody,
		base64.StdEncoding.EncodeToString(release.SignManifest(hostileBody, hostilePriv)))

	badSigManifest, _, _ := contSigned(t, release.ChannelStable, "26.9.0", nil)
	_, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	badSigKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	badSig := contChannelServer(t, release.ChannelStable, badSigManifest,
		base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, []byte("other bytes"))))

	unreadableSig := contChannelServer(t, release.ChannelStable, badSigManifest, "%%% not base64 %%%")

	wrongManifest, wrongSig, wrongKey := contSigned(t, release.ChannelStable, "26.9.0", nil)
	wrong := contChannelServer(t, release.ChannelSecurity, wrongManifest, wrongSig)

	past := time.Now().UTC().Add(-time.Hour)
	staleManifest, staleSig, staleKey := contSigned(t, release.ChannelStable, "26.9.0", &past)
	stale := contChannelServer(t, release.ChannelStable, staleManifest, staleSig)

	cases := []struct {
		name string
		cfg  Config
		ctx  context.Context
		want string // the full expected Status.Error, host aside
		host string // "" when the message carries no host
	}{
		{
			name: "resolver refuses a query and a fragment",
			cfg: Config{Endpoint: "https://" + contUser + ":" + contPassword + "@example.test/base?token=" +
				contQuery + "#" + contQuery, Channel: "stable", PubKey: anyKey},
			want: "update check: " + stageResolve + " (https://example.test): " + reasonEndpointQueryFr,
		},
		{
			name: "resolver refuses an unknown channel",
			cfg:  Config{Endpoint: "https://" + contUser + ":" + contPassword + "@example.test/base", Channel: "nightly-" + contQuery, PubKey: anyKey},
			want: "update check: " + stageResolve + " (https://example.test): " + reasonChannelUnknown,
		},
		{
			name: "resolver refuses a relative endpoint",
			cfg:  Config{Endpoint: "example.test/base/" + contSegment, Channel: "stable", PubKey: anyKey},
			want: "update check: " + stageResolve + ": " + reasonEndpointNotURL,
		},
		{
			name: "manifest fetch keeps the numeric status",
			cfg:  Config{Endpoint: contCredentialed(notFound.URL), Channel: "stable", PubKey: anyKey, Client: client},
			want: "update check: " + stageManifest + " (%s): HTTP 404",
			host: notFound.URL,
		},
		{
			name: "signature fetch is a different stage from the manifest",
			cfg:  Config{Endpoint: contCredentialed(sigSrv.URL), Channel: "stable", PubKey: goodKey, Client: client},
			want: "update check: " + stageSignature + " (%s): HTTP 502",
			host: sigSrv.URL,
		},
		{
			name: "a refused connection is a network error, not a URL",
			cfg:  Config{Endpoint: contCredentialed(deadURL), Channel: "stable", PubKey: anyKey, Client: client},
			want: "update check: " + stageManifest + " (%s): " + reasonNetwork,
			host: deadURL,
		},
		{
			name: "a remote Location header cannot name the failure",
			cfg:  Config{Endpoint: contCredentialed(redirector.URL), Channel: "stable", PubKey: anyKey, Client: client},
			want: "update check: " + stageManifest + " (%s): " + reasonNetwork,
			host: redirector.URL,
		},
		{
			name: "a truncated body is a read failure",
			cfg:  Config{Endpoint: contCredentialed(shortBody.URL), Channel: "stable", PubKey: anyKey, Client: client},
			want: "update check: " + stageManifest + " (%s): " + reasonReadFailed,
			host: shortBody.URL,
		},
		{
			name: "an oversize body fails as oversize, not as a malformed manifest",
			cfg:  Config{Endpoint: contCredentialed(tooBig.URL), Channel: "stable", PubKey: anyKey, Client: client},
			want: "update check: " + stageManifest + " (%s): " + reasonOversize,
			host: tooBig.URL,
		},
		{
			name: "a hostile manifest body is rejected without quoting it",
			cfg:  Config{Endpoint: contCredentialed(hostile.URL), Channel: "stable", PubKey: hostileKey, Client: client},
			want: "update check: " + stageVerify + " (%s): " + reasonBadManifest,
			host: hostile.URL,
		},
		{
			name: "a signature that does not verify is its own reason",
			cfg:  Config{Endpoint: contCredentialed(badSig.URL), Channel: "stable", PubKey: badSigKey, Client: client},
			want: "update check: " + stageVerify + " (%s): " + reasonBadSignature,
			host: badSig.URL,
		},
		{
			name: "an unreadable signature is not the same as one that fails to verify",
			cfg:  Config{Endpoint: contCredentialed(unreadableSig.URL), Channel: "stable", PubKey: badSigKey, Client: client},
			want: "update check: " + stageVerify + " (%s): " + reasonBadSigFormat,
			host: unreadableSig.URL,
		},
		{
			name: "a wrong-channel answer names both channels",
			cfg: Config{Endpoint: contCredentialed(wrong.URL), Channel: release.ChannelSecurity,
				PubKey: wrongKey, CurrentVersion: "26.8.0", Client: client},
			want: "update check: " + stageBinding + " (%s): " +
				wrongChannelReason(release.ChannelSecurity, release.ChannelStable),
			host: wrong.URL,
		},
		{
			name: "an expired manifest is a failed check",
			cfg: Config{Endpoint: contCredentialed(stale.URL), Channel: "stable", PubKey: staleKey,
				CurrentVersion: "26.8.0", Client: client},
			want: "update check: " + stageFreshness + " (%s): " + reasonExpired,
			host: stale.URL,
		},
		{
			name: "an unstamped build says so without quoting its stamp",
			cfg: Config{Endpoint: contCredentialed(good.URL), Channel: "stable", PubKey: goodKey,
				CurrentVersion: "dev", Client: client},
			want: "update check: " + stageCompare + ": " + reasonUnstamped + "stable",
		},
		{
			name: "an unparsable local stamp does not reach the console",
			cfg: Config{Endpoint: contCredentialed(good.URL), Channel: "stable", PubKey: goodKey,
				CurrentVersion: "banana-" + contQuery, Client: client},
			want: "update check: " + stageCompare + ": " + reasonVersionUnparsable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ctx
			if tc.ctx != nil {
				c = tc.ctx
			}
			st := Check(c, tc.cfg)
			assertContained(t, tc.name, st, requestEcho{tc.cfg.Channel, tc.cfg.CurrentVersion})
			want := tc.want
			if tc.host != "" {
				want = fmt.Sprintf(tc.want, tc.host)
			}
			if st.Error != want {
				t.Errorf("Status.Error =\n  %q\nwant\n  %q", st.Error, want)
			}
			if st.Available || st.UpToDate {
				t.Errorf("a failed check must make no claim: available=%v up_to_date=%v", st.Available, st.UpToDate)
			}
			if !st.Enabled {
				t.Error("a configured check stays Enabled even when it fails")
			}
		})
	}
}

// TestCheckSeparatesCancellationFromTimeout pins the two cases an operator acts on
// differently: the engine stopping, and the channel not answering.
func TestCheckSeparatesCancellationFromTimeout(t *testing.T) {
	manifest, sig, key := contSigned(t, release.ChannelStable, "26.9.0", nil)
	srv := contChannelServer(t, release.ChannelStable, manifest, sig)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	st := Check(canceled, Config{Endpoint: contCredentialed(srv.URL), Channel: "stable", PubKey: key,
		CurrentVersion: "26.8.0", Client: contLoopbackClient(10 * time.Second)})
	assertContained(t, "canceled", st, requestEcho{"stable", "26.8.0"})
	if want := "update check: " + stageManifest + " (" + srv.URL + "): " + reasonCanceled; st.Error != want {
		t.Errorf("canceled: Status.Error = %q, want %q", st.Error, want)
	}

	// A server that never answers, against a client with a short timeout.
	block := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(block.Close)
	st = Check(context.Background(), Config{Endpoint: contCredentialed(block.URL), Channel: "stable", PubKey: key,
		CurrentVersion: "26.8.0", Client: contLoopbackClient(100 * time.Millisecond)})
	assertContained(t, "timeout", st, requestEcho{"stable", "26.8.0"})
	if want := "update check: " + stageManifest + " (" + block.URL + "): " + reasonTimeout; st.Error != want {
		t.Errorf("timeout: Status.Error = %q, want %q", st.Error, want)
	}
}

// TestCheckStillAddressesAndAuthenticatesTheChannel is the NON-FIRING direction, and it is
// what stops the containment from being an outage: the check must still ask for the exact
// URL the operator configured, still present the credential, and still conclude.
func TestCheckStillAddressesAndAuthenticatesTheChannel(t *testing.T) {
	manifest, sig, key := contSigned(t, release.ChannelStable, "26.9.0", nil)
	srv := contChannelServer(t, release.ChannelStable, manifest, sig)

	st := Check(context.Background(), Config{
		Endpoint: contCredentialed(srv.URL), Channel: "stable", PubKey: key,
		CurrentVersion: "26.8.0", InstallID: "n1", Client: contLoopbackClient(10 * time.Second),
	})
	if st.Error != "" {
		t.Fatalf("the authenticated channel was refused: %q", st.Error)
	}
	if !st.Enabled || !st.Available || st.UpToDate || st.LatestVersion != "26.9.0" {
		t.Fatalf("status = %+v, want an available 26.9.0 over a running 26.8.0", st)
	}
	want := []string{
		"/" + contSegment + "/stable/manifest.json",
		"/" + contSegment + "/stable/manifest.json.sig",
	}
	if len(srv.seen) != len(want) {
		t.Fatalf("requests = %v, want exactly %v", srv.seen, want)
	}
	for i, w := range want {
		if srv.seen[i] != w {
			t.Errorf("request %d = %q, want %q", i, srv.seen[i], w)
		}
		if !srv.auth[i] {
			t.Errorf("request %d did not present the configured credential", i)
		}
	}

	// And the same channel WITHOUT the credential fails: the fixture really is authenticated,
	// so the assertions above are not vacuous.
	st = Check(context.Background(), Config{
		Endpoint: srv.URL + "/" + contSegment, Channel: "stable", PubKey: key,
		CurrentVersion: "26.8.0", Client: contLoopbackClient(10 * time.Second),
	})
	assertContained(t, "unauthenticated", st, requestEcho{"stable", "26.8.0"})
	if want := "update check: " + stageManifest + " (" + srv.URL + "): HTTP 401"; st.Error != want {
		t.Errorf("unauthenticated: Status.Error = %q, want %q", st.Error, want)
	}
}

// TestCheckerCachesTheContainedStatus: the console reads Latest(), not Check(), so the
// containment has to survive the cache.
func TestCheckerCachesTheContainedStatus(t *testing.T) {
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)
	key, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	c := NewChecker(Config{
		Endpoint: contCredentialed(notFound.URL), Channel: "stable", PubKey: key,
		CurrentVersion: "26.8.0", Client: contLoopbackClient(10 * time.Second),
	}, time.Hour)

	assertContained(t, "before refresh", c.Latest(), requestEcho{"stable", "26.8.0"})
	refreshed := c.Refresh(context.Background())
	assertContained(t, "refresh", refreshed, requestEcho{"stable", "26.8.0"})
	assertContained(t, "latest", c.Latest(), requestEcho{"stable", "26.8.0"})
	want := "update check: " + stageManifest + " (" + notFound.URL + "): HTTP 404"
	if refreshed.Error != want || c.Latest().Error != want {
		t.Errorf("Refresh=%q Latest=%q, want %q", refreshed.Error, c.Latest().Error, want)
	}
	if c.Latest().Available || c.Latest().UpToDate {
		t.Errorf("a cached failure must make no claim: %+v", c.Latest())
	}
}

// TestFailIsTheOnlyWriterOfStatusError drives the funnel directly. An unowned error must not
// be able to print itself, and a failure must clear every claim — including one a future
// branch might have set before failing.
func TestFailIsTheOnlyWriterOfStatusError(t *testing.T) {
	raw := errors.New(`release: bad update endpoint "https://u:` + contPassword + `@h/x?token=` + contQuery + `"`)
	st := fail(Status{Enabled: true, Available: true, UpToDate: true, Security: true,
		Advisories: []string{"OSV-2026-1"}, LatestVersion: "26.9.0"}, raw)
	if strings.Contains(st.Error, contPassword) || strings.Contains(st.Error, contQuery) {
		t.Fatalf("an unowned error printed itself: %q", st.Error)
	}
	if st.Error != "update check: failed for an unrecognized reason" {
		t.Errorf("Status.Error = %q, want the unowned-path constant", st.Error)
	}
	if st.Available || st.UpToDate || st.Security || st.Advisories != nil {
		t.Errorf("a failure must clear every claim, got %+v", st)
	}
	if st.LatestVersion != "26.9.0" {
		t.Errorf("the channel version is a separate JSON key and is preserved, got %q", st.LatestVersion)
	}

	// A checkFailure prints its owned text and keeps the cause reachable for a caller that
	// needs it — reachable, never formatted.
	cause := fmt.Errorf("transport: %w", context.Canceled)
	f := &checkFailure{stage: stageManifest, where: "https://h", reason: reasonCanceled, err: cause}
	if got := fail(Status{}, f).Error; got != "update check: "+stageManifest+" (https://h): "+reasonCanceled {
		t.Errorf("Status.Error = %q", got)
	}
	if !errors.Is(f, context.Canceled) {
		t.Error("the cause must stay reachable through Unwrap")
	}
}

func TestClampStatusErrorBoundsTheField(t *testing.T) {
	short := "update check: fine"
	if got := clampStatusError(short); got != short {
		t.Errorf("a short message must pass through, got %q", got)
	}
	long := &checkFailure{stage: stageManifest, where: "https://h", reason: strings.Repeat("ó", 400)}
	got := clampStatusError(long.Error())
	if len(got) > maxStatusError+3 {
		t.Errorf("clamped message is %d bytes, want <= %d", len(got), maxStatusError+3)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a clamped message must say so, got the tail %q", got[len(got)-8:])
	}
	// Cutting inside a multi-byte rune would put invalid UTF-8 into a JSON field.
	if !json.Valid(mustJSON(t, Status{Error: got})) {
		t.Error("a clamped message must still serialize")
	}
	for _, r := range got {
		if r == '�' {
			t.Fatalf("clamped message cut a rune in half: %q", got)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestDisplayEndpointKeepsOnlySchemeAndHost(t *testing.T) {
	longHost := strings.Repeat("h", 120) + ".example"
	for _, tc := range []struct{ in, want string }{
		{"https://" + contUser + ":" + contPassword + "@example.test/" + contSegment + "?q=" + contQuery + "#f", "https://example.test"},
		{"http://127.0.0.1:8080/a/b", "http://127.0.0.1:8080"},
		{"HTTPS://Example.test/x", "https://Example.test"},
		{"", ""},
		{"://", ""},
		{"ftp://example.test/x", ""},
		{"https://host\x1b[31m.example/x", ""},
		{"https://" + longHost + "/x", ""},
		{"https:///nohost", ""},
	} {
		if got := displayEndpoint(tc.in); got != tc.want {
			t.Errorf("displayEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveReasonClassifiesTheConfiguredInput(t *testing.T) {
	for _, tc := range []struct{ endpoint, channel, want string }{
		{"https://example.test/base", "nightly", reasonChannelUnknown},
		{"   ", "stable", reasonEndpointEmpty},
		{"example.test/base", "stable", reasonEndpointNotURL},
		{"https://example.test/base?x=1", "stable", reasonEndpointQueryFr},
		{"https://example.test/base#", "stable", reasonEndpointQueryFr},
		{"https://github.com/one-segment", "stable", reasonEndpointLayout},
		// Check normalises an empty channel to stable BEFORE the resolver runs, so "" never
		// reaches this helper; the default itself is pinned by
		// TestCheckDefaultChannelIsStableAndStillAccepted. Classified as handed, not guessed.
		{"https://example.test/base", "", reasonChannelUnknown},
	} {
		if got := resolveReason(tc.endpoint, tc.channel); got != tc.want {
			t.Errorf("resolveReason(%q, %q) = %q, want %q", tc.endpoint, tc.channel, got, tc.want)
		}
	}
}

func TestVerifyReasonBranchesOnTypedCauses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"no key", release.ErrNoKey, reasonNoKey},
		{"wrapped no key", fmt.Errorf("verify: %w", release.ErrNoKey), reasonNoKey},
		{"bad signature", release.ErrBadSignature, reasonBadSignature},
		{"manifest rejected", &release.ManifestError{}, reasonBadManifest},
		{"unreadable signature", errors.New("release: signature is neither 64 raw bytes nor base64"), reasonBadSigFormat},
	} {
		if got := verifyReason(tc.err); got != tc.want {
			t.Errorf("%s: verifyReason = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A manifest channel this build does not publish must not be echoed: the value comes from
// the remote payload, and ParseManifest's validation is another package's invariant.
func TestWrongChannelReasonOnlyEchoesAPublishedChannel(t *testing.T) {
	got := wrongChannelReason(release.ChannelSecurity, release.ChannelStable)
	if !strings.Contains(got, release.ChannelSecurity) || !strings.Contains(got, release.ChannelStable) {
		t.Errorf("both channels must be named: %q", got)
	}
	hostile := wrongChannelReason(release.ChannelStable, "\x1b[31m"+contField)
	if strings.Contains(hostile, contField) || strings.Contains(hostile, "\x1b") {
		t.Errorf("an unpublished channel must not be echoed: %q", hostile)
	}
}
