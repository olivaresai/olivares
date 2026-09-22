// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var documentScanTargets = []string{"knowledge.document", "knowledge.chunk", "knowledge.sensitivity_label"}

const documentScanPayload = "synthetic-content-must-not-enter-the-receipt"

func TestErasureDocumentCascadeOrphansAreUnverified(t *testing.T) {
	for _, tt := range []struct {
		name, residue string
		kind          model.Kind
		alias         bool
	}{
		{"chunk", "knowledge.chunk.doc_ref", chunkStandInKind, false},
		{"label", "knowledge.sensitivity_label.subject_ref", labelStandInKind, false},
		{"chunk alias", "knowledge.chunk.doc_ref", chunkStandInKind, true},
		{"label alias", "knowledge.sensitivity_label.subject_ref", labelStandInKind, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, WithApprovalGate(approvedGate("apr-doc-orphan")), WithProviderEraser(wiredProvider()))
			tenant, owner := openTenant(t, h, "docorphan")
			ref := model.NewID().String()
			match := ref
			var aliases []string
			if tt.alias {
				match = model.NewID().String()
				aliases = []string{match}
			}
			seedDocumentScanRow(h, tenant, tt.kind, match, "document")
			r, status, id := runDocumentScanErasure(t, h, owner, tenant, ref, aliases, nil)
			if r.code != http.StatusOK || r.body["verify_ok"] != false || status != erasureStatusGaps {
				t.Fatalf("orphan verdict = %d / %v / %q, want 200 / false / gaps: %s", r.code, r.body["verify_ok"], status, r.raw)
			}
			if why := jsonText(r.body["verify_reason"]); !strings.Contains(why, "residual identifiers at: "+tt.residue) {
				t.Fatalf("orphan not identified structurally: %q", why)
			}
			assertDocumentScanCoverage(t, r, documentScanTargets)
			receipt := h.do("GET", "/v1/m/compliance/erasure/"+id+"/receipt", owner, nil, tenantHdr(tenant))
			if receipt.code != http.StatusOK || receipt.body["verify_ok"] != false || jsonText(receipt.body["manifest_hash"]) != jsonText(r.body["manifest_hash"]) {
				t.Fatalf("stored orphan receipt changed its verdict or hash: %d %s", receipt.code, receipt.raw)
			}
			assertDocumentScanCoverage(t, receipt, documentScanTargets)
			if strings.Contains(receipt.raw, documentScanPayload) || strings.Contains(jsonText(receipt.body["verify_reason"]), match) {
				t.Fatal("the receipt disclosed row content or the subject reference")
			}
		})
	}
}

func TestErasureDocumentCascadeCleanReceiptCoversAllStores(t *testing.T) {
	h := newHarness(t, WithApprovalGate(approvedGate("apr-doc-clean")), WithProviderEraser(wiredProvider()))
	tenant, owner := openTenant(t, h, "docclean")
	ref := seedDocumentScanRow(h, tenant, documentStandInKind, "", "")
	seedDocumentScanRow(h, tenant, chunkStandInKind, ref, "document")
	seedDocumentScanRow(h, tenant, labelStandInKind, ref, "document")
	r, status, id := runDocumentScanErasure(t, h, owner, tenant, ref, nil, nil)
	if r.code != http.StatusOK || r.body["verify_ok"] != true || status != erasureStatusCompleted {
		t.Fatalf("clean cascade = %d / %v / %q: %s", r.code, r.body["verify_ok"], status, r.raw)
	}
	assertDocumentScanCoverage(t, r, documentScanTargets)
	if got, want := jsonText(r.body["manifest_hash"]), hashHex(manifestCandidate(r.body, true)); got != want {
		t.Fatalf("cascade coverage is not sealed into the existing manifest: got %s, want %s", got, want)
	}
	receipt := h.do("GET", "/v1/m/compliance/erasure/"+id+"/receipt", owner, nil, tenantHdr(tenant))
	if receipt.code != http.StatusOK || receipt.body["verify_ok"] != true {
		t.Fatalf("clean stored receipt = %d %s", receipt.code, receipt.raw)
	}
	assertDocumentScanCoverage(t, receipt, documentScanTargets)
}

func TestErasureDocumentCascadeKeepsTenantAndLabelKindScope(t *testing.T) {
	h := newHarness(t, WithApprovalGate(approvedGate("apr-doc-scope")), WithProviderEraser(wiredProvider()))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "docscope")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	other := h.createOrg(admin, "docother")
	ref := model.NewID().String()
	seedDocumentScanRow(h, other, chunkStandInKind, ref, "document")
	seedDocumentScanRow(h, other, labelStandInKind, ref, "document")
	seedDocumentScanRow(h, tenant, labelStandInKind, ref, "source_document")
	seedDocumentScanRow(h, tenant, chunkStandInKind, model.NewID().String(), "document")
	r, status, _ := runDocumentScanErasure(t, h, owner, tenant, ref, nil, nil)
	if r.code != http.StatusOK || r.body["verify_ok"] != true || status != erasureStatusCompleted {
		t.Fatalf("unrelated rows became document residues: %d %s", r.code, r.raw)
	}
	assertDocumentScanCoverage(t, r, documentScanTargets)
	report, err := h.mod.residualScan(context.Background(), other, subjectKey{Kind: "document", Ref: ref}, []string{classKnowledgeContent})
	if err != nil || !sameStrings(report.Residues, []string{"knowledge.chunk.doc_ref", "knowledge.sensitivity_label.subject_ref"}) {
		t.Fatalf("the other tenant's residues were lost: %+v, %v", report, err)
	}
}

func TestErasureDocumentCascadeOutOfScopeIsUnestablished(t *testing.T) {
	h := newHarness(t, WithApprovalGate(approvedGate("apr-doc-class")), WithProviderEraser(wiredProvider()))
	tenant, owner := openTenant(t, h, "docclass")
	ref := model.NewID().String()
	seedDocumentScanRow(h, tenant, chunkStandInKind, ref, "document")
	seedDocumentScanRow(h, tenant, labelStandInKind, ref, "document")
	r, status, _ := runDocumentScanErasure(t, h, owner, tenant, ref, nil, []string{classAgentMemory})
	if r.code != http.StatusOK || r.body["verify_ok"] != false || status != erasureStatusGaps {
		t.Fatalf("out-of-scope verdict = %d %s", r.code, r.raw)
	}
	if len(jsonStrings(r.body["residual_scan_applicable"])) != 0 || len(jsonStrings(r.body["residual_scan_opened"])) != 0 || jsonText(r.body["residual_scan_depth"]) != "" {
		t.Fatalf("out-of-scope cascade was declared scanned: %s", r.raw)
	}
	why := jsonText(r.body["verify_reason"])
	if !strings.Contains(why, wantDepthMarker) || strings.Contains(why, "residual identifiers at:") {
		t.Fatalf("out-of-scope scan declaration = %q", why)
	}
}

func TestErasureDocumentCascadeMissingStoresAreDeclared(t *testing.T) {
	for _, missing := range []model.Kind{documentStandInKind, chunkStandInKind, labelStandInKind, "all"} {
		t.Run(string(missing), func(t *testing.T) {
			h := newScanHarness(t, func(reg store.ExtensionRegistry) error {
				return registerErasureStandIns(documentScanRegistry{ExtensionRegistry: reg, missing: missing})
			}, WithApprovalGate(approvedGate("apr-doc-missing")), WithProviderEraser(wiredProvider()))
			tenant, owner := openTenant(t, h, "docmissing")
			ref := model.NewID().String()
			if missing == documentStandInKind {
				seedDocumentScanRow(h, tenant, chunkStandInKind, ref, "document")
			}
			r, status, _ := runDocumentScanErasure(t, h, owner, tenant, ref, nil, nil)
			if r.code != http.StatusOK || r.body["verify_ok"] != false || status != erasureStatusGaps {
				t.Fatalf("missing-store verdict = %d / %v / %q: %s", r.code, r.body["verify_ok"], status, r.raw)
			}
			var opened []string
			for _, target := range documentScanTargets {
				if missing != "all" && target != string(missing) {
					opened = append(opened, target)
				}
			}
			assertDocumentScanCoverage(t, r, opened)
			marker := wantCoverageMarker
			if missing == "all" {
				marker = wantDepthMarker
			}
			why := jsonText(r.body["verify_reason"])
			if !strings.Contains(why, marker) {
				t.Fatalf("missing-store declaration = %q, want %q", why, marker)
			}
			if missing == documentStandInKind && !strings.Contains(why, "knowledge.chunk.doc_ref") {
				t.Fatalf("unavailable parent hid a surviving chunk: %q", why)
			}
		})
	}
}

func TestErasureDocumentCascadeReadFailureSealsNothing(t *testing.T) {
	for _, kind := range []model.Kind{chunkStandInKind, labelStandInKind} {
		for _, failure := range []string{"open", "query"} {
			t.Run(string(kind)+"/"+failure, func(t *testing.T) {
				provider := wiredProvider()
				h := newHarness(t, WithApprovalGate(approvedGate("apr-doc-failure")), WithProviderEraser(provider))
				tenant, owner := openTenant(t, h, "docfailure")
				data := &documentScanFailureData{ModuleData: api.NewModuleData(h.st), kind: kind, open: failure == "open"}
				h.mod.UseData(data)
				provider.afterErase = func(context.Context, model.TenantID) error { data.armed = true; return nil }
				r, status, id := runDocumentScanErasure(t, h, owner, tenant, model.NewID().String(), nil, nil)
				data.armed = false
				if !data.reached || r.code != http.StatusInternalServerError || status != erasureStatusFailed {
					t.Fatalf("cascade read failure = reached:%v / %d / %q: %s", data.reached, r.code, status, r.raw)
				}
				if receipt := h.do("GET", "/v1/m/compliance/erasure/"+id+"/receipt", owner, nil, tenantHdr(tenant)); receipt.code != http.StatusNotFound {
					t.Fatalf("failed scan sealed a receipt: %d %s", receipt.code, receipt.raw)
				}
				provider.afterErase = nil
				retry := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, tenantHdr(tenant))
				if retry.code != http.StatusOK || retry.body["verify_ok"] != true {
					t.Fatalf("read failure destroyed the retry path: %d %s", retry.code, retry.raw)
				}
				assertDocumentScanCoverage(t, retry, documentScanTargets)
			})
		}
	}
}

func assertDocumentScanCoverage(t *testing.T, r resp, opened []string) {
	t.Helper()
	if got := jsonStrings(r.body["residual_scan_applicable"]); !sameStrings(got, documentScanTargets) {
		t.Fatalf("applicable = %v, want %v", got, documentScanTargets)
	}
	if got := jsonStrings(r.body["residual_scan_opened"]); !sameStrings(got, opened) {
		t.Fatalf("opened = %v, want %v", got, opened)
	}
	want := ""
	if len(opened) > 0 {
		want = wantDepth
	}
	if got := jsonText(r.body["residual_scan_depth"]); got != want {
		t.Fatalf("depth = %q, want %q", got, want)
	}
}

func runDocumentScanErasure(t *testing.T, h *harness, owner string, tenant model.TenantID, ref string, aliases, classes []string) (resp, string, string) {
	t.Helper()
	body := map[string]any{"subject_kind": "document", "subject_ref": ref, "case_ref": "DSR-DOCUMENT-SCAN"}
	if len(aliases) > 0 {
		body["aliases"] = aliases
	}
	if len(classes) > 0 {
		body["data_classes"] = classes
	}
	hdr := tenantHdr(tenant)
	created := h.do("POST", "/v1/m/compliance/erasure", owner, body, hdr)
	if created.code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.code, created.raw)
	}
	id := jsonText(created.body["id"])
	r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	status := jsonText(h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr).body["status"])
	return r, status, id
}

func seedDocumentScanRow(h *harness, tenant model.TenantID, kind model.Kind, ref, subjectKind string) string {
	h.t.Helper()
	var id string
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		var record model.Record
		switch kind {
		case documentStandInKind:
			record = model.Record{"kb_ref": model.NewID().String(), "title": documentScanPayload, "chunk_count": int64(1)}
		case chunkStandInKind:
			record = model.Record{"doc_ref": ref, "chunk_index": int64(0), "text": documentScanPayload}
		case labelStandInKind:
			record = model.Record{"subject_kind": subjectKind, "subject_ref": ref, "max_severity": documentScanPayload}
		default:
			return errors.New("unsupported document scan fixture kind")
		}
		created, err := repo.Create(context.Background(), record)
		id = created.String(model.ColID)
		return err
	})
	return id
}

type documentScanRegistry struct {
	store.ExtensionRegistry
	missing model.Kind
}

func (r documentScanRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == r.missing || (r.missing == "all" && (d.Kind == documentStandInKind || d.Kind == chunkStandInKind || d.Kind == labelStandInKind)) {
		return nil
	}
	return r.ExtensionRegistry.Register(d)
}

// The failure is armed only after erasure, at the read-only store seam.
type documentScanFailureData struct {
	api.ModuleData
	kind                 model.Kind
	open, armed, reached bool
}

func (d *documentScanFailureData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		if d.armed {
			return fn(documentScanFailureScope{Scope: sc, failure: d})
		}
		return fn(sc)
	})
}

type documentScanFailureScope struct {
	store.Scope
	failure *documentScanFailureData
}

func (s documentScanFailureScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if kind == s.failure.kind && s.failure.open {
		s.failure.reached = true
		return nil, errors.New("document cascade store unavailable")
	}
	repo, err := s.Scope.Ext(kind)
	if err == nil && kind == s.failure.kind {
		return documentScanFailureRepo{GenericRepo: repo, failure: s.failure}, nil
	}
	return repo, err
}

type documentScanFailureRepo struct {
	store.GenericRepo
	failure *documentScanFailureData
}

func (r documentScanFailureRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	r.failure.reached = true
	return nil, model.Page{}, errors.New("document cascade query failed")
}
