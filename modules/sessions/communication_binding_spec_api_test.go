// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type protocolBindingUnsupportedValidator struct{}

func (protocolBindingUnsupportedValidator) ValidateProtocolBindingSpec(
	context.Context,
	model.TenantID,
	ProtocolBindingSpecInput,
) (ProtocolBindingValidation, error) {
	return ProtocolBindingValidation{}, ErrProtocolBindingSpecUnsupported
}

func TestProtocolBindingSpecAPIDraftActivateDisableAndRead(t *testing.T) {
	h, _ := newProtocolBindingAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-spec-api")
	workspace := workAPICreateWorkspace(t, h, tenant, "protocol-spec")
	input := protocolSpecInputForTest(workspace, BindingProtocolA2A, "primary-agent", 1, "")
	base := "/v1/m/sessions/protocol-binding-specs"

	plan := h.doJSON(http.MethodPost, base+"?mode=plan", admin, input, tenantHdr(tenant))
	planHash, _ := plan.body["plan_hash"].(string)
	if plan.code != http.StatusOK || len(planHash) != 64 ||
		plan.body["code"] != "draft_planned" || plan.body["generation"] != float64(1) {
		t.Fatalf("draft plan = %d %s", plan.code, plan.raw)
	}
	missingPlan := h.doJSON(http.MethodPost, base+"?mode=apply", admin, input,
		workAPIHeaders(tenant, map[string]string{"Idempotency-Key": model.NewID().String()}))
	if missingPlan.code != http.StatusPreconditionRequired || workAPIErrorCode(missingPlan) != "plan_hash_required" {
		t.Fatalf("draft apply without plan = %d %s", missingPlan.code, missingPlan.raw)
	}
	draftKey := model.NewID().String()
	created := h.doJSON(http.MethodPost, base+"?mode=apply", admin, input,
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": draftKey, "If-Plan-Hash": planHash,
		}))
	spec, _ := created.body["spec"].(map[string]any)
	specID, _ := spec["id"].(string)
	knownLosses, lossesAreArray := spec["known_losses"].([]any)
	if created.code != http.StatusCreated || specID == "" || spec["state"] != string(ProtocolBindingSpecDraft) ||
		created.header.Get("ETag") != `"v1"` || !lossesAreArray || len(knownLosses) != 0 {
		t.Fatalf("draft apply = %d %s headers=%v", created.code, created.raw, created.header)
	}
	replayed := h.doJSON(http.MethodPost, base+"?mode=apply", admin, input,
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": draftKey, "If-Plan-Hash": planHash,
		}))
	if replayed.code != http.StatusOK || replayed.body["replayed"] != true ||
		replayed.header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("draft replay = %d %s headers=%v", replayed.code, replayed.raw, replayed.header)
	}

	listed := h.do(http.MethodGet,
		base+"?workspace_id="+workspace.String()+"&binding_key=primary-agent&state=draft",
		admin, tenantHdr(tenant))
	items, _ := listed.body["items"].([]any)
	if listed.code != http.StatusOK || len(items) != 1 ||
		items[0].(map[string]any)["id"] != specID {
		t.Fatalf("draft list = %d %s", listed.code, listed.raw)
	}
	got := h.do(http.MethodGet, base+"/"+specID, admin, tenantHdr(tenant))
	if got.code != http.StatusOK || got.body["id"] != specID || got.header.Get("ETag") != `"v1"` {
		t.Fatalf("draft get = %d %s headers=%v", got.code, got.raw, got.header)
	}

	activatePath := base + "/" + specID + "/activate"
	activationPlan := h.doJSON(http.MethodPost, activatePath+"?mode=plan", admin,
		map[string]any{}, tenantHdr(tenant))
	activationHash, _ := activationPlan.body["plan_hash"].(string)
	if activationPlan.code != http.StatusOK || len(activationHash) != 64 ||
		activationPlan.header.Get("ETag") != `"v1"` {
		t.Fatalf("activation plan = %d %s headers=%v", activationPlan.code, activationPlan.raw, activationPlan.header)
	}
	activated := h.doJSON(http.MethodPost, activatePath+"?mode=apply", admin,
		map[string]any{}, workAPIHeaders(tenant, map[string]string{
			"If-Match": `"v1"`, "If-Plan-Hash": activationHash,
			"Idempotency-Key": model.NewID().String(),
		}))
	activeSpec, _ := activated.body["spec"].(map[string]any)
	if activated.code != http.StatusOK || activeSpec["state"] != string(ProtocolBindingSpecActive) ||
		activated.header.Get("ETag") != `"v2"` {
		t.Fatalf("activation apply = %d %s headers=%v", activated.code, activated.raw, activated.header)
	}

	disablePath := base + "/" + specID + "/disable"
	disablePlan := h.doJSON(http.MethodPost, disablePath+"?mode=plan", admin,
		map[string]any{}, tenantHdr(tenant))
	disableHash, _ := disablePlan.body["plan_hash"].(string)
	disabled := h.doJSON(http.MethodPost, disablePath+"?mode=apply", admin,
		map[string]any{}, workAPIHeaders(tenant, map[string]string{
			"If-Match": `"v2"`, "If-Plan-Hash": disableHash,
			"Idempotency-Key": model.NewID().String(),
		}))
	disabledSpec, _ := disabled.body["spec"].(map[string]any)
	if disablePlan.code != http.StatusOK || disabled.code != http.StatusOK ||
		disabledSpec["state"] != string(ProtocolBindingSpecDisabled) ||
		disabled.header.Get("ETag") != `"v3"` {
		t.Fatalf("disable plan/apply = plan:%d %s apply:%d %s",
			disablePlan.code, disablePlan.raw, disabled.code, disabled.raw)
	}
}

func TestProtocolBindingSpecAPIConfinementAndPreconditions(t *testing.T) {
	h, remote := newProtocolBindingAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-spec-confinement")
	workspaceA := workAPICreateWorkspace(t, h, tenant, "protocol-spec-a")
	workspaceB := workAPICreateWorkspace(t, h, tenant, "protocol-spec-b")
	confined := workAPIRoleTokenIn(
		t, h, admin, tenant, auth.RoleAdmin, "protocol-spec-confined@a.test", workspaceA,
	)
	input := protocolSpecInputForTest(workspaceB, BindingProtocolMCP, "foreign", 1, "")
	base := "/v1/m/sessions/protocol-binding-specs"
	malformed := protocolSpecInputForTest(workspaceA, BindingProtocolA2A, "malformed", 1, "")
	malformed.Mapping = nil
	invalid := h.doJSON(http.MethodPost, base+"?mode=plan", confined, malformed, tenantHdr(tenant))
	if invalid.code != http.StatusBadRequest || remote.validationCalls != 0 {
		t.Fatalf("invalid draft reached capability validator = %d %s calls=%d",
			invalid.code, invalid.raw, remote.validationCalls)
	}

	foreignCreate := h.doJSON(http.MethodPost, base+"?mode=plan", confined, input, tenantHdr(tenant))
	if foreignCreate.code != http.StatusNotFound {
		t.Fatalf("confined foreign draft = %d %s", foreignCreate.code, foreignCreate.raw)
	}
	missingWorkspace := h.do(http.MethodGet, base, admin, tenantHdr(tenant))
	if missingWorkspace.code != http.StatusBadRequest || workAPIErrorCode(missingWorkspace) != "invalid_command" {
		t.Fatalf("unconfined list without workspace = %d %s", missingWorkspace.code, missingWorkspace.raw)
	}
	foreignList := h.do(http.MethodGet,
		base+"?workspace_id="+workspaceB.String(), confined, tenantHdr(tenant))
	if foreignList.code != http.StatusNotFound {
		t.Fatalf("confined foreign list = %d %s", foreignList.code, foreignList.raw)
	}

	input = protocolSpecInputForTest(workspaceA, BindingProtocolA2A, "owned", 1, "")
	plan := h.doJSON(http.MethodPost, base+"?mode=plan", confined, input, tenantHdr(tenant))
	planHash, _ := plan.body["plan_hash"].(string)
	created := h.doJSON(http.MethodPost, base+"?mode=apply", confined, input,
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Plan-Hash": planHash,
		}))
	spec := created.body["spec"].(map[string]any)
	path := base + "/" + spec["id"].(string) + "/activate"
	missingVersion := h.doJSON(http.MethodPost, path+"?mode=apply", confined,
		map[string]any{}, workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(),
		}))
	if missingVersion.code != http.StatusPreconditionRequired || workAPIErrorCode(missingVersion) != "version_required" {
		t.Fatalf("activation without If-Match = %d %s", missingVersion.code, missingVersion.raw)
	}
	stale := h.doJSON(http.MethodPost, path+"?mode=apply", confined,
		map[string]any{}, workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Match": `"v9"`,
		}))
	if stale.code != http.StatusPreconditionFailed || workAPIErrorCode(stale) != "version_mismatch" {
		t.Fatalf("activation stale If-Match = %d %s", stale.code, stale.raw)
	}
}

func TestProtocolBindingSpecAPIIgnoresClientCapabilityAssertion(t *testing.T) {
	module := New(
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	h := newHarness(t, module)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-spec-server-witness")
	workspace := workAPICreateWorkspace(t, h, tenant, "protocol-spec-server-witness")
	input := protocolSpecInputForTest(workspace, BindingProtocolA2A, "client-clean", 1, "")
	base := "/v1/m/sessions/protocol-binding-specs"

	plan := h.doJSON(http.MethodPost, base+"?mode=plan", admin, input, tenantHdr(tenant))
	planHash, _ := plan.body["plan_hash"].(string)
	planValidation, _ := plan.body["validation"].(map[string]any)
	if plan.code != http.StatusOK ||
		planValidation["verdict"] != string(ProtocolObservationUnknown) ||
		planValidation["code"] != "capability_validator_unwired" {
		t.Fatalf("server-owned validation plan = %d %s", plan.code, plan.raw)
	}
	created := h.doJSON(http.MethodPost, base+"?mode=apply", admin, input,
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Plan-Hash": planHash,
		}))
	spec, _ := created.body["spec"].(map[string]any)
	validation, _ := spec["validation"].(map[string]any)
	if created.code != http.StatusCreated ||
		validation["verdict"] != string(ProtocolObservationUnknown) ||
		validation["code"] != "capability_validator_unwired" {
		t.Fatalf("server-owned validation = %d %s", created.code, created.raw)
	}

	activation := h.doJSON(http.MethodPost,
		base+"/"+spec["id"].(string)+"/activate?mode=plan",
		admin, map[string]any{}, tenantHdr(tenant),
	)
	if activation.code != http.StatusServiceUnavailable ||
		workAPIErrorCode(activation) != "observation_unavailable" {
		t.Fatalf("activation without server witness = %d %s", activation.code, activation.raw)
	}
}

func TestProtocolBindingSpecAPIRefreshesCapabilityWithoutChangingPlanHash(t *testing.T) {
	h, remote := newProtocolBindingAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-spec-refreshed-witness")
	workspace := workAPICreateWorkspace(t, h, tenant, "protocol-spec-refreshed-witness")
	input := protocolSpecInputForTest(workspace, BindingProtocolA2A, "refreshed-witness", 1, "")
	base := "/v1/m/sessions/protocol-binding-specs"

	plan := h.doJSON(http.MethodPost, base+"?mode=plan", admin, input, tenantHdr(tenant))
	planHash, _ := plan.body["plan_hash"].(string)
	planValidation, _ := plan.body["validation"].(map[string]any)
	created := h.doJSON(http.MethodPost, base+"?mode=apply", admin, input,
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Plan-Hash": planHash,
		}))
	if plan.code != http.StatusOK || planValidation["verdict"] != string(ProtocolObservationClean) ||
		created.code != http.StatusCreated || remote.validationCalls != 2 {
		t.Fatalf("refreshed draft plan/apply = plan:%d %s apply:%d %s calls=%d",
			plan.code, plan.raw, created.code, created.raw, remote.validationCalls)
	}
	spec := created.body["spec"].(map[string]any)
	specID := spec["id"].(string)
	activatePath := base + "/" + specID + "/activate"
	activationPlan := h.doJSON(http.MethodPost, activatePath+"?mode=plan", admin,
		map[string]any{}, tenantHdr(tenant))
	activationHash, _ := activationPlan.body["plan_hash"].(string)
	activated := h.doJSON(http.MethodPost, activatePath+"?mode=apply", admin,
		map[string]any{}, workAPIHeaders(tenant, map[string]string{
			"If-Match": `"v1"`, "If-Plan-Hash": activationHash,
			"Idempotency-Key": model.NewID().String(),
		}))
	if activationPlan.code != http.StatusOK || activated.code != http.StatusOK ||
		remote.validationCalls != 4 {
		t.Fatalf("refreshed activation plan/apply = plan:%d %s apply:%d %s calls=%d",
			activationPlan.code, activationPlan.raw, activated.code, activated.raw, remote.validationCalls)
	}
}

func TestProtocolBindingSpecValidatorsComposeIndependentRoutes(t *testing.T) {
	t.Parallel()

	module := New()
	remote := &protocolBindingAPIRemote{}
	module.UseProtocolBindingSpecValidator(BindingProtocolA2A, protocolBindingUnsupportedValidator{})
	module.AddProtocolBindingSpecValidator(BindingProtocolA2A, remote)
	validation := module.validateProtocolBindingSpecCapability(
		context.Background(), model.NewTenantID(),
		ProtocolBindingSpecInput{Protocol: BindingProtocolA2A},
	)
	if validation.Verdict != ProtocolObservationClean || remote.validationCalls != 1 {
		t.Fatalf("composed validators = %#v, calls=%d", validation, remote.validationCalls)
	}

	module.UseProtocolBindingSpecValidator(BindingProtocolMCP, protocolBindingUnsupportedValidator{})
	unsupported := module.validateProtocolBindingSpecCapability(
		context.Background(), model.NewTenantID(),
		ProtocolBindingSpecInput{Protocol: BindingProtocolMCP},
	)
	if unsupported.Verdict != ProtocolObservationUnknown ||
		unsupported.Code != "capability_route_unconfigured" {
		t.Fatalf("unsupported route = %#v", unsupported)
	}
}
