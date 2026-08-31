// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package secadvisory is the machine-readable security-advisory feed the product
// signs, publishes and self-checks against. An advisory says "version X of this
// product is affected by GHSA-… / CVE-…, fixed in Y"; the feed is the set of them; the
// self-check answers "is the version I am running affected right now?" so the console
// and `olivares security check` can surface an honest "you are affected — fix in vY"
// finding with an upgrade path.
//
// # Format
//
// Advisories are OSV-schema records (ossf.github.io/osv-schema, schema_version 1.x): an
// open, widely-tooled standard so any OSV-aware scanner also understands our feed. We
// model the subset we produce and consume; unknown OSV fields are preserved on decode
// but not interpreted.
//
// # Signing
//
// The feed is signed OFFLINE with the release key via core/sigbundle under the domain
// tag sigbundle.TagSecurityAdvisories, so an air-gapped install verifies it with no
// network, and a signature minted for another document type (an update manifest, a DDIL
// bundle, a license) can never pass as an advisories feed. A tampered feed is refused
// before it is parsed.
package secadvisory

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/sigbundle"
)

// FeedSchemaVersion is our envelope schema around the OSV records (not the OSV schema
// version, which each record carries).
const FeedSchemaVersion = 1

// The OSV ecosystem and range type we use for the product itself. The product is a Go
// module, so OSV ecosystem "Go" with SEMVER ranges is the interoperable choice
// (ossf.github.io/osv-schema Ecosystems).
const (
	EcosystemGo   = "Go"
	RangeSemver   = "SEMVER"
	SeverityCVSS3 = "CVSS_V3"
	SeverityCVSS4 = "CVSS_V4"
)

// Feed is the signed set of advisories plus a freshness/authorship envelope.
type Feed struct {
	SchemaVersion int        `json:"schema_version"`
	Author        string     `json:"author"`
	Modified      string     `json:"modified"` // RFC3339; the feed's own last-modified
	Advisories    []Advisory `json:"advisories"`
}

// Advisory is the OSV-schema subset we produce/consume.
type Advisory struct {
	SchemaVersion string     `json:"schema_version,omitempty"` // OSV record schema, e.g. "1.6.0"
	ID            string     `json:"id"`                       // GHSA-…, CVE-…, or an x_ self-prefixed id
	Modified      string     `json:"modified"`
	Published     string     `json:"published,omitempty"`
	Aliases       []string   `json:"aliases,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	Details       string     `json:"details,omitempty"`
	Severity      []Severity `json:"severity,omitempty"`
	Affected      []Affected `json:"affected"`
	References    []Ref      `json:"references,omitempty"`
}

// Severity is one OSV severity entry (a scoring type + its vector/score string).
type Severity struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

// Affected binds a package to the version ranges the advisory covers.
type Affected struct {
	Package Package `json:"package"`
	Ranges  []Range `json:"ranges,omitempty"`
	// Versions is an explicit list of affected versions (OSV allows either or both).
	Versions []string `json:"versions,omitempty"`
}

// Package identifies the affected software in OSV terms.
type Package struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Purl      string `json:"purl,omitempty"`
}

// Range is an OSV affected range: a sequence of introduced/fixed events.
type Range struct {
	Type   string  `json:"type"`
	Events []Event `json:"events"`
}

// Event carries exactly one of introduced / fixed / last_affected (OSV rule).
type Event struct {
	Introduced   string `json:"introduced,omitempty"`
	Fixed        string `json:"fixed,omitempty"`
	LastAffected string `json:"last_affected,omitempty"`
}

// Ref is an OSV reference.
type Ref struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// Sign serializes the feed and returns its bytes plus a detached signature under the
// security-advisories domain tag. The bytes are what a consumer stores and re-verifies;
// there is no canonicalisation — the served bytes are the signed bytes.
func (f Feed) Sign(priv ed25519.PrivateKey) (feedBytes, sig []byte, err error) {
	if err := f.validate(); err != nil {
		return nil, nil, err
	}
	feedBytes, err = json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	sig = sigbundle.Sign(sigbundle.TagSecurityAdvisories, feedBytes, priv)
	return feedBytes, sig, nil
}

// VerifyFeed authenticates a feed OFFLINE (signature-before-parse) and returns it. A nil
// key fails closed; a tampered feed or one signed under a different domain is refused.
func VerifyFeed(feedBytes, sig []byte, pub ed25519.PublicKey) (Feed, error) {
	if err := sigbundle.Verify(sigbundle.TagSecurityAdvisories, feedBytes, sig, pub); err != nil {
		return Feed{}, err
	}
	return ParseFeed(feedBytes)
}

// ParseFeed decodes and validates a feed's shape WITHOUT checking its signature (the
// untrusted path, e.g. for tooling). Callers that must trust the feed use VerifyFeed.
func ParseFeed(b []byte) (Feed, error) {
	var f Feed
	if err := json.Unmarshal(b, &f); err != nil {
		return Feed{}, fmt.Errorf("secadvisory: feed is not valid JSON: %w", err)
	}
	if err := f.validate(); err != nil {
		return Feed{}, err
	}
	return f, nil
}

func (f Feed) validate() error {
	if f.SchemaVersion != FeedSchemaVersion {
		return fmt.Errorf("secadvisory: feed schema_version %d unsupported (this build understands %d)", f.SchemaVersion, FeedSchemaVersion)
	}
	for i, a := range f.Advisories {
		if strings.TrimSpace(a.ID) == "" {
			return fmt.Errorf("secadvisory: advisory %d has no id", i)
		}
		if len(a.Affected) == 0 {
			return fmt.Errorf("secadvisory: advisory %s lists nothing affected", a.ID)
		}
	}
	return nil
}

// ErrVersionNotCheckable is returned by Check when the version it was asked about has
// no position in the release ordering, so NO advisory range can be evaluated against it
// and no verdict — affected or clean — would be a measurement.
//
// Two shapes reach it, and they are one defect, not two: a build carrying no version
// stamp at all (release.IsUnstamped: "dev", ""), and a stamp that is not a semantic
// version (the bare commit SHA `git describe --tags --always` yields when no tag is
// reachable). Callers branch on this ONE error to abstain, rather than on two guards
// that could drift apart.
var ErrVersionNotCheckable = errors.New("secadvisory: product version is not checkable")

// Finding is one "you are affected" result from a self-check.
type Finding struct {
	ID        string
	Summary   string
	Severity  string // the highest CVSS vector/score string carried, or ""
	FixedIn   string // the lowest fixed version that resolves it, or "" if none published
	Reference string // a primary reference URL, or ""
}

// Unevaluable names one advisory that is ABOUT this product but could not be decided:
// its ranges carry a version we cannot parse, or a range type this build does not
// understand. It is neither a finding nor a clean result, and it must never be silently
// dropped — an advisory nobody could evaluate is a question still open.
type Unevaluable struct {
	ID     string
	Reason string
}

// Report is the outcome of a self-check: what matched, and what could not be looked at.
// The second list exists because "I checked and you are clean" and "I could not check
// part of the catalog" are different answers that used to share one empty slice.
type Report struct {
	Findings    []Finding
	Unevaluable []Unevaluable
}

// Determined reports whether the check reached a complete answer — every advisory about
// this product was decided one way or the other. A caller may only say "no known advisory
// affects this version" when this is true AND Findings is empty.
func (r Report) Determined() bool { return len(r.Unevaluable) == 0 }

// Check reports which advisories in the feed affect productVersion for the given product
// module path (the OSV package name, e.g. "github.com/olivaresai/olivares/cmd/olivares").
// It is the engine behind `olivares security check`: version in, findings out, no network.
//
// A version is affected by an advisory iff, for some affected entry whose package matches
// (ecosystem "Go", name == module), the version falls inside a SEMVER range
// (introduced <= v < fixed, with an open upper bound when no fixed event is present) OR
// is listed explicitly in Versions. A range this build cannot evaluate — an unparseable
// version in an event, or a range type other than SEMVER — does NOT quietly become "not
// affected": the advisory lands in Report.Unevaluable and the caller must say so.
// That silence was a real hole, not a theoretical one: an advisory carrying
// "introduced":"26.5" (not MAJOR.MINOR.PATCH) dropped out of a SIGNED, VERIFIED feed and
// the command printed "no known advisory affects this version" with exit 0. The comment
// that used to sit here claimed the caller compensated by logging the parse issue; no
// caller ever did, so the compensating control was documentation, not code.
//
// A version that has no position in the release ordering yields NO verdict: Check
// returns ErrVersionNotCheckable rather than an empty finding list. This is the
// difference between "I looked and you are clean" and "I could not look", and the two
// must never share a return value: an unstamped build parses to the ZERO version, and
// against the zero version the answer is decided by the CATALOG rather than by the
// binary — "introduced":"0" reports AFFECTED, any real "introduced" reports CLEAN.
// Both are artifacts of comparing against zero, and the CLEAN one is the dangerous
// half: it is what an operator pastes into an audit as "not vulnerable".
func (f Feed) Check(module, productVersion string) (Report, error) {
	if release.IsUnstamped(productVersion) {
		return Report{}, fmt.Errorf("%w: %q names a build with no version stamp, so no advisory range can be evaluated against it",
			ErrVersionNotCheckable, strings.TrimSpace(productVersion))
	}
	cur, err := release.ParseVersion(productVersion)
	if err != nil {
		return Report{}, fmt.Errorf("%w: %q is not a semantic version: %w", ErrVersionNotCheckable, productVersion, err)
	}
	var rep Report
	for _, a := range f.Advisories {
		hit, why := affects(a, module, cur, productVersion)
		switch {
		case hit:
			// A definite match outranks an unevaluable sibling range: we already know
			// the answer for this advisory, and it is the urgent one.
			rep.Findings = append(rep.Findings, Finding{
				ID:        a.ID,
				Summary:   a.Summary,
				Severity:  highestSeverity(a.Severity),
				FixedIn:   lowestFixed(a, module),
				Reference: firstRef(a.References),
			})
		case why != nil:
			rep.Unevaluable = append(rep.Unevaluable, Unevaluable{ID: a.ID, Reason: why.Error()})
		}
	}
	sort.Slice(rep.Findings, func(i, j int) bool { return rep.Findings[i].ID < rep.Findings[j].ID })
	sort.Slice(rep.Unevaluable, func(i, j int) bool { return rep.Unevaluable[i].ID < rep.Unevaluable[j].ID })
	return rep, nil
}

// affects reports whether the advisory covers cur. The second result is non-nil when the
// advisory is about this module but at least one of its ranges could not be evaluated —
// "no" and "I could not tell" are different answers and the caller separates them.
func affects(a Advisory, module string, cur release.Version, rawVersion string) (bool, error) {
	var undecided error
	for _, af := range a.Affected {
		if af.Package.Ecosystem != EcosystemGo || af.Package.Name != module {
			continue
		}
		for _, v := range af.Versions {
			if v == rawVersion {
				return true, nil
			}
		}
		for _, r := range af.Ranges {
			if r.Type != RangeSemver {
				// OSV also defines ECOSYSTEM and GIT ranges. This build orders SEMVER
				// only, so another type is not "does not apply" — it is unread.
				if undecided == nil {
					undecided = fmt.Errorf("range type %q is not one this build can evaluate (only %s)", r.Type, RangeSemver)
				}
				continue
			}
			hit, err := inRange(r, cur)
			if err != nil {
				if undecided == nil {
					undecided = err
				}
				continue
			}
			if hit {
				return true, nil
			}
		}
	}
	return false, undecided
}

// inRange walks an OSV SEMVER range's events in order. OSV semantics: a version is
// affected if it is >= an "introduced" and < the next "fixed" (or has no later fixed).
// "introduced":"0" means from the beginning.
func inRange(r Range, cur release.Version) (bool, error) {
	affected := false
	for _, e := range r.Events {
		switch {
		case e.Introduced != "":
			intro := e.Introduced
			if intro == "0" {
				affected = true
				continue
			}
			iv, err := release.ParseVersion(intro)
			if err != nil {
				return false, fmt.Errorf(`"introduced":%q is not a version this build can order: %w`, intro, err)
			}
			if release.Compare(cur, iv) >= 0 {
				affected = true
			}
		case e.Fixed != "":
			fv, err := release.ParseVersion(e.Fixed)
			if err != nil {
				return false, fmt.Errorf(`"fixed":%q is not a version this build can order: %w`, e.Fixed, err)
			}
			if affected && release.Compare(cur, fv) < 0 {
				return true, nil
			}
			if release.Compare(cur, fv) >= 0 {
				affected = false // a later introduced may re-open it
			}
		}
	}
	return affected, nil
}

// lowestFixed returns the smallest "fixed" version across the advisory's ranges for the
// module, so the finding can point the operator at the nearest safe release.
func lowestFixed(a Advisory, module string) string {
	var best release.Version
	bestStr := ""
	for _, af := range a.Affected {
		if af.Package.Name != module {
			continue
		}
		for _, r := range af.Ranges {
			for _, e := range r.Events {
				if e.Fixed == "" {
					continue
				}
				fv, err := release.ParseVersion(e.Fixed)
				if err != nil {
					continue
				}
				if bestStr == "" || release.Compare(fv, best) < 0 {
					best, bestStr = fv, e.Fixed
				}
			}
		}
	}
	return bestStr
}

func highestSeverity(sev []Severity) string {
	for _, s := range sev {
		if s.Type == SeverityCVSS4 || s.Type == SeverityCVSS3 {
			return s.Score
		}
	}
	if len(sev) > 0 {
		return sev[0].Score
	}
	return ""
}

func firstRef(refs []Ref) string {
	for _, r := range refs {
		if r.URL != "" {
			return r.URL
		}
	}
	return ""
}

// NewFeed builds a feed with the current modified timestamp.
func NewFeed(author string, modified time.Time, advisories []Advisory) Feed {
	return Feed{
		SchemaVersion: FeedSchemaVersion,
		Author:        author,
		Modified:      modified.UTC().Format(time.RFC3339),
		Advisories:    advisories,
	}
}
