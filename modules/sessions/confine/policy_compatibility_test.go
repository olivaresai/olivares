// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

var _ func(context.Context, confine.Policy, string, ...string) (*exec.Cmd, confine.State, error) = confine.Command

// The 26.10.1 Policy contract permits positional literals and its default JSON
// has exactly the four path keys. Borrowed launch handles must remain separate.
func TestSessionPolicyKeepsItsPublishedLiteralAndDefaultJSON(t *testing.T) {
	p := confine.Policy{[]string{"/rw"}, []string{"/ro"}, []string{"/protect"}, []string{"/sealed"}}
	if !reflect.DeepEqual(p.Sealed, []string{"/sealed"}) {
		t.Fatal("published positional literal changed")
	}
	data, err := json.Marshal(confine.Policy{})
	if err != nil || string(data) != `{"ReadWrite":null,"ReadOnly":null,"Protect":null,"Sealed":null}` {
		t.Fatalf("published default Policy JSON changed: %s: %v", data, err)
	}
}
