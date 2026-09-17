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
// PERCENT-ENCODING IS READ, NEVER SUBTRACTED. A tag is one path segment, and an operator may
// have to escape it: `v1%20beta`, `v1%C3%B1`, or a branch-shaped `a%2Fb`. The first cut of this
// file measured the segments on the DECODED path and then cut the root out of the RAW endpoint
// by those decoded lengths, so every escape moved the cut left by the bytes it saved:
// `…/releases/tag/v1%20beta` produced the root `…/releases/t`, and `…/releases/%74ag/v1`
// produced `…/releases/%` — a root carrying half an escape. Segments are therefore split out of
// the RAW path, matched by their DECODED value, and the root is cut at a segment BOUNDARY.
// Two consequences:
//
//   - `%2F` belongs to its segment and is never a separator. `…/releases/tag/a%2Fb` is the
//     release tagged `a/b`, and the asset builders escape that tag once on the way back out.
//   - The root keeps the bytes the operator typed, escapes included: a mirror published under
//     `/%72eleases` is addressed as `/%72eleases`, never silently as `/releases`.
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
	// ⛔ A QUERY OR A FRAGMENT PUTS BYTES AFTER THE PATH, and every root below is cut out of the
	// RAW endpoint so the caller gets back the URL it typed. With `?x=1` on the end the query
	// lands INSIDE the root and `/latest/download/<asset>` is appended after it, producing a URL
	// that addresses nothing. An update endpoint has no use for either, so they are refused here
	// rather than mishandled later.
	//
	// A BARE `#` COUNTS. `https://h/base#` parses with an EMPTY Fragment, so the three tests
	// above miss it while the delimiter is still in the string — the endpoint went on to be used
	// as the directory base `https://h/base#`, and every URL built on it addressed `https://h/base`
	// with the rest swallowed as a fragment. The delimiter is what disqualifies the endpoint, not
	// whether anything follows it, and after this test u.Path spans the whole tail of trimmed.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(trimmed, "#") {
		return ChannelLayout{}, fmt.Errorf("release: bad update endpoint %q: an update endpoint is a base path, so it carries no query string and no fragment", endpoint)
	}
	pathStart, ok := endpointPathStart(trimmed, u)
	if !ok {
		// Deny-closed belt: unreachable once the query and fragment delimiters are refused,
		// because Go then parses the whole tail of trimmed as the path. Cutting a root out of
		// bytes we cannot read back is the one failure this file must not have quietly.
		return ChannelLayout{}, fmt.Errorf("release: bad update endpoint %q: its path does not read back byte for byte, so no root can be cut from it", endpoint)
	}
	segs, err := splitRawPath(trimmed[pathStart:])
	if err != nil {
		return ChannelLayout{}, fmt.Errorf("release: bad update endpoint %q: %w", endpoint, err)
	}
	n := len(segs)
	// cut truncates the endpoint just before segment k, keeping every preceding segment as the
	// operator typed it. The byte at that offset is the separator, so a root is never cut inside
	// an escape and never inside a segment.
	cut := func(k int) string { return trimmed[:pathStart+segs[k].start-1] }

	// github.com is decided FIRST and EXHAUSTIVELY: on that host the accepted shapes are
	// enumerated and everything else is refused, so no later rule can shadow one of them.
	if isGitHubHost(u.Host) {
		switch {
		case n == 2:
			// The repository. The collection lives one segment below it.
			return ChannelLayout{channel: ch, releasesRoot: trimmed + "/releases"}, nil
		case n == 3 && segs[2].value == "releases":
			// The releases page — the URL an operator most often has open.
			return ChannelLayout{channel: ch, releasesRoot: trimmed}, nil
		case n == 5 && segs[2].value == "releases" && segs[3].value == "latest" && segs[4].value == "download":
			return ChannelLayout{channel: ch, releasesRoot: cut(3)}, nil
		case n == 5 && segs[2].value == "releases" && (segs[3].value == "tag" || segs[3].value == "download"):
			// A pinned release, INCLUDING one whose tag happens to be `releases`, `tag` or
			// `download`: the position decides, never the word.
			return ChannelLayout{channel: ch, releasesRoot: cut(3), tag: segs[4].value}, nil
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
	if n >= 3 && segs[n-3].value == "releases" && segs[n-2].value == "latest" && segs[n-1].value == "download" {
		return ChannelLayout{channel: ch, releasesRoot: cut(n - 2)}, nil
	}
	if n >= 3 && segs[n-3].value == "releases" && (segs[n-2].value == "download" || segs[n-2].value == "tag") {
		return ChannelLayout{channel: ch, releasesRoot: cut(n - 2), tag: segs[n-1].value}, nil
	}
	return ChannelLayout{channel: ch, base: trimmed}, nil
}

// pathSegment is one non-empty segment of an endpoint's path, kept BOTH as the caller typed it
// and as the value it denotes. Keeping both is the whole point: the shape is matched on value
// (`%74ag` IS `tag`) and the root is cut on raw bytes (`/%72eleases` stays `/%72eleases`).
type pathSegment struct {
	// start is the byte offset of raw within the raw path it was split out of.
	start int
	// value is the segment with its escapes resolved. `%2F` resolves to a slash INSIDE the
	// value: an encoded separator is part of the segment, never a new one.
	value string
}

// splitRawPath splits a RAW (still percent-encoded) path into its non-empty segments.
//
// Splitting the raw text rather than url.URL.Path is what keeps an encoded slash inside its
// segment. On the decoded path `/o/r/releases/tag/a%2Fb` is six segments and matches no shape;
// here it is five, the last of which is the tag `a/b`.
func splitRawPath(rawPath string) ([]pathSegment, error) {
	out := make([]pathSegment, 0, 6)
	for i := 0; i < len(rawPath); {
		if rawPath[i] == '/' {
			i++
			continue
		}
		end := len(rawPath)
		if j := strings.IndexByte(rawPath[i:], '/'); j >= 0 {
			end = i + j
		}
		value, err := url.PathUnescape(rawPath[i:end])
		if err != nil {
			return nil, err
		}
		out = append(out, pathSegment{start: i, value: value})
		i = end
	}
	return out, nil
}

// endpointPathStart returns the byte offset in trimmed at which u's path begins, so a root can
// be cut out of the endpoint the operator typed instead of out of a re-encoded copy of it.
//
// ⛔ IT IS NOT u.EscapedPath(), AND THAT IS MEASURED, NOT STYLISTIC. EscapedPath re-encodes
// whenever RawPath is not a valid encoding of Path, so the endpoint `https://h/a b` — which
// url.Parse accepts — yields `/a%20b`, one byte longer than the text it came from. An offset
// derived from it would cut in the wrong place, which is the class of defect this file just
// removed. The returned offset is verified by reading the suffix back: url.PathUnescape must
// reproduce u.Path exactly, or the caller refuses the endpoint.
func endpointPathStart(trimmed string, u *url.URL) (int, bool) {
	i := strings.Index(trimmed, "://")
	if i < 0 {
		return 0, false
	}
	// Go itself ends the authority at the first `/`, so this agrees with what it parsed.
	authority := i + len("://")
	start := len(trimmed)
	if j := strings.IndexByte(trimmed[authority:], '/'); j >= 0 {
		start = authority + j
	}
	if got, err := url.PathUnescape(trimmed[start:]); err != nil || got != u.Path {
		return 0, false
	}
	return start, true
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
