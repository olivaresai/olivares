// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// The unit-G wireproof: the PRODUCTION composition, not a module fixture.
//
// It exists because this campaign has already shipped a control that was present in
// the code and absent from the binary's behavior — a gate nobody had wired,
// indistinguishable from a working one until somebody audited it. The module-level
// tests prove the rule; only this proves the rule is IN FORCE in the thing we ship.
//
// It also pins the deliberate asymmetry: the module tolerates a nil rollout seam
// (behaving as it did before this unit, which is the upgrade-safe reading for a
// custom embedder), and the first-party binary does NOT — a store without the
// capability is a boot failure there. If this test ever passes trivially, it is
// because the seam stopped being wired and the module's tolerant default took over,
// which is exactly the failure it is here to catch.
func TestBootEnforcesEgressDestinationsOnAFreshInstall(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test"})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	// 1. The engine classified the control, and a FRESH database is enforced.
	rs, ok := eng.store.(store.RolloutStater)
	if !ok {
		t.Fatal("the production store does not expose durable rollout state, so boot's own guard would have refused")
	}
	st, err := rs.RolloutState(ctx, eventing.EgressRolloutControlKey)
	if err != nil {
		t.Fatalf("read the egress control's durable state: %v", err)
	}
	if st.CurrentMode != store.RolloutEnforced || st.ClassifiedMode != store.RolloutEnforced {
		t.Fatalf("a fresh install classified %q/%q, want %q: an install with nothing to grandfather must not start ungoverned",
			st.ClassifiedMode, st.CurrentMode, store.RolloutEnforced)
	}
	if st.EnforcementCommitted {
		t.Fatal("a classification is not a decision; the commitment flag must start clear")
	}

	// 2. And the module ACTS on it. No operator policy is configured in this fixture, so
	// on an enforced deployment there is nothing that could permit a destination — and
	// the authoring path must say so rather than write a subscription that never
	// delivers.
	h := eng.api.Handler()
	do := func(method, path, token string, body any, tenant string) (int, string) {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.RemoteAddr = "10.0.0.1:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if tenant != "" {
			req.Header.Set("X-Olivares-Tenant", tenant)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	tok, _, terr := eng.setupTok.Ensure()
	if terr != nil {
		t.Fatalf("setup token: %v", terr)
	}
	code, body := do("POST", "/v1/setup", "", map[string]any{
		"token": tok, "email": "root@olivares.test", "password": "Sup3r-secret-passphrase!",
	}, "")
	if code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}
	code, body = do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@olivares.test", "password": "Sup3r-secret-passphrase!",
	}, "")
	if code != http.StatusOK {
		t.Fatalf("login: %d %s", code, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &login); err != nil || login.Token == "" {
		t.Fatalf("login token: %v %s", err, body)
	}

	code, body = do("POST", "/v1/system/orgs", login.Token, map[string]any{"slug": "acme", "name": "Acme"}, "")
	if code != http.StatusCreated {
		t.Fatalf("create org: %d %s", code, body)
	}
	var org struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &org); err != nil || org.ID == "" {
		t.Fatalf("org id: %v %s", err, body)
	}

	code, body = do("POST", "/v1/m/eventing/subscriptions", login.Token, map[string]any{
		"name": "siem", "endpoint": "https://collector.example.com/hooks",
		"event_types": []string{"finding.reported"}, "role": "viewer",
	}, org.ID)
	if code != http.StatusBadRequest {
		t.Fatalf("authoring a destination on an enforced install with no policy returned %d (%s); want 400 — the control is not wired to the binary",
			code, body)
	}
	if !strings.Contains(strings.ToLower(body), "platform operator") {
		t.Fatalf("the refusal does not name the remediation owner, so the caller cannot act on it: %s", body)
	}

	// 3. The status surface reports the disposition, so an operator can see WHY.
	code, body = do("GET", "/v1/m/eventing/egress-policy", login.Token, nil, org.ID)
	if code != http.StatusOK {
		t.Fatalf("egress-policy status: %d %s", code, body)
	}
	if !strings.Contains(body, `"mode":"enforced"`) {
		t.Fatalf("the status surface does not report the disposition in force: %s", body)
	}
}
