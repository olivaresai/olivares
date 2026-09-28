// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// spendDimsWireNames are the names SpendDims travels under in the reserve document, the
// ones the published schema of POST /admission/reserve lists for `dims`.
var spendDimsWireNames = []string{
	"agent_group_refs", "agent_ref", "api_key_ref", "context_window", "cost_center_ref", "cost_type",
	"gateway", "identity_ref", "inference_geo", "model_ref", "project", "provider_ref", "routine_ref",
	"service_tier", "session_ref", "team", "user_group_refs", "workspace_ref",
}

// TestSpendDimsSnakeCaseOnWire: SpendDims crosses the wire inside the reserve document
// under the snake_case names every other FinOps body uses. An attribution with every
// member set encodes exactly those names, one per field, and decodes back into the same
// attribution under a decoder that refuses unknown names; an empty one encodes no name;
// and a multi-word Go field name is not a wire name. encoding/json matches names without
// regard to case, so a one-word field such as Team decodes under either spelling.
func TestSpendDimsSnakeCaseOnWire(t *testing.T) {
	full := SpendDims{
		ProviderRef: "anthropic", ModelRef: "m-1", AgentRef: "agent-1", SessionRef: "s-1", Team: "t-1",
		Project: "p-1", WorkspaceRef: "w-1", APIKeyRef: "key-1", ServiceTier: "standard",
		ContextWindow: "200k", InferenceGeo: "eu", Gateway: "g-1", CostType: "tokens",
		IdentityRef: "spiffe://t/agent-1", RoutineRef: "r-1", CostCenterRef: "cc-1",
		UserGroupRefs: []string{"ug-1"}, AgentGroupRefs: []string{"ag-1"},
	}
	if got, want := reflect.TypeOf(full).NumField(), len(spendDimsWireNames); got != want {
		t.Fatalf("SpendDims has %d fields and %d wire names: a field was added without its name", got, want)
	}

	raw, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatalf("decode as an object: %v", err)
	}
	names := make([]string, 0, len(encoded))
	for name := range encoded {
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, spendDimsWireNames) {
		t.Fatalf("SpendDims encodes %v, want %v", names, spendDimsWireNames)
	}

	var back SpendDims
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&back); err != nil || !reflect.DeepEqual(back, full) {
		t.Fatalf("the wire names decode to %+v (err=%v), want %+v", back, err, full)
	}

	if empty, err := json.Marshal(SpendDims{}); err != nil || string(empty) != "{}" {
		t.Fatalf("an empty attribution encodes %s (err=%v), want {}", empty, err)
	}

	dec = json.NewDecoder(bytes.NewReader([]byte(`{"ProviderRef":"anthropic"}`)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&SpendDims{}); err == nil {
		t.Fatal("a multi-word Go field name decoded as a wire name")
	}
}
