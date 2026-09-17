// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"encoding/json"
	"io"
)

// planDocumentKind is the private discriminant of ApprovedPlanDocument.
// Constructors are the only code that may set it; a missing pointer is not a version.
type planDocumentKind int

const (
	planDocumentUnknown planDocumentKind = iota
	planDocumentV1
	planDocumentV2
)

// ApprovedPlanDocument is the tagged union of v1 and v2 approval files.
// Callers inspect Schema, V1 and V2; they never infer the version from a nil.
type ApprovedPlanDocument struct {
	kind planDocumentKind
	v1   *Plan
	v2   *PlanV2
}

// planSchemaHeader is decoded with encoding/json name folding so Schema, SCHEMA
// and long-s spellings match the schema field. Extra fields are ignored here;
// the version decoder then applies its own strict rules.
type planSchemaHeader struct {
	Schema string `json:"schema"`
}

// ReadApprovedPlan reads a bounded approval file once and dispatches solely by
// the schema value. v1 is handed to ReadPlan unchanged; v2 is handed to
// ReadPlanV2. There is no fallback from one decoder's error to the other.
func ReadApprovedPlan(r io.Reader) (*ApprovedPlanDocument, error) {
	data, err := readBoundedPlanFile(r)
	if err != nil {
		return nil, err
	}
	if err := inspectPlanJSON(data); err != nil {
		return nil, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, refuse(KindInvalidRequest, "plan file is not a JSON object: %v", err)
	}
	if k := observationKeyIn(keys); k != "" {
		return nil, refuse(KindInvalidRequest, "plan file carries the observation field %q, which an approval never contains; regenerate it with plan --out instead of editing it", k)
	}
	var header planSchemaHeader
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, refuse(KindInvalidRequest, "plan file is not a JSON object: %v", err)
	}
	switch header.Schema {
	case PlanSchema:
		p, err := ReadPlan(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return approvedPlanFromV1(p)
	case PlanSchemaV2:
		p, err := parsePlanV2(data)
		if err != nil {
			return nil, err
		}
		return approvedPlanFromV2(p)
	case "":
		return nil, refuse(KindInvalidRequest, "plan file declares no schema")
	default:
		return nil, refuse(KindInvalidRequest, "plan file declares schema %q, which this installer does not read", header.Schema)
	}
}

func approvedPlanFromV1(p *Plan) (*ApprovedPlanDocument, error) {
	if p == nil {
		return nil, refuse(KindInvalidRequest, "v1 plan is missing")
	}
	if p.Driver != claudeDriver {
		return nil, refuse(KindInvalidRequest, "plan file declares schema %q with driver %q; %s only accepts %q", PlanSchema, p.Driver, PlanSchema, claudeDriver)
	}
	return &ApprovedPlanDocument{kind: planDocumentV1, v1: p}, nil
}

func approvedPlanFromV2(p *PlanV2) (*ApprovedPlanDocument, error) {
	if p == nil {
		return nil, refuse(KindInvalidRequest, "v2 plan is missing")
	}
	return &ApprovedPlanDocument{kind: planDocumentV2, v2: p}, nil
}

// Schema reports the discriminant's schema, not a field loaded from a pointer.
func (d *ApprovedPlanDocument) Schema() string {
	if d == nil {
		return ""
	}
	switch d.kind {
	case planDocumentV1:
		return PlanSchema
	case planDocumentV2:
		return PlanSchemaV2
	default:
		return ""
	}
}

// V1 returns the v1 plan only when the discriminant is v1 and the pointer is present.
func (d *ApprovedPlanDocument) V1() (*Plan, bool) {
	if d == nil || d.kind != planDocumentV1 || d.v1 == nil {
		return nil, false
	}
	return d.v1, true
}

// V2 returns the v2 plan only when the discriminant is v2 and the pointer is present.
func (d *ApprovedPlanDocument) V2() (*PlanV2, bool) {
	if d == nil || d.kind != planDocumentV2 || d.v2 == nil {
		return nil, false
	}
	return d.v2, true
}
