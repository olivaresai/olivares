// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type nondisclosureUnavailableProjector struct{ panicOnAvailability bool }

func (nondisclosureUnavailableProjector) SupportsModuleCapability(ModuleCapabilityOperation) bool {
	return true
}
func (p nondisclosureUnavailableProjector) ModuleCapabilityAvailable(context.Context, ModuleCapabilityOperation, ModuleCapabilityScope) ModuleCapabilityAvailability {
	if p.panicOnAvailability {
		panic("private availability failure")
	}
	// A producer returning an input-gate code still reports AUTHORITY availability.
	return ModuleCapabilityAvailability{Code: capabilityCodeInputsRequired}
}
func (nondisclosureUnavailableProjector) ProjectModuleCapability(context.Context, ModuleCapabilityQuestion) ModuleCapabilityResult {
	panic("must not resolve target")
}

func TestCapabilityNonDisclosureInternalProvenance(t *testing.T) {
	question := CapabilityQuestion{ID: "q", Kind: capabilityKindOperation, Operation: "GET /v1/m/test/{id}",
		WorkspaceID: model.NewID().String(), Selectors: &CapabilitySelectors{Path: map[string]string{"id": model.NewID().String()}}}
	for _, panicOnAvailability := range []bool{false, true} {
		s := &Server{routeDescriptors: map[string]routeDescriptor{question.Operation: {
			method: "GET", pattern: "/v1/m/test/{id}", perm: "test:channel:admin",
			entity:    &EntityRef{Kind: "sessions.channel", IDParam: "id", WorkspaceColumn: "workspace_id", ConcealDeniedAsNotFound: true},
			projector: nondisclosureUnavailableProjector{panicOnAvailability: panicOnAvailability},
		}}}
		evaluation := s.evaluateCapabilityQuestion(httptest.NewRequest("POST", "/v1/auth/capabilities", nil), auth.Principal{}, model.NewTenantID(), question)
		if evaluation.projection.state != CapabilityUnknown || evaluation.projection.inputRejected || evaluation.recoveredPanic != panicOnAvailability {
			t.Fatalf("authority failure lost its internal provenance: %+v", evaluation)
		}
		before := evaluation
		marker := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
		result := evaluation.publicResult(question, marker, marker.Add(time.Second))
		if result.State != CapabilityUndisclosed || result.Code != capabilityCodeNotDisclosed || result.ObservedAt != marker || result.RefreshAfterMS != nil {
			t.Fatalf("authority failure escaped non-disclosure: %+v", result)
		}
		if evaluation != before {
			t.Fatal("public mapping mutated the internal outcome")
		}
	}
	denial := capabilityEvaluation{conceal: true, projection: capabilityProjection{state: CapabilityDenied, code: capabilityCodeNotAvailable}}
	_ = denial.publicResult(question, time.Now(), time.Now())
	if denial.projection.state != CapabilityDenied || denial.recoveredPanic {
		t.Fatal("established denial was rewritten internally")
	}
}

func TestCapabilityNonDisclosureOpenAPI(t *testing.T) {
	schemas := buildOpenAPI()["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"CapabilityQuestions", "CapabilityResults"} {
		properties := schemas[name].(map[string]any)["properties"].(map[string]any)
		if got := properties["schema_version"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []any{2}) {
			t.Fatalf("%s schema versions = %v", name, got)
		}
	}
	properties := schemas["CapabilityResult"].(map[string]any)["properties"].(map[string]any)
	if got := properties["state"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []any{"allowed", "denied", "unknown", "undisclosed", "reachable", "not_reachable"}) {
		t.Fatalf("wire states = %v", got)
	}
	codes := properties["code"].(map[string]any)["enum"].([]any)
	found := false
	for _, code := range codes {
		if code == "not_available" {
			t.Fatal("retired v1 concealment code is still public")
		}
		found = found || code == "not_disclosed"
	}
	if !found {
		t.Fatal("non-disclosure code is absent")
	}
}
