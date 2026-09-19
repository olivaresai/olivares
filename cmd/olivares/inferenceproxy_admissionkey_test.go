// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"
)

// TestProxyAdmissionKeyIsPerCallNotPerPayload is the caller half of the replay
// definition. The proxy is handed no idempotency key: the client sends a request,
// not a claim about which earlier request it repeats. A key derived from the bytes
// therefore says "this is a retry" about two calls that are merely identical — a
// prompt sent twice, a scheduled job that sends the same thing each run — and the
// second one was admitted on the first one's verdict instead of on the ledger.
//
// The module now bounds that to a retry window, which is the general fix; this is
// the caller-side one, and it is not redundant with it. Inside the window the two
// are indistinguishable to the module, and only the caller knows that it cannot
// tell a retry from a repeat.
func TestProxyAdmissionKeyIsPerCallNotPerPayload(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	req := userReq("the same prompt, sent twice", false)

	for i := 0; i < 2; i++ {
		if dec := d.Authorize(context.Background(), req, "bearer"); !dec.Allow {
			t.Fatalf("call %d was denied: status=%d reason=%q", i, dec.Status, dec.Reason)
		}
	}

	if len(bg.keys) != 2 {
		t.Fatalf("the budget gate was asked %d time(s) for two calls: %v", len(bg.keys), bg.keys)
	}
	if bg.keys[0] == bg.keys[1] {
		t.Fatalf("two separate calls carrying the same bytes claimed one idempotency key %q: "+
			"the second one is admitted on the first one's verdict", bg.keys[0])
	}
	for i, k := range bg.keys {
		if !strings.HasPrefix(k, "model_gateway/") {
			t.Errorf("key %d = %q, want the model_gateway scope prefix", i, k)
		}
	}
}
