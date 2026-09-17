// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"os"
	osexec "os/exec"
	"strings"
	"testing"
)

// R63 C3 — the two proofs the correction owes, and neither can be argued from
// reading the cleanup code.
//
// The first is that the NEXT ordinary provisioning in the SAME PostgreSQL instance
// succeeds. That is the failure that was actually measured: a promoted cluster-global
// application role is invisible until something else tries to provision, and then it
// is 298 red tests in one package.
//
// The second is that the restoration runs when the test that made the mutation DIES.
// A cleanup that only runs on the happy path is the cleanup this defect already had:
// any t.Fatal between the ALTER and a deferred restore leaves the role promoted.
// Go cannot assert "this subtest fails and that is fine" in-process — a failing
// subtest fails its parent — so the dying test is a REAL child process of this test
// binary. It fails for real, its cleanups run for real, and the parent then measures
// the server the child left behind.
const drSharedRoleFatalChildEnv = "OLIVARES_TEST_DR_SHARED_ROLE_FATAL_CHILD"

func TestDRSharedRoleCleanupSurvivesAFatalTestAndLeavesProvisioningUsable(t *testing.T) {
	if os.Getenv(drSharedRoleFatalChildEnv) == "1" {
		drSharedRoleFatalChild(t)
		return
	}

	// The premise, and the gate: if this instance cannot provision now, nothing this
	// test concludes afterwards would mean anything.
	before := isolatedPGSplit(t)
	role := before.Result.AppPosture.Role
	superDB := drOpenSuper(t, before.Superuser)
	if drReadRoleAttributes(t, superDB, role).superuser {
		t.Fatalf("this instance ALREADY carries application-role drift on %q before the test ran", role)
	}

	child := osexec.Command(os.Args[0],
		"-test.run=^TestDRSharedRoleCleanupSurvivesAFatalTestAndLeavesProvisioningUsable$",
		"-test.count=1", "-test.v", "-test.timeout=4m")
	child.Env = append(os.Environ(), drSharedRoleFatalChildEnv+"=1")
	out, err := child.CombinedOutput()
	if err == nil {
		t.Fatalf("the child was supposed to FAIL with the shared role promoted; it passed:\n%s", out)
	}
	if !strings.Contains(string(out), "deliberate failure with the shared role") {
		t.Fatalf("the child did not reach its deliberate failure, so nothing was proven:\n%s", out)
	}
	if !strings.Contains(string(out), "promotion took: ") {
		t.Fatalf("the child never actually promoted the role, so its cleanup proves nothing:\n%s", out)
	}
	t.Logf("child exited with %v after promoting and failing on purpose", err)

	// PROOF 1: the exact attribute is back.
	if attrs := drReadRoleAttributes(t, superDB, role); attrs.superuser {
		t.Fatalf("the fatal child leaked application-role drift: %q is still a SUPERUSER (%+v)", role, attrs)
	}
	// PROOF 2: and provisioning — the thing that actually broke — works again, in
	// this same instance, against this same cluster-global role.
	after := isolatedPGSplit(t)
	if after.Result.AppPosture.Role != role {
		t.Fatalf("the second provisioning used role %q, want the same cluster-global %q",
			after.Result.AppPosture.Role, role)
	}
	if posture := after.Result.AppPosture; posture.Role != role {
		t.Fatalf("provisioning after the fatal child reported role %q, want %q", posture.Role, role)
	}
	if drReadRoleAttributes(t, superDB, role).superuser {
		t.Fatalf("provisioning after the fatal child re-promoted %q", role)
	}
	t.Logf("ordinary provisioning succeeded again in this instance: database %q, application role %q", after.Database, role)
}

// drSharedRoleFatalChild promotes the shared role under the C3 contract and then
// dies. It is only ever reached inside the child process above.
func drSharedRoleFatalChild(t *testing.T) {
	pg := isolatedPGSplit(t)
	super := drOpenSuper(t, pg.Superuser)
	role := pg.Result.AppPosture.Role
	drHoldSharedRoleAttributes(t, super, role)
	drExec(t, super, `ALTER ROLE `+quoteIdent(role)+` SUPERUSER`)
	if !drReadRoleAttributes(t, super, role).superuser {
		t.Fatalf("child could not establish the premise on %q", role)
	}
	t.Logf("promotion took: %q is a SUPERUSER", role)
	t.Fatalf("deliberate failure with the shared role %q promoted", role)
}
