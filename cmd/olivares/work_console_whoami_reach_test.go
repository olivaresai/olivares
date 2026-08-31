// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// THE WORK CONSOLE'S AUTHORITY, MEASURED ON THE WIRE.
//
// Since #578 the console's can() is pure membership of the set /v1/auth/whoami serves.
// There is no verb arithmetic and no "admin implies write" client-side, so a permission
// that does not REACH that set is an entire screen the console hides, in silence, with
// no 403 anywhere to notice it by.
//
// The work cockpit asks for six permissions and only six. This test boots the REAL
// composition root and reads them off the wire, because every cheaper method answers a
// different question:
//
//   - auth.RoleGrants says what a role WOULD grant. It knows nothing about whether the
//     sessions module is mounted, so it cannot see the failure mode that matters.
//   - the permission catalog (core/api.buildPermCatalog) is built from the modules the
//     server ACTUALLY MOUNTS. A module compiled but not composed declares nothing, and
//     tools/permsdump builds its own module set rather than the server's.
//   - modules/sessions.Module.Permissions() is a declaration; whoami serves the
//     intersection of that declaration with RoleGrants over the mounted catalog.
//
// Only the wire crosses all three.
//
// THE NON-FIRING DIRECTION IS ASSERTED, not assumed: a whoami that answered "every
// string you ask about" would pass the six assertions above and be worthless. So the
// same served set is checked to NOT contain the system permission, which no tenant role
// may ever hold (auth.RoleGrants returns false for it before any other branch). If that
// second half ever passes vacuously — because the set went empty — the length assertion
// below catches it first.
//
// WHAT THIS CELL IS AND IS NOT LOAD-BEARING FOR. Both halves were measured by mutation
// on 2026-08-10, and the first result contradicted what this comment originally claimed,
// so it is written down rather than smoothed over.
//
// REDUNDANT — the DECLARATION path. Deleting the six from
// modules/sessions.Module.Permissions() (api.go:44-45) does NOT reach this assertion:
// boot fails first, at core/api/server.go:1011, with "module sessions mounts routes
// requiring undeclared permissions". A second invariant already blocks that path, so
// killing this mutant would measure nothing about this test. Asserting the redundancy is
// the honest third answer, and it is better news than a witness: an undeclared
// permission cannot even start the binary.
//
// LOAD-BEARING — the SERVING path, which the boot guard cannot see because it compares
// routes against declarations and never asks what whoami hands out. Isolating mutant:
// drop VerbAdmin from the admin/owner arm of auth.roleGrantsVerb
// (core/auth/permission.go:276-277). It compiles, boot stays green, whoami still serves
// 205 permissions — and exactly sessions:work:admin and sessions:decision:admin vanish
// from an owner's set. This cell fails; the declaration guard is silent, because nothing
// was undeclared. That is the defect class this test exists for: the console loses a
// screen while every declaration still lines up.
//
// NON-FIRING DIRECTION. A module declaring an ADDITIONAL permission
// ("sessions:workneighbour:read", measured the same day) leaves this cell green. The
// assertion is deliberately containment, not equality: the six must be reachable, and a
// module growing its surface is a legitimate neighboring operation, not a regression.
var workConsolePermissions = []string{
	"sessions:decision:admin",
	"sessions:decision:read",
	"sessions:decision:write",
	"sessions:work:admin",
	"sessions:work:read",
	"sessions:work:write",
}

func TestWorkConsolePermissionsReachWhoami(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), DemoSeed: true,
	})
	if err != nil {
		t.Fatalf("boot demo estate: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	// announceDemo is what provisions the demo account; without it login answers
	// setup_required and nothing below measures anything.
	if err := announceDemo(ctx, io.Discard, eng); err != nil {
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
	if len(grants) == 0 {
		t.Fatal("whoami returned no grants; every permission assertion below would be vacuous")
	}
	g, _ := grants[0].(map[string]any)
	role, _ := g["role"].(string)
	rawPerms, _ := g["permissions"].([]any)
	if len(rawPerms) == 0 {
		t.Fatalf("whoami served an EMPTY permission set for role %q: the console would hide "+
			"every screen, and the six assertions below would each fail for the same single "+
			"reason rather than measuring anything", role)
	}
	served := map[string]bool{}
	for _, p := range rawPerms {
		if s, ok := p.(string); ok {
			served[s] = true
		}
	}

	// The demo account is owner (TestDemoBootGrantsTheSuperadminItsEstate pins that), the
	// highest tenant role. If the six do not reach an owner they reach nobody.
	if role != auth.RoleOwner {
		t.Fatalf("demo grant role = %q, want %q: this test asserts the CEILING of tenant "+
			"authority, so a lower role would make a missing permission ambiguous between "+
			"'the console cannot see it' and 'this role legitimately lacks it'", role, auth.RoleOwner)
	}

	var missing []string
	for _, p := range workConsolePermissions {
		if !served[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("whoami does NOT serve %d of the work cockpit's permissions to an %s: %v\n"+
			"served %d permissions in total.\n"+
			"The console hides a screen per missing permission and emits no 403, so this is "+
			"invisible from the UI. Fix it in the ENGINE (the module's Permissions() "+
			"declaration or its mounting), never with a client-side exception in can().",
			len(missing), role, missing, len(served))
	}

	// NON-FIRING DIRECTION. A set that contained everything would satisfy the loop above
	// while telling the console nothing. The system permission is the sharpest available
	// negative: RoleGrants refuses it for every tenant role, superadmin flag or not.
	if served[string(auth.PermSystemAdmin)] {
		t.Errorf("whoami served %q inside a tenant grant's permission set. No tenant role "+
			"holds it (auth.RoleGrants denies it before any other branch), so the set is "+
			"reporting authority the engine will refuse — and the assertions above stop "+
			"discriminating anything.", auth.PermSystemAdmin)
	}
}
