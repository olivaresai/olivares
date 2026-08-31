// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oasf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.oasf"

// The FindingReport kinds this connector emits (all SeverityMedium,
// detect/alert-first; titles are short categories, never file contents).
const (
	// FindingDescriptorInvalid is emitted per record that fails OASF 1.0.0
	// REQUIRED-field validation; the title carries the reason category.
	FindingDescriptorInvalid = "oasf_descriptor_invalid"
	// FindingBadgeInvalid is emitted per badge whose verification fails (a
	// tampered/malformed JWS or a malformed envelope/VC).
	FindingBadgeInvalid = "oasf_badge_invalid"
	// FindingBadgeRequired is emitted per record denied by require_badge=true
	// (its badge status is "none", "unverified" or "invalid").
	FindingBadgeRequired = "oasf_badge_required"
)

// defaultTimeout bounds the single optional network call (the issuer JWKS GET).
const defaultTimeout = 30 * time.Second

// Source is the AGNTCY/OASF agent-descriptor connector (EXPERIMENTAL).
// It satisfies sdk.SourceConnector (Gather emits the badge/descriptor findings)
// and identitysource.GraphProvider (the agent-descriptor roster). Both halves
// recompute from the configured files per call: the connector is stateless
// between calls, so Snapshot and Gather agree by construction.
type Source struct {
	recordsFile   string
	recordsDir    string
	badgesFile    string
	badgesDir     string
	issuerJWKSURL string
	requireBadge  bool
	timeout       time.Duration

	keyset *jose.JSONWebKeySet // inline issuer_jwks, parsed once in Open; never mutated after (URL fetches stay per-pass in keyResolver)
	doer   httpx.Doer          // injected transport (tests); nil => default
	now    func() time.Time    // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an OASF connector with default configuration.
func New() *Source {
	return &Source{timeout: defaultTimeout}
}

// Descriptor returns the connector's self-description and declared
// configuration. Nothing here is a secret: records and badges are local files,
// and the issuer JWKS is public key material (the operator trust anchor).
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AGNTCY / OASF agent descriptors (EXPERIMENTAL)",
		Description: "Imports OASF 1.0.0 agent descriptors as governed NHIs with honest 4-state Agent Badge verification (JOSE-only trust), read-only. EXPERIMENTAL: the AGNTCY identity spec is v1alpha1 and not VCDM 2.0 conformant.",
		ConfigFields: []sdk.ConfigField{
			{Key: "records_file", Type: sdk.FieldString, Description: "Path to a JSON file holding one OASF record object or an array of records."},
			{Key: "records_dir", Type: sdk.FieldString, Description: "Directory of *.json OASF record files (each one object or an array). With records_file also empty the connector is offline (empty graph)."},
			{Key: "badges_file", Type: sdk.FieldString, Description: "Path to one Agent Badge: a {\"envelope_type\":\"JOSE\"|\"EMBEDDED_PROOF\",\"value\":...} envelope, a bare VC JSON object, or a compact JWS string."},
			{Key: "badges_dir", Type: sdk.FieldString, Description: "Directory of Agent Badge files (*.json, *.jws, *.jwt), one badge per file."},
			{Key: "issuer_jwks", Type: sdk.FieldString, Description: "Inline JWKS JSON: the operator trust anchor (public keys) for JOSE badge verification. Mutually exclusive with issuer_jwks_url."},
			{Key: "issuer_jwks_url", Type: sdk.FieldString, Description: "Read-only URL of the issuer JWKS (public keys). Mutually exclusive with issuer_jwks."},
			{Key: "require_badge", Type: sdk.FieldBool, Default: "false", Description: "When true, only records with a VERIFIED JOSE badge are rostered; none/unverified/invalid are denied and surfaced as findings."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Timeout for the issuer JWKS fetch (the only network call)."},
		},
	}
}

// Open reads configuration. It fails ONLY for malformed config — both JWKS
// sources set at once, or an inline JWKS that does not parse — never for a
// missing one: with no records source the connector simply runs offline.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.recordsFile = strings.TrimSpace(cfg.Get("records_file"))
	s.recordsDir = strings.TrimSpace(cfg.Get("records_dir"))
	s.badgesFile = strings.TrimSpace(cfg.Get("badges_file"))
	s.badgesDir = strings.TrimSpace(cfg.Get("badges_dir"))
	s.issuerJWKSURL = strings.TrimSpace(cfg.Get("issuer_jwks_url"))
	s.requireBadge = cfg.GetBool("require_badge", false)
	s.timeout = cfg.GetDuration("timeout", s.timeout)

	inline := strings.TrimSpace(cfg.Get("issuer_jwks"))
	if inline != "" && s.issuerJWKSURL != "" {
		return fmt.Errorf("oasf: issuer_jwks and issuer_jwks_url are mutually exclusive; set exactly one")
	}
	if inline != "" {
		var ks jose.JSONWebKeySet
		if err := json.Unmarshal([]byte(inline), &ks); err != nil {
			return fmt.Errorf("oasf: parse issuer_jwks: %w", err)
		}
		s.keyset = &ks
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// offline reports whether no records source is configured.
func (s *Source) offline() bool { return s.recordsFile == "" && s.recordsDir == "" }

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Snapshot assembles the agent-descriptor roster: every VALID record (and,
// under require_badge, only badge-VERIFIED ones) becomes one PrincipalNHI
// identity of kind "agent_descriptor". Offline it returns an empty graph with
// Source and CapturedAt set, nil error. It never returns credential material —
// the inputs hold none to begin with.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceOASF, CapturedAt: s.clock().UTC()}
	if s.offline() {
		return g, nil
	}
	ev, err := s.evaluate(ctx)
	if err != nil {
		return identitysource.Graph{}, err
	}
	for _, row := range ev.rostered {
		g.Identities = append(g.Identities, identityFor(row))
	}
	return g, nil
}

// Gather emits the connector's findings: one per invalid record (validation
// reason category in the title), one per badge that failed verification, and
// one per record denied by require_badge. The roster itself travels Snapshot
// (the pattern). Offline it returns nil immediately.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.offline() {
		return nil
	}
	ev, err := s.evaluate(ctx)
	if err != nil {
		return err
	}
	for _, f := range ev.findings {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// recordRow is one rostered record with its aggregated badge status.
type recordRow struct {
	rec    Record
	status string
}

// evaluation is what both Snapshot and Gather derive from the configured files.
type evaluation struct {
	rostered []recordRow
	findings []model.FindingReport
}

// evaluate loads records and badges, aggregates the per-record badge status,
// applies the require_badge gate, and collects the findings. It is recomputed
// per call (stateless), so the two contract halves never disagree.
func (s *Source) evaluate(ctx context.Context) (evaluation, error) {
	now := s.clock().UTC()
	var ev evaluation

	records, invalids, err := s.loadRecords(ctx)
	if err != nil {
		return evaluation{}, err
	}
	for _, iv := range invalids {
		ev.findings = append(ev.findings, finding(FindingDescriptorInvalid,
			"agent descriptor invalid: "+iv.reason, iv.ref, now))
	}

	outcomes, err := s.loadBadges(ctx)
	if err != nil {
		return evaluation{}, err
	}
	statusByRef := map[string]string{}
	for _, o := range outcomes {
		if o.status == badgeInvalid {
			ev.findings = append(ev.findings, finding(FindingBadgeInvalid,
				"agent badge failed verification", o.ref, now))
		}
		if statusRank[o.status] > statusRank[statusByRef[o.ref]] {
			statusByRef[o.ref] = o.status
		}
	}

	for _, rec := range records {
		st := statusByRef[rec.Ref()]
		if st == "" {
			st = badgeNone
		}
		if s.requireBadge && st != badgeVerified {
			ev.findings = append(ev.findings, finding(FindingBadgeRequired,
				"agent badge required but not verified", rec.Ref(), now))
			continue
		}
		ev.rostered = append(ev.rostered, recordRow{rec: rec, status: st})
	}
	return ev, nil
}

// finding builds one of this connector's FindingReports. The detail hash keys
// on the stable, non-sensitive (kind, subject) pair — never on file contents.
func finding(kind, title, ref string, now time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        kind,
		Severity:    model.SeverityMedium,
		SubjectKind: "identity",
		SubjectRef:  ref,
		Title:       title,
		DetailHash:  redact.Hash(kind + "|" + ref),
		OccurredAt:  now,
	}
}

// identityFor maps one rostered record to its roster identity. An OASF
// descriptor is a governed NHI but NOT a dedicated per-agent registry identity
// (identitysource.KindAgentIdentity), so its kind is "agent_descriptor" and the
// access-map attribution axis treats it as approximate. Zero counts are pruned
// rather than emitted as "0" (only-present metadata, diff-stable).
func identityFor(row recordRow) identitysource.Identity {
	locators := ""
	if n := len(row.rec.Locators); n > 0 {
		locators = strconv.Itoa(n)
	}
	return identitysource.Identity{
		Ref:         row.rec.Ref(),
		Type:        identitysource.PrincipalNHI,
		Kind:        "agent_descriptor",
		DisplayName: row.rec.Name,
		Source:      identitysource.SourceOASF,
		Attributes: pruneAttrs(map[string]string{
			"schema_version": row.rec.SchemaVersion,
			"badge":          row.status,
			"skills":         strconv.Itoa(len(row.rec.Skills)),
			"locators":       locators,
		}),
	}
}

// invalidRecord remembers a rejected record for Gather's finding: its subject
// ref and the validation reason category (never file contents).
type invalidRecord struct {
	ref    string
	reason string
}

// loadRecords reads the configured record files. A file holds one record object
// or an array of records. Invalid records are NOT rostered; they are returned
// as invalidRecord rows for the findings. Duplicate name@version keeps the
// first occurrence. An unreadable configured file/dir is a hard error.
func (s *Source) loadRecords(ctx context.Context) ([]Record, []invalidRecord, error) {
	files, err := gatherFiles(s.recordsFile, s.recordsDir, []string{".json"})
	if err != nil {
		return nil, nil, err
	}
	var (
		recs []Record
		bad  []invalidRecord
		seen = map[string]bool{}
	)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, fmt.Errorf("oasf: read records: %w", err)
		}
		raws, ok := splitJSONDocs(data)
		if !ok {
			bad = append(bad, invalidRecord{ref: "oasf:file:" + filepath.Base(f), reason: "not_json"})
			continue
		}
		for _, raw := range raws {
			rec, reason := parseRecord(raw)
			if reason != "" {
				bad = append(bad, invalidRecord{ref: recordFindingRef(rec, f), reason: reason})
				continue
			}
			if seen[rec.Ref()] {
				continue
			}
			seen[rec.Ref()] = true
			recs = append(recs, rec)
		}
	}
	return recs, bad, nil
}

// loadBadges reads and verifies the configured badge files (one badge each).
func (s *Source) loadBadges(ctx context.Context) ([]badgeOutcome, error) {
	files, err := gatherFiles(s.badgesFile, s.badgesDir, []string{".json", ".jws", ".jwt"})
	if err != nil {
		return nil, err
	}
	r := s.newKeyResolver() // per-pass: no shared mutable key state, one fetch max
	var out []badgeOutcome
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("oasf: read badge: %w", err)
		}
		o, err := parseBadge(ctx, r, data, "oasf:badge:"+filepath.Base(f))
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// recordFindingRef derives a stable subject ref for an invalid record: the
// record's own "oasf:<name>@<version>" when a name was salvageable, else a
// file-derived ref (the operator's own file name — non-sensitive).
func recordFindingRef(rec Record, file string) string {
	if rec.Name != "" {
		return rec.Ref()
	}
	return "oasf:file:" + filepath.Base(file)
}

// gatherFiles assembles the configured single file plus the matching files of
// the configured directory, sorted for deterministic order.
func gatherFiles(file, dir string, exts []string) ([]string, error) {
	var out []string
	if file != "" {
		out = append(out, file)
	}
	if dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("oasf: read dir: %w", err)
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			for _, ext := range exts {
				if strings.HasSuffix(e.Name(), ext) {
					names = append(names, e.Name())
					break
				}
			}
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, filepath.Join(dir, n))
		}
	}
	return out, nil
}

// splitJSONDocs splits a record file's content into its record documents: a
// top-level array yields its elements, a top-level object yields itself.
// Anything else (including unparsable JSON) reports !ok.
func splitJSONDocs(data []byte) ([]json.RawMessage, bool) {
	body := bytes.TrimSpace(data)
	if len(body) == 0 {
		return nil, false
	}
	switch body[0] {
	case '[':
		var raws []json.RawMessage
		if err := json.Unmarshal(body, &raws); err != nil {
			return nil, false
		}
		return raws, true
	case '{':
		if !json.Valid(body) {
			return nil, false
		}
		return []json.RawMessage{body}, true
	default:
		return nil, false
	}
}

// pruneAttrs drops empty values so the attribute map carries only present
// metadata, and returns nil when nothing remains (diff-stable Snapshots; the
// claude-wif convention).
func pruneAttrs(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
