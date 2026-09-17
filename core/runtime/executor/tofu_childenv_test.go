// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"strings"
	"testing"
	"time"
)

// childEnv copies, by NAME, variables from the control plane's own process into a
// third-party binary's environment (tofu.go, the PassthroughEnv loop).
//
// Today that is not an escalation: the list can only come from the operator's boot file,
// and an operator passing their own machine's variables to their own tool is their
// business. It BECOMES one the moment a console lets a tenant admin write that list, which
// is exactly the feature being built on top of this. So the invariant below is not a
// repair of somebody else's debt: it is a PRECONDITION of that feature, and it belongs in
// the engine — a rule that only the form enforces is bypassed by every other caller (API,
// CLI, seed, a composition test). Case "the form is bypassed" is what separates the two
// implementations; it calls childEnv directly, which IS the bypass.
//
// The rule is deliberately narrow: `OLIVARES_` names the control plane's OWN trust domain
// and is never legitimate to hand to a child, not even for its owner. Everything else stays
// allowed here, and `TestChildEnvKeepsThirdPartyCredentials` is the control that says so:
// handing cloud credentials to terraform is the ordinary use of passthrough, and a cure
// that blocked it would read as "more secure" while breaking a real deploy. Bounding what a
// CONSOLE may write is a different problem with a different shape (an allow-list, because a
// deny-list cannot enumerate a host's secrets) and it is not this test's subject.

// tofuForEnv builds a backend whose only interesting configuration is the passthrough list.
func tofuForEnv(pass []string) *TofuBackend {
	return NewTofuBackend(TofuConfig{
		Binary:         "tofu",
		WorkdirRoot:    "/nonexistent",
		CredentialEnv:  []string{"VAULT_TOKEN"},
		PassthroughEnv: pass,
		LockTimeout:    time.Second,
		Timeout:        time.Second,
	})
}

// envHasName reports whether the built environment carries an assignment for name.
func envHasName(env []string, name string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

// envCarriesValue reports whether the value leaked into ANY assignment, under any name.
// Checking only the name would miss a copy made under a different key.
func envCarriesValue(env []string, value string) bool {
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 && kv[i+1:] == value {
			return true
		}
	}
	return false
}

func TestChildEnvRefusesControlPlaneOwnSecrets(t *testing.T) {
	const secret = "sk-control-plane-must-never-cross"

	cases := []struct {
		name    string
		envName string
	}{
		{"the exact name", "OLIVARES_MASTER_KEY"},
		// Prefix, not equality: this is the case that kills a cure written as `== "OLIVARES_MASTER_KEY"`.
		{"a longer name under the same prefix", "OLIVARES_MASTER_KEY_2"},
		// The comparison must not depend on case: an operator typing the name in lower case
		// still names the same process variable on a case-sensitive OS only if it matches, but
		// a cure that lower-cases one side and not the other lets a near-miss through.
		{"the same prefix in lower case", "olivares_master_key"},
		// Padding, because the name does NOT arrive as an identifier: it is a string in the
		// operator's JSON list, and a human typing one into a console will pad it. The OS
		// accepts a space inside a variable name, so os.LookupEnv resolves the padded name
		// and the value crosses — the predicate has to trim BEFORE it compares. Removing the
		// trim leaves every case above green and only this one red.
		{"the prefix padded with whitespace", "  OLIVARES_MASTER_KEY  "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envName, secret)

			env, err := tofuForEnv([]string{tc.envName}).childEnv(Credential{Token: "t"})
			if err != nil {
				t.Fatalf("childEnv returned an error, so this case proves nothing about the copy: %v", err)
			}
			if envHasName(env, tc.envName) {
				t.Errorf("%q was copied into the child environment", tc.envName)
			}
			if envCarriesValue(env, secret) {
				t.Errorf("the value of %q reached the child under some name", tc.envName)
			}
		})
	}
}

// The control that stops the cure from over-reaching. Handing cloud credentials to terraform
// is the ORDINARY use of passthrough; a deny-list that swallowed them would look like extra
// security and would break a real deployment, and no mutant of the cases above would notice.
func TestChildEnvKeepsThirdPartyCredentials(t *testing.T) {
	const value = "AKIA-example-not-a-real-key"
	t.Setenv("AWS_SECRET_ACCESS_KEY", value)

	env, err := tofuForEnv([]string{"AWS_SECRET_ACCESS_KEY"}).childEnv(Credential{Token: "t"})
	if err != nil {
		t.Fatalf("childEnv: %v", err)
	}
	if !envHasName(env, "AWS_SECRET_ACCESS_KEY") {
		t.Error("AWS_SECRET_ACCESS_KEY was NOT passed through: the rule over-reached and this breaks the ordinary case")
	}
}

// One bad entry must not poison the list: the operator's legitimate names still travel.
func TestChildEnvDropsOnlyTheRefusedEntry(t *testing.T) {
	t.Setenv("OLIVARES_MASTER_KEY", "secret")
	t.Setenv("TF_LOG", "INFO")

	env, err := tofuForEnv([]string{"TF_LOG", "OLIVARES_MASTER_KEY"}).childEnv(Credential{Token: "t"})
	if err != nil {
		t.Fatalf("childEnv: %v", err)
	}
	if !envHasName(env, "TF_LOG") {
		t.Error("a refused entry took a legitimate one with it")
	}
	if envHasName(env, "OLIVARES_MASTER_KEY") {
		t.Error("the refused entry was copied")
	}
}

// The precondition of the whole file: if the base environment did not carry PATH and HOME,
// every assertion above would pass over an empty environment and prove nothing.
func TestChildEnvBaseIsPresent(t *testing.T) {
	env, err := tofuForEnv(nil).childEnv(Credential{Token: "t"})
	if err != nil {
		t.Fatalf("childEnv: %v", err)
	}
	for _, name := range []string{"PATH", "HOME", "TF_IN_AUTOMATION"} {
		if !envHasName(env, name) {
			t.Errorf("%s missing from the child environment: the other cases would be vacuous", name)
		}
	}
}

// The LOUD half. The engine's refusal is silent by design, and a silent skip alone would
// mislead an operator into believing the variable travels — so this is the half that says so
// where someone is watching. Both halves share ONE predicate. Two callers exist in this
// tree (config loader and engine); there is no implemented console caller. Callers that
// each re-derived the rule would drift, and the drift would be silent.
func TestValidatePassthroughNamesTheRefusedEntry(t *testing.T) {
	rej := ValidatePassthrough([]string{"HOME", "OLIVARES_MASTER_KEY", "AWS_SECRET_ACCESS_KEY", "olivares_admin_token"})

	if len(rej) != 2 {
		t.Fatalf("rejected %d entries, want 2 (only the control-plane ones): %+v", len(rej), rej)
	}
	got := map[string]bool{}
	for _, r := range rej {
		got[r.Name] = true
		if r.Code != RejectionControlPlaneDomain {
			t.Errorf("%s carries code %q: a caller switching on the code cannot decide", r.Name, r.Code)
		}
		if r.Reason == "" {
			t.Errorf("%s has no prose for the human", r.Name)
		}
	}
	if !got["OLIVARES_MASTER_KEY"] || !got["olivares_admin_token"] {
		t.Errorf("the refusal is not by prefix and case-insensitively: %+v", rej)
	}
}

// The control that keeps the loud half from over-reaching, mirroring the engine's:
// a list with nothing refusable must produce NO rejection at all, not an empty-but-present
// complaint that a console would render as a problem.
func TestValidatePassthroughStaysQuietOnLegitimateNames(t *testing.T) {
	if rej := ValidatePassthrough([]string{"HOME", "PATH", "TF_LOG", "AWS_SECRET_ACCESS_KEY"}); len(rej) != 0 {
		t.Fatalf("refused %d legitimate names: %+v", len(rej), rej)
	}
}

// Both halves must answer the same about the same name. If they ever diverge, the console
// would accept what the engine silently drops — the operator would be told it travels and it
// would not, which is the exact failure the pair exists to prevent.
func TestBothHalvesAgree(t *testing.T) {
	for _, name := range []string{
		"HOME", "PATH", "TF_LOG", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN",
		"OLIVARES_MASTER_KEY", "OLIVARES_MASTER_KEY_2", "olivares_master_key", "  OLIVARES_X  ",
	} {
		loud := len(ValidatePassthrough([]string{name})) > 0
		quiet := deniedPassthrough(name)
		if loud != quiet {
			t.Errorf("%q: the door says refused=%v and the engine says %v — they would drift apart", name, loud, quiet)
		}
	}
}
