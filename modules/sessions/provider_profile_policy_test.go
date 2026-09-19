// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// argvValue returns the value that follows flag in an argv, and whether the flag
// is present at all. Presence and value are separate answers here because
// `--tools ""` — the deny-closed surface — is a PRESENT flag with an empty value,
// and a test that could not tell those apart would pass on the defect.
func argvValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

// TestProfiledLaunchWithNoDeclaredPolicyGetsNoTools pins what a profile that
// declares nothing may use. Measured 2026-09-18: under permission_mode=default
// the governed child was handed
// its full tool surface — 34 tools including Bash, Write, Edit and NotebookEdit —
// and the product narrowed nothing. A profile that declares no policy is now
// deny-closed: the child is launched with no built-in tools at all.
func TestProfiledLaunchWithNoDeclaredPolicyGetsNoTools(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-policy-none"}
	m, _, tenant, a, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()

	if _, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		ProviderProfileRef: a.Ref, Actor: "user:u1", ActorKind: "user",
	}); err != nil {
		t.Fatalf("createRun: %v", err)
	}
	args := fr.lastSpec().Args
	value, present := argvValue(args, "--tools")
	if !present {
		t.Fatalf("no tool surface was decided for a profiled launch: %v", args)
	}
	if value != "" {
		t.Fatalf("an undeclared profile handed the child %q; deny-closed means no built-in tools", value)
	}
}

// TestProfiledLaunchAppliesTheDeclaredSurfaceAndMode proves the declaration is
// what decides, on both axes, and that the mode reaches the row an operator reads.
func TestProfiledLaunchAppliesTheDeclaredSurfaceAndMode(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-policy-declared"}
	m, _, tenant, a, _ := profiledHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()

	tools := []string{"Read", "Grep"}
	mode := "plan"
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{
		SessionTools: &tools, SessionPermissionMode: &mode,
	}); err != nil {
		t.Fatalf("declare the policy: %v", err)
	}
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		PermissionMode:     "default", // what the CALLER asked for
		ProviderProfileRef: a.Ref, Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	args := fr.lastSpec().Args
	if value, _ := argvValue(args, "--tools"); value != "Grep,Read" {
		t.Fatalf("--tools = %q, want the declared surface canonicalised (Grep,Read)", value)
	}
	if value, _ := argvValue(args, "--permission-mode"); value != "plan" {
		t.Fatalf("--permission-mode = %q, want the profile's declared mode", value)
	}
	if dto.PermissionMode != "plan" {
		t.Fatalf("the record says permission_mode=%q; the operator would read the mode nobody ran under", dto.PermissionMode)
	}
	// And the declaration is readable back off the profile, as a declaration.
	got, err := m.GetProfile(ctx, tenant, a.Ref)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !got.SessionToolsDeclared || strings.Join(got.SessionTools, ",") != "Grep,Read" {
		t.Fatalf("the profile does not carry its declaration: %+v", got)
	}
}

// TestADeclaredEmptySurfaceIsKeptApartFromAnUndeclaredOne: both launch the same
// child, and only one of them is something an operator SAID. The difference is
// what the profile reports, and it is the difference between a policy and a gap.
func TestADeclaredEmptySurfaceIsKeptApartFromAnUndeclaredOne(t *testing.T) {
	t.Parallel()

	m, _, tenant, a, b := profiledHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	ctx := context.Background()

	none := []string{}
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{SessionTools: &none}); err != nil {
		t.Fatalf("declare none: %v", err)
	}
	declared, err := m.GetProfile(ctx, tenant, a.Ref)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !declared.SessionToolsDeclared || len(declared.SessionTools) != 0 {
		t.Fatalf("a declared-empty surface did not survive the round trip: %+v", declared)
	}
	silent, err := m.GetProfile(ctx, tenant, b.Ref)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if silent.SessionToolsDeclared {
		t.Fatalf("a profile nobody declared for reads as declared: %+v", silent)
	}
	// Withdrawing a declaration returns the profile to deny-closed AND to silence.
	var withdraw *[]string = new([]string)
	*withdraw = nil
	if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{SessionTools: withdraw}); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	after, err := m.GetProfile(ctx, tenant, a.Ref)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if after.SessionToolsDeclared {
		t.Fatalf("a withdrawn declaration still reads as declared: %+v", after)
	}
}

// TestADeclaredPolicyIsRefusedForADriverThatCannotApplyIt keeps the governance
// surface honest: a Codex or ACP child negotiates its tools in protocol, so a
// policy declared for one would be stored, shown, and never applied.
func TestADeclaredPolicyIsRefusedForADriverThatCannotApplyIt(t *testing.T) {
	t.Parallel()

	m, _, tenant, _, _ := profiledHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	ctx := context.Background()
	configHome, userHome, _, _ := twoHomes(t)

	_, err := m.CreateProfile(ctx, tenant, CreateProfileInput{
		Driver: "grok", ConfigHome: configHome, UserHome: userHome, DisplayName: "G",
		AuthSource:   AuthSourceAccountHome,
		SessionTools: []string{"Read"}, SessionToolsDeclared: true,
	})
	if err == nil {
		t.Fatal("a tool policy was accepted for a driver whose launch form cannot express one")
	}
	if statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %v", err)
	}
	if !strings.Contains(err.Error(), "own protocol") {
		t.Fatalf("the refusal must say why, got %q", err.Error())
	}
}

// TestAProfilePolicyDoesNotOverrideATemplate: a template's terms are
// approval-bound, so the identity must not widen them.
func TestAProfilePolicyDoesNotOverrideATemplate(t *testing.T) {
	t.Parallel()

	p := &CreateRunParams{PermissionMode: "plan", TemplateID: "tpl-1"}
	if err := applySessionPolicy(p, providerDriverClaude, sessionPolicy{PermissionMode: "bypassPermissions"}); err != nil {
		t.Fatalf("applySessionPolicy: %v", err)
	}
	if p.PermissionMode != "plan" {
		t.Fatalf("the profile widened a template's mode to %q", p.PermissionMode)
	}
	// Without a template, the profile's declaration is what governs.
	q := &CreateRunParams{PermissionMode: "default"}
	if err := applySessionPolicy(q, providerDriverClaude, sessionPolicy{PermissionMode: "plan"}); err != nil {
		t.Fatalf("applySessionPolicy: %v", err)
	}
	if q.PermissionMode != "plan" {
		t.Fatalf("the profile's declared mode was ignored: %q", q.PermissionMode)
	}
}

// TestSessionToolNamesAreBoundedByShapeNotByAList keeps tomorrow's tool name
// launchable while refusing one that would make the argv ambiguous.
func TestSessionToolNamesAreBoundedByShapeNotByAList(t *testing.T) {
	t.Parallel()

	ok, err := normalizeSessionTools([]string{"Read", "SomeToolShippedNextYear", "Read", " Grep "})
	if err != nil {
		t.Fatalf("a future tool name was refused: %v", err)
	}
	if strings.Join(ok, ",") != "Grep,Read,SomeToolShippedNextYear" {
		t.Fatalf("declaration not canonicalised: %v", ok)
	}
	for _, bad := range []string{"Read,Write", "Bash;rm", "a\"b", "x`y", "$PATH", "a|b", "\x01"} {
		if _, err := normalizeSessionTools([]string{bad}); err == nil {
			t.Fatalf("tool name %q was accepted", bad)
		}
	}
}
