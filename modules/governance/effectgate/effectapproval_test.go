// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package effectgate

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// lookupBridge records which lookup Gate used. Both lookups answer an approved grant.
type lookupBridge struct{ opened []string }

func (b *lookupBridge) GateOnceForSession(context.Context, model.TenantID, string, string, string, string, string, string, string) (string, string, string, error) {
	b.opened = append(b.opened, "reusable")
	return "appr-1", governance.GateStatusApproved, "plan", nil
}

func (b *lookupBridge) GateUnspentForSession(context.Context, model.TenantID, string, string, string, string, string, string, string) (string, string, string, error) {
	b.opened = append(b.opened, "unspent")
	return "appr-2", governance.GateStatusApproved, "plan", nil
}

func (b *lookupBridge) StatusScoped(context.Context, model.TenantID, string, string, string, string, string) (string, string, error) {
	return governance.GateStatusApproved, "plan", nil
}

func (b *lookupBridge) ConsumeApproval(context.Context, model.TenantID, string, string, string) (bool, bool, error) {
	return true, false, nil
}

// A single-use spend under a fresh consumer can never match an earlier spend, so it asks for
// an unspent grant. A spend under the caller's own consumer id (a tool-call retrying inside
// the engine's retry window), an approval reused in its time box and a named approval keep
// the lookup they always had.
func TestGateAsksForAnUnspentGrantOnlyUnderAFreshConsumer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		effect   GatedEffect
		wantOpen string
	}{
		{"fresh consumer spends once", GatedEffect{}, "unspent"},
		{"its own consumer id retries", GatedEffect{Consumer: "toolu_1"}, "reusable"},
		{"an approval reused inside its time box", GatedEffect{Spend: ReuseInWindow}, "reusable"},
		{"a named approval is read, not looked up", GatedEffect{Ref: "appr-9"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &lookupBridge{}
			answer, err := Gate(context.Background(), b, tc.effect)
			if err != nil || answer.Outcome != Allowed {
				t.Fatalf("answer=%+v err=%v, want allowed", answer, err)
			}
			if len(b.opened) > 1 {
				t.Fatalf("opened %v, want at most one lookup", b.opened)
			}
			got := ""
			if len(b.opened) == 1 {
				got = b.opened[0]
			}
			if got != tc.wantOpen {
				t.Fatalf("lookup = %q, want %q", got, tc.wantOpen)
			}
		})
	}
}
