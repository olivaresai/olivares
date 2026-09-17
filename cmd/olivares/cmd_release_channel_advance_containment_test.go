// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_release_channel_advance_containment_test.go is the witness for what this command
// PRINTS when it refuses an endpoint, and for the fact that containing the print changed
// nothing about the request.
//
// It exists because the fence resolved the endpoint itself, before buildCommunitySource,
// and returned the resolver's own text. release.ResolveChannel is a library: it quotes the
// raw endpoint so its caller can see what it rejected. An independent review measured the
// consequence at the command level — a fabricated password, a query token and a fragment
// token on stderr from one mistyped `--endpoint` — while `olivares upgrade`, which routes
// the same class of refusal through wrapCommunitySourceResolve, printed none of it.
//
// The second surface is the pinned-endpoint refusal, which echoed `--endpoint %q` and the
// TAG. The tag is a percent-DECODED path segment, so it carries whatever the operator's
// endpoint encoded, terminal escapes included, unbounded.
//
// Fabricated credentials and loopback servers only.

const (
	advUser  = "fenceuser"
	advPass  = "s3cr3t-FENCE-PASSWORD"
	advDecoy = "decoy-fence-LEAKME"
	advESC   = "\x1b[31mPWNED\x1b[0m"
)

func runChannelAdvanceSeparated(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newReleaseVerifyChannelAdvanceCmd()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// assertAdvanceContained is the display rule of this command in one place: no fabricated
// secret, no decoy, no control byte, and a bounded length. The error value is included
// because cobra prints it and a caller may log it.
func assertAdvanceContained(t *testing.T, stdout, stderr string, err error) {
	t.Helper()
	text := ""
	if err != nil {
		text = err.Error()
	}
	combined := stdout + "\n" + stderr + "\n" + text
	for _, leak := range []string{advPass, advUser + ":" + advPass, advDecoy, advESC, "\x1b"} {
		if strings.Contains(combined, leak) {
			t.Fatalf("operator output leaked %q\nstdout=%q\nstderr=%q\nerr=%q", leak, stdout, stderr, text)
		}
	}
	for name, s := range map[string]string{"stdout": stdout, "stderr": stderr, "err": text} {
		for i := 0; i < len(s); i++ {
			if c := s[i]; c < 0x20 && c != '\n' && c != '\t' {
				t.Fatalf("%s holds control byte 0x%02x at %d", name, c, i)
			}
		}
		if len(s) > 4096 {
			t.Fatalf("%s is %d bytes; an operator diagnostic must stay bounded", name, len(s))
		}
	}
}

// advanceFixture is a loopback channel that RECORDS what it was asked and what credential
// it was asked with, and refuses an unauthenticated read. The refusal is what makes the
// positive control causal: display containment that also disarmed authentication would
// fail here rather than pass quietly.
type advanceFixture struct {
	base string

	mu       sync.Mutex
	requests []string
	auths    []string
}

// seen returns the request targets and the credentials presented with them, in order.
func (f *advanceFixture) seen() (targets, auths []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...), append([]string(nil), f.auths...)
}

func newAdvanceFixture(t *testing.T, manifest []byte, sg signer) *advanceFixture {
	t.Helper()
	f := &advanceFixture{}
	sig := sg.sign(manifest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		f.mu.Lock()
		f.requests = append(f.requests, r.RequestURI)
		if ok {
			f.auths = append(f.auths, u+":"+p)
		} else {
			f.auths = append(f.auths, "")
		}
		f.mu.Unlock()
		if !ok || u != advUser || p != advPass {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Both layouts, because the shape under test is the PATH, not the filename:
		// `<root>/…/stable-manifest.json` and `<base>/stable/manifest.json`.
		switch {
		case strings.HasSuffix(r.URL.Path, "manifest.json.sig"):
			_, _ = w.Write([]byte(sig))
		case strings.HasSuffix(r.URL.Path, "manifest.json"):
			_, _ = w.Write(manifest)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	f.base = strings.TrimPrefix(srv.URL, "http://")
	return f
}

// endpoint returns an authenticated endpoint on this fixture.
func (f *advanceFixture) endpoint(path string) string {
	return fmt.Sprintf("http://%s:%s@%s%s", advUser, advPass, f.base, path)
}

func TestVerifyChannelAdvanceResolverRefusalIsDisplaySafe(t *testing.T) {
	// ⛔ THE MEASURED DEFECT. Every one of these is refused by release.ResolveChannel BEFORE
	// buildCommunitySource, so the wrapper the upgrade path relies on was never reached.
	cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
	key := newSigner(t).pub
	cases := []struct {
		name     string
		endpoint string
	}{
		{"userinfo, query and fragment", "https://" + advUser + ":" + advPass + "@example.test/olivares?token=" + advDecoy + "#" + advDecoy},
		{"userinfo on a github shape that is refused", "https://" + advUser + ":" + advPass + "@github.com/acme"},
		{"an endpoint that is not absolute", advUser + ":" + advPass + "@example.test/olivares"},
		{"a control-carrying encoded path on a refused shape", "https://github.com/acme/tree/%1b%5b31mPWNED%1b%5b0m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, err := runChannelAdvanceSeparated(t,
				"--candidate", cand, "--endpoint", c.endpoint, "--pubkey", key)
			if err == nil {
				t.Fatalf("%s must be refused\nstdout=%q", c.endpoint, stdout)
			}
			if code := exitcode.From(err); code != exitcode.Usage {
				t.Fatalf("a wrong endpoint is a USAGE error (%d), got %d: %v", exitcode.Usage, code, err)
			}
			assertAdvanceContained(t, stdout, stderr, err)
			// The refusal is the upgrade path's owned one, not the resolver's text.
			var re *communitySourceResolveError
			if !errors.As(err, &re) {
				t.Fatalf("the refusal must be the owned display-safe type, got %T: %v", err, err)
			}
			// ⛔ AND THE ORIGINAL CAUSE SURVIVES. Containment that also dropped the chain
			// would leave a machine consumer with nothing to branch on, and the resolver's
			// full text is still available to a caller that asks for it explicitly.
			cause := errors.Unwrap(re)
			if cause == nil {
				t.Fatal("the wrapper dropped the resolver's cause")
			}
			if !strings.Contains(cause.Error(), "release: ") {
				t.Fatalf("the preserved cause is not the resolver's: %v", cause)
			}
			if strings.Contains(err.Error(), cause.Error()) {
				t.Fatal("the printed refusal interpolates the resolver's text, which quotes the raw endpoint")
			}
		})
	}
}

func TestVerifyChannelAdvancePinnedRefusalNamesNoTag(t *testing.T) {
	// The pinned refusal is a SHAPE refusal: it needs no value from the endpoint to be
	// actionable, and every value it could quote is unbounded operator input.
	cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
	key := newSigner(t).pub
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "the fence must not reach this", http.StatusTeapot)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	cases := []struct {
		name     string
		endpoint string
	}{
		{"a plain pinned tag with a credential", fmt.Sprintf("http://%s:%s@%s/o/r/releases/tag/v26.8.0", advUser, advPass, host)},
		{"a pinned download base", fmt.Sprintf("http://%s:%s@%s/o/r/releases/download/v26.8.0", advUser, advPass, host)},
		// ⛔ A TAG CARRYING TERMINAL ESCAPES. Tag() is percent-DECODED, so this arrives as raw
		// ESC bytes; the old refusal wrote it to stderr with %s.
		{"an encoded control-byte tag", fmt.Sprintf("http://%s:%s@%s/o/r/releases/tag/%%1b%%5b31mPWNED%%1b%%5b0m-%s", advUser, advPass, host, strings.Repeat("Z", 200))},
		// A tag with an encoded slash resolves to ONE pinned release since the encoding fix,
		// so it is refused here rather than fetched as a static base.
		{"a branch-shaped tag", fmt.Sprintf("http://%s:%s@%s/o/r/releases/tag/rel%%2Fv26.8.0", advUser, advPass, host)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, err := runChannelAdvanceSeparated(t,
				"--candidate", cand, "--endpoint", c.endpoint, "--pubkey", key)
			if err == nil {
				t.Fatalf("a pinned endpoint must be refused\nstdout=%q", stdout)
			}
			if code := exitcode.From(err); code != exitcode.Usage {
				t.Fatalf("a wrong endpoint is a USAGE error (%d), got %d: %v", exitcode.Usage, code, err)
			}
			if !strings.Contains(err.Error(), "CHANNEL HEAD") {
				t.Fatalf("the refusal must still say why, got: %v", err)
			}
			assertAdvanceContained(t, stdout, stderr, err)
			// The endpoint is named as scheme://host and nothing else.
			if !strings.Contains(err.Error(), "http://"+host) {
				t.Fatalf("the refusal must name the host it refused, got: %v", err)
			}
			for _, echoed := range []string{"/releases/", "v26.8.0", "rel/v26.8.0", "ZZZZ"} {
				if strings.Contains(err.Error(), echoed) {
					t.Fatalf("the refusal echoed %q from the endpoint: %v", echoed, err)
				}
			}
		})
	}
	// ⛔ AND IT IS REFUSED BEFORE ANY HTTP EFFECT. The fence's whole subject is the live
	// channel; reading a pinned one first would be the request it exists not to make.
	if n := hits.Load(); n != 0 {
		t.Fatalf("the pinned refusal made %d request(s); it must refuse by shape alone", n)
	}
}

// TestVerifyChannelAdvanceKeepsTheRequestAndTheCredential is the non-firing direction of both
// changes at once: an authenticated endpoint still reaches the right paths with the right
// credential and still reaches the ADVANCES verdict, while stdout names neither.
func TestVerifyChannelAdvanceKeepsTheRequestAndTheCredential(t *testing.T) {
	sg := newSigner(t)
	f := newAdvanceFixture(t, chanManifest(t, "stable", "26.8.0"), sg)
	cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
	endpoint := f.endpoint("/o/r/releases/latest/download")
	stdout, stderr, err := runChannelAdvanceSeparated(t,
		"--candidate", cand, "--endpoint", endpoint, "--pubkey", sg.pub)
	if err != nil {
		t.Fatalf("a forward step must be accepted: %v\nstdout=%q\nstderr=%q", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "ADVANCES") || !strings.Contains(stdout, "signature: VERIFIED") {
		t.Fatalf("the verdict and the authentication must both be reported:\n%s", stdout)
	}
	want := []string{
		"/o/r/releases/latest/download/stable-manifest.json",
		"/o/r/releases/latest/download/stable-manifest.json.sig",
	}
	targets, auths := f.seen()
	if len(targets) != len(want) {
		t.Fatalf("the channel saw %v, want exactly %v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("request %d = %q, want %q", i, targets[i], want[i])
		}
		if auths[i] != advUser+":"+advPass {
			t.Fatalf("request %d presented %q; the configured credential must still be sent", i, auths[i])
		}
	}
	assertAdvanceContained(t, stdout, stderr, nil)
	// The source line is the upgrade path's own description: scheme and host, no path.
	if !strings.Contains(stdout, "live:      public channel stable (release assets of http://"+f.base+", latest release)") {
		t.Fatalf("the source line must be the display-safe description:\n%s", stdout)
	}
}

// TestVerifyChannelAdvanceEncodedPathReachesTheServerVerbatim closes the loop the resolver fix
// opened, on the shapes THIS command accepts (a pinned tag is refused above, so the tag
// cases live in core/release).
func TestVerifyChannelAdvanceEncodedPathReachesTheServerVerbatim(t *testing.T) {
	cases := []struct {
		name string
		path string
		want []string
	}{{
		// ⛔ THE DISCRIMINATING CASE. `/latest/download` is the only part of an accepted
		// release shape an operator can encode, and it is exactly the part the old cut
		// subtracted by DECODED length: the root came back as `…/releases/%6`, and the URL
		// built on it does not even parse.
		name: "an encoded keyword in the latest shape",
		path: "/o/r/releases/%6Catest/download",
		want: []string{
			"/o/r/releases/latest/download/stable-manifest.json",
			"/o/r/releases/latest/download/stable-manifest.json.sig",
		},
	}, {
		// NON-FIRING: the directory fallback takes the base verbatim and always did. It is
		// here so a future normalisation of the base fails a test instead of a channel.
		name: "an encoded directory base is not respelled",
		path: "/mir%72or/v%201",
		want: []string{
			"/mir%72or/v%201/stable/manifest.json",
			"/mir%72or/v%201/stable/manifest.json.sig",
		},
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sg := newSigner(t)
			f := newAdvanceFixture(t, chanManifest(t, "stable", "26.8.0"), sg)
			cand := writeManifestFile(t, chanManifest(t, "stable", "26.8.1"))
			stdout, stderr, err := runChannelAdvanceSeparated(t,
				"--candidate", cand, "--endpoint", f.endpoint(c.path), "--pubkey", sg.pub)
			if err != nil {
				t.Fatalf("unexpected refusal: %v\nstdout=%q\nstderr=%q", err, stdout, stderr)
			}
			if !strings.Contains(stdout, "ADVANCES") {
				t.Fatalf("the run must reach the verdict:\n%s", stdout)
			}
			targets, auths := f.seen()
			if len(targets) != len(c.want) {
				t.Fatalf("the channel saw %v, want %v", targets, c.want)
			}
			for i := range c.want {
				if targets[i] != c.want[i] {
					t.Fatalf("request %d = %q, want %q — the path must reach the server as the shape names it", i, targets[i], c.want[i])
				}
				if auths[i] != advUser+":"+advPass {
					t.Fatalf("request %d presented %q; the configured credential must still be sent", i, auths[i])
				}
			}
			assertAdvanceContained(t, stdout, stderr, nil)
		})
	}
}
