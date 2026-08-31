// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oasf

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SchemaVersion is the OASF schema version this connector imports, validates
// against and stamps on export (verified 2026-06-11, schema.oasf.outshift.com).
const SchemaVersion = "1.0.0"

// Record is one typed OASF agent descriptor. Field order is fixed (alphabetical
// by JSON name) so Export emits deterministic, canonical-ish JSON — the
// primitive the export wave builds on. The REQUIRED fields per the OASF
// 1.0.0 JSON Schema are authors, created_at, description, name, schema_version,
// version and skills; domains and modules are recommended; locators and
// annotations are optional.
type Record struct {
	// Annotations are free-form string metadata (optional).
	Annotations map[string]string `json:"annotations,omitempty"`
	// Authors are the record's authors (REQUIRED, non-empty).
	Authors []string `json:"authors"`
	// CreatedAt is the record creation timestamp (REQUIRED, RFC 3339).
	CreatedAt string `json:"created_at"`
	// Description is the human description (REQUIRED).
	Description string `json:"description"`
	// Domains are the taxonomy domains (recommended).
	Domains []Domain `json:"domains,omitempty"`
	// Locators say where the agent's artifacts live (optional).
	Locators []Locator `json:"locators,omitempty"`
	// Modules are the record's extension modules (recommended).
	Modules []Module `json:"modules,omitempty"`
	// Name is the agent name (REQUIRED; with Version it forms the roster Ref).
	Name string `json:"name"`
	// SchemaVersion is the OASF schema version (REQUIRED; Export fills "1.0.0").
	SchemaVersion string `json:"schema_version"`
	// Skills are the agent's taxonomy skills (REQUIRED, non-empty array).
	Skills []Skill `json:"skills"`
	// Version is the agent version (REQUIRED).
	Version string `json:"version"`
}

// Skill is one OASF skill taxonomy entry.
type Skill struct {
	ID   uint64 `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Domain is one OASF domain taxonomy entry.
type Domain struct {
	ID   uint64 `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Module is one OASF extension module. Data is kept as a generic JSON object so
// import→export is lossless for object-valued module data (the only shape the
// published schema uses).
type Module struct {
	Name string         `json:"name,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

// Locator is one OASF artifact locator.
type Locator struct {
	Type string `json:"type,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Ref returns the record's roster reference: "oasf:<name>@<version>".
func (r Record) Ref() string { return "oasf:" + r.Name + "@" + r.Version }

// validateReason checks the OASF 1.0.0 REQUIRED field set and returns a short,
// non-sensitive reason CATEGORY ("" when valid). The category is safe to place
// in a finding title; it never carries file contents. Go cannot distinguish an
// absent string field from an empty one, so empty-required-string == missing.
func (r Record) validateReason() string {
	switch {
	case strings.TrimSpace(r.Name) == "":
		return "missing_name"
	case strings.TrimSpace(r.Version) == "":
		return "missing_version"
	case strings.TrimSpace(r.SchemaVersion) == "":
		return "missing_schema_version"
	case strings.TrimSpace(r.Description) == "":
		return "missing_description"
	case len(r.Authors) == 0:
		return "missing_authors"
	case strings.TrimSpace(r.CreatedAt) == "":
		return "missing_created_at"
	case len(r.Skills) == 0:
		return "missing_skills"
	}
	if _, err := time.Parse(time.RFC3339, r.CreatedAt); err != nil {
		return "created_at_not_rfc3339"
	}
	return ""
}

// Import parses one OASF record JSON object and validates the REQUIRED field
// set (types checked by the typed unmarshal; created_at must parse RFC 3339;
// skills must be a non-empty array). It is the inverse of Export:
// Import(Export(x)) == x.
func Import(data []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("oasf: import: %w", err)
	}
	if reason := rec.validateReason(); reason != "" {
		return Record{}, fmt.Errorf("oasf: import: invalid record (%s)", reason)
	}
	return rec, nil
}

// Export serializes rec as an OASF record JSON document: it fills
// schema_version "1.0.0" when empty, validates the same REQUIRED set Import
// enforces, and emits deterministic JSON (fixed struct field order; map keys
// sorted by encoding/json). It is a pure helper — the primitive the export
// wave builds on.
func Export(rec Record) ([]byte, error) {
	if rec.SchemaVersion == "" {
		rec.SchemaVersion = SchemaVersion
	}
	if reason := rec.validateReason(); reason != "" {
		return nil, fmt.Errorf("oasf: export: invalid record (%s)", reason)
	}
	out, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("oasf: export: %w", err)
	}
	return append(out, '\n'), nil
}

// parseRecord parses one record object from raw and returns it with a
// validation reason category ("" when valid). On a type-level parse failure it
// best-effort salvages name/version (for the finding's subject ref only) from a
// generic probe and reports "wrong_field_type"; raw that is not a JSON object
// at all reports "not_json".
func parseRecord(raw json.RawMessage) (Record, string) {
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		var probe map[string]any
		if json.Unmarshal(raw, &probe) != nil {
			return Record{}, "not_json"
		}
		if v, ok := probe["name"].(string); ok {
			rec.Name = v
		}
		if v, ok := probe["version"].(string); ok {
			rec.Version = v
		}
		return rec, "wrong_field_type"
	}
	return rec, rec.validateReason()
}
