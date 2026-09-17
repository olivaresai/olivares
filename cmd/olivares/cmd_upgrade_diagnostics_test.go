// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/release"
)

// cmd_upgrade_diagnostics_test.go is the C5 (QA-08) witness: a non-2xx or
// transport failure on the upgrade path names the condition and never echoes a
// remote body, userinfo, query, path, or control bytes. Display is scheme://host
// only. Request URLs and live authentication are unchanged. Exit codes stay as
// they are. Local httptest only.

const (
	diagUser     = "mirroruser"
	diagPassword = "s3cr3t-PASSWORD"
	diagDecoy    = "decoy-secret-LEAKME"
	diagESC      = "\x1b[31mRED\x1b[0m"
)

func diagHostileBody() string {
	return "<!DOCTYPE html>\n<html lang=\"en\">\n<p>" + diagESC + " " + diagDecoy + "</p>\n" +
		strings.Repeat("W", 64*1024)
}

func withUserinfo(raw, user, pass string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}

func runUpgradeSeparated(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	// Do not let upgrade exec-probe THIS test binary (it is huge and slow to
	// start). A missing target plus --current-version is the declared path.
	args = append([]string{"--target", filepath.Join(t.TempDir(), "not-installed")}, args...)
	cmd := newUpgradeCmd()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func assertNoLeak(t *testing.T, stdout, stderr string) {
	t.Helper()
	combined := stdout + "\n" + stderr
	for _, leak := range []string{
		diagPassword, diagUser + ":" + diagPassword, diagDecoy,
		"<!DOCTYPE", "<html", diagESC, string(rune(0x1b)),
	} {
		if strings.Contains(combined, leak) {
			t.Fatalf("operator output leaked %q\nstdout=%q\nstderr=%q", leak, stdout, stderr)
		}
	}
	if strings.Contains(combined, "/stable/") || strings.Contains(combined, "/download/") {
		t.Fatalf("operator output included a path\nstdout=%q\nstderr=%q", stdout, stderr)
	}
	if strings.Contains(combined, "?") && strings.Contains(combined, "os=") {
		t.Fatalf("operator output included a query\nstdout=%q\nstderr=%q", stdout, stderr)
	}
	for name, s := range map[string]string{"stdout": stdout, "stderr": stderr} {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c < 0x20 && c != '\n' && c != '\t' {
				t.Fatalf("%s holds control byte 0x%02x at %d", name, c, i)
			}
		}
		if len(s) > 4096 {
			t.Fatalf("%s is %d bytes; operator diagnostics must stay bounded", name, len(s))
		}
	}
}

func dummyPubkey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pub)
}

func TestUpgradeDisplayEndpointIsSchemeAndHostOnly(t *testing.T) {
	longHost := strings.Repeat("a", 90) + ".example"
	cases := []struct {
		in, want string
	}{
		{"http://" + diagUser + ":" + diagPassword + "@127.0.0.1:8731/stable/manifest.json?token=x#f", "http://127.0.0.1:8731"},
		{"https://example.com/a/b", "https://example.com"},
		{"http://[::1]:8080/path", "http://[::1]:8080"},
		{"", displayEndpointUnavailable},
		{"://", displayEndpointUnavailable},
		{"ftp://example.com", displayEndpointUnavailable},
		{"http://host\x1b.example", displayEndpointUnavailable},
		{"http://" + longHost + "/", displayEndpointUnavailable},
		{"http://exämple.test", displayEndpointUnavailable},
	}
	for _, tc := range cases {
		if got := displayEndpoint(tc.in); got != tc.want {
			t.Errorf("displayEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := displayEndpoint(tc.in); strings.Contains(got, diagPassword) {
			t.Errorf("displayEndpoint leaked the password: %q", got)
		}
	}
}

func TestUpgradeNonSuccessNamesTheConditionOnly(t *testing.T) {
	var sawBasic atomic.Bool
	var sawPath atomic.Value
	hostile := diagHostileBody()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawBasic.Store(true)
		}
		sawPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, hostile)
	}))
	t.Cleanup(srv.Close)

	endpoint := withUserinfo(srv.URL, diagUser, diagPassword)
	if !strings.Contains(endpoint, diagPassword) {
		t.Fatal("fixture premise: the configured URL must carry userinfo")
	}

	t.Run("httpGet typed 404, no body, auth still sent", func(t *testing.T) {
		_, err := httpGet(context.Background(), srv.Client(), endpoint+"/stable/manifest.json")
		if !isHTTPStatus(err, http.StatusNotFound) {
			t.Fatalf("isHTTPStatus(., 404) = false, err=%v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("status missing from error: %v", err)
		}
		msg := err.Error()
		if strings.Contains(msg, hostile[:20]) || strings.Contains(msg, diagPassword) || strings.Contains(msg, "/stable/") {
			t.Fatalf("httpGet error leaked body/userinfo/path: %q", msg)
		}
		if !sawBasic.Load() {
			t.Fatal("the request dropped live userinfo authentication; logging redaction must not strip the wire URL")
		}
		if p, _ := sawPath.Load().(string); p != "/stable/manifest.json" {
			t.Fatalf("path on the wire = %q; the request URL must keep its path", p)
		}
		if exitcode.From(err) != exitcode.Err {
			t.Fatalf("exit classification = %d, want %d (unchanged generic)", exitcode.From(err), exitcode.Err)
		}
	})

	t.Run("command stdout and stderr", func(t *testing.T) {
		stdout, stderr, err := runUpgradeSeparated(t,
			"--endpoint", endpoint, "--pubkey", dummyPubkey(t),
			"--check", "--current-version", "26.7.0",
			"--os", "linux", "--arch", "amd64")
		if err == nil {
			t.Fatalf("upgrade --check against a 404 must fail")
		}
		if !isHTTPStatus(err, http.StatusNotFound) {
			t.Fatalf("typed 404 lost through the command wrapper: %v", err)
		}
		if exitcode.From(err) != exitcode.Err {
			t.Fatalf("exit classification = %d, want %d", exitcode.From(err), exitcode.Err)
		}
		if !strings.Contains(stdout, "source: public channel stable (http://127.0.0.1:") {
			t.Fatalf("stdout must name the display endpoint, got %q", stdout)
		}
		if strings.Contains(stdout, diagPassword) || strings.Contains(stdout, diagUser+":") {
			t.Fatalf("stdout leaked userinfo: %q", stdout)
		}
		if !strings.Contains(stderr, "Error:") || !strings.Contains(stderr, "404") {
			t.Fatalf("stderr must name the condition, got %q", stderr)
		}
		assertNoLeak(t, stdout, stderr)
	})
}

func TestUpgradePublic200KeepsAuthAndRedactsSource(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       "26.8.0",
		ReleasedAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Artifacts: []release.Artifact{{
			OS: "linux", Arch: "amd64", Filename: "olivares_26.8.0_linux_amd64.tar.gz",
			SHA256: strings.Repeat("ab", 32), Size: 16,
		}},
	}
	mb, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(release.SignManifest(mb, priv))
	var sawBasic atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/stable/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawBasic.Store(true)
		}
		_, _ = w.Write(mb)
	})
	mux.HandleFunc("/stable/manifest.json.sig", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawBasic.Store(true)
		}
		_, _ = w.Write([]byte(sig))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	endpoint := withUserinfo(srv.URL, diagUser, diagPassword)
	stdout, stderr, runErr := runUpgradeSeparated(t,
		"--endpoint", endpoint, "--pubkey", base64.StdEncoding.EncodeToString(pub),
		"--check", "--current-version", "26.7.0",
		"--os", "linux", "--arch", "amd64")
	if runErr != nil {
		t.Fatalf("positive 200 --check failed: %v\nstdout=%s\nstderr=%s", runErr, stdout, stderr)
	}
	if !sawBasic.Load() {
		t.Fatal("the 200 path dropped live userinfo authentication")
	}
	if !strings.Contains(stdout, "source: public channel stable (http://127.0.0.1:") {
		t.Fatalf("stdout source line missing display endpoint:\n%s", stdout)
	}
	assertNoLeak(t, stdout, stderr)
	if exitcode.From(runErr) != exitcode.OK && runErr != nil {
		t.Fatalf("unexpected classification %d", exitcode.From(runErr))
	}
}

func TestUpgradeTransportErrorOmitsNestedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := withUserinfo(srv.URL, diagUser, diagPassword)
	srv.Close()

	_, err := httpGet(context.Background(), http.DefaultClient, endpoint+"/stable/manifest.json")
	if err == nil {
		t.Fatal("closed server succeeded")
	}
	msg := err.Error()
	if strings.Contains(msg, diagPassword) || strings.Contains(msg, diagUser+":") {
		t.Fatalf("transport error leaked userinfo: %q", msg)
	}
	if strings.Contains(msg, `Get "`) {
		t.Fatalf("raw url.Error nested in operator text: %q", msg)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Fatalf("errors.As(*url.Error) = false; redacting Error() must not strip the chain, err=%v", err)
	}
	if isHTTPStatus(err, http.StatusNotFound) {
		t.Fatal("a transport failure must not look like a 404")
	}
	if exitcode.From(err) != exitcode.Err {
		t.Fatalf("exit classification = %d, want %d", exitcode.From(err), exitcode.Err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = httpGet(ctx, http.DefaultClient, endpoint+"/stable/manifest.json")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request Unwrap lost context.Canceled: %v", err)
	}
	if !errors.As(err, &ue) {
		t.Fatalf("canceled request Unwrap lost *url.Error: %v", err)
	}
	if strings.Contains(err.Error(), diagPassword) {
		t.Fatalf("canceled error leaked userinfo: %q", err)
	}
}

// diagHostileCause is a nested typed transport cause whose Error() contains the
// gated-redirect phrase plus a hostile payload, without "@" or `Get "`. Matching
// that text would print the payload; the owned type is the only authentic signal.
type diagHostileCause struct{ msg string }

func (e *diagHostileCause) Error() string { return e.msg }

type diagRoundTripFunc func(*http.Request) (*http.Response, error)

func (f diagRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpgradeHostileRoundTripperPhraseIsHidden(t *testing.T) {
	phrase := "refusing to follow a redirect " + diagESC + " " + diagDecoy + " payload"
	if strings.Contains(phrase, "@") || strings.Contains(phrase, `Get "`) {
		t.Fatal("fixture premise: the hostile phrase must omit @ and Get \"")
	}
	cause := &diagHostileCause{msg: phrase}
	client := &http.Client{
		Transport: diagRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, cause
		}),
	}
	endpoint := withUserinfo("http://127.0.0.1:1", diagUser, diagPassword)

	assertHidden := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("hostile RoundTripper succeeded")
		}
		msg := err.Error()
		if strings.Contains(msg, phrase) || strings.Contains(msg, diagDecoy) || strings.Contains(msg, diagESC) ||
			strings.Contains(msg, diagPassword) || strings.Contains(msg, `Get "`) {
			t.Fatalf("operator text authenticated an untyped phrase: %q", msg)
		}
		if !strings.Contains(msg, "network error") {
			t.Fatalf("untyped transport must display as network error, got %q", msg)
		}
		var re *upgradeRedirectRefusal
		if errors.As(err, &re) {
			t.Fatalf("a custom RoundTripper must not become *upgradeRedirectRefusal: %+v", re)
		}
		var ue *url.Error
		if !errors.As(err, &ue) {
			t.Fatalf("errors.As(*url.Error) = false, err=%v", err)
		}
		var got *diagHostileCause
		if !errors.As(err, &got) || got != cause {
			t.Fatalf("nested typed cause lost: got=%v err=%v", got, err)
		}
		if strings.Contains(err.Error(), got.Error()) {
			t.Fatalf("Error() formatted the nested cause: %q", err.Error())
		}
	}

	t.Run("httpGet", func(t *testing.T) {
		_, err := httpGet(context.Background(), client, endpoint+"/stable/manifest.json")
		assertHidden(t, err)
	})
	t.Run("gatedGet", func(t *testing.T) {
		o := &upgradeOptions{
			token:    "ota-secret-bearer",
			endpoint: endpoint,
			channel:  "stable", goos: "linux", goarch: "amd64",
		}
		_, err := downloadGated(context.Background(), client, o, "manifest", "")
		assertHidden(t, err)
	})
}

func TestUpgradeOwnedRedirectRefusalRemainsUseful(t *testing.T) {
	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(receiver.Close)
	loc := withUserinfo(receiver.URL, diagUser, diagPassword) + "/steal?token=" + diagDecoy + "#" + diagDecoy

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loc, http.StatusFound)
	}))
	t.Cleanup(gateway.Close)

	endpoint := withUserinfo(gateway.URL, diagUser, diagPassword)
	o := &upgradeOptions{
		token:    "ota-secret-bearer",
		endpoint: endpoint,
		channel:  "stable", goos: "linux", goarch: "amd64",
	}
	_, err := downloadGated(context.Background(), gateway.Client(), o, "manifest", "")
	if err == nil {
		t.Fatal("gated redirect succeeded")
	}
	var re *upgradeRedirectRefusal
	if !errors.As(err, &re) {
		t.Fatalf("owned redirect must be inspectable via errors.As, err=%v", err)
	}
	wantFrom := displayEndpoint(gateway.URL)
	wantTo := displayEndpoint(receiver.URL)
	if re.from != wantFrom || re.to != wantTo {
		t.Fatalf("bounded labels from=%q to=%q, want from=%q to=%q", re.from, re.to, wantFrom, wantTo)
	}
	msg := err.Error()
	if !strings.Contains(msg, "refusing to follow a redirect from "+wantFrom+" to "+wantTo) {
		t.Fatalf("owned refusal must remain useful, got %q", msg)
	}
	if strings.Contains(msg, diagPassword) || strings.Contains(msg, diagDecoy) ||
		strings.Contains(msg, "/steal") || strings.Contains(msg, "token=") {
		t.Fatalf("owned refusal leaked Location/userinfo: %q", msg)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Fatalf("redirect refusal Unwrap lost *url.Error: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("redirected request was sent (%d)", hits.Load())
	}
}

func TestUpgradeCommunitySourceQueryRefusalIsDisplaySafe(t *testing.T) {
	raw := "https://" + diagUser + ":" + diagPassword + "@example.test/olivares?token=" + diagDecoy + "#" + diagDecoy
	if !strings.Contains(raw, diagPassword) || !strings.Contains(raw, "token=") {
		t.Fatal("fixture premise: the endpoint must carry userinfo and a query")
	}

	t.Run("constructor", func(t *testing.T) {
		_, err := buildCommunitySource(raw, release.ChannelStable, &http.Client{})
		if err == nil {
			t.Fatal("query/fragment endpoint resolved")
		}
		var wrapped *communitySourceResolveError
		if !errors.As(err, &wrapped) {
			t.Fatalf("constructor refusal must be typed, err=%v", err)
		}
		if wrapped.reason != communityResolveQueryFrag {
			t.Fatalf("reason = %q, want query/fragment", wrapped.reason)
		}
		if wrapped.endpoint != "https://example.test" {
			t.Fatalf("display endpoint = %q, want https://example.test", wrapped.endpoint)
		}
		inner := errors.Unwrap(err)
		if inner == nil {
			t.Fatal("wrapper stripped the ResolveChannel chain")
		}
		if !strings.Contains(inner.Error(), "query string") {
			t.Fatalf("original resolver error missing from chain: %v", inner)
		}
		msg := err.Error()
		if strings.Contains(msg, diagPassword) || strings.Contains(msg, diagUser+":") ||
			strings.Contains(msg, diagDecoy) || strings.Contains(msg, "token=") ||
			strings.Contains(msg, raw) || strings.Contains(msg, "/olivares") {
			t.Fatalf("constructor Error leaked the raw endpoint: %q", msg)
		}
		if !strings.Contains(msg, communityResolveQueryFrag) {
			t.Fatalf("constructor Error lost the useful reason: %q", msg)
		}
		src, err := buildCommunitySource("https://updates.example.test/olivares", release.ChannelStable, &http.Client{})
		if err != nil {
			t.Fatalf("directory layout without query must still resolve: %v", err)
		}
		if src.(communitySource).layout.ManifestURL() != "https://updates.example.test/olivares/stable/manifest.json" {
			t.Fatalf("resolution changed: %s", src.(communitySource).layout.ManifestURL())
		}
	})

	t.Run("command", func(t *testing.T) {
		stdout, stderr, err := runUpgradeSeparated(t,
			"--endpoint", raw, "--pubkey", dummyPubkey(t),
			"--check", "--current-version", "26.7.0",
			"--os", "linux", "--arch", "amd64")
		if err == nil {
			t.Fatal("upgrade --check with a query endpoint succeeded")
		}
		var wrapped *communitySourceResolveError
		if !errors.As(err, &wrapped) {
			t.Fatalf("CLI refusal must stay typed, err=%v", err)
		}
		if !strings.Contains(stderr, "Error:") || !strings.Contains(stderr, communityResolveQueryFrag) {
			t.Fatalf("stderr must name the query/fragment condition, got %q", stderr)
		}
		if strings.Contains(stdout+stderr, diagPassword) || strings.Contains(stdout+stderr, "token=") ||
			strings.Contains(stdout+stderr, diagDecoy) {
			t.Fatalf("CLI leaked credentials/query\nstdout=%q\nstderr=%q", stdout, stderr)
		}
		assertNoLeak(t, stdout, stderr)
		if isHTTPStatus(err, http.StatusNotFound) {
			t.Fatal("a constructor refusal must not look like a 404")
		}
		if exitcode.From(err) != exitcode.Err {
			t.Fatalf("exit classification = %d, want %d", exitcode.From(err), exitcode.Err)
		}
	})
}

func TestUpgradeGatedNonSuccessNamesTheConditionOnly(t *testing.T) {
	hostile := diagHostileBody()
	// Printable and MIME-legal (Go rejects most controls in header values).
	// Displaying this raw would leak a remote excerpt; the contract maps it
	// to the fixed unknown label.
	unknownHeader := "not-a-real-code<!DOCTYPE html>" + diagDecoy

	t.Run("legacy downloadGated 404", func(t *testing.T) {
		var sawBearer atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer ota-secret-bearer" {
				sawBearer.Store(true)
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, hostile)
		}))
		t.Cleanup(srv.Close)
		o := &upgradeOptions{
			token:    "ota-secret-bearer",
			endpoint: withUserinfo(srv.URL, diagUser, diagPassword),
			channel:  "stable", goos: "linux", goarch: "amd64",
		}
		_, err := downloadGated(context.Background(), srv.Client(), o, "manifest", "")
		if !isHTTPStatus(err, http.StatusNotFound) {
			t.Fatalf("isHTTPStatus(., 404) = false, err=%v", err)
		}
		if !sawBearer.Load() {
			t.Fatal("gated request dropped the bearer; logging redaction must not strip Authorization")
		}
		msg := err.Error()
		if strings.Contains(msg, hostile[:20]) || strings.Contains(msg, diagPassword) || strings.Contains(msg, "ota-secret-bearer") {
			t.Fatalf("downloadGated error leaked body/userinfo/token: %q", msg)
		}
		if strings.Contains(msg, "/download") || strings.Contains(msg, "kind=") {
			t.Fatalf("downloadGated error included path/query: %q", msg)
		}
		if exitcode.From(err) != exitcode.Err {
			t.Fatalf("exit classification = %d, want %d", exitcode.From(err), exitcode.Err)
		}
	})

	t.Run("release-v1 known and unknown codes, no body", func(t *testing.T) {
		var code atomic.Value
		code.Store(v1ErrTokenVersionStale)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(hdrDownloadProtocol, releaseV1Marker)
			w.Header().Set(hdrDownloadError, code.Load().(string))
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, hostile)
		}))
		t.Cleanup(srv.Close)
		o := &upgradeOptions{
			token:    "ota-secret-bearer",
			endpoint: withUserinfo(srv.URL, diagUser, diagPassword),
			channel:  "stable", goos: "linux", goarch: "amd64",
		}
		_, _, err := downloadReleaseV1(context.Background(), srv.Client(), o, "manifest", nil)
		if err == nil {
			t.Fatal("marked 409 succeeded")
		}
		var ref *releaseV1Refusal
		if !errors.As(err, &ref) || ref.status != http.StatusConflict || ref.code != v1ErrTokenVersionStale {
			t.Fatalf("typed refusal lost: %+v / %v", ref, err)
		}
		if !strings.Contains(err.Error(), v1ErrTokenVersionStale) {
			t.Fatalf("known code missing from display: %v", err)
		}
		if strings.Contains(err.Error(), hostile[:20]) || strings.Contains(err.Error(), diagPassword) {
			t.Fatalf("v1 known-code error leaked: %q", err)
		}

		code.Store(unknownHeader)
		_, _, err = downloadReleaseV1(context.Background(), srv.Client(), o, "manifest", nil)
		if err == nil {
			t.Fatal("unknown-code 409 succeeded")
		}
		if !errors.As(err, &ref) {
			t.Fatalf("typed refusal lost on unknown code: %v", err)
		}
		if ref.code != unknownHeader {
			t.Fatalf("raw code for branching = %q, want the header value", ref.code)
		}
		msg := err.Error()
		if !strings.Contains(msg, "("+v1RefusalCodeUnknown+")") {
			t.Fatalf("unknown header must display as %q, got %q", v1RefusalCodeUnknown, msg)
		}
		if strings.Contains(msg, "not-a-code") || strings.Contains(msg, diagESC) || strings.Contains(msg, diagDecoy) {
			t.Fatalf("unknown code displayed a header excerpt: %q", msg)
		}
		if sameVersionConflict(err) {
			t.Fatal("an unknown header must not classify as a same-version conflict")
		}
	})

	t.Run("legacy 200 still succeeds", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("object"))
		}))
		t.Cleanup(srv.Close)
		o := &upgradeOptions{
			token: "ota-secret-bearer", endpoint: srv.URL,
			channel: "stable", goos: "linux", goarch: "amd64",
		}
		got, err := downloadGated(context.Background(), srv.Client(), o, "manifest", "")
		if err != nil {
			t.Fatalf("200 downloadGated: %v", err)
		}
		if string(got) != "object" {
			t.Fatalf("body = %q", got)
		}
	})
}

func TestUpgradeGatedCommandStdoutStderr(t *testing.T) {
	hostile := diagHostileBody()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, hostile)
	}))
	t.Cleanup(srv.Close)

	dataDir := t.TempDir()
	installDevLicense(t, dataDir)
	endpoint := withUserinfo(srv.URL, diagUser, diagPassword)
	stdout, stderr, err := runUpgradeSeparated(t,
		"--enterprise", "--download-protocol", "legacy",
		"--token", "tkn", "--endpoint", endpoint,
		"--pubkey", dummyPubkey(t), "--data-dir", dataDir,
		"--check", "--current-version", "26.7.0",
		"--os", "linux", "--arch", "amd64")
	if err == nil {
		t.Fatal("gated 404 --check succeeded")
	}
	if !isHTTPStatus(err, http.StatusNotFound) {
		t.Fatalf("typed 404 lost: %v", err)
	}
	if exitcode.From(err) != exitcode.Err {
		t.Fatalf("exit classification = %d, want %d", exitcode.From(err), exitcode.Err)
	}
	if !strings.Contains(stdout, "licensed worker channel") {
		t.Fatalf("stdout source line missing:\n%s", stdout)
	}
	if !strings.Contains(stderr, "Error:") || !strings.Contains(stderr, "404") {
		t.Fatalf("stderr must name the condition, got %q", stderr)
	}
	assertNoLeak(t, stdout, stderr)
}

func TestUpgradeReleaseTagDisplayIsBounded(t *testing.T) {
	craft := "%1b[31mPWNED%1b[0m-" + strings.Repeat("Z", 200)
	raw := "https://github.com/owner/repo/releases/tag/" + craft
	src, err := buildCommunitySource(raw, release.ChannelStable, &http.Client{})
	if err != nil {
		t.Fatalf("endpoint refused, probe inconclusive: %v", err)
	}
	cs := src.(communitySource)
	if !strings.Contains(cs.layout.Tag(), "\x1b") {
		t.Fatal("layout tag must remain the exact requested (decoded) tag")
	}
	d := src.describe()
	if strings.Contains(d, "\x1b") || strings.Contains(d, "PWNED") || strings.Contains(d, craft) {
		t.Fatalf("describe() leaked the raw tag: %q", d)
	}
	if !strings.Contains(d, displayReleaseTagUnavailable) {
		t.Fatalf("describe() must use the bounded tag label, got %q", d)
	}
	if len(d) > 256 {
		t.Fatalf("describe() is unbounded: %d bytes", len(d))
	}
	hintSrc, err := buildCommunitySource(raw, release.ChannelSecurity, &http.Client{})
	if err != nil {
		t.Fatalf("security layout: %v", err)
	}
	hint := hintSrc.(communitySource).missingChannelHint()
	if strings.Contains(hint, "\x1b") || strings.Contains(hint, "PWNED") {
		t.Fatalf("missingChannelHint leaked the raw tag: %q", hint)
	}
	if !strings.Contains(hint, displayReleaseTagUnavailable) {
		t.Fatalf("missingChannelHint must use the bounded tag label, got %q", hint)
	}
	payload, err := json.Marshal(upgradeResult{Source: d})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "\\u001b") || strings.Contains(string(payload), "PWNED") {
		t.Fatalf("json source field leaked the raw tag: %s", payload)
	}

	okSrc, err := buildCommunitySource("https://github.com/owner/repo/releases/tag/v26.8.0", release.ChannelStable, &http.Client{})
	if err != nil {
		t.Fatalf("legitimate tag refused: %v", err)
	}
	if got := okSrc.describe(); !strings.Contains(got, "release v26.8.0") {
		t.Fatalf("legitimate tag missing from describe(): %q", got)
	}
	if displayReleaseTag("v26.8.0") != "v26.8.0" {
		t.Fatalf("displayReleaseTag changed a safe tag: %q", displayReleaseTag("v26.8.0"))
	}
}

func TestUpgradeCLISourceLineSanitizesTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	craft := "%1b[31mPWNED%1b[0m" + strings.Repeat("Z", 120)
	endpoint := srv.URL + "/releases/tag/" + craft

	stdout, stderr, err := runUpgradeSeparated(t,
		"--endpoint", endpoint, "--pubkey", dummyPubkey(t),
		"--check", "--current-version", "26.7.0",
		"--os", "linux", "--arch", "amd64")
	if err == nil {
		t.Fatal("404 upgrade --check succeeded")
	}
	if strings.Contains(stdout, "\x1b") || strings.Contains(stderr, "\x1b") ||
		strings.Contains(stdout+stderr, "PWNED") {
		t.Fatalf("command leaked the raw tag\nstdout=%q\nstderr=%q", stdout, stderr)
	}
	if !strings.Contains(stdout, "source:") || !strings.Contains(stdout, displayReleaseTagUnavailable) {
		t.Fatalf("stdout must name the bounded tag, got %q", stdout)
	}
	assertNoLeak(t, stdout, stderr)

	stdout, stderr, err = runUpgradeSeparated(t,
		"--endpoint", endpoint, "--channel", "security", "--pubkey", dummyPubkey(t),
		"--check", "--current-version", "26.7.0",
		"--os", "linux", "--arch", "amd64")
	if err == nil {
		t.Fatal("security 404 succeeded")
	}
	if strings.Contains(stderr, "\x1b") || strings.Contains(stderr, "PWNED") {
		t.Fatalf("stderr hint leaked the raw tag: %q", stderr)
	}
	if !strings.Contains(stderr, displayReleaseTagUnavailable) {
		t.Fatalf("stderr hint must use the bounded tag, got %q", stderr)
	}
	assertNoLeak(t, stdout, stderr)
}

func TestUpgradeReleaseV1RemoteHeadersAreNotEchoed(t *testing.T) {
	tuple := v1Tuple{
		set:             "ent",
		manifestSHA256:  strings.Repeat("a", 64),
		signatureSHA256: strings.Repeat("b", 64),
		version:         "26.8.0",
	}
	valid := func() http.Header {
		h := make(http.Header)
		h.Set(hdrReleaseVersion, tuple.version)
		h.Set(hdrReleaseSet, tuple.set)
		h.Set(hdrManifestSHA256, tuple.manifestSHA256)
		h.Set(hdrSignatureSHA256, tuple.signatureSHA256)
		return h
	}

	t.Run("parseV1Tuple omits remote values and parser text", func(t *testing.T) {
		if _, err := parseV1Tuple(valid()); err != nil {
			t.Fatalf("control: %v", err)
		}
		cases := []struct {
			name   string
			mutate func(http.Header)
			want   string
			forbid []string
		}{
			{
				name:   "whitespace version",
				mutate: func(h http.Header) { h[hdrReleaseVersion] = []string{" 26.8.0"} },
				want:   "whitespace",
				forbid: []string{" 26.8.0", "26.8.0"},
			},
			{
				name:   "unparseable version",
				mutate: func(h http.Header) { h.Set(hdrReleaseVersion, "latest-"+diagDecoy) },
				want:   "unparseable version",
				forbid: []string{diagDecoy, "latest-", "ParseVersion"},
			},
			{
				name:   "malformed set",
				mutate: func(h http.Header) { h.Set(hdrReleaseSet, "biz;"+diagDecoy) },
				want:   "malformed set",
				forbid: []string{diagDecoy, "biz;"},
			},
			{
				name: "malformed digest",
				mutate: func(h http.Header) {
					h.Set(hdrManifestSHA256, "not-hex-"+diagDecoy+strings.Repeat("Q", 80))
				},
				want:   "malformed digests",
				forbid: []string{diagDecoy, "not-hex-"},
			},
		}
		for _, tc := range cases {
			h := valid()
			tc.mutate(h)
			_, err := parseV1Tuple(h)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: want %q, got %v", tc.name, tc.want, err)
			}
			msg := err.Error()
			for _, leak := range tc.forbid {
				if strings.Contains(msg, leak) {
					t.Fatalf("%s: operator error echoed %q: %q", tc.name, leak, msg)
				}
			}
			if len(msg) > 256 {
				t.Fatalf("%s: operator error is unbounded: %d", tc.name, len(msg))
			}
		}
	})

	t.Run("corroborateV1Headers names the condition only", func(t *testing.T) {
		other := valid()
		other.Set(hdrReleaseVersion, "26.9.0")
		err := corroborateV1Headers(other, tuple, "signature")
		if err == nil || !strings.Contains(err.Error(), "names another tuple") {
			t.Fatalf("want a corroboration refusal, got %v", err)
		}
		msg := err.Error()
		for _, leak := range []string{"26.9.0", "26.8.0", tuple.set, tuple.manifestSHA256, tuple.signatureSHA256} {
			if strings.Contains(msg, leak) {
				t.Fatalf("corroboration echoed tuple value %q: %q", leak, msg)
			}
		}
	})

	t.Run("artifact digest mismatch omits remote header", func(t *testing.T) {
		hostileHdr := "not-a-digest " + diagDecoy + " " + strings.Repeat("Q", 2000)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set(hdrDownloadProtocol, releaseV1Marker)
			h.Set(hdrReleaseVersion, tuple.version)
			h.Set(hdrReleaseSet, tuple.set)
			h.Set(hdrManifestSHA256, tuple.manifestSHA256)
			h.Set(hdrSignatureSHA256, tuple.signatureSHA256)
			h.Set(hdrArtifactSHA256, hostileHdr)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("artifact-bytes"))
		}))
		t.Cleanup(srv.Close)
		o := &upgradeOptions{
			token: "ota-secret-bearer", endpoint: withUserinfo(srv.URL, diagUser, diagPassword),
			channel: "stable", goos: "linux", goarch: "amd64",
		}
		s := &gatedSource{o: o, client: srv.Client(), tuple: tuple, resolved: true}
		m := release.Manifest{Version: tuple.version}
		a := release.Artifact{OS: "linux", Arch: "amd64", SHA256: strings.Repeat("c", 64)}
		_, err := s.fetchArtifact(context.Background(), m, a)
		if err == nil {
			t.Fatal("mismatched digest header was accepted")
		}
		msg := err.Error()
		if strings.Contains(msg, diagDecoy) || strings.Contains(msg, hostileHdr[:12]) ||
			strings.Contains(msg, strings.Repeat("c", 64)) {
			t.Fatalf("operator error echoed digest values: %q", msg)
		}
		if len(msg) > 512 {
			t.Fatalf("operator error is unbounded: %d bytes", len(msg))
		}
		if !strings.Contains(msg, "linux/amd64") {
			t.Fatalf("mismatch must still name the descriptor platform, got %q", msg)
		}
	})

	t.Run("matching digest still returns bytes", func(t *testing.T) {
		want := strings.Repeat("c", 64)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set(hdrDownloadProtocol, releaseV1Marker)
			h.Set(hdrReleaseVersion, tuple.version)
			h.Set(hdrReleaseSet, tuple.set)
			h.Set(hdrManifestSHA256, tuple.manifestSHA256)
			h.Set(hdrSignatureSHA256, tuple.signatureSHA256)
			h.Set(hdrArtifactSHA256, want)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("artifact-bytes"))
		}))
		t.Cleanup(srv.Close)
		o := &upgradeOptions{
			token: "tkn", endpoint: srv.URL,
			channel: "stable", goos: "linux", goarch: "amd64",
		}
		s := &gatedSource{o: o, client: srv.Client(), tuple: tuple, resolved: true}
		m := release.Manifest{Version: tuple.version}
		a := release.Artifact{OS: "linux", Arch: "amd64", SHA256: want}
		got, err := s.fetchArtifact(context.Background(), m, a)
		if err != nil {
			t.Fatalf("matching digest refused: %v", err)
		}
		if string(got) != "artifact-bytes" {
			t.Fatalf("body = %q", got)
		}
	})
}

func TestUpgradeNonHTTPSEndpointIsOwnedRefusal(t *testing.T) {
	for _, ep := range []string{
		"ftp://updates.example.test/olivares",
		"htp://updates.example.test/olivares",
		"file:///srv/mirror",
	} {
		_, err := buildCommunitySource(ep, release.ChannelStable, &http.Client{})
		if err == nil {
			t.Fatalf("%s resolved", ep)
		}
		var wrapped *communitySourceResolveError
		if !errors.As(err, &wrapped) || wrapped.reason != communityResolveAbsolute {
			t.Fatalf("%s: want owned HTTP(S) refusal, got %v", ep, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, communityResolveAbsolute) {
			t.Fatalf("%s: missing owned reason: %q", ep, msg)
		}
		if strings.Contains(msg, "network error") || strings.Contains(msg, "unavailable-endpoint") {
			t.Fatalf("%s: fell through to transport: %q", ep, msg)
		}
	}
	if !errors.Is(func() error {
		_, err := buildCommunitySource("ftp://updates.example.test/olivares", release.ChannelStable, &http.Client{})
		return err
	}(), errCommunityNotHTTP) {
		t.Fatal("ftp refusal must keep errCommunityNotHTTP on the chain")
	}

	auth := "https://" + diagUser + ":" + diagPassword + "@updates.example.test/olivares"
	src, err := buildCommunitySource(auth, release.ChannelStable, &http.Client{})
	if err != nil {
		t.Fatalf("authenticated HTTPS refused: %v", err)
	}
	if src.(communitySource).layout.ManifestURL() != "https://"+diagUser+":"+diagPassword+"@updates.example.test/olivares/stable/manifest.json" {
		t.Fatalf("live layout URL lost userinfo: %s", src.(communitySource).layout.ManifestURL())
	}

	for _, ep := range []string{
		"ftp://updates.example.test/olivares",
		"htp://updates.example.test/olivares",
		"file:///srv/mirror",
	} {
		stdout, stderr, err := runUpgradeSeparated(t,
			"--endpoint", ep, "--pubkey", dummyPubkey(t),
			"--check", "--current-version", "26.7.0",
			"--os", "linux", "--arch", "amd64")
		if err == nil {
			t.Fatalf("CLI accepted %s", ep)
		}
		var wrapped *communitySourceResolveError
		if !errors.As(err, &wrapped) || wrapped.reason != communityResolveAbsolute {
			t.Fatalf("CLI %s: want owned refusal, got %v", ep, err)
		}
		if !strings.Contains(stderr, communityResolveAbsolute) {
			t.Fatalf("CLI %s stderr=%q", ep, stderr)
		}
		if strings.Contains(stderr, "network error") || strings.Contains(stdout, "source:") {
			t.Fatalf("CLI %s reached transport\nstdout=%q\nstderr=%q", ep, stdout, stderr)
		}
		assertNoLeak(t, stdout, stderr)
		if exitcode.From(err) != exitcode.Err {
			t.Fatalf("exit classification = %d, want %d", exitcode.From(err), exitcode.Err)
		}
	}
}

func TestUpgradeGatedMalformedEndpointPreservesCause(t *testing.T) {
	_, _, _, err := gatedGet(context.Background(), http.DefaultClient, "http://[", "tok", "/download", nil)
	if err == nil {
		t.Fatal("malformed gated URL succeeded")
	}
	assertNoLeak(t, "", err.Error())
	var te *upgradeTransportError
	if !errors.As(err, &te) {
		t.Fatalf("want *upgradeTransportError, got %T %v", err, err)
	}
	if errors.Unwrap(err) == nil {
		t.Fatal("malformed gated URL dropped the parse cause")
	}
	if strings.Contains(err.Error(), "http://[") {
		t.Fatalf("operator text formatted the raw URL: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Fatalf("want redacted transport text, got %q", err.Error())
	}
}

// The JSON result must use the same bounded source description as text output.
func TestUpgradeJSONSourceFieldRedactsCredentialsAndBoundsTag(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	tag := "v26.8.0-" + strings.Repeat("J", 120)
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       "26.8.0",
		ReleasedAt:    time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC),
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64",
			Filename: "olivares_26.8.0_linux_amd64.tar.gz", SHA256: strings.Repeat("ab", 32), Size: 16}},
	}
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(release.SignManifest(manifest, priv))
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != diagUser || password != diagPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		requests.Add(1)
		switch r.URL.Path {
		case "/releases/download/" + tag + "/stable-manifest.json":
			_, _ = w.Write(manifest)
		case "/releases/download/" + tag + "/stable-manifest.json.sig":
			_, _ = io.WriteString(w, signature)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	endpoint := withUserinfo(srv.URL, diagUser, diagPassword) + "/releases/tag/" + tag
	cmd := newRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"upgrade", "--output", "json",
		"--target", filepath.Join(t.TempDir(), "not-installed"),
		"--endpoint", endpoint, "--pubkey", base64.StdEncoding.EncodeToString(pub),
		"--check", "--current-version", "26.7.0", "--os", "linux", "--arch", "amd64"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("signed channel check failed: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("wanted two authenticated metadata reads, got %d", requests.Load())
	}
	var doc struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	if len(doc.Source) == 0 || len(doc.Source) > 256 || !strings.Contains(doc.Source, displayReleaseTagUnavailable) {
		t.Fatalf("JSON source must contain the bounded source description: %q", doc.Source)
	}
	for _, forbidden := range []string{diagUser, diagPassword, tag, "/releases/", "\x1b"} {
		if strings.Contains(doc.Source, forbidden) {
			t.Fatalf("JSON source disclosed %q", forbidden)
		}
	}
}
