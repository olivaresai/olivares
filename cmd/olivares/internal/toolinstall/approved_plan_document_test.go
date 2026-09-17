// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testdataFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(b, "\n")
}

func TestV1CanonicalBytesAndDigestViaReadApprovedPlan(t *testing.T) {
	p := v1ClaudePlan()
	raw := mustMarshalV1(t, p)
	frozen := testdataFile(t, "plan-v1-claude.json")
	if !bytes.Equal(raw, frozen) {
		t.Fatalf("v1 MarshalPlan bytes changed\ngot:\n%s\nwant:\n%s", raw, frozen)
	}
	wantDigest := ComputeDigest(p)
	viaRead, err := ReadPlan(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadPlan: %v", err)
	}
	if viaRead.Digest != wantDigest {
		t.Fatalf("ReadPlan digest %s, want %s", viaRead.Digest, wantDigest)
	}
	doc, err := ReadApprovedPlan(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadApprovedPlan: %v", err)
	}
	if doc.Schema() != PlanSchema {
		t.Fatalf("Schema()=%q, want %q", doc.Schema(), PlanSchema)
	}
	got, ok := doc.V1()
	if !ok || got == nil {
		t.Fatal("V1() did not return the v1 plan")
	}
	if _, ok := doc.V2(); ok {
		t.Fatal("V2() reported true for a v1 document")
	}
	if got.Digest != wantDigest || got.Action != "" || got.Existing != nil || len(got.Observed) != 0 {
		t.Fatalf("ReadApprovedPlan v1 plan drifted: digest=%s action=%q observed=%v existing=%v", got.Digest, got.Action, got.Observed, got.Existing)
	}
	rewritten := mustMarshalV1(t, got)
	if !bytes.Equal(rewritten, frozen) {
		t.Fatalf("re-MarshalPlan of ReadApprovedPlan v1 changed bytes")
	}
	if viaRead.Digest != got.Digest {
		t.Fatal("ReadPlan and ReadApprovedPlan produced different v1 digests")
	}
}

func TestReadPlanRejectsPlanV2Document(t *testing.T) {
	raw := testdataFile(t, "plan-v2-codex.json")
	_, err := ReadPlan(bytes.NewReader(raw))
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("ReadPlan accepted a v2 document: %v", err)
	}
}

func TestReadPlanV2RejectsPlanV1Document(t *testing.T) {
	raw := testdataFile(t, "plan-v1-claude.json")
	_, err := ReadPlanV2(bytes.NewReader(raw))
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("ReadPlanV2 accepted a v1 document: %v", err)
	}
}

func TestReadApprovedPlanCodexAndGrokV2(t *testing.T) {
	cases := []struct {
		name   string
		file   string
		build  func() *PlanV2
		driver string
	}{
		{"codex", "plan-v2-codex.json", validCodexPlanV2, DriverCodex},
		{"grok", "plan-v2-grok.json", validGrokPlanV2, DriverGrok},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			built := tc.build()
			raw := mustMarshalV2(t, built)
			frozen := testdataFile(t, tc.file)
			if !bytes.Equal(raw, frozen) {
				t.Fatalf("v2 MarshalPlanV2 bytes changed\ngot:\n%s\nwant:\n%s", raw, frozen)
			}
			doc, err := ReadApprovedPlan(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("ReadApprovedPlan: %v", err)
			}
			if doc.Schema() != PlanSchemaV2 {
				t.Fatalf("Schema()=%q", doc.Schema())
			}
			if _, ok := doc.V1(); ok {
				t.Fatal("V1() reported true for a v2 document")
			}
			got, ok := doc.V2()
			if !ok {
				t.Fatal("V2() did not return the v2 plan")
			}
			if got.Selection.Driver != tc.driver {
				t.Fatalf("driver %q, want %q", got.Selection.Driver, tc.driver)
			}
			if got.Digest != ComputeDigestV2(built) {
				t.Fatalf("digest %s, want %s", got.Digest, ComputeDigestV2(built))
			}
			direct, err := ReadPlanV2(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("ReadPlanV2: %v", err)
			}
			if direct.Digest != got.Digest {
				t.Fatal("ReadPlanV2 and ReadApprovedPlan produced different v2 digests")
			}
		})
	}
}

func TestReadApprovedPlanRefusesSchemaAndDriverMismatch(t *testing.T) {
	v2 := mustMarshalV2(t, validCodexPlanV2())
	v2Claude := bytes.Replace(v2, []byte(`"driver": "codex"`), []byte(`"driver": "claude"`), 1)
	_, err := ReadApprovedPlan(bytes.NewReader(v2Claude))
	if KindOf(err) != KindInvalidRequest {
		t.Fatalf("v2+claude: %v", err)
	}

	v1 := v1ClaudePlan()
	v1.Driver = DriverCodex
	raw, err := MarshalPlan(v1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPlan(bytes.NewReader(raw)); err != nil {
		t.Fatalf("ReadPlan must still accept a v1 document whose driver is not claude: %v", err)
	}
	_, err = ReadApprovedPlan(bytes.NewReader(raw))
	if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "driver") {
		t.Fatalf("v1+codex via ReadApprovedPlan: %v", err)
	}

	unknown := []byte(`{"schema":"olivares.ai/tool-install/plan/v0","digest":"` + fixtureHex(0x00) + `"}`)
	_, err = ReadApprovedPlan(bytes.NewReader(unknown))
	if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("unknown schema: %v", err)
	}
}

func TestReadApprovedPlanDispatchesByFoldedSchemaKey(t *testing.T) {
	raw := testdataFile(t, "plan-v1-claude.json")
	folded := bytes.Replace(raw, []byte(`"schema"`), []byte(`"Schema"`), 1)
	doc, err := ReadApprovedPlan(bytes.NewReader(folded))
	if err != nil {
		t.Fatalf("folded Schema key: %v", err)
	}
	if _, ok := doc.V1(); !ok {
		t.Fatal("folded schema key did not dispatch to v1")
	}
	longS := bytes.Replace(raw, []byte(`"schema"`), []byte(`"\u017fchema"`), 1)
	doc, err = ReadApprovedPlan(bytes.NewReader(longS))
	if err != nil {
		t.Fatalf("long-s schema key: %v", err)
	}
	if _, ok := doc.V1(); !ok {
		t.Fatal("long-s schema key did not dispatch to v1")
	}
}

func TestReadApprovedPlanRefusesUnknownTrailingOversizeDuplicate(t *testing.T) {
	raw := testdataFile(t, "plan-v2-codex.json")

	t.Run("unknown field", func(t *testing.T) {
		edited := bytes.Replace(raw, []byte(`"digest":`), []byte(`"extra": true,`+"\n  "+`"digest":`), 1)
		_, err := ReadApprovedPlan(bytes.NewReader(edited))
		if KindOf(err) != KindInvalidRequest {
			t.Fatalf("unknown field: %v", err)
		}
	})
	t.Run("trailing", func(t *testing.T) {
		edited := append(append([]byte{}, raw...), []byte("\n0")...)
		_, err := ReadApprovedPlan(bytes.NewReader(edited))
		if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "trailing") {
			t.Fatalf("trailing: %v", err)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		huge := bytes.Repeat([]byte{'a'}, maxPlanFileBytes+2)
		_, err := ReadApprovedPlan(bytes.NewReader(huge))
		if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "larger") {
			t.Fatalf("oversize: %v", err)
		}
	})
	t.Run("duplicate keys", func(t *testing.T) {
		edited := bytes.Replace(raw, []byte(`"schema":`), []byte(`"schema": "ignored",`+"\n  "+`"schema":`), 1)
		_, err := ReadApprovedPlan(bytes.NewReader(edited))
		if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("duplicate keys: %v", err)
		}
	})
	t.Run("folded duplicate keys", func(t *testing.T) {
		edited := bytes.Replace(raw, []byte(`"schema":`), []byte(`"SCHEMA": "ignored",`+"\n  "+`"schema":`), 1)
		_, err := ReadApprovedPlan(bytes.NewReader(edited))
		if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("folded duplicate keys: %v", err)
		}
	})
}

func TestReadApprovedPlanRefusesB1ObservationFields(t *testing.T) {
	raw := testdataFile(t, "plan-v2-codex.json")
	for _, tc := range observationApprovalInjections {
		t.Run("v2 "+tc.name, func(t *testing.T) {
			edited := bytes.Replace(raw, []byte(`"digest":`), []byte(tc.field+"\n  \"digest\":"), 1)
			if bytes.Equal(edited, raw) {
				t.Fatal("injection anchor missing")
			}
			_, err := ReadApprovedPlan(bytes.NewReader(edited))
			if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "observation field") {
				t.Fatalf("accepted decoder-equivalent observation %s: %v", tc.field, err)
			}
		})
	}
	v1 := testdataFile(t, "plan-v1-claude.json")
	for _, tc := range observationApprovalInjections {
		t.Run("v1 "+tc.name, func(t *testing.T) {
			edited, ok := injectApprovalJSON(v1, tc.field)
			if !ok {
				t.Fatal("injection anchor missing")
			}
			_, err := ReadApprovedPlan(bytes.NewReader(edited))
			if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "observation field") {
				t.Fatalf("accepted v1 observation via ReadApprovedPlan %s: %v", tc.field, err)
			}
		})
	}
}

func TestApprovedPlanDocumentAccessorsFollowDiscriminant(t *testing.T) {
	d := &ApprovedPlanDocument{kind: planDocumentV1, v1: nil, v2: validCodexPlanV2()}
	if d.Schema() != PlanSchema {
		t.Fatalf("schema follows discriminant, got %q", d.Schema())
	}
	if _, ok := d.V1(); ok {
		t.Fatal("a missing v1 pointer must not report V1")
	}
	if _, ok := d.V2(); ok {
		t.Fatal("a v2 pointer must not imply version when the discriminant is v1")
	}
	if (&ApprovedPlanDocument{}).Schema() != "" {
		t.Fatal("unknown discriminant must not report a schema")
	}
}
