// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// planDigestInput is the selection the digest binds. Field order is fixed by the
// struct, so encoding/json produces one canonical byte string.
type planDigestInput struct {
	Schema           string                `json:"schema"`
	Driver           string                `json:"driver"`
	Channel          string                `json:"channel"`
	RequestedVersion string                `json:"requested_version"`
	Version          string                `json:"version"`
	Platform         Platform              `json:"platform"`
	VendorPlatform   string                `json:"vendor_platform"`
	Source           Source                `json:"source"`
	Artifact         Artifact              `json:"artifact"`
	Provenance       provenanceDigestInput `json:"provenance"`
	Destination      Destination           `json:"destination"`
	// Verified is derived from the resolution, never observed, so binding it
	// cannot stale a legitimate approval while it stops an approval file from
	// claiming facts the resolver did not establish.
	Verified []string `json:"verified"`
}

// provenanceDigestInput is the part of the provenance that is a SELECTION: which
// manifest content, under which pinned key. The signature bytes, their timestamp,
// the signing subkey and the verifier's version string are proof of that
// selection, not part of it: a vendor re-signing the identical manifest, or a
// gpg upgrade, must not turn an approved plan stale while a different manifest,
// artifact or key must.
type provenanceDigestInput struct {
	Class          string `json:"class"`
	KeyFingerprint string `json:"key_fingerprint"`
	ManifestSHA256 string `json:"manifest_sha256"`
	KeySHA256      string `json:"key_sha256"`
}

// ComputeDigest returns the SHA-256 of the plan's canonical selection.
func ComputeDigest(p *Plan) string {
	in := planDigestInput{
		Schema: p.Schema, Driver: p.Driver, Channel: p.Channel, RequestedVersion: p.RequestedVersion,
		Version: p.Version, Platform: p.Platform, VendorPlatform: p.VendorPlatform, Source: p.Source,
		Artifact: p.Artifact, Destination: p.Destination, Verified: p.Verified,
		Provenance: provenanceDigestInput{Class: p.Provenance.Class, KeyFingerprint: p.Provenance.KeyFingerprint,
			ManifestSHA256: p.Provenance.ManifestSHA256, KeySHA256: p.Provenance.KeySHA256},
	}
	b, err := json.Marshal(in)
	if err != nil {
		// Only unmarshalable values reach here and none of the fields can be one.
		panic("toolinstall: plan digest: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// approvalPlan is the document plan --out writes and install --plan reads.
// It has no Action, Observed or Existing fields, so encoding/json's
// case-insensitive matching and DisallowUnknownFields refuse every
// decoder-equivalent spelling of those names (foldName / bytes.EqualFold,
// including non-ASCII fold classes such as U+017F long s). Selection,
// Verified and digest remain; install re-derives observations against the
// destination when it runs.
type approvalPlan struct {
	Schema           string      `json:"schema"`
	Driver           string      `json:"driver"`
	Channel          string      `json:"channel"`
	RequestedVersion string      `json:"requested_version"`
	Version          string      `json:"version"`
	Platform         Platform    `json:"platform"`
	VendorPlatform   string      `json:"vendor_platform"`
	Source           Source      `json:"source"`
	Artifact         Artifact    `json:"artifact"`
	Provenance       Provenance  `json:"provenance"`
	Destination      Destination `json:"destination"`
	Verified         []string    `json:"verified"`
	Digest           string      `json:"digest"`
}

func approvalFromPlan(p *Plan) approvalPlan {
	return approvalPlan{
		Schema: p.Schema, Driver: p.Driver, Channel: p.Channel, RequestedVersion: p.RequestedVersion,
		Version: p.Version, Platform: p.Platform, VendorPlatform: p.VendorPlatform, Source: p.Source,
		Artifact: p.Artifact, Provenance: p.Provenance, Destination: p.Destination, Verified: p.Verified,
	}
}

func (a approvalPlan) asPlan() *Plan {
	return &Plan{
		Schema: a.Schema, Driver: a.Driver, Channel: a.Channel, RequestedVersion: a.RequestedVersion,
		Version: a.Version, Platform: a.Platform, VendorPlatform: a.VendorPlatform, Source: a.Source,
		Artifact: a.Artifact, Provenance: a.Provenance, Destination: a.Destination, Verified: a.Verified,
		Digest: a.Digest,
	}
}

// MarshalPlan renders the APPROVAL representation of a plan: the selection, the
// fixed Verified list and the digest over exactly those, with its digest set.
// The observation fields (action, observed, existing) are absent from the type,
// so they cannot appear even as empty omitempty values. Install re-derives them
// against the destination when it runs.
func MarshalPlan(p *Plan) ([]byte, error) {
	a := approvalFromPlan(p)
	a.Digest = ComputeDigest(p)
	return json.MarshalIndent(&a, "", "  ")
}

const maxPlanFileBytes = 256 << 10

// observationKeys are the canonical JSON names of fields that describe one
// moment on one disk. An approval never contains them, nor any spelling
// encoding/json would fold onto them.
var observationKeys = []string{"action", "observed", "existing"}

// decoderEquivalentObservationKey reports whether k is a name encoding/json
// would match onto an observation field. The relation is bytes.EqualFold, the
// same one foldName implements in encoding/json/fold.go; strings.ToLower is
// not equivalent (U+017F LATIN SMALL LETTER LONG S lowercases to itself).
func decoderEquivalentObservationKey(k string) (canonical string, ok bool) {
	kb := []byte(k)
	for _, name := range observationKeys {
		if bytes.EqualFold(kb, []byte(name)) {
			return name, true
		}
	}
	return "", false
}

func observationKeyIn(keys map[string]json.RawMessage) string {
	// Prefer an exact canonical spelling so the historical lowercase
	// invalid_request diagnostic stays byte-stable.
	for _, name := range observationKeys {
		if _, present := keys[name]; present {
			return name
		}
	}
	var found []string
	for k := range keys {
		if _, ok := decoderEquivalentObservationKey(k); ok {
			found = append(found, k)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	return found[0]
}

// ReadPlan parses an approval file strictly: bounded, no observation fields
// under any decoder-equivalent spelling, no unknown fields, schema matched,
// and the recorded digest equal to the digest of its own selection. Every
// edit to a selection or a Verified claim changes the digest; every
// observation claim is refused by presence before the document is decoded.
func ReadPlan(r io.Reader) (*Plan, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPlanFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	if len(data) > maxPlanFileBytes {
		return nil, refuse(KindInvalidRequest, "plan file is larger than %d bytes", maxPlanFileBytes)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, refuse(KindInvalidRequest, "plan file is not a JSON object: %v", err)
	}
	if k := observationKeyIn(keys); k != "" {
		return nil, refuse(KindInvalidRequest, "plan file carries the observation field %q, which an approval never contains; regenerate it with plan --out instead of editing it", k)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var a approvalPlan
	if err := dec.Decode(&a); err != nil {
		return nil, refuse(KindInvalidRequest, "plan file is not a %s document: %v", PlanSchema, err)
	}
	p := a.asPlan()
	if p.Schema != PlanSchema {
		return nil, refuse(KindInvalidRequest, "plan file declares schema %q, want %q", p.Schema, PlanSchema)
	}
	if got := ComputeDigest(p); got != p.Digest {
		return nil, refuse(KindInvalidRequest, "plan file digest %s does not match its content (%s); the file was edited after it was written", short(p.Digest), short(got))
	}
	return p, nil
}

func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}
