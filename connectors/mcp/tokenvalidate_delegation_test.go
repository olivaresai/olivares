// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"
)

func TestDelegationFromClaims(t *testing.T) {
	t.Run("delegated pair", func(t *testing.T) {
		actAs, delegated, err := delegationFromClaims(map[string]json.RawMessage{
			"is_delegated": json.RawMessage(`true`),
			"act_as":       json.RawMessage(`"user:on-behalf-of"`),
		})
		if err != nil || !delegated || actAs != "user:on-behalf-of" {
			t.Fatalf("delegationFromClaims = (%q, %t, %v)", actAs, delegated, err)
		}
	})

	t.Run("direct principal", func(t *testing.T) {
		actAs, delegated, err := delegationFromClaims(nil)
		if err != nil || delegated || actAs != "" {
			t.Fatalf("delegationFromClaims = (%q, %t, %v)", actAs, delegated, err)
		}
	})

	for _, tc := range []struct {
		name string
		raw  map[string]json.RawMessage
	}{
		{name: "delegated without effective subject", raw: map[string]json.RawMessage{
			"is_delegated": json.RawMessage(`true`),
		}},
		{name: "effective subject marked direct", raw: map[string]json.RawMessage{
			"is_delegated": json.RawMessage(`false`),
			"act_as":       json.RawMessage(`"user:on-behalf-of"`),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := delegationFromClaims(tc.raw); err == nil {
				t.Fatal("inconsistent delegation claims must be rejected")
			}
		})
	}
}
