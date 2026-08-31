// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// TestDemoBootGrantsTheSuperadminItsEstate boots the demo estate through the real
// composition root and runs the REAL announceDemo path, then checks the thing its
// own banner promises: that the printed account can switch to the demo
// organization. The console builds that switcher from /v1/auth/whoami grants, so
// an account with no grant makes the instruction unfollowable and every
// tenant-scoped route answer "tenant required".
func TestDemoBootGrantsTheSuperadminItsEstate(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), DemoSeed: true,
	})
	if err != nil {
		t.Fatalf("boot demo estate: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if eng.demoTenant.IsZero() {
		t.Fatal("boot did not return the demo tenant")
	}

	var banner bytes.Buffer
	if err := announceDemo(ctx, &banner, eng); err != nil {
		t.Fatalf("announceDemo: %v", err)
	}

	handler := eng.api.Handler()
	code, login, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": demoEmail, "password": demoPassword,
	})
	if code != http.StatusOK {
		t.Fatalf("demo login = %d: %s", code, raw)
	}
	token, _ := login["token"].(string)

	code, who, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/auth/whoami", token, "", nil)
	if code != http.StatusOK {
		t.Fatalf("whoami = %d: %s", code, raw)
	}
	grants, _ := who["grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("demo superadmin grants = %v, want one grant on the demo tenant %s", who["grants"], eng.demoTenant)
	}
	g, _ := grants[0].(map[string]any)
	if g["tenant"] != eng.demoTenant.String() || g["role"] != auth.RoleOwner {
		t.Fatalf("demo grant = %v, want owner on %s", g, eng.demoTenant)
	}

	// And the estate is readable with that tenant, which is what the banner sends
	// the operator off to do.
	code, _, raw = doDemoViewJSON(t, handler, http.MethodGet, "/v1/agents", token, eng.demoTenant.String(), nil)
	if code != http.StatusOK {
		t.Fatalf("GET /v1/agents on the demo tenant = %d: %s", code, raw)
	}
}
