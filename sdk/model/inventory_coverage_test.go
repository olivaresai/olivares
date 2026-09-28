// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestInventoryCoverageScopeContract(t *testing.T) {
	scope := model.InventoryScope{Contract: model.AzureInventoryContract, Family: "azure.resource", Selectors: []string{"sub-1", "sub-2"}}
	if !scope.Valid() || len(scope.Fingerprint()) != 64 {
		t.Fatal("canonical explicit scope rejected")
	}
	copy := scope.Clone()
	scope.Selectors[0] = "changed"
	if copy.Selectors[0] != "sub-1" {
		t.Fatal("scope did not own selectors")
	}
	for _, selectors := range [][]string{nil, {"sub-2", "sub-1"}, {"sub-1", "sub-1"}, {"Sub-1"}, {"sub/1"}, {strings.Repeat("a", 129)}} {
		s := copy
		s.Selectors = selectors
		if s.Valid() {
			t.Fatalf("ambiguous/unbounded selectors accepted: %#v", selectors)
		}
	}
	edge := model.EdgeObservation{OriginKind: "azure.subscription", OriginRef: "sub-1", ResourceKind: "azure.resource", ResourceRef: "/subscriptions/sub-1/providers/test/things/a", Source: "azure"}
	if !copy.Contains(edge) {
		t.Fatal("declared resource refused")
	}
	edge.Source = "azure_activity"
	if copy.Contains(edge) {
		t.Fatal("activity counted as inventory")
	}
	edge.Source = "azure"
	edge.ResourceRef = "/subscriptions/sub-3/providers/test/things/a"
	if copy.Contains(edge) {
		t.Fatal("foreign subscription counted")
	}
	if model.ValidInventoryResult("complete", "raw provider error") || model.ValidInventoryFingerprint(strings.Repeat("Z", 64)) {
		t.Fatal("unbounded diagnostic/fingerprint accepted")
	}
}
