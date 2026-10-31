// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package compliance

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestEvidenceSupplement(t *testing.T) {
	var anchor int64
	h := newHarness(t, WithEvidenceSupplement(func(_ context.Context, _ store.Scope, seq int64) (map[string]any, error) {
		anchor = seq
		return map[string]any{"example": true}, nil
	}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "supplement")
	token := h.roleToken(admin, tenant, "reader@x.io", "editor")

	var id string
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(packageKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), model.Record{
			colFramework: "eu_ai_act", colFrameworkVer: "stored-v1", colGeneratedAt: "2026-06-04T12:00:00Z", colGeneratedBy: "system",
			colLedgerSeq: int64(7), colLedgerHash: "stored-ledger", colIntegrityOK: true, colIntegrityN: int64(7),
			colCtrlTotal: int64(0), colSatisfied: int64(0), colPartial: int64(0), colGap: int64(0), colUnmapped: int64(0), colManifestHash: "stored-manifest",
		})
		if err == nil {
			id = rec.String(model.ColID)
		}
		return err
	})
	exported := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export", token, nil, tenantHdr(tenant))
	if exported.code != 200 || anchor != 7 || exported.body["ledger_evidence"] == nil {
		t.Fatalf("export anchor=%d: %d %s", anchor, exported.code, exported.raw)
	}
	h.mod.evidenceSupplement = func(context.Context, store.Scope, int64) (map[string]any, error) { return nil, store.ErrConflict }
	failed := h.do("GET", "/v1/m/compliance/evidence/"+id+"/export", token, nil, tenantHdr(tenant))
	if failed.code != 409 || failed.body["ledger_evidence"] != nil {
		t.Fatalf("failed supplement escaped: %d %s", failed.code, failed.raw)
	}
}
