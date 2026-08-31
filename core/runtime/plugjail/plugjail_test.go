// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package plugjail

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestScopedEnv_ExcludesEngineSecrets is the load-bearing C1 test: the confined env
// must NEVER carry the engine's environment (where every connector secret + KMS/signing
// key lives), only PATH + explicit allowlisted extras.
func TestScopedEnv_ExcludesEngineSecrets(t *testing.T) {
	t.Setenv("OLIVARES_AUDIT_SIGNING_KEY", "super-secret-key")
	t.Setenv("OLIVARES_SOURCES_CONFIG", "{...other-connector-secrets...}")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "AKIAEXAMPLE")

	env := ScopedEnv("PLUGIN_MODE=strict")
	joined := strings.Join(env, "\n")
	for _, leaked := range []string{
		"OLIVARES_AUDIT_SIGNING_KEY", "super-secret-key",
		"OLIVARES_SOURCES_CONFIG", "AWS_SECRET_ACCESS_KEY", "AKIAEXAMPLE",
	} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("scoped env leaked engine secret %q: %v", leaked, env)
		}
	}
	var hasPath, hasExtra bool
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
		}
		if e == "PLUGIN_MODE=strict" {
			hasExtra = true
		}
	}
	if !hasPath {
		t.Error("scoped env must keep a hermetic PATH")
	}
	if !hasExtra {
		t.Error("scoped env must include the explicit allowlisted extra")
	}
}

// TestScopedEnv_RejectsMalformedExtras: an extra without '=' or blank is dropped, so a
// caller cannot smuggle a bare secret name or unbounded string into the plugin env.
func TestScopedEnv_RejectsMalformedExtras(t *testing.T) {
	joined := strings.Join(ScopedEnv("", "NOEQUALS", "GOOD=1", "   "), "\n")
	if strings.Contains(joined, "NOEQUALS") {
		t.Error("a malformed (no '=') extra must be dropped")
	}
	if !strings.Contains(joined, "GOOD=1") {
		t.Error("a valid extra must be kept")
	}
}

// TestApply_ScopesEnvAndAttests: Apply scopes the command env, marks it scoped, and
// records an HONEST level plus the PRE-release egress degrade.
func TestApply_ScopesEnvAndAttests(t *testing.T) {
	t.Setenv("OLIVARES_SECRET", "leak-me")
	cmd := exec.Command("/bin/true")
	att, cleanup, err := Apply(cmd, Default("test-plugin"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if strings.Contains(strings.Join(cmd.Env, "\n"), "OLIVARES_SECRET") {
		t.Fatal("Apply must not leak the engine env into the plugin command")
	}
	if !att.EnvScoped {
		t.Error("EnvScoped must be true")
	}
	if att.Plugin != "test-plugin" {
		t.Errorf("plugin = %q, want test-plugin", att.Plugin)
	}
	if att.Platform != runtime.GOOS {
		t.Errorf("platform = %q, want %q", att.Platform, runtime.GOOS)
	}
	if att.EgressBounded {
		t.Error("egress must be reported as NOT bounded PRE-release")
	}
	var foundEgress bool
	for _, d := range att.Degraded {
		if strings.Contains(d, "egress") {
			foundEgress = true
		}
	}
	if !foundEgress {
		t.Error("the egress degrade must be recorded honestly")
	}
	// Without seccomp+landlock this release, the level must never claim strong.
	if att.Level == LevelStrong {
		t.Errorf("level must not be strong without seccomp+landlock: %+v", att)
	}
	if att.Level != LevelMinimal && att.Level != LevelPartial {
		t.Errorf("unexpected level %q", att.Level)
	}
}

// TestApply_HonestAttestation pins the honesty contract the adversarial review enforced:
// a control that is not verifiably applied is NEVER asserted, and env-scoping without a
// uid drop is recorded as bypassable (a same-uid plugin can read /proc/<engine>/environ).
func TestApply_HonestAttestation(t *testing.T) {
	cmd := exec.Command("/bin/true")
	att, cleanup, err := Apply(cmd, Default("honest"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Controls that are NOT wired this release must never be asserted.
	if att.CapsDropped {
		t.Error("CapsDropped must not be asserted until bounding-drop + no-new-privs are wired")
	}
	if att.Seccomp {
		t.Error("Seccomp must not be asserted this release")
	}
	if att.Landlock {
		t.Error("Landlock must not be asserted this release")
	}
	if att.Level == LevelStrong {
		t.Errorf("strong is unreachable this release, got %q", att.Level)
	}
	// A cgroup must never be asserted without at least one ceiling recorded as effective.
	if att.Cgroup && att.MemoryBytes == 0 && att.PidsMax == 0 {
		t.Error("Cgroup asserted true but no effective ceiling recorded — over-claim")
	}
	// If the uid was NOT dropped (e.g. a non-root test host), the /proc-read bypass of
	// env-scoping MUST be recorded so env_scoped is never read as secret-protection.
	if !att.DedicatedUID {
		var noted bool
		for _, d := range att.Degraded {
			if strings.Contains(d, "/proc") || strings.Contains(d, "environ") {
				noted = true
			}
		}
		if !noted {
			t.Errorf("without a uid drop, the /proc-read bypass of env-scoping must be recorded: %v", att.Degraded)
		}
	}
}

// TestResolveLevel_HonestGrading: a level is never more optimistic than the controls
// that actually applied.
func TestResolveLevel_HonestGrading(t *testing.T) {
	if got := resolveLevel(Attestation{EnvScoped: true}); got != LevelMinimal {
		t.Errorf("env-only ⇒ minimal, got %q", got)
	}
	if got := resolveLevel(Attestation{DedicatedUID: true, Cgroup: true}); got != LevelPartial {
		t.Errorf("uid+cgroup (no seccomp/landlock) ⇒ partial, got %q", got)
	}
	full := Attestation{
		DedicatedUID: true, CapsDropped: true, NoNewPrivs: true,
		Cgroup: true, Seccomp: true, Landlock: true,
	}
	if got := resolveLevel(full); got != LevelStrong {
		t.Errorf("full set ⇒ strong, got %q", got)
	}
}
