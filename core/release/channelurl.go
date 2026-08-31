// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// channelurl.go answers ONE question for every reader of a public update channel: given
// an endpoint and a channel, WHERE do the manifest, its detached signature and an
// artifact live?
//
// WHY IT IS HERE AND NOT IN THE CLI. There are two readers of the same channel and they
// are in different packages: `olivares upgrade` (cmd/olivares) and the console's update
// indicator (core/updatecheck). Until 2026-08-27 both spelled the layout out themselves,
// as `<base>/<channel>/manifest.json`, and while that was the only layout the duplication
// was invisible. FIRMA B (2026-08-21) added a SECOND one — GitHub release assets are
// flat under `/releases/download/<tag>/<asset>` — and a fact that lives in two places
// drifts: rewiring only the CLI would leave the console silently 404-ing against the very
// carrier the product now ships with, with the badge reporting a transport error the
// operator cannot act on. One resolver, two callers.
//
// THE TWO LAYOUTS, both of which FIRMA B keeps alive:
//
//	RELEASE ASSETS (the carrier)     the public repository's GitHub Releases
//	  https://github.com/<owner>/<repo>              the latest published release
//	  https://github.com/<owner>/<repo>/releases     the same, as the releases page
//	  <root>/releases/latest/download                the latest published release
//	  <root>/releases/download/<tag>                 that exact release
//	  <root>/releases/tag/<tag>                      the same, as the release page
//	  manifest: <channel>-manifest.json[.sig]      artifact: alongside it, under the tag
//
//	⛔ The two SHORTHAND forms are accepted on github.com ONLY, and that restriction is the
//	fix for a set of rules that was not disjoint. Read on any host, "a path whose last
//	segment is `releases`" shadowed a repository actually NAMED `releases`, shadowed a
//	release whose TAG is `releases`, and swallowed a static mirror whose base ends that way.
//	On github.com the repository path is exactly two segments, so there is nothing to
//	confuse; on any other host we cannot know, and an operator with a mirror writes the
//	explicit `/releases/latest/download`.
//
//	DIRECTORY (the R2/mirror fallback)   a static channel host
//	  <base>/<channel>/manifest.json[.sig]         artifact: <base>/<channel>/<filename>
//
// THE LAYOUT IS DECIDED BY THE ENDPOINT'S SHAPE, NEVER BY SNIFFING THE ANSWER. Three
// consequences worth stating, because each of them is a defect avoided:
//
//   - It is TESTABLE. Keying on the host `github.com` would make the shipped path
//     exercisable only against github.com itself, and a control nobody can run is a
//     control nobody runs. The batteries drive both layouts against a local server.
//   - It admits a MIRROR that serves release-shaped paths from another host, which FIRMA B
//     explicitly allows ("y/o el registry").
//   - It is DENY-CLOSED on github.com: an endpoint on that host which is neither a
//     repository nor a releases base is REFUSED rather than quietly treated as a static
//     host. Falling through would GET `<path>/stable/manifest.json`, receive an HTML 404
//     page, and report a mistyped endpoint as an unreachable channel.

// ChannelLayout is a resolved endpoint: which layout it is, and where each object sits.
// The zero value is not usable — build one with ResolveChannel.
type ChannelLayout struct {
	// channel is the channel this layout addresses.
	channel string
	// base is, for the directory layout, the endpoint with any trailing slash removed.
	base string
	// releasesRoot is, for the release-asset layout, the URL up to and including
	// `/releases`. Empty for the directory layout.
	releasesRoot string
	// tag pins one release in the release-asset layout. Empty means "the latest
	// published release", resolved through the host's /releases/latest/download redirect.
	tag string
}

// ReleaseAssets reports whether this endpoint uses the flat release-asset layout.
func (l ChannelLayout) ReleaseAssets() bool { return l.releasesRoot != "" }

// Channel is the channel this layout addresses.
func (l ChannelLayout) Channel() string { return l.channel }

// Tag is the release this layout is pinned to, or "" for the latest published release.
// It is meaningless for the directory layout, which has no notion of a release.
func (l ChannelLayout) Tag() string { return l.tag }

// ManifestURL is where the signed channel manifest lives.
func (l ChannelLayout) ManifestURL() string {
	if l.ReleaseAssets() {
		return l.assetURL(l.channel + "-manifest.json")
	}
	return l.base + "/" + l.channel + "/manifest.json"
}

// SignatureURL is the detached signature that sits beside the manifest. Both layouts put
// it at the manifest's own URL plus `.sig`, which is what makes the pair atomic to reason
// about: a manifest without its signature is a channel every conforming client refuses.
func (l ChannelLayout) SignatureURL() string { return l.ManifestURL() + ".sig" }

// ArtifactURL is where the artifact named by a VERIFIED manifest lives. It takes the
// manifest's version because the release-asset layout needs it, and taking it explicitly
// is what keeps that derivation on signed data:
//
//   - `latest` is a MUTABLE pointer. A release published between the manifest read and
//     the artifact read would move it, and the second read would serve a different
//     release's asset. Deriving the tag from the manifest pins both to ONE release — the
//     one whose signature the caller has already checked.
//   - The `v` prefix is this repository's documented version-prefix contract, not a
//     guess: "git/GitHub tag has `v`; GoReleaser filenames, manifest JSON and WS-COMMERCE
//     ENTERPRISE_VERSION omit it" (docs/RELEASE-GO-LIVE-RUNBOOK.md).
//
// filename must be a bare leaf name. A signed manifest carries one, and refusing a
// separator here names the reason instead of letting a percent-encoded slash come back as
// an unexplained 404.
func (l ChannelLayout) ArtifactURL(version, filename string) (string, error) {
	name := path.Base(filename)
	// `.` and `..` survive path.Base UNCHANGED, so a name-equals-base test lets both through.
	// A manifest is signed, not sanitised — ParseManifest only requires a non-empty filename —
	// and `..` spliced into a URL path is a traversal the server, not this process, resolves.
	// Rejecting them costs one condition and removes the whole question.
	if filename == "" || name != filename || name == "." || name == ".." ||
		strings.ContainsAny(filename, "/\\") {
		return "", fmt.Errorf("release: artifact name %q must be a bare filename", filename)
	}
	if !l.ReleaseAssets() {
		return l.base + "/" + l.channel + "/" + url.PathEscape(name), nil
	}
	tag := l.tag
	if tag == "" {
		// AN UNSTAMPED VERSION HAS NO TAG, and inventing one is worse than refusing. IsUnstamped
		// is the repository's single answer to "does this string have a position in the
		// ordering" — "" and "dev" do not — and without it `v` + "dev" would address a release
		// called `vdev`, i.e. a confident URL for a release that cannot exist.
		v := strings.TrimSpace(version)
		if IsUnstamped(v) {
			return "", fmt.Errorf("release: cannot locate %q: this endpoint is the latest release, and the manifest declares version %q, which has no position in the ordering to derive a tag from", filename, version)
		}
		tag = "v" + strings.TrimPrefix(v, "v")
	}
	return l.releasesRoot + "/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name), nil
}

// Describe is a short human label, printed before anything is downloaded so the layout
// decision is never silent.
func (l ChannelLayout) Describe() string {
	if !l.ReleaseAssets() {
		return "public channel " + l.channel + " (" + l.base + ")"
	}
	where := "latest release"
	if l.tag != "" {
		where = "release " + l.tag
	}
	return "public channel " + l.channel + " (release assets of " + l.releasesRoot + ", " + where + ")"
}

func (l ChannelLayout) assetURL(name string) string {
	if l.tag == "" {
		return l.releasesRoot + "/latest/download/" + url.PathEscape(name)
	}
	return l.releasesRoot + "/download/" + url.PathEscape(l.tag) + "/" + url.PathEscape(name)
}

// ResolveChannel decides which layout an endpoint means and returns the resolved
// addresses. See the file doc for the shapes it accepts and why it refuses the rest.
//
// ⛔ THE ORDER OF THESE BRANCHES IS THE CONTRACT, and the first cut of it was NOT DISJOINT.
// The external contrast found four inputs where two rules matched at once or the wrong one
// won, and every single one came from a convenience added last: "a path whose LAST segment is
// `releases` means the collection". Read on any host, before the pinned forms, that rule:
//
//	· shadowed a real GitHub repository NAMED `releases` (`github.com/acme/releases` resolved
//	  to `/acme/releases/latest/download/…` instead of `/acme/releases/releases/…`);
//	· shadowed a release whose TAG is literally `releases` (`…/releases/tag/releases` matched
//	  the bare rule first and the tag was silently dropped);
//	· absorbed a static mirror whose base legitimately ends in `/releases`.
//
// So the branches are ordered from MOST specific to least, github.com is decided FIRST and
// exhaustively, and the bare-collection convenience is confined to github.com — where it is
// unambiguous because the repository path is exactly two segments. On any other host we cannot
// know whether `/x/releases` is a collection or a directory base, and guessing is what created
// the collision: an operator with a mirror writes the explicit `/releases/latest/download`.
func ResolveChannel(endpoint, channel string) (ChannelLayout, error) {
	ch := strings.TrimSpace(channel)
	if ch == "" {
		ch = ChannelStable
	}
	if !ValidChannel(ch) {
		return ChannelLayout{}, fmt.Errorf("release: channel %q is not one of %s", channel, strings.Join(Channels, "|"))
	}
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if trimmed == "" {
		return ChannelLayout{}, fmt.Errorf("release: empty update endpoint")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return ChannelLayout{}, fmt.Errorf("release: bad update endpoint %q: %w", endpoint, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return ChannelLayout{}, fmt.Errorf("release: bad update endpoint %q: want an absolute URL such as https://github.com/<owner>/<repo>", endpoint)
	}
	// ⛔ A QUERY OR A FRAGMENT MAKES THE STRING ARITHMETIC BELOW WRONG, silently. The SHAPE is
	// read from u.Path, but every root is cut from the RAW string so the caller gets back the
	// URL it typed. With `?x=1` on the end those two disagree: the query lands INSIDE the root
	// and `/latest/download/<asset>` is appended after it, producing a URL that addresses
	// nothing — and the length-based trim can cut the wrong suffix entirely. An update endpoint
	// has no use for either, so they are refused here rather than mishandled later.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ChannelLayout{}, fmt.Errorf("release: bad update endpoint %q: an update endpoint is a base path, so it carries no query string and no fragment", endpoint)
	}
	segs := pathSegments(u.Path)
	n := len(segs)

	// github.com is decided FIRST and EXHAUSTIVELY: on that host the accepted shapes are
	// enumerated and everything else is refused, so no later rule can shadow one of them.
	if isGitHubHost(u.Host) {
		switch {
		case n == 2:
			// The repository. The collection lives one segment below it.
			return ChannelLayout{channel: ch, releasesRoot: trimmed + "/releases"}, nil
		case n == 3 && segs[2] == "releases":
			// The releases page — the URL an operator most often has open.
			return ChannelLayout{channel: ch, releasesRoot: trimmed}, nil
		case n == 5 && segs[2] == "releases" && segs[3] == "latest" && segs[4] == "download":
			return ChannelLayout{channel: ch, releasesRoot: trimmed[:len(trimmed)-len("/latest/download")]}, nil
		case n == 5 && segs[2] == "releases" && (segs[3] == "tag" || segs[3] == "download"):
			// A pinned release, INCLUDING one whose tag happens to be `releases`, `tag` or
			// `download`: the position decides, never the word.
			return ChannelLayout{channel: ch, releasesRoot: trimmed[:len(trimmed)-len("/"+segs[3]+"/"+segs[4])], tag: segs[4]}, nil
		}
		return ChannelLayout{}, fmt.Errorf(
			"release: REFUSING the update endpoint %q: on github.com a channel is served from a repository's RELEASES, so the endpoint must be one of\n"+
				"  https://github.com/<owner>/<repo>                       (the latest published release)\n"+
				"  https://github.com/<owner>/<repo>/releases              (the same, written as the releases page)\n"+
				"  https://github.com/<owner>/<repo>/releases/tag/<tag>    (that exact release)\n"+
				"the static-host layout (<base>/%s/manifest.json) is the mirror fallback and does not exist on github.com",
			endpoint, ch)
	}

	// Any other host. Only the two EXPLICIT release shapes are recognised, most specific first;
	// a bare `/releases` is NOT one of them, because on a foreign host it is indistinguishable
	// from a directory base that happens to be called that.
	if n >= 3 && segs[n-3] == "releases" && segs[n-2] == "latest" && segs[n-1] == "download" {
		return ChannelLayout{channel: ch, releasesRoot: trimmed[:len(trimmed)-len("/latest/download")]}, nil
	}
	if n >= 3 && segs[n-3] == "releases" && (segs[n-2] == "download" || segs[n-2] == "tag") {
		root := trimmed[:len(trimmed)-len("/"+segs[n-2]+"/"+segs[n-1])]
		return ChannelLayout{channel: ch, releasesRoot: root, tag: segs[n-1]}, nil
	}
	return ChannelLayout{channel: ch, base: trimmed}, nil
}

// pathSegments returns the non-empty path segments of p.
func pathSegments(p string) []string {
	out := make([]string, 0, 6)
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// isGitHubHost reports whether h is github.com (with or without a port, a www prefix, or the
// fully-qualified trailing dot).
//
// THE TRAILING DOT IS NOT PEDANTRY HERE. `github.com.` is the same name to DNS, and this
// predicate's only job is to decide whether the DENY-CLOSED branch applies. Missing it would
// send a github.com endpoint of an unrecognised shape into the static layout instead of into a
// refusal — which is the direction that hides a mistyped endpoint behind a transport error.
func isGitHubHost(h string) bool {
	host := strings.ToLower(strings.TrimSpace(h))
	// Strip a port, leaving a bracketed IPv6 literal alone.
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ".")
	return host == "github.com" || host == "www.github.com"
}
