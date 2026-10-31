// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

type storageActPolicy struct {
	deny bool
	seen auth.Request
}

func (p *storageActPolicy) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	if req.Resource.Kind == "appliance.task" {
		p.seen = req
	}
	return auth.Decision{Allow: req.Resource.Kind != "appliance.task" || !p.deny}, nil
}
func TestStorageAuthorityUsesCurrentPolicyForExactActTargetAndIntent(t *testing.T) {
	st := openDRStore(t, t.TempDir())
	policy := &storageActPolicy{}
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, func(o *api.Options) { o.Authorizer = auth.NewAuthorizer(policy) })
	admin := h.adminLogin()
	target := "drive:" + strings.Repeat("a", 64)
	intent := strings.Repeat("b", 64)
	for _, deny := range []bool{false, true} {
		policy.deny = deny
		got := h.do("POST", "/v1/console/appliance/storage/authorize", admin, map[string]any{"task": "storage.backup", "target": target, "intent_digest": intent}, nil)
		if got.code != 200 || got.body["allowed"] != !deny {
			t.Fatalf("deny=%v: %d %s", deny, got.code, got.raw)
		}
		req := policy.seen
		if req.Permission != auth.PermSystemAdmin || req.Resource.ID != "storage.backup" || req.Resource.Extra["target"] != target || req.Resource.Extra["intent_digest"] != intent || req.Resource.Extra["act"] != "storage" || req.Resource.Extra["audience"] != "appliance-helper:olivares-portal-storage" {
			t.Fatalf("policy saw different intent: %+v", req)
		}
	}
}
