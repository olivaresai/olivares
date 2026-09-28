// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestTheStepKindTableIsTheConfigDecoder: every kind the table accepts decodes
// into the type its entry names and reaches that type's validator, and a kind
// the table lacks is refused, so the declaration the census reads and the
// decoder the writer runs cannot drift apart.
func TestTheStepKindTableIsTheConfigDecoder(t *testing.T) {
	seen := map[reflect.Type]string{}
	for _, kind := range stepConfigs.Kinds() {
		entry := stepConfigs[kind]
		if entry == nil || entry.Type == nil {
			t.Errorf("step kind %s names no config type", kind)
			continue
		}
		if other, dup := seen[entry.Type]; dup {
			t.Errorf("step kinds %s and %s share the config type %s, so the decoder cannot tell them apart", other, kind, entry.Type)
		}
		seen[entry.Type] = kind
		if _, ge := canonicalStepConfig(kind, json.RawMessage(`{}`)); ge != nil && ge.Message == "unknown step kind "+kind {
			t.Errorf("step kind %s is in the table but has no validator for %s", kind, entry.Type)
		}
	}
	if _, ge := canonicalStepConfig("work-escalate", json.RawMessage(`{}`)); ge == nil || ge.Message != "unknown step kind work-escalate" {
		t.Errorf("a kind the table lacks = %+v, want the unknown-kind refusal", ge)
	}
}
