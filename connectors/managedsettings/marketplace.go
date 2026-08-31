// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// marketplace.go models the managed-settings PLUGIN MARKETPLACE allowlist/blocklist —
// the two managed-only keys that govern which plugin marketplaces a fleet may add and
// fetch from (B1). The control plane AUTHORS these from its own governance state
// and VERIFIES the host against them; the OS file is the only non-overridable layer.
//
// CORRECTION to (VERIFIED 2026-06-09, recorded in
// Against
// code.claude.com/docs/en/{settings,plugin-marketplaces}):
//
//	`strictKnownMarketplaces` is an ARRAY of marketplace-SOURCE objects, NOT a boolean.
//	Modeled it as a bool (rendering `"strictKnownMarketplaces": true`), which is a
//	correctness bug: a real-world file using the array form is decoded as
//	present-but-invalid JSON (a Go bool field receiving a JSON array errors), so the host
//	is mis-reported as ungoverned. The three documented STATES are distinct and must be
//	representable: undefined = no restriction; `[]` = COMPLETE LOCKDOWN (no marketplace
//	may be added); a list = an exact-match allowlist. So the authored form is a
//	*[]Marketplace (a nil pointer = unset; a non-nil empty slice = the `[]` lockdown).
//
// Enforcement (verbatim): "Enforced on marketplace add and on plugin install, update,
// refresh, and auto-update, so a marketplace added before the policy was set cannot be
// used to fetch plugins. Blocked sources are checked before downloading, so they never
// touch the filesystem." — i.e. enforce-before-network/fs (see precedence.go).

// Marketplace source discriminators (the verified `source` field values). github/url
// match EXACTLY (url is not normalized); hostPattern/pathPattern are REGEXes matched
// against the marketplace host / filesystem path respectively.
const (
	MarketplaceSourceGitHub      = "github"
	MarketplaceSourceURL         = "url"
	MarketplaceSourceHostPattern = "hostPattern"
	MarketplaceSourcePathPattern = "pathPattern"
)

// Marketplace is one plugin-marketplace SOURCE entry — an element of the
// strictKnownMarketplaces allowlist or the blockedMarketplaces blocklist. Its wire JSON
// is identical to the authored form (like HookMatcher), so one type serves both halves
// and the Render→fromWire round-trip preserves it for drift. The field set is
// discriminated by Source; only the relevant fields are populated for a given source.
type Marketplace struct {
	Source      string `json:"source"`
	Repo        string `json:"repo,omitempty"`        // github: "owner/repo" (required)
	Ref         string `json:"ref,omitempty"`         // github: branch/tag (optional, must match if set)
	Path        string `json:"path,omitempty"`        // github: path (optional, must match if set)
	URL         string `json:"url,omitempty"`         // url: exact URL (required; NOT normalized)
	HostPattern string `json:"hostPattern,omitempty"` // hostPattern: regex on the host (required)
	PathPattern string `json:"pathPattern,omitempty"` // pathPattern: regex on the fs path (required)
}

// knownMarketplaceSources is the closed set of verified source discriminators.
func knownMarketplaceSource(s string) bool {
	switch s {
	case MarketplaceSourceGitHub, MarketplaceSourceURL, MarketplaceSourceHostPattern, MarketplaceSourcePathPattern:
		return true
	default:
		return false
	}
}

// validateMarketplace checks one allowlist/blocklist entry SERVER-SIDE (defense in
// depth). It enforces the verified per-source required fields and that a regex source
// carries a COMPILABLE pattern (an uncompilable hostPattern/pathPattern would silently
// match nothing on the host — a governance hole that must never publish looking valid).
// It is forward-compatible: it does NOT reject extra sibling fields (Claude Code may add
// them), only a missing required field or a malformed regex. ctx is the JSON path for
// the message (e.g. "strictKnownMarketplaces[0]").
func validateMarketplace(m Marketplace, ctx string) []string {
	var issues []string
	src := strings.TrimSpace(m.Source)
	switch src {
	case "":
		issues = append(issues, ctx+".source is required (one of github|url|hostPattern|pathPattern)")
	case MarketplaceSourceGitHub:
		if strings.TrimSpace(m.Repo) == "" {
			issues = append(issues, ctx+`.repo is required for a "github" source (e.g. "owner/repo")`)
		}
	case MarketplaceSourceURL:
		if strings.TrimSpace(m.URL) == "" {
			issues = append(issues, ctx+`.url is required for a "url" source`)
		}
	case MarketplaceSourceHostPattern:
		issues = append(issues, validateRegexField(m.HostPattern, ctx+".hostPattern", "hostPattern")...)
	case MarketplaceSourcePathPattern:
		issues = append(issues, validateRegexField(m.PathPattern, ctx+".pathPattern", "pathPattern")...)
	default:
		issues = append(issues, fmt.Sprintf("%s.source %q is not one of github|url|hostPattern|pathPattern", ctx, src))
	}
	return issues
}

// validateRegexField reports issues for a required regex field: empty (the regex source
// type requires it) or uncompilable (it would match nothing — a silent governance hole).
func validateRegexField(pattern, ctx, src string) []string {
	if strings.TrimSpace(pattern) == "" {
		return []string{fmt.Sprintf(`%s is required for a "%s" source`, ctx, src)}
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return []string{fmt.Sprintf("%s is not a valid regular expression: %v", ctx, err)}
	}
	return nil
}

// validateMarketplaceArray validates a raw managed-settings marketplace value (the
// strictKnownMarketplaces / blockedMarketplaces wire form). The value MUST be a JSON
// array (the verified shape); a present-but-non-array value (e.g. the legacy bool
// emitted) is itself an issue — it would not enforce the intended allowlist. An absent
// value (nil/empty raw) is valid (the key is optional). key is the JSON key for messages.
func validateMarketplaceArray(raw json.RawMessage, key string) []string {
	if !rawPresent(raw) {
		return nil
	}
	var arr []Marketplace
	if err := json.Unmarshal(raw, &arr); err != nil {
		return []string{fmt.Sprintf("%s must be an ARRAY of marketplace sources (undefined = no restriction, [] = lockdown); got a non-array value", key)}
	}
	var issues []string
	for i, m := range arr {
		issues = append(issues, validateMarketplace(m, fmt.Sprintf("%s[%d]", key, i))...)
	}
	return issues
}

// liveMarketplaces parses a wire marketplace value into entries. present is true ONLY
// when raw is a JSON array (the conformant form); any other shape (bool/object/null/
// absent) yields present=false so drift treats a non-array as "no valid allowlist on
// host". An empty array `[]` parses to a non-nil empty slice with present=true (the
// lockdown posture is a PRESENT allowlist, not an absent one).
func liveMarketplaces(raw json.RawMessage) (entries []Marketplace, present bool) {
	if !rawPresent(raw) {
		return nil, false
	}
	var arr []Marketplace
	if json.Unmarshal(raw, &arr) != nil {
		return nil, false
	}
	if arr == nil {
		arr = []Marketplace{}
	}
	return arr, true
}

// marketplaceKey is the stable identity of one entry, for order-independent set
// comparison in drift. Every discriminating field participates so two entries differing
// only in ref/path/pattern are distinct.
func marketplaceKey(m Marketplace) string {
	return strings.Join([]string{
		strings.TrimSpace(m.Source), strings.TrimSpace(m.Repo), strings.TrimSpace(m.Ref),
		strings.TrimSpace(m.Path), strings.TrimSpace(m.URL),
		strings.TrimSpace(m.HostPattern), strings.TrimSpace(m.PathPattern),
	}, "\x00")
}

// sameMarketplaceSet reports whether two marketplace lists denote the SAME set of
// sources (order-independent, duplicate-insensitive). Used by drift to decide whether
// the host's allowlist matches the authored one exactly.
func sameMarketplaceSet(a, b []Marketplace) bool {
	sa, sb := marketplaceKeySet(a), marketplaceKeySet(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if _, ok := sb[k]; !ok {
			return false
		}
	}
	return true
}

// marketplaceKeySet collapses a list to its set of entry keys.
func marketplaceKeySet(ms []Marketplace) map[string]struct{} {
	out := make(map[string]struct{}, len(ms))
	for _, m := range ms {
		out[marketplaceKey(m)] = struct{}{}
	}
	return out
}

// missingMarketplaceEntries returns the authored entries that are ABSENT from the live
// list (the host is not blocking/allowing something the org declared) — the drift signal
// for the blocklist (mirrors the permissions.deny "missing on host" check). The returned
// entries are in stable, deterministic order.
func missingMarketplaceEntries(authored, live []Marketplace) []Marketplace {
	liveSet := marketplaceKeySet(live)
	var missing []Marketplace
	for _, m := range authored {
		if _, ok := liveSet[marketplaceKey(m)]; !ok {
			missing = append(missing, m)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return marketplaceKey(missing[i]) < marketplaceKey(missing[j]) })
	return missing
}

// describeMarketplace renders a short, non-sensitive label for an entry (for drift
// titles). It names the source and its key identifier; a regex/url is included verbatim
// (these are operator-authored allowlist values, not secrets).
func describeMarketplace(m Marketplace) string {
	switch m.Source {
	case MarketplaceSourceGitHub:
		s := "github:" + strings.TrimSpace(m.Repo)
		if r := strings.TrimSpace(m.Ref); r != "" {
			s += "@" + r
		}
		return s
	case MarketplaceSourceURL:
		return "url:" + strings.TrimSpace(m.URL)
	case MarketplaceSourceHostPattern:
		return "hostPattern:" + strings.TrimSpace(m.HostPattern)
	case MarketplaceSourcePathPattern:
		return "pathPattern:" + strings.TrimSpace(m.PathPattern)
	default:
		return "source:" + strings.TrimSpace(m.Source)
	}
}

// marketplacesToRaw marshals a marketplace list to a JSON array RawMessage for the wire
// shape, normalizing a nil slice to `[]` (so the `[]` LOCKDOWN posture renders as an
// empty array, never as JSON null which would read as "unset"). It never errors for a
// well-formed []Marketplace.
func marketplacesToRaw(ms []Marketplace) json.RawMessage {
	if ms == nil {
		ms = []Marketplace{}
	}
	b, err := json.Marshal(ms)
	if err != nil {
		return json.RawMessage("[]")
	}
	return b
}

// rawPresent reports whether a json.RawMessage carries a present, non-null value.
func rawPresent(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}
