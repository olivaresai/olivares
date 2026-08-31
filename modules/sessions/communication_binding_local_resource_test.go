// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type protocolLocalResourceResolverFunc func(
	context.Context,
	model.TenantID,
	ProtocolLocalResourceRequest,
) (ProtocolLocalResourceProjection, error)

func (f protocolLocalResourceResolverFunc) ResolveProtocolLocalResource(
	ctx context.Context,
	tenant model.TenantID,
	request ProtocolLocalResourceRequest,
) (ProtocolLocalResourceProjection, error) {
	return f(ctx, tenant, request)
}

func protocolAgentRuntimeInputForTest(resourceID model.ID) ProtocolBindingSpecInput {
	input := protocolRuntimeInputForTest()
	input.LocalKind = BindingLocalAgent
	input.LocalSelector = json.RawMessage(`{"id":"` + resourceID.String() + `"}`)
	input.Mapping = []ProtocolMappingRule{{
		Source: "agent.name", Target: "message.text",
		Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformText,
	}}
	return input
}

func TestProtocolLocalResourcePreviewRequiresAuthoritativeProjection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tenant := model.NewTenantID()
	resourceID := model.NewID()
	input := protocolAgentRuntimeInputForTest(resourceID)

	unwired := (&Module{}).validateProtocolLocalResourcePreview(ctx, tenant, input)
	if unwired == nil || unwired.Verdict != ProtocolObservationUnknown ||
		unwired.Code != "local_resource_resolver_unwired" {
		t.Fatalf("unwired preview = %#v", unwired)
	}

	module := &Module{}
	module.UseProtocolLocalResourceResolver(protocolLocalResourceResolverFunc(func(
		_ context.Context,
		gotTenant model.TenantID,
		request ProtocolLocalResourceRequest,
	) (ProtocolLocalResourceProjection, error) {
		if gotTenant != tenant || request.WorkspaceID != input.WorkspaceID ||
			request.Kind != BindingLocalAgent || request.ID != resourceID {
			t.Fatalf("resolver request = tenant:%s request:%#v", gotTenant, request)
		}
		return ProtocolLocalResourceProjection{
			WorkspaceID: request.WorkspaceID, Kind: request.Kind, ID: request.ID, Version: 3,
			Fields: map[string]any{"agent.name": "Operations agent"},
		}, nil
	}))
	if validation := module.validateProtocolLocalResourcePreview(ctx, tenant, input); validation != nil {
		t.Fatalf("authoritative preview = %#v, want success", validation)
	}
}

func TestProtocolLocalResourcePreviewSupportsEveryClosedNonWorkKind(t *testing.T) {
	t.Parallel()

	for _, row := range []struct {
		kind   BindingLocalKind
		source string
	}{
		{kind: BindingLocalAgent, source: "agent.name"},
		{kind: BindingLocalModel, source: "model.name"},
		{kind: BindingLocalChannel, source: "channel.name"},
	} {
		t.Run(string(row.kind), func(t *testing.T) {
			resourceID := model.NewID()
			input := protocolRuntimeInputForTest()
			input.LocalKind = row.kind
			input.LocalSelector = json.RawMessage(`{"id":"` + resourceID.String() + `"}`)
			input.Mapping = []ProtocolMappingRule{{
				Source: row.source, Target: "message.text",
				Cardinality: ProtocolMappingOneToOne, Transform: ProtocolTransformText,
			}}
			module := &Module{}
			module.UseProtocolLocalResourceResolver(protocolLocalResourceResolverFunc(func(
				_ context.Context,
				_ model.TenantID,
				request ProtocolLocalResourceRequest,
			) (ProtocolLocalResourceProjection, error) {
				return ProtocolLocalResourceProjection{
					WorkspaceID: request.WorkspaceID, Kind: request.Kind, ID: request.ID, Version: 1,
					Fields: map[string]any{row.source: "resolved resource"},
				}, nil
			}))
			if validation := module.validateProtocolLocalResourcePreview(
				context.Background(), model.NewTenantID(), input,
			); validation != nil {
				t.Fatalf("%s preview = %#v", row.kind, validation)
			}
		})
	}
}

func TestProtocolLocalResourcePreviewRejectsUnusableEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tenant := model.NewTenantID()
	resourceID := model.NewID()
	input := protocolAgentRuntimeInputForTest(resourceID)

	rows := []struct {
		name       string
		projection ProtocolLocalResourceProjection
		wantCode   string
	}{
		{
			name: "mismatched identity",
			projection: ProtocolLocalResourceProjection{
				WorkspaceID: input.WorkspaceID, Kind: BindingLocalAgent, ID: model.NewID(), Version: 1,
				Fields: map[string]any{"agent.name": "Agent"},
			},
			wantCode: "local_resource_evidence_invalid",
		},
		{
			name: "mapping source absent",
			projection: ProtocolLocalResourceProjection{
				WorkspaceID: input.WorkspaceID, Kind: BindingLocalAgent, ID: resourceID, Version: 1,
				Fields: map[string]any{"agent.id": resourceID.String()},
			},
			wantCode: "local_mapping_preview_failed",
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			module := &Module{}
			module.UseProtocolLocalResourceResolver(protocolLocalResourceResolverFunc(func(
				context.Context,
				model.TenantID,
				ProtocolLocalResourceRequest,
			) (ProtocolLocalResourceProjection, error) {
				return row.projection, nil
			}))
			validation := module.validateProtocolLocalResourcePreview(ctx, tenant, input)
			if validation == nil || validation.Verdict == ProtocolObservationClean ||
				validation.Code != row.wantCode {
				t.Fatalf("preview = %#v, want %q", validation, row.wantCode)
			}
		})
	}

	unavailable := &Module{}
	unavailable.UseProtocolLocalResourceResolver(protocolLocalResourceResolverFunc(func(
		context.Context,
		model.TenantID,
		ProtocolLocalResourceRequest,
	) (ProtocolLocalResourceProjection, error) {
		return ProtocolLocalResourceProjection{}, errors.New("not available")
	}))
	validation := unavailable.validateProtocolLocalResourcePreview(ctx, tenant, input)
	if validation == nil || validation.Code != "local_resource_unavailable" {
		t.Fatalf("unavailable preview = %#v", validation)
	}
}

func TestProtocolLocalResourceSelectorIsClosed(t *testing.T) {
	t.Parallel()

	resourceID := model.NewID()
	for _, selector := range []string{
		`{"id":"` + resourceID.String() + `","query":"all"}`,
		`{"id":"not-a-canonical-id"}`,
		`{"name":"agent"}`,
	} {
		input := protocolAgentRuntimeInputForTest(resourceID)
		input.LocalSelector = json.RawMessage(selector)
		if _, err := normalizeProtocolSpecInput(input); !errors.Is(err, ErrInvalidProtocolBinding) {
			t.Fatalf("selector %s normalize = %v, want invalid binding", selector, err)
		}
	}
}

func TestProtocolBindingSpecAPINonWorkResourceIsResolvedForEveryDecision(t *testing.T) {
	module := New(
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	remote := &protocolBindingAPIRemote{module: module}
	module.UseProtocolBindingSpecValidator(BindingProtocolA2A, remote)
	resourceID := model.NewID()
	resolverCalls := 0
	module.UseProtocolLocalResourceResolver(protocolLocalResourceResolverFunc(func(
		_ context.Context,
		_ model.TenantID,
		request ProtocolLocalResourceRequest,
	) (ProtocolLocalResourceProjection, error) {
		resolverCalls++
		return ProtocolLocalResourceProjection{
			WorkspaceID: request.WorkspaceID, Kind: request.Kind, ID: request.ID, Version: 1,
			Fields: map[string]any{"agent.name": "Resolved agent"},
		}, nil
	}))
	h := newHarness(t, module)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-agent-preview")
	workspace := workAPICreateWorkspace(t, h, tenant, "protocol-binding-agent-preview")
	input := protocolAgentRuntimeInputForTest(resourceID)
	input.WorkspaceID = workspace
	base := "/v1/m/sessions/protocol-binding-specs"

	plan := h.doJSON(http.MethodPost, base+"?mode=plan", admin, input, tenantHdr(tenant))
	planHash, _ := plan.body["plan_hash"].(string)
	created := h.doJSON(http.MethodPost, base+"?mode=apply", admin, input,
		workAPIHeaders(tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(), "If-Plan-Hash": planHash,
		}))
	spec, _ := created.body["spec"].(map[string]any)
	specID, _ := spec["id"].(string)
	activation := base + "/" + specID + "/activate"
	activationPlan := h.doJSON(
		http.MethodPost, activation+"?mode=plan", admin, map[string]any{}, tenantHdr(tenant),
	)
	activationHash, _ := activationPlan.body["plan_hash"].(string)
	activated := h.doJSON(http.MethodPost, activation+"?mode=apply", admin, map[string]any{},
		workAPIHeaders(tenant, map[string]string{
			"If-Match": `"v1"`, "If-Plan-Hash": activationHash,
			"Idempotency-Key": model.NewID().String(),
		}))
	activeSpec, _ := activated.body["spec"].(map[string]any)
	if plan.code != http.StatusOK || created.code != http.StatusCreated ||
		activationPlan.code != http.StatusOK || activated.code != http.StatusOK ||
		activeSpec["state"] != string(ProtocolBindingSpecActive) {
		t.Fatalf("non-work plan/create/activate = plan:%d %s create:%d %s activation-plan:%d %s activate:%d %s",
			plan.code, plan.raw, created.code, created.raw,
			activationPlan.code, activationPlan.raw, activated.code, activated.raw)
	}
	if resolverCalls != 4 || remote.validationCalls != 4 {
		t.Fatalf("decision consumers = resolver:%d remote:%d, want 4/4", resolverCalls, remote.validationCalls)
	}
}
