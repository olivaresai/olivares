// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package ddil assembles and reconciles the air-gap bundle a disconnected Olivares
// edge deployment carries across a gap by sneakernet (ADR-0024): a single signed
// envelope holding the site's policy snapshot, its accumulated audit segments, and
// evidence exports.
//
// The bundle IS a core/sigbundle envelope under the domain tag
// sigbundle.TagDDILBundle — the SAME verifiable format uses for OTA updates, not a
// second one. On top of that generic envelope, ddil adds a typed Index describing what
// the bundle carries so the receiving side can reconcile it OFFLINE and IDEMPOTENTLY:
//
//   - Audit is the crux. A disconnected node keeps appending to its local
//     hash-chained ledger (core/audit); when a link (or a courier) appears, the
//     accumulated ledger segments travel in the bundle. The importer must (a) skip
//     segments it already holds — reconnection re-sends must not duplicate rows — and
//     (b) verify the chain is continuous ACROSS the segments in the bundle, so a gap
//     or a reordering introduced in transit is caught. Both are done here without any
//     network and without re-deriving the chain: the segment manifests already carry
//     from_seq/to_seq and the prev-segment/last-hash links (core/audit.SegmentManifest).
//
// This package does NOT re-verify the per-event signatures or re-hash the events inside
// a segment — that is the offline archive verifier's job
// (core/audit.VerifyArchiveDir), run after import. ddil verifies the ENVELOPE
// (signature + per-file digest, via sigbundle) and the SEQ CONTINUITY across segments.
package ddil

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/sigbundle"
)

// Bundle kind and in-bundle path prefixes. Keeping the layout explicit means an
// operator can list the tar and understand it without this code.
const (
	Kind = "ddil"

	indexName      = "ddil-index.json"
	policyPrefix   = "policy/"
	auditPrefix    = "audit/"
	evidencePrefix = "evidence/"
)

// IndexSchemaVersion is the DDIL index schema. A reader refuses a newer major schema.
const IndexSchemaVersion = 1

// Index is the typed DDIL control record (carried as a payload named ddil-index.json).
// It is inside the signed envelope, so it is authenticated with everything else.
type Index struct {
	SchemaVersion int    `json:"schema_version"`
	Tenant        string `json:"tenant"`
	// PolicyRevision identifies the policy snapshot carried under policy/. It is the
	// value the importer compares to decide whether the bundle advances local policy.
	PolicyRevision string `json:"policy_revision,omitempty"`
	// PolicyMaxStaleness is the ratified offline-trust bound (ADR-0024 Q1): after this
	// duration without a refresh, positive grants expire deny-closed while deny rules
	// stay enforced. It travels with the policy so the edge honors the centre's
	// chosen bound. Zero means "unset" (the deployment default applies).
	PolicyMaxStaleness Duration `json:"policy_max_staleness,omitempty"`
	// Segments describes the audit ledger segments carried under audit/, in ascending
	// seq order. Each references the segment manifest and events files by their
	// in-bundle names and repeats the seq/hash boundaries for reconciliation.
	Segments []SegmentRef `json:"segments,omitempty"`
	// Evidence lists evidence export files carried under evidence/.
	Evidence []string `json:"evidence,omitempty"`
}

// SegmentRef is one audit segment's reconciliation metadata, mirrored from
// core/audit.SegmentManifest so the importer needs only the index to reason about
// continuity and idempotency.
type SegmentRef struct {
	ManifestName        string `json:"manifest_name"` // in-bundle path under audit/
	EventsName          string `json:"events_name"`   // in-bundle path under audit/
	FromSeq             int64  `json:"from_seq"`
	ToSeq               int64  `json:"to_seq"`
	FirstHash           string `json:"first_hash"`
	LastHash            string `json:"last_hash"`
	PrevSegmentLastHash string `json:"prev_segment_last_hash"`
}

// Duration is a JSON-friendly time.Duration serialized as a Go duration string.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("ddil: policy_max_staleness %q is not a duration: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Segment is one audit segment to place in a bundle: its manifest bytes, its events
// (JSONL) bytes, and the reconciliation boundaries (taken from the segment manifest by
// the caller, which already parsed it).
type Segment struct {
	FromSeq             int64
	ToSeq               int64
	FirstHash           string
	LastHash            string
	PrevSegmentLastHash string
	ManifestJSON        []byte
	EventsJSONL         []byte
}

// ExportInput is what Export assembles into a signed bundle.
type ExportInput struct {
	Tenant             string
	PolicyRevision     string
	PolicySnapshot     []byte // opaque policy bundle bytes (may be empty)
	PolicyMaxStaleness time.Duration
	Segments           []Segment
	Evidence           map[string][]byte // name -> bytes (may be nil)
	CreatedAt          time.Time
	Expires            *time.Time
	Notes              string
}

// Export builds a signed DDIL bundle and writes it to w. Segments are sorted by
// from_seq and validated for internal continuity BEFORE signing, so a producer can
// never emit a bundle whose own segments are gapped or overlapping.
func Export(w io.Writer, in ExportInput, priv ed25519.PrivateKey) error {
	if strings.TrimSpace(in.Tenant) == "" {
		return fmt.Errorf("ddil: export requires a tenant")
	}
	segs := append([]Segment(nil), in.Segments...)
	sort.Slice(segs, func(i, j int) bool { return segs[i].FromSeq < segs[j].FromSeq })
	if err := checkContinuity(segs); err != nil {
		return err
	}

	idx := Index{
		SchemaVersion:      IndexSchemaVersion,
		Tenant:             in.Tenant,
		PolicyRevision:     in.PolicyRevision,
		PolicyMaxStaleness: Duration(in.PolicyMaxStaleness),
	}
	payloads := make([]sigbundle.Payload, 0, len(segs)*2+len(in.Evidence)+2)

	if len(in.PolicySnapshot) > 0 {
		name := policyPrefix + "snapshot.bin"
		payloads = append(payloads, sigbundle.Payload{Name: name, Body: in.PolicySnapshot})
	}
	for _, s := range segs {
		mName := fmt.Sprintf("%sseg-%012d-%012d.jsonl.manifest.json", auditPrefix, s.FromSeq, s.ToSeq)
		eName := fmt.Sprintf("%sseg-%012d-%012d.jsonl", auditPrefix, s.FromSeq, s.ToSeq)
		payloads = append(payloads,
			sigbundle.Payload{Name: mName, Body: s.ManifestJSON},
			sigbundle.Payload{Name: eName, Body: s.EventsJSONL},
		)
		idx.Segments = append(idx.Segments, SegmentRef{
			ManifestName: mName, EventsName: eName,
			FromSeq: s.FromSeq, ToSeq: s.ToSeq,
			FirstHash: s.FirstHash, LastHash: s.LastHash, PrevSegmentLastHash: s.PrevSegmentLastHash,
		})
	}
	evNames := make([]string, 0, len(in.Evidence))
	for name := range in.Evidence {
		evNames = append(evNames, name)
	}
	sort.Strings(evNames)
	for _, name := range evNames {
		p := evidencePrefix + name
		payloads = append(payloads, sigbundle.Payload{Name: p, Body: in.Evidence[name]})
		idx.Evidence = append(idx.Evidence, p)
	}

	indexBytes, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	payloads = append(payloads, sigbundle.Payload{Name: indexName, Body: indexBytes})

	return sigbundle.Write(w, sigbundle.TagDDILBundle, Kind, in.CreatedAt, in.Expires, in.Notes, payloads, priv)
}

// Imported is the verified, parsed result of reading a DDIL bundle.
type Imported struct {
	Index    Index
	Payloads map[string][]byte
	// CreatedAt is the bundle's AUTHENTICATED creation time — the envelope manifest's
	// created_at, covered by the detached signature sigbundle already verified. It is
	// the only clock in the bundle an importer may trust: policy adoption stamps the
	// tenant's freshness window from it (never from the importer's wall clock), so a
	// courier delay or a replayed old bundle can never extend the offline-trust
	// window beyond what the exporting center actually signed (ADR-0024 Q1).
	CreatedAt time.Time
}

// PolicySnapshot returns the policy bytes carried in the bundle, or nil.
func (im Imported) PolicySnapshot() []byte { return im.Payloads[policyPrefix+"snapshot.bin"] }

// Import verifies a DDIL bundle OFFLINE (signature + per-file digest via sigbundle),
// parses the typed index, and checks that the index matches the payloads actually
// present and that the carried segments are internally continuous. It does NOT decide
// what to apply — Reconcile does, against a local cursor.
func Import(r io.Reader, pub ed25519.PublicKey, now time.Time) (Imported, error) {
	opened, err := sigbundle.Read(r, sigbundle.TagDDILBundle, pub, now)
	if err != nil {
		return Imported{}, err
	}
	rawIdx, ok := opened.Payloads[indexName]
	if !ok {
		return Imported{}, fmt.Errorf("ddil: bundle has no %s", indexName)
	}
	idx, err := parseIndex(rawIdx)
	if err != nil {
		return Imported{}, err
	}
	// Every segment/evidence the index names must be present (sigbundle already proved
	// nothing UNdeclared rides along; here we prove nothing declared is missing beyond
	// sigbundle's own entry set — the index is a second, typed manifest).
	for _, s := range idx.Segments {
		if _, ok := opened.Payloads[s.ManifestName]; !ok {
			return Imported{}, fmt.Errorf("ddil: index names segment manifest %q but the bundle omits it", s.ManifestName)
		}
		if _, ok := opened.Payloads[s.EventsName]; !ok {
			return Imported{}, fmt.Errorf("ddil: index names segment events %q but the bundle omits it", s.EventsName)
		}
	}
	for _, e := range idx.Evidence {
		if _, ok := opened.Payloads[e]; !ok {
			return Imported{}, fmt.Errorf("ddil: index names evidence %q but the bundle omits it", e)
		}
	}
	// Segments must be internally continuous (ascending, gap-free, prev-hash linked).
	segs := make([]Segment, len(idx.Segments))
	for i, s := range idx.Segments {
		segs[i] = Segment{FromSeq: s.FromSeq, ToSeq: s.ToSeq, FirstHash: s.FirstHash, LastHash: s.LastHash, PrevSegmentLastHash: s.PrevSegmentLastHash}
	}
	if err := checkContinuity(segs); err != nil {
		return Imported{}, err
	}
	// sigbundle.Read already validated created_at as RFC3339 before this point; a
	// parse failure here would mean the two ever disagreed on the format, so refuse
	// rather than hand the caller a zero trust anchor.
	createdAt, err := time.Parse(time.RFC3339, opened.Manifest.CreatedAt)
	if err != nil {
		return Imported{}, fmt.Errorf("ddil: bundle created_at %q is not RFC3339: %w", opened.Manifest.CreatedAt, err)
	}
	return Imported{Index: idx, Payloads: opened.Payloads, CreatedAt: createdAt}, nil
}

// Plan is the reconciliation decision Reconcile produces against a local audit cursor.
type Plan struct {
	// NewSegments are the segments whose seq range is not already held locally, in
	// ascending order — the ones to apply.
	NewSegments []SegmentRef
	// SkippedSegments are segments the local cursor already covers — a re-sent bundle
	// after a flaky reconnection. Applying them would duplicate rows.
	SkippedSegments []SegmentRef
	// GapBeforeApply is true when the first new segment does not begin exactly at
	// localCursor+1: applying it would leave a hole in the local chain. The caller must
	// refuse to apply (deny-closed) and wait for the missing segment.
	GapBeforeApply bool
	// PolicyAdvances is true when the bundle's PolicyRevision differs from localPolicyRev
	// and is non-empty — the caller should adopt the carried policy snapshot.
	PolicyAdvances bool
}

// Reconcile decides, IDEMPOTENTLY, what an importer should apply from a verified bundle
// given its current state: localCursor is the highest audit seq already durably held
// (0 for a fresh node), localPolicyRev is the currently-active policy revision.
//
// Idempotency: a segment fully at or below localCursor is skipped, so re-importing the
// same bundle after a dropped link applies nothing new (zero duplicates). Continuity:
// the first segment to apply must begin at localCursor+1, else GapBeforeApply is set and
// the caller must not apply — the local chain would otherwise gain a hole.
func (im Imported) Reconcile(localCursor int64, localPolicyRev string) Plan {
	var p Plan
	for _, s := range im.Index.Segments {
		if s.ToSeq <= localCursor {
			p.SkippedSegments = append(p.SkippedSegments, s)
			continue
		}
		p.NewSegments = append(p.NewSegments, s)
	}
	if len(p.NewSegments) > 0 && p.NewSegments[0].FromSeq != localCursor+1 {
		p.GapBeforeApply = true
	}
	p.PolicyAdvances = im.Index.PolicyRevision != "" && im.Index.PolicyRevision != localPolicyRev
	return p
}

// checkContinuity verifies a sorted segment list is ascending, gap-free (each
// from == prev.to+1) and prev-hash linked (each PrevSegmentLastHash == prev.LastHash).
// A single segment or an empty list is trivially continuous. It never re-hashes events;
// it checks the declared boundaries, which the offline archive verifier later confirms
// against the actual bytes.
func checkContinuity(segs []Segment) error {
	for i := 1; i < len(segs); i++ {
		prev, cur := segs[i-1], segs[i]
		if cur.FromSeq != prev.ToSeq+1 {
			return fmt.Errorf("ddil: audit segment gap or overlap: segment starts at seq %d, previous ended at %d", cur.FromSeq, prev.ToSeq)
		}
		if cur.PrevSegmentLastHash != prev.LastHash {
			return fmt.Errorf("ddil: audit chain break between seq %d and %d: prev_segment_last_hash does not match the previous segment's last_hash", prev.ToSeq, cur.FromSeq)
		}
	}
	for _, s := range segs {
		if s.FromSeq < 1 || s.ToSeq < s.FromSeq {
			return fmt.Errorf("ddil: audit segment has an invalid seq range %d..%d", s.FromSeq, s.ToSeq)
		}
	}
	return nil
}

func parseIndex(b []byte) (Index, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var idx Index
	if err := dec.Decode(&idx); err != nil {
		return Index{}, fmt.Errorf("ddil: index is not valid JSON: %w", err)
	}
	if idx.SchemaVersion != IndexSchemaVersion {
		return Index{}, fmt.Errorf("ddil: index schema_version %d unsupported (this build understands %d)", idx.SchemaVersion, IndexSchemaVersion)
	}
	if strings.TrimSpace(idx.Tenant) == "" {
		return Index{}, fmt.Errorf("ddil: index has no tenant")
	}
	return idx, nil
}
