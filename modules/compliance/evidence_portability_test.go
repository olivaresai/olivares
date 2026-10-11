// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package compliance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A downgrade preserves the stored package and its controls, including their audit reads.
func TestStoredEvidencePortableAcrossEditions(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "stored-evidence")
	other := h.createOrg(admin, "other-evidence")
	viewer := h.roleToken(admin, tenant, "v@evidence.test", "viewer")
	otherViewer := h.roleToken(admin, other, "other@evidence.test", "viewer")
	var id string
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(packageKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), model.Record{
			colFramework: "eu_ai_act", colFrameworkVer: "stored-v1", colGeneratedAt: "2026-06-04T12:00:00Z", colGeneratedBy: "system",
			colLedgerSeq: int64(7), colLedgerHash: "stored-ledger", colIntegrityOK: true, colIntegrityN: int64(7),
			colCtrlTotal: int64(1), colSatisfied: int64(0), colPartial: int64(0), colGap: int64(1), colUnmapped: int64(0), colManifestHash: "stored-manifest",
		})
		if err != nil {
			return err
		}
		id = rec.String(model.ColID)
		controls, err := sc.Ext(resultKind)
		if err != nil {
			return err
		}
		_, err = controls.Create(context.Background(), model.Record{colPackageRef: id, colFramework: "eu_ai_act", colControlID: "stored-control", colTitle: "Stored control", colStatus: "gap", colCaps: "[]", colOccurredAt: "2026-06-04T12:00:00Z"})
		return err
	})
	h.mod.packsAuthorize = func(string) error { return errors.New("pack withdrawn") }
	for _, suffix := range []string{"", "/export?format=json", "/export?format=csv"} {
		r := h.do("GET", "/v1/m/compliance/evidence/"+id+suffix, viewer, nil, tenantHdr(tenant))
		if r.code != http.StatusOK || !strings.Contains(r.raw, "stored-control") {
			t.Fatalf("stored evidence %s: %d %s", suffix, r.code, r.raw)
		}
	}
	r := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export?format=oscal", viewer, nil, tenantHdr(tenant))
	want := http.StatusNotImplemented
	if complianceViewsAvailable() {
		want = http.StatusOK
	}
	if r.code != want {
		t.Fatalf("stored evidence OSCAL = %d, want %d: %s", r.code, want, r.raw)
	}
	if want == http.StatusOK && !strings.Contains(r.raw, "stored-control") {
		t.Fatal("withdrawn-pack OSCAL lost the stored control")
	}
	r = h.do("GET", "/v1/m/compliance/evidence/"+id, otherViewer, nil, tenantHdr(other))
	if r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant stored evidence: %d", r.code)
	}
	actions := strings.Join(h.auditActions(tenant), ",")
	if !strings.Contains(actions, "compliance.evidence.read") || !strings.Contains(actions, "compliance.evidence.export") {
		t.Fatalf("stored evidence must self-audit: %s", actions)
	}
}
