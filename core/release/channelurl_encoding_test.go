// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// channelurl_encoding_test.go pins ONE rule of the resolver: a percent escape in the
// endpoint's path is READ, never subtracted.
//
// It exists because the first cut measured the segments on the DECODED path and then cut
// the root out of the RAW endpoint by those decoded lengths. Every escape moved the cut
// left by the bytes it saved, so the failure grew with the encoding and was invisible for
// the unencoded tags the batteries used: `…/releases/tag/v1%20beta` resolved to the root
// `…/releases/t`, and `…/releases/%74ag/v1` to `…/releases/%` — a root with half an escape
// in it. Nothing refused; the client simply asked a URL that addresses nothing and reported
// a 404 the operator could not act on.
//
// The cases below therefore assert the WHOLE constructed URL, not the tag: a resolver that
// decodes the tag correctly and still cuts the root by the wrong number of bytes passes any
// test that only reads Tag().

func TestResolveChannelKeepsRawSegmentBoundaries(t *testing.T) {
	t.Parallel()
	const asset = "olivares_26.8.0_linux_amd64.tar.gz"
	cases := []struct {
		name     string
		endpoint string
		wantTag  string
		wantMani string
		wantArt  string
	}{{
		name:     "an encoded space in the tag",
		endpoint: "https://github.com/o/r/releases/tag/v1%20beta",
		wantTag:  "v1 beta",
		wantMani: "https://github.com/o/r/releases/download/v1%20beta/stable-manifest.json",
		wantArt:  "https://github.com/o/r/releases/download/v1%20beta/" + asset,
	}, {
		name:     "an encoded plus in the tag stays a plus in a path segment",
		endpoint: "https://github.com/o/r/releases/tag/v1%2Bbeta",
		wantTag:  "v1+beta",
		wantMani: "https://github.com/o/r/releases/download/v1+beta/stable-manifest.json",
		wantArt:  "https://github.com/o/r/releases/download/v1+beta/" + asset,
	}, {
		name:     "an encoded percent in the tag is re-escaped, not doubled away",
		endpoint: "https://github.com/o/r/releases/tag/v1%25beta",
		wantTag:  "v1%beta",
		wantMani: "https://github.com/o/r/releases/download/v1%25beta/stable-manifest.json",
		wantArt:  "https://github.com/o/r/releases/download/v1%25beta/" + asset,
	}, {
		name:     "an encoded non-ASCII tag",
		endpoint: "https://github.com/o/r/releases/tag/v1%C3%B1",
		wantTag:  "v1ñ",
		wantMani: "https://github.com/o/r/releases/download/v1%C3%B1/stable-manifest.json",
		wantArt:  "https://github.com/o/r/releases/download/v1%C3%B1/" + asset,
	}, {
		// url.Parse accepts the raw bytes; the resolver must produce the SAME addresses as
		// the encoded spelling above, or the two ways of writing one tag address two releases.
		name:     "a literal non-ASCII tag resolves to the same release",
		endpoint: "https://github.com/o/r/releases/tag/v1ñ",
		wantTag:  "v1ñ",
		wantMani: "https://github.com/o/r/releases/download/v1%C3%B1/stable-manifest.json",
		wantArt:  "https://github.com/o/r/releases/download/v1%C3%B1/" + asset,
	}, {
		name:     "an encoded dot in the tag",
		endpoint: "https://github.com/o/r/releases/tag/v1%2E0",
		wantTag:  "v1.0",
		wantMani: "https://github.com/o/r/releases/download/v1.0/stable-manifest.json",
		wantArt:  "https://github.com/o/r/releases/download/v1.0/" + asset,
	}, {
		// ⛔ AN ENCODED SLASH IS PART OF THE TAG, NEVER A SEPARATOR. On the decoded path this
		// endpoint is six segments and matches no shape, so github.com REFUSED it and a
		// foreign host swallowed it as a static base. A branch-shaped tag is ordinary.
		name:     "an encoded slash stays inside the tag and is escaped once on the way out",
		endpoint: "https://github.com/o/r/releases/tag/a%2Fb",
		wantTag:  "a/b",
		wantMani: "https://github.com/o/r/releases/download/a%2Fb/stable-manifest.json",
		wantArt:  "https://github.com/o/r/releases/download/a%2Fb/" + asset,
	}, {
		// The SHAPE is matched on the decoded value, so an escaped keyword still names its
		// position; the ROOT keeps the bytes the operator typed.
		name:     "an encoded shape keyword is the keyword",
		endpoint: "https://github.com/o/r/releases/%74ag/v1.2.3",
		wantTag:  "v1.2.3",
		wantMani: "https://github.com/o/r/releases/download/v1.2.3/stable-manifest.json",
		wantArt:  "https://github.com/o/r/releases/download/v1.2.3/" + asset,
	}, {
		name:     "an encoded root segment is kept encoded in the root",
		endpoint: "https://github.com/o/r/%72eleases/tag/v1.2.3",
		wantTag:  "v1.2.3",
		wantMani: "https://github.com/o/r/%72eleases/download/v1.2.3/stable-manifest.json",
		wantArt:  "https://github.com/o/r/%72eleases/download/v1.2.3/" + asset,
	}, {
		name:     "an encoded owner and repository are kept encoded",
		endpoint: "https://github.com/%6Fwner/wid%67et/releases/tag/v1.2.3",
		wantTag:  "v1.2.3",
		wantMani: "https://github.com/%6Fwner/wid%67et/releases/download/v1.2.3/stable-manifest.json",
		wantArt:  "https://github.com/%6Fwner/wid%67et/releases/download/v1.2.3/" + asset,
	}, {
		name:     "a foreign host with an encoded tag on the download shape",
		endpoint: "https://mirror.example.test/u/releases/download/v1%20beta",
		wantTag:  "v1 beta",
		wantMani: "https://mirror.example.test/u/releases/download/v1%20beta/stable-manifest.json",
		wantArt:  "https://mirror.example.test/u/releases/download/v1%20beta/" + asset,
	}, {
		name:     "a foreign host with an encoded root on the latest shape",
		endpoint: "https://mirror.example.test/u/%72eleases/latest/download",
		wantTag:  "",
		wantMani: "https://mirror.example.test/u/%72eleases/latest/download/stable-manifest.json",
		wantArt:  "https://mirror.example.test/u/%72eleases/download/v26.8.0/" + asset,
	}, {
		// A tag whose decoded value is one of the shape words. The POSITION decides, and the
		// encoding must not change which position it is read in.
		name:     "an encoded tag whose value is a shape keyword",
		endpoint: "https://github.com/acme/widget/releases/tag/%72eleases",
		wantTag:  "releases",
		wantMani: "https://github.com/acme/widget/releases/download/releases/stable-manifest.json",
		wantArt:  "https://github.com/acme/widget/releases/download/releases/" + asset,
	}, {
		// ⛔ NOT NORMALISED. path.Clean would fold `/u/../v` to `/v` and address a base the
		// operator did not write; the server owns that resolution, not this process.
		name:     "a dot-dot segment in the base is carried through, not cleaned",
		endpoint: "https://mirror.example.test/u/../v/releases/tag/v1.2.3",
		wantTag:  "v1.2.3",
		wantMani: "https://mirror.example.test/u/../v/releases/download/v1.2.3/stable-manifest.json",
		wantArt:  "https://mirror.example.test/u/../v/releases/download/v1.2.3/" + asset,
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			l, err := ResolveChannel(c.endpoint, ChannelStable)
			if err != nil {
				t.Fatalf("ResolveChannel(%q): %v", c.endpoint, err)
			}
			if !l.ReleaseAssets() {
				t.Fatalf("%q must resolve to the release-asset layout", c.endpoint)
			}
			if l.Tag() != c.wantTag {
				t.Fatalf("Tag() = %q, want %q", l.Tag(), c.wantTag)
			}
			if got := l.ManifestURL(); got != c.wantMani {
				t.Fatalf("ManifestURL() = %q, want %q", got, c.wantMani)
			}
			if got := l.SignatureURL(); got != c.wantMani+".sig" {
				t.Fatalf("SignatureURL() = %q, want the manifest URL plus .sig", got)
			}
			got, err := l.ArtifactURL("26.8.0", "olivares_26.8.0_linux_amd64.tar.gz")
			if err != nil {
				t.Fatalf("ArtifactURL: %v", err)
			}
			if got != c.wantArt {
				t.Fatalf("ArtifactURL() = %q, want %q", got, c.wantArt)
			}
			// Every address this layout produces must survive a re-parse with the segment
			// count it was built with. A root cut inside an escape (`…/releases/%`) does not.
			for _, u := range []string{l.ManifestURL(), l.SignatureURL(), got} {
				if _, err := url.Parse(u); err != nil {
					t.Fatalf("the resolver produced an unparseable URL %q: %v", u, err)
				}
			}
		})
	}
}

func TestResolveChannelEncodedDirectoryBase(t *testing.T) {
	t.Parallel()
	// NON-FIRING DIRECTION: the mirror fallback keeps the bytes it was given too. A base that
	// is percent-encoded must not be re-spelled, or the manifest is fetched from another path.
	l, err := ResolveChannel("https://updates.example.test/oliv%61res/v%201/", ChannelSecurity)
	if err != nil {
		t.Fatalf("static mirror rejected: %v", err)
	}
	if l.ReleaseAssets() {
		t.Fatal("a static base must keep the directory layout")
	}
	if want := "https://updates.example.test/oliv%61res/v%201/security/manifest.json"; l.ManifestURL() != want {
		t.Fatalf("ManifestURL() = %q, want %q", l.ManifestURL(), want)
	}
}

func TestResolveChannelMalformedEscapesAreRefused(t *testing.T) {
	t.Parallel()
	// A truncated or non-hex escape has no reading. url.Parse refuses it, and the refusal has
	// to survive here rather than reach the transport as an address nobody can resolve.
	for _, raw := range []string{
		"https://github.com/o/r/releases/tag/v1%zz",
		"https://github.com/o/r/releases/tag/v1%2",
		"https://github.com/o/r/releases/tag/v1%",
		"https://github.com/o/r/%7releases/tag/v1",
		"https://mirror.example.test/u/releases/tag/v1%gg",
		"https://mirror.example.test/u%/releases/latest/download",
	} {
		if got, err := ResolveChannel(raw, ChannelStable); err == nil {
			t.Fatalf("%s carries a malformed escape and must be refused; got %+v", raw, got)
		}
	}
	// The split under the cut, driven directly. url.Parse refuses these before ResolveChannel
	// reaches it, so the belt costs one branch and is what keeps that ordering safe to change.
	if segs, err := splitRawPath("/a/b%zz/c"); err == nil {
		t.Fatalf("splitRawPath must refuse a malformed escape; got %+v", segs)
	}
	// NON-FIRING, and the two facts the cut depends on: an encoded slash does not split, and
	// start points at the RAW offset of the segment.
	segs, err := splitRawPath("/a/b%2Fc//d/")
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 3 || segs[1].value != "b/c" || segs[2].value != "d" {
		t.Fatalf("splitRawPath = %+v, want three segments with the encoded slash inside the second", segs)
	}
	if segs[1].start != 3 || segs[2].start != 10 {
		t.Fatalf("splitRawPath starts = %d, %d, want 3, 10 (raw offsets, not decoded ones)", segs[1].start, segs[2].start)
	}
}

func TestResolveChannelSeparatorRuns(t *testing.T) {
	t.Parallel()
	// Repeated and trailing separators are ACCEPTED today and stay accepted: the endpoint is
	// the operator's, and collapsing it would address a path they did not write. What must not
	// happen is a cut that lands inside a segment because an empty one was counted.
	cases := []struct {
		endpoint string
		wantTag  string
		wantMani string
	}{{
		endpoint: "https://github.com/o/r/releases/tag/v1%20beta/",
		wantTag:  "v1 beta",
		wantMani: "https://github.com/o/r/releases/download/v1%20beta/stable-manifest.json",
	}, {
		endpoint: "https://github.com/o/r/releases/tag/v1%20beta///",
		wantTag:  "v1 beta",
		wantMani: "https://github.com/o/r/releases/download/v1%20beta/stable-manifest.json",
	}, {
		endpoint: "https://mirror.example.test/u//releases/download/v1%20beta",
		wantTag:  "v1 beta",
		wantMani: "https://mirror.example.test/u//releases/download/v1%20beta/stable-manifest.json",
	}, {
		endpoint: "https://mirror.example.test/u/releases//download/v1%20beta",
		wantTag:  "v1 beta",
		wantMani: "https://mirror.example.test/u/releases//download/v1%20beta/stable-manifest.json",
	}}
	for _, c := range cases {
		l, err := ResolveChannel(c.endpoint, ChannelStable)
		if err != nil {
			t.Fatalf("ResolveChannel(%q): %v", c.endpoint, err)
		}
		if l.Tag() != c.wantTag {
			t.Fatalf("%s -> Tag() = %q, want %q", c.endpoint, l.Tag(), c.wantTag)
		}
		if got := l.ManifestURL(); got != c.wantMani {
			t.Fatalf("%s -> ManifestURL() = %q, want %q", c.endpoint, got, c.wantMani)
		}
	}
}

func TestResolveChannelBareFragmentDelimiterIsRefused(t *testing.T) {
	t.Parallel()
	// `https://h/base#` parses with an EMPTY Fragment, so the RawQuery/ForceQuery/Fragment
	// tests all miss it while the delimiter is still in the endpoint. It used to be accepted
	// as the directory base `https://h/base#`, and every URL built on it addressed
	// `https://h/base` with the channel and the filename swallowed as a fragment.
	for _, raw := range []string{
		"https://mirror.example.test/updates#",
		"https://github.com/o/r#",
		"https://github.com/o/r/releases/tag/v1.2.3#",
	} {
		if got, err := ResolveChannel(raw, ChannelStable); err == nil {
			t.Fatalf("%s carries a fragment delimiter and must be refused; got %+v", raw, got)
		}
	}
	// NON-FIRING: the same endpoints without the delimiter still resolve.
	for _, raw := range []string{
		"https://mirror.example.test/updates",
		"https://github.com/o/r",
		"https://github.com/o/r/releases/tag/v1.2.3",
	} {
		if _, err := ResolveChannel(raw, ChannelStable); err != nil {
			t.Fatalf("%s must still resolve: %v", raw, err)
		}
	}
}

func TestEndpointPathStartRefusesAPathItCannotReadBack(t *testing.T) {
	t.Parallel()
	// The deny-closed belt under the cut, driven directly: ResolveChannel refuses the query
	// and fragment delimiters before it calls this, so no endpoint reaches it with a mismatch
	// today. The belt is what makes that ordering safe to change, and it only means something
	// if it actually refuses.
	u, err := url.Parse("https://h/a/b")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := endpointPathStart("https://h/a/b", u); !ok {
		t.Fatal("the honest case must be accepted")
	}
	for _, trimmed := range []string{
		"https://h/a/b#frag", // bytes after the path
		"https://h/a/b?q=1",  // the same, as a query
		"https://h/a/DIFFERENT",
		"https://h/a%zz/b", // an escape the path cannot be read back through
		"h/a/b",            // no authority delimiter at all
	} {
		if _, ok := endpointPathStart(trimmed, u); ok {
			t.Fatalf("%q does not read back as %q and must be refused", trimmed, u.Path)
		}
	}
}

// TestResolveChannelAddressesTheServerItNames is the causal end of the file: the strings
// above are only right if they reach the right place on a real server with the credentials
// the operator configured. A returned string cannot show either.
func TestResolveChannelAddressesTheServerItNames(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []seenRequest
	seen := func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), got...)
	}
	reset := func() {
		mu.Lock()
		defer mu.Unlock()
		got = nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		mu.Lock()
		got = append(got, seenRequest{target: r.RequestURI, user: u, pass: p, ok: ok})
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	// Fabricated credentials for a loopback fixture. They are a live credential on the wire
	// and the point of the case: display containment must not disarm authentication.
	const authority = "ops:s3cr3t-fixture@"

	fetch := func(t *testing.T, urls ...string) {
		t.Helper()
		for _, u := range urls {
			resp, err := srv.Client().Get(u)
			if err != nil {
				t.Fatalf("GET %s: %v", u, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}

	t.Run("a pinned release with an encoded tag", func(t *testing.T) {
		reset()
		endpoint := "http://" + authority + host + "/o/r/releases/tag/v1%20beta"
		l, err := ResolveChannel(endpoint, ChannelStable)
		if err != nil {
			t.Fatalf("ResolveChannel(%q): %v", endpoint, err)
		}
		art, err := l.ArtifactURL("26.8.0", "olivares_26.8.0_linux_amd64.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		fetch(t, l.ManifestURL(), l.SignatureURL(), art)
		want := []string{
			"/o/r/releases/download/v1%20beta/stable-manifest.json",
			"/o/r/releases/download/v1%20beta/stable-manifest.json.sig",
			"/o/r/releases/download/v1%20beta/olivares_26.8.0_linux_amd64.tar.gz",
		}
		assertTargets(t, seen(), want)
		assertAuth(t, seen())
	})

	t.Run("an encoded slash reaches the server as one segment", func(t *testing.T) {
		reset()
		endpoint := "http://" + authority + host + "/o/r/releases/tag/a%2Fb"
		l, err := ResolveChannel(endpoint, ChannelStable)
		if err != nil {
			t.Fatalf("ResolveChannel(%q): %v", endpoint, err)
		}
		fetch(t, l.ManifestURL())
		// ⛔ `%2F`, NOT `/`: the tag is one segment. Sending it decoded would ask a different
		// path, and escaping it twice (`%252F`) would ask for a release named `a%2Fb`.
		assertTargets(t, seen(), []string{"/o/r/releases/download/a%2Fb/stable-manifest.json"})
		assertAuth(t, seen())
	})

	t.Run("the latest pointer and the directory fallback", func(t *testing.T) {
		reset()
		latest := "http://" + authority + host + "/o/r/%72eleases/latest/download"
		l, err := ResolveChannel(latest, ChannelStable)
		if err != nil {
			t.Fatalf("ResolveChannel(%q): %v", latest, err)
		}
		fetch(t, l.ManifestURL())
		dir := "http://" + authority + host + "/mir%72or/base"
		d, err := ResolveChannel(dir, ChannelSecurity)
		if err != nil {
			t.Fatalf("ResolveChannel(%q): %v", dir, err)
		}
		if d.ReleaseAssets() {
			t.Fatal("a static base must keep the directory layout")
		}
		fetch(t, d.ManifestURL())
		assertTargets(t, seen(), []string{
			"/o/r/%72eleases/latest/download/stable-manifest.json",
			"/mir%72or/base/security/manifest.json",
		})
		assertAuth(t, seen())
	})
}

// seenRequest is what the loopback fixture actually received: the request target as it
// arrived on the wire (escapes intact) and the credential the client presented.
type seenRequest struct {
	target string
	user   string
	pass   string
	ok     bool
}

func assertTargets(t *testing.T, got []seenRequest, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("the server saw %d requests, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].target != want[i] {
			t.Fatalf("request %d target = %q, want %q", i, got[i].target, want[i])
		}
	}
}

func assertAuth(t *testing.T, got []seenRequest) {
	t.Helper()
	for i, g := range got {
		if !g.ok || g.user != "ops" || g.pass != "s3cr3t-fixture" {
			t.Fatalf("request %d lost the configured credential: ok=%v user=%q", i, g.ok, g.user)
		}
	}
}
