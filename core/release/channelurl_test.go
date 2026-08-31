// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import "testing"

// channelurl_test.go pins the layout decision itself. It lives here, next to the resolver,
// because BOTH readers of a public channel depend on it — `olivares upgrade` and the
// console's update indicator — and a rule proved in only one of their packages is a rule
// the other one can drift away from.

func TestResolveChannelReleaseAssets(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		endpoint    string
		wantTag     string
		wantMani    string
		wantArtifac string
	}{
		{
			name:        "the github.com shorthand is the latest release",
			endpoint:    "https://github.com/olivaresai/olivares",
			wantTag:     "",
			wantMani:    "https://github.com/olivaresai/olivares/releases/latest/download/stable-manifest.json",
			wantArtifac: "https://github.com/olivaresai/olivares/releases/download/v26.8.0/olivares_26.8.0_linux_amd64.tar.gz",
		},
		{
			name:        "a trailing slash changes nothing",
			endpoint:    "https://github.com/olivaresai/olivares/",
			wantTag:     "",
			wantMani:    "https://github.com/olivaresai/olivares/releases/latest/download/stable-manifest.json",
			wantArtifac: "https://github.com/olivaresai/olivares/releases/download/v26.8.0/olivares_26.8.0_linux_amd64.tar.gz",
		},
		{
			name:        "an explicit latest-download base",
			endpoint:    "https://github.com/olivaresai/olivares/releases/latest/download",
			wantTag:     "",
			wantMani:    "https://github.com/olivaresai/olivares/releases/latest/download/stable-manifest.json",
			wantArtifac: "https://github.com/olivaresai/olivares/releases/download/v26.8.0/olivares_26.8.0_linux_amd64.tar.gz",
		},
		{
			name:        "a release PAGE url pins that release",
			endpoint:    "https://github.com/example-owner/example-rehearsal/releases/tag/v0.0.0-rehearsal.1",
			wantTag:     "v0.0.0-rehearsal.1",
			wantMani:    "https://github.com/example-owner/example-rehearsal/releases/download/v0.0.0-rehearsal.1/stable-manifest.json",
			wantArtifac: "https://github.com/example-owner/example-rehearsal/releases/download/v0.0.0-rehearsal.1/olivares_26.8.0_linux_amd64.tar.gz",
		},
		{
			name:        "a release DOWNLOAD base pins that release",
			endpoint:    "https://github.com/example-owner/example-rehearsal/releases/download/v0.0.0-rehearsal.1",
			wantTag:     "v0.0.0-rehearsal.1",
			wantMani:    "https://github.com/example-owner/example-rehearsal/releases/download/v0.0.0-rehearsal.1/stable-manifest.json",
			wantArtifac: "https://github.com/example-owner/example-rehearsal/releases/download/v0.0.0-rehearsal.1/olivares_26.8.0_linux_amd64.tar.gz",
		},
		{
			name:        "the bare releases page means the latest release",
			endpoint:    "https://github.com/olivaresai/olivares/releases",
			wantTag:     "",
			wantMani:    "https://github.com/olivaresai/olivares/releases/latest/download/stable-manifest.json",
			wantArtifac: "https://github.com/olivaresai/olivares/releases/download/v26.8.0/olivares_26.8.0_linux_amd64.tar.gz",
		},
		{
			name:        "a mirror serving the release shape from another host is accepted",
			endpoint:    "https://mirror.example.test/olivaresai/olivares/releases/latest/download",
			wantTag:     "",
			wantMani:    "https://mirror.example.test/olivaresai/olivares/releases/latest/download/stable-manifest.json",
			wantArtifac: "https://mirror.example.test/olivaresai/olivares/releases/download/v26.8.0/olivares_26.8.0_linux_amd64.tar.gz",
		},
	}
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
			if got != c.wantArtifac {
				t.Fatalf("ArtifactURL() = %q, want %q", got, c.wantArtifac)
			}
			// THE ONE THAT MATTERS: an artifact is NEVER addressed through the mutable
			// `latest` pointer. A second read of it can serve a different release than the
			// manifest whose signature was just checked.
			if l.Tag() == "" && got == l.ManifestURL() {
				t.Fatal("the artifact must not resolve through the latest pointer")
			}
		})
	}
}

func TestResolveChannelDirectoryLayout(t *testing.T) {
	t.Parallel()
	// NON-FIRING DIRECTION for the whole file: the mirror fallback FIRMA B keeps must not
	// be swallowed by the release layout.
	l, err := ResolveChannel("https://updates.example.test/olivares/", ChannelSecurity)
	if err != nil {
		t.Fatalf("static mirror rejected: %v", err)
	}
	if l.ReleaseAssets() {
		t.Fatal("a static base must keep the directory layout")
	}
	if want := "https://updates.example.test/olivares/security/manifest.json"; l.ManifestURL() != want {
		t.Fatalf("ManifestURL() = %q, want %q", l.ManifestURL(), want)
	}
	got, err := l.ArtifactURL("26.8.0", "olivares_26.8.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("ArtifactURL: %v", err)
	}
	if want := "https://updates.example.test/olivares/security/olivares_26.8.0_linux_amd64.tar.gz"; got != want {
		t.Fatalf("ArtifactURL() = %q, want %q", got, want)
	}
}

func TestResolveChannelRefusals(t *testing.T) {
	t.Parallel()
	t.Run("a github.com endpoint of neither shape is refused", func(t *testing.T) {
		t.Parallel()
		// Falling through to the directory layout would GET `<path>/stable/manifest.json`
		// from github.com, receive an HTML 404 and report a mistyped endpoint as an
		// unreachable channel.
		for _, raw := range []string{
			"https://github.com/olivaresai",
			"https://github.com/olivaresai/olivares/tree/main",
			"https://github.com/olivaresai/olivares/blob/main/README.md",
			"https://github.com/",
			"https://WWW.GitHub.com/olivaresai/olivares/wiki",
		} {
			if _, err := ResolveChannel(raw, ChannelStable); err == nil {
				t.Fatalf("%s must be refused", raw)
			}
		}
	})
	t.Run("an endpoint that is not an absolute URL is refused", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{"", "   ", "github.com/olivaresai/olivares", "/updates"} {
			if _, err := ResolveChannel(raw, ChannelStable); err == nil {
				t.Fatalf("%q must be refused", raw)
			}
		}
	})
	t.Run("an unknown channel is refused", func(t *testing.T) {
		t.Parallel()
		if _, err := ResolveChannel("https://github.com/olivaresai/olivares", "nightly"); err == nil {
			t.Fatal("an unrecognised channel must be refused, not spliced into a URL")
		}
	})
	t.Run("an empty channel defaults to stable", func(t *testing.T) {
		t.Parallel()
		l, err := ResolveChannel("https://github.com/olivaresai/olivares", "")
		if err != nil {
			t.Fatal(err)
		}
		if l.Channel() != ChannelStable {
			t.Fatalf("Channel() = %q, want %q", l.Channel(), ChannelStable)
		}
	})
	t.Run("an artifact name with a separator is refused, in BOTH layouts", func(t *testing.T) {
		t.Parallel()
		for _, ep := range []string{
			"https://github.com/olivaresai/olivares",
			"https://updates.example.test/olivares",
		} {
			l, err := ResolveChannel(ep, ChannelStable)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"../etc/passwd", "a/b.tar.gz", `a\b.tar.gz`, ""} {
				if _, err := l.ArtifactURL("26.8.0", name); err == nil {
					t.Fatalf("%s: artifact name %q must be refused", ep, name)
				}
			}
		}
	})
	t.Run("the latest layout cannot address an artifact without a version", func(t *testing.T) {
		t.Parallel()
		// Deriving the tag from the manifest is the whole point; with no version there is
		// no tag, and guessing one would silently address a release nobody verified.
		l, err := ResolveChannel("https://github.com/olivaresai/olivares", ChannelStable)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.ArtifactURL("", "olivares_26.8.0_linux_amd64.tar.gz"); err == nil {
			t.Fatal("an empty version must be refused, not turned into the tag \"v\"")
		}
		// A pinned endpoint does not need one — it already names the release.
		pinned, err := ResolveChannel("https://github.com/o/r/releases/tag/v1.2.3", ChannelStable)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pinned.ArtifactURL("", "olivares_1.2.3_linux_amd64.tar.gz"); err != nil {
			t.Fatalf("a pinned endpoint needs no version: %v", err)
		}
	})
	t.Run("a dot or dot-dot artifact name is refused", func(t *testing.T) {
		t.Parallel()
		// `.` and `..` survive path.Base unchanged, so a name-equals-base test alone lets them
		// through — and `..` spliced into a URL path is a traversal the SERVER resolves. A
		// manifest is signed, not sanitised.
		l, err := ResolveChannel("https://github.com/o/r", ChannelStable)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".", ".."} {
			if got, err := l.ArtifactURL("26.8.0", name); err == nil {
				t.Fatalf("artifact name %q must be refused; got %q", name, got)
			}
		}
	})

	t.Run("an unstamped version cannot be turned into a tag", func(t *testing.T) {
		t.Parallel()
		// ParseManifest accepts "dev" as a version (ParseVersion parses it to the zero
		// Version), so a signed manifest can carry one. Deriving a tag from it would produce a
		// confident URL for a release called `vdev`.
		l, err := ResolveChannel("https://github.com/o/r", ChannelStable)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"dev", "", "   ", "v"} {
			if got, err := l.ArtifactURL(v, "x.tar.gz"); err == nil {
				t.Fatalf("version %q has no position in the ordering and must be refused; got %q", v, got)
			}
		}
		// NON-FIRING: a real version still resolves.
		if _, err := l.ArtifactURL("26.8.0", "x.tar.gz"); err != nil {
			t.Fatalf("a stamped version must still resolve: %v", err)
		}
	})

	t.Run("the trailing-dot form of github.com is still github.com", func(t *testing.T) {
		t.Parallel()
		// Same name to DNS. Missing it would send an unrecognised github.com shape into the
		// STATIC layout instead of into the refusal — the direction that hides a mistyped
		// endpoint behind a transport error.
		if _, err := ResolveChannel("https://github.com./olivaresai", ChannelStable); err == nil {
			t.Fatal("github.com. with an unrecognised path must be refused, not treated as a static host")
		}
		l, err := ResolveChannel("https://github.com./olivaresai/olivares", ChannelStable)
		if err != nil {
			t.Fatalf("github.com. with a repository path must resolve: %v", err)
		}
		if !l.ReleaseAssets() {
			t.Fatal("github.com. with a repository path must select the release-asset layout")
		}
		// NON-FIRING: a host that merely CONTAINS github.com is somebody else's, and must keep
		// the static layout rather than be refused.
		for _, h := range []string{"https://github.com.evil.test/a/b", "https://notgithub.com/a/b"} {
			other, err := ResolveChannel(h, ChannelStable)
			if err != nil {
				t.Fatalf("%s is another host and must be accepted as a static base: %v", h, err)
			}
			if other.ReleaseAssets() {
				t.Fatalf("%s must not be treated as GitHub", h)
			}
		}
	})

	t.Run("the rules are DISJOINT — the four inputs the contrast found", func(t *testing.T) {
		t.Parallel()
		// ⛔ EVERY CASE HERE IS ONE THE FIRST VERSION GOT WRONG, and they all came from a single
		// convenience read on any host: "a path whose LAST segment is `releases` is the
		// collection". The external contrast found them; each is pinned so the convenience
		// cannot come back unrestricted.

		// 1 · A repository actually NAMED `releases`. The bare rule fired first and ate the
		// collection segment, so the manifest was addressed one level too high.
		l, err := ResolveChannel("https://github.com/acme/releases", ChannelStable)
		if err != nil {
			t.Fatalf("a repository named `releases` must resolve: %v", err)
		}
		if want := "https://github.com/acme/releases/releases"; l.releasesRoot != want {
			t.Fatalf("releasesRoot = %q, want %q — the repository is `acme/releases` and its collection is below it", l.releasesRoot, want)
		}

		// 2 · A release whose TAG is literally one of the words the shapes are made of. The
		// POSITION decides, never the word.
		for _, tag := range []string{"releases", "tag", "download", "latest"} {
			for _, form := range []string{"tag", "download"} {
				raw := "https://github.com/acme/widget/releases/" + form + "/" + tag
				got, err := ResolveChannel(raw, ChannelStable)
				if err != nil {
					t.Fatalf("%s must resolve: %v", raw, err)
				}
				if got.Tag() != tag {
					t.Fatalf("%s -> tag %q, want %q (the bare-collection rule must not shadow a pinned release)", raw, got.Tag(), tag)
				}
				if want := "https://github.com/acme/widget/releases"; got.releasesRoot != want {
					t.Fatalf("%s -> releasesRoot %q, want %q", raw, got.releasesRoot, want)
				}
			}
		}

		// 3 · A static mirror whose base legitimately ends in `/releases` KEEPS the directory
		// layout. This is the non-firing direction of the whole convenience: on a foreign host
		// we cannot tell a collection from a directory, so we do not guess.
		mirror, err := ResolveChannel("https://mirror.example.test/updates/releases", ChannelStable)
		if err != nil {
			t.Fatalf("static mirror rejected: %v", err)
		}
		if mirror.ReleaseAssets() {
			t.Fatal("a bare `/releases` on a foreign host must NOT be read as the release collection")
		}
		if want := "https://mirror.example.test/updates/releases/stable/manifest.json"; mirror.ManifestURL() != want {
			t.Fatalf("ManifestURL() = %q, want %q", mirror.ManifestURL(), want)
		}

		// 4 · A query or a fragment is REFUSED. The shape is read from the parsed path but every
		// root is cut from the raw string, so the two disagree the moment a query exists: it
		// lands inside the root and the asset path is appended after it.
		for _, raw := range []string{
			"https://github.com/acme/widget/releases?tab=1",
			"https://github.com/acme/widget/releases/latest/download?x=1",
			"https://github.com/acme/widget#readme",
			"https://mirror.example.test/updates?v=2",
		} {
			if got, err := ResolveChannel(raw, ChannelStable); err == nil {
				t.Fatalf("%s carries a query or fragment and must be refused; got %+v", raw, got)
			}
		}
	})

	t.Run("a version with a v prefix is not doubled", func(t *testing.T) {
		t.Parallel()
		l, err := ResolveChannel("https://github.com/o/r", ChannelStable)
		if err != nil {
			t.Fatal(err)
		}
		got, err := l.ArtifactURL("v26.8.0", "x.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if want := "https://github.com/o/r/releases/download/v26.8.0/x.tar.gz"; got != want {
			t.Fatalf("ArtifactURL() = %q, want %q", got, want)
		}
	})
}
