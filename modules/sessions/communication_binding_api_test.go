// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type protocolBindingAPIRemote struct {
	module          *Module
	validationCalls int
	testCalls       int
	reconcileCalls  int
	lastRequest     ProtocolBindingReconcileRequest
	fail            error
}

func (f *protocolBindingAPIRemote) ValidateProtocolBindingSpec(
	_ context.Context,
	_ model.TenantID,
	_ ProtocolBindingSpecInput,
) (ProtocolBindingValidation, error) {
	f.validationCalls++
	return ProtocolBindingValidation{
		Verdict: ProtocolObservationClean, Code: "capability_validated",
		ObservedAt: time.Date(2026, time.August, 18, 11, 55, 0, 0, time.UTC).
			Add(time.Duration(f.validationCalls) * time.Second),
	}, nil
}

func (f *protocolBindingAPIRemote) TestProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	request ProtocolBindingReconcileRequest,
) (ProtocolBindingReconcileResult, error) {
	f.testCalls++
	f.lastRequest = request
	if f.fail != nil {
		return ProtocolBindingReconcileResult{}, f.fail
	}
	return ProtocolBindingReconcileResult{
		Verdict: ProtocolObservationClean, Code: "remote_reachable",
		ObservedAt: time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC),
		Checks: []ProtocolBindingRemoteCheck{{
			Name: "remote_get", Verdict: ProtocolObservationClean,
			EvidenceRef: "sha256:" + strings.Repeat("a", 64),
		}},
		Binding: request.Binding,
	}, nil
}

func (f *protocolBindingAPIRemote) ReconcileProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	request ProtocolBindingReconcileRequest,
) (ProtocolBindingReconcileResult, error) {
	f.reconcileCalls++
	f.lastRequest = request
	if f.fail != nil {
		return ProtocolBindingReconcileResult{}, f.fail
	}
	observedAt := time.Date(2026, time.August, 18, 12, 1, 0, 0, time.UTC)
	var binding ProtocolBinding
	err := f.module.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		record, err := repo.Get(ctx, request.Binding.ID)
		if err != nil {
			return err
		}
		stored, err := decodeProtocolBinding(record)
		if err != nil {
			return err
		}
		if stored.Version != request.ExpectedVersion || stored.Generation != request.Binding.Generation {
			return errors.New("test remote: stale reconcile request")
		}
		stored.ObservationVerdict = ProtocolObservationClean
		stored.ObservationCode = "remote_observed"
		stored.RemoteRevision = "remote-revision-2"
		stored.LastObservedAt = &observedAt
		stored.LastCommandID = model.NewID()
		stored.LastEventID = model.NewID()
		stored.LastEventSeq++
		updated, err := repo.Update(ctx, encodeProtocolBinding(stored))
		if err != nil {
			return err
		}
		stored, err = decodeProtocolBinding(updated)
		if err == nil {
			binding = stored.ProtocolBinding
		}
		return err
	})
	if err != nil {
		return ProtocolBindingReconcileResult{}, err
	}
	return ProtocolBindingReconcileResult{
		Verdict: ProtocolObservationClean, Code: "remote_observed", ObservedAt: observedAt,
		Checks: []ProtocolBindingRemoteCheck{{
			Name: "remote_get", Verdict: ProtocolObservationClean,
			EvidenceRef: "sha256:" + strings.Repeat("b", 64),
		}},
		Binding: binding,
	}, nil
}

func newProtocolBindingAPIHarness(t *testing.T) (*harness, *protocolBindingAPIRemote) {
	t.Helper()
	module := New(
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	remote := &protocolBindingAPIRemote{module: module}
	module.UseProtocolBindingRemoteReconciler(remote)
	module.UseProtocolBindingSpecValidator(BindingProtocolA2A, remote)
	module.UseProtocolBindingSpecValidator(BindingProtocolMCP, remote)
	return newHarness(t, module), remote
}

func seedProtocolBindingAPI(
	t *testing.T,
	h *harness,
	tenant model.TenantID,
	workspace model.ID,
	externalID string,
) ProtocolBinding {
	t.Helper()
	now := time.Date(2026, time.August, 18, 11, 0, 0, 0, time.UTC)
	id := model.NewID()
	stored := storedProtocolBinding{
		ProtocolBinding: ProtocolBinding{
			MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
				ID: id, WorkspaceID: workspace,
			}},
			BindingSpecID: model.NewID(), BindingSpecGeneration: 1,
			PinnedSpecHash:    hashBytes([]byte("api-spec:" + externalID)),
			PinnedMappingHash: hashBytes([]byte("api-mapping:" + externalID)),
			PinnedLossesHash:  hashBytes([]byte("api-losses:" + externalID)),
			WorkItemID:        model.NewID(), Protocol: BindingProtocolA2A,
			ProtocolVersion: "1.0.1", Direction: BindingOutbound,
			PeerAuthority: "https://peer.example", RemoteResourceRef: "agent:remote",
			AttemptID: model.NewID(), Generation: 1, SyntheticSID: newSID(),
			OwnerKind: "user", OwnerRef: "user:" + model.NewID().String(), OwnerEpoch: 1,
			LeaseFence: 1, ExternalKind: string(ProtocolBindingResultTask),
			ExternalID: externalID, ContextID: "context:" + externalID,
			LocalState: "active", RemoteState: "working", RemoteRevision: "remote-revision-1",
			ObservationVerdict: ProtocolObservationClean, ObservationCode: "task_working",
			LastObservedAt: &now, DetailHash: hashBytes([]byte("detail:" + externalID)),
			LastCommandID: model.NewID(), LastEventID: model.NewID(), LastEventSeq: 1,
		},
		dispatchKeyHash: hashBytes([]byte("dispatch:" + externalID)),
		reservationHash: hashBytes([]byte("reservation:" + externalID)),
	}
	var result ProtocolBinding
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		created, err := repo.CreateWithID(context.Background(), id, encodeProtocolBinding(stored))
		if err != nil {
			return err
		}
		decoded, err := decodeProtocolBinding(created)
		if err == nil {
			result = decoded.ProtocolBinding
		}
		return err
	}); err != nil {
		t.Fatalf("seed protocol binding: %v", err)
	}
	return result
}

func TestProtocolBindingAPIListGetAndKeyset(t *testing.T) {
	h, remote := newProtocolBindingAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-api-list")
	workspace := workAPICreateWorkspace(t, h, tenant, "protocol-list")
	first := seedProtocolBindingAPI(t, h, tenant, workspace, "task:list-1")
	second := seedProtocolBindingAPI(t, h, tenant, workspace, "task:list-2")
	base := "/v1/m/sessions/protocol-bindings"

	missingWorkspace := h.do(http.MethodGet, base, admin, tenantHdr(tenant))
	if missingWorkspace.code != http.StatusBadRequest || workAPIErrorCode(missingWorkspace) != "invalid_command" {
		t.Fatalf("missing workspace = %d %s", missingWorkspace.code, missingWorkspace.raw)
	}
	pageOne := h.do(http.MethodGet,
		base+"?workspace_id="+workspace.String()+"&limit=1", admin, tenantHdr(tenant))
	items, _ := pageOne.body["items"].([]any)
	cursor, _ := pageOne.body["next_cursor"].(string)
	if pageOne.code != http.StatusOK || len(items) != 1 || pageOne.body["has_more"] != true ||
		!validWorkCursor(cursor) {
		t.Fatalf("first binding page = %d %s", pageOne.code, pageOne.raw)
	}
	pageTwo := h.do(http.MethodGet,
		base+"?workspace_id="+workspace.String()+"&limit=1&cursor="+cursor,
		admin, tenantHdr(tenant))
	items, _ = pageTwo.body["items"].([]any)
	if pageTwo.code != http.StatusOK || len(items) != 1 || pageTwo.body["has_more"] != false {
		t.Fatalf("second binding page = %d %s", pageTwo.code, pageTwo.raw)
	}
	filtered := h.do(http.MethodGet,
		base+"?workspace_id="+workspace.String()+"&external_id="+second.ExternalID,
		admin, tenantHdr(tenant))
	items, _ = filtered.body["items"].([]any)
	if filtered.code != http.StatusOK || len(items) != 1 ||
		items[0].(map[string]any)["id"] != second.ID.String() {
		t.Fatalf("filtered binding list = %d %s", filtered.code, filtered.raw)
	}
	got := h.do(http.MethodGet, base+"/"+first.ID.String(), admin, tenantHdr(tenant))
	if got.code != http.StatusOK || got.body["id"] != first.ID.String() ||
		got.header.Get("ETag") != `"v1"` {
		t.Fatalf("binding get = %d %s headers=%v", got.code, got.raw, got.header)
	}
	if remote.testCalls != 0 || remote.reconcileCalls != 0 {
		t.Fatalf("read routes called remote adapter: test=%d apply=%d", remote.testCalls, remote.reconcileCalls)
	}
}

func TestProtocolBindingAPIReconcileModesAndPreconditions(t *testing.T) {
	h, remote := newProtocolBindingAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-api-reconcile")
	workspace := workAPICreateWorkspace(t, h, tenant, "protocol-reconcile")
	binding := seedProtocolBindingAPI(t, h, tenant, workspace, "task:reconcile-1")
	path := "/v1/m/sessions/protocol-bindings/" + binding.ID.String() + "/reconcile"

	plan := h.doJSON(http.MethodPost, path+"?mode=plan", admin, map[string]any{}, tenantHdr(tenant))
	planHash, _ := plan.body["plan_hash"].(string)
	if plan.code != http.StatusOK || len(planHash) != 64 || plan.header.Get("ETag") != `"v1"` ||
		remote.testCalls != 0 || remote.reconcileCalls != 0 {
		t.Fatalf("reconcile plan = %d %s headers=%v calls=%d/%d",
			plan.code, plan.raw, plan.header, remote.testCalls, remote.reconcileCalls)
	}
	validated := h.doJSON(http.MethodPost, path+"?mode=validate", admin, map[string]any{}, tenantHdr(tenant))
	if validated.code != http.StatusOK || validated.body["verdict"] != string(VerdictClean) ||
		validated.body["plan_hash"] != "" || remote.testCalls != 0 || remote.reconcileCalls != 0 {
		t.Fatalf("reconcile validate = %d %s", validated.code, validated.raw)
	}
	tested := h.doJSON(http.MethodPost, path+"?mode=test", admin, map[string]any{}, tenantHdr(tenant))
	resource, _ := tested.body["resource"].(map[string]any)
	if tested.code != http.StatusOK || tested.body["verdict"] != string(VerdictClean) ||
		tested.body["code"] != "remote_reachable" || resource["version"] != float64(1) ||
		remote.testCalls != 1 || remote.reconcileCalls != 0 {
		t.Fatalf("reconcile test = %d %s calls=%d/%d", tested.code, tested.raw, remote.testCalls, remote.reconcileCalls)
	}
	afterTest := h.do(http.MethodGet,
		"/v1/m/sessions/protocol-bindings/"+binding.ID.String(), admin, tenantHdr(tenant))
	if afterTest.header.Get("ETag") != `"v1"` {
		t.Fatalf("test mode mutated durable binding: %d %s", afterTest.code, afterTest.raw)
	}

	applyHeaders := workAPIHeaders(tenant, map[string]string{
		"If-Match": `"v1"`, "If-Plan-Hash": planHash,
	})
	missingKey := h.doJSON(http.MethodPost, path+"?mode=apply", admin, map[string]any{}, applyHeaders)
	if missingKey.code != http.StatusBadRequest || workAPIErrorCode(missingKey) != "idempotency_key_required" {
		t.Fatalf("apply missing idempotency = %d %s", missingKey.code, missingKey.raw)
	}
	missingVersion := h.doJSON(http.MethodPost, path+"?mode=apply", admin, map[string]any{},
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	if missingVersion.code != http.StatusPreconditionRequired || workAPIErrorCode(missingVersion) != "version_required" {
		t.Fatalf("apply missing If-Match = %d %s", missingVersion.code, missingVersion.raw)
	}
	staleVersion := h.doJSON(http.MethodPost, path+"?mode=apply", admin, map[string]any{},
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Match": `"v9"`,
		}))
	if staleVersion.code != http.StatusPreconditionFailed || workAPIErrorCode(staleVersion) != "version_mismatch" {
		t.Fatalf("apply stale If-Match = %d %s", staleVersion.code, staleVersion.raw)
	}
	stalePlan := h.doJSON(http.MethodPost, path+"?mode=apply", admin, map[string]any{},
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Match": `"v1"`,
			"If-Plan-Hash": strings.Repeat("0", 64),
		}))
	if stalePlan.code != http.StatusPreconditionFailed || workAPIErrorCode(stalePlan) != "plan_changed" {
		t.Fatalf("apply stale plan = %d %s", stalePlan.code, stalePlan.raw)
	}
	clientObservation := h.doJSON(http.MethodPost, path+"?mode=apply", admin,
		map[string]any{"remote_state": "completed"}, workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Match": `"v1"`,
		}))
	if clientObservation.code != http.StatusBadRequest || workAPIErrorCode(clientObservation) != "invalid_command" {
		t.Fatalf("client observation body = %d %s", clientObservation.code, clientObservation.raw)
	}
	if remote.reconcileCalls != 0 {
		t.Fatalf("failed preconditions reached remote apply: %d", remote.reconcileCalls)
	}

	applyHeaders["Idempotency-Key"] = model.NewID().String()
	applied := h.doJSON(http.MethodPost, path+"?mode=apply", admin, map[string]any{}, applyHeaders)
	resource, _ = applied.body["resource"].(map[string]any)
	if applied.code != http.StatusOK || applied.body["verdict"] != string(VerdictClean) ||
		applied.body["code"] != "remote_observed" || resource["version"] != float64(2) ||
		applied.header.Get("ETag") != `"v2"` || remote.reconcileCalls != 1 ||
		remote.lastRequest.ExpectedVersion != 1 || remote.lastRequest.ExpectedPlanHash != planHash ||
		!strings.HasPrefix(remote.lastRequest.SemanticKey, "http_binding_apply_") {
		t.Fatalf("reconcile apply = %d %s headers=%v request=%#v calls=%d",
			applied.code, applied.raw, applied.header, remote.lastRequest, remote.reconcileCalls)
	}

	h.m.UseProtocolBindingRemoteReconciler(nil)
	currentPlan := h.doJSON(http.MethodPost, path+"?mode=plan", admin, map[string]any{}, tenantHdr(tenant))
	currentHash, _ := currentPlan.body["plan_hash"].(string)
	unwired := h.doJSON(http.MethodPost, path+"?mode=apply", admin, map[string]any{},
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Match": `"v2"`,
			"If-Plan-Hash": currentHash,
		}))
	if unwired.code != http.StatusServiceUnavailable || unwired.body["verdict"] != string(VerdictUnknown) ||
		workAPIErrorCode(unwired) != "observation_unavailable" {
		t.Fatalf("unwired reconcile = %d %s", unwired.code, unwired.raw)
	}
}

func TestProtocolBindingAPIWorkspaceConfinement(t *testing.T) {
	h, remote := newProtocolBindingAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-api-confinement")
	workspaceA := workAPICreateWorkspace(t, h, tenant, "protocol-a")
	workspaceB := workAPICreateWorkspace(t, h, tenant, "protocol-b")
	bindingA := seedProtocolBindingAPI(t, h, tenant, workspaceA, "task:workspace-a")
	bindingB := seedProtocolBindingAPI(t, h, tenant, workspaceB, "task:workspace-b")
	confined := workAPIRoleTokenIn(
		t, h, admin, tenant, auth.RoleAdmin, "protocol-confined@a.test", workspaceA,
	)
	base := "/v1/m/sessions/protocol-bindings"

	listed := h.do(http.MethodGet, base, confined, tenantHdr(tenant))
	items, _ := listed.body["items"].([]any)
	if listed.code != http.StatusOK || len(items) != 1 ||
		items[0].(map[string]any)["id"] != bindingA.ID.String() {
		t.Fatalf("confined binding list = %d %s", listed.code, listed.raw)
	}
	foreignList := h.do(http.MethodGet,
		base+"?workspace_id="+workspaceB.String(), confined, tenantHdr(tenant))
	if foreignList.code != http.StatusNotFound {
		t.Fatalf("confined foreign list = %d %s", foreignList.code, foreignList.raw)
	}
	if own := h.do(http.MethodGet, base+"/"+bindingA.ID.String(), confined, tenantHdr(tenant)); own.code != http.StatusOK {
		t.Fatalf("confined own get = %d %s", own.code, own.raw)
	}
	if foreign := h.do(http.MethodGet, base+"/"+bindingB.ID.String(), confined, tenantHdr(tenant)); foreign.code != http.StatusNotFound {
		t.Fatalf("confined foreign get = %d %s", foreign.code, foreign.raw)
	}
	foreignReconcile := h.doJSON(http.MethodPost,
		base+"/"+bindingB.ID.String()+"/reconcile?mode=test", confined,
		map[string]any{}, tenantHdr(tenant))
	if foreignReconcile.code != http.StatusNotFound || remote.testCalls != 0 || remote.reconcileCalls != 0 {
		t.Fatalf("confined foreign reconcile = %d %s calls=%d/%d",
			foreignReconcile.code, foreignReconcile.raw, remote.testCalls, remote.reconcileCalls)
	}
}
