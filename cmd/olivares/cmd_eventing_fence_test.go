// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// Unit H, commit 6 — the operator's ceremony, measured against the PRODUCTION composition.
//
// These go through boot(), not a module fixture, for the reason unit G's wireproof states: this
// campaign has already shipped a control that was present in the code and absent from the binary's
// behavior. What the module tests prove is the rule; what these prove is that the lever an operator
// actually pulls reaches it.
//
// MUTATION-TESTED, same discipline as the enforcement:
//
//	mutation                                              test that went red
//	----------------------------------------------------  -----------------------------------------
//	let the probe report "refused" when the write lands    TestTheProbeReportsAnAcceptedWriteAndStillLeavesNothingBehind
//	drop the probe's rollback sentinel                     TestTheProbeReportsAnAcceptedWriteAndStillLeavesNothingBehind
//	drop the fence check from the destination ceremony     TestEnforcingDestinationsRefusesWhileTheFenceIsDormant
//	probe only the first governed mutation                 TestAPartialFenceIsNotReportedAsEnforcing
//	restore the "already committed" arming shortcut        TestAFailedArmingDoesNotBecomeASuccessOnTheSecondRun
//	count any error carrying the words as a refusal        TestOnlyTheFencesOwnRefusalCountsAsEnforcement
//
// The first two rows are the reason this file has the test it has. They were run FIRST against
// TestTheProbeMeasuresARefusalNotTheCatalog, and it stayed GREEN through both: on a fresh armed
// database the probe's write is always refused, so neither the accepted-write branch nor the
// rollback is ever reached there. The test was measuring half a mechanism and reading as though it
// measured all of it — which is the failure this campaign keeps finding, this time in my own test.

// bootFence opens the production composition on a fresh SQLite database, where the fence is armed by
// classification.
func bootFence(t *testing.T) *engine {
	t.Helper()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test",
	})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// TestTheProbeMeasuresARefusalNotTheCatalog is the property the whole verification rests on: what is
// reported as "live" is a refusal the engine actually produced, not a trigger that exists.
func TestTheProbeMeasuresARefusalNotTheCatalog(t *testing.T) {
	ctx := context.Background()
	eng := bootFence(t)

	accepted, err := probeFenceEnforcement(ctx, eng.store)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(accepted) != 0 {
		t.Fatalf("the database accepted these governed mutations with no capability attestation: %v", accepted)
	}
	// The count is pinned so a probe REMOVED is caught here. It does NOT catch a governed surface
	// added without a probe — the count stays at six and this stays green — and an earlier version
	// of this comment claimed it did. That direction is the one that matters, and it is enforced
	// where it can be DERIVED instead of asserted: modules/eventing's TestEveryGovernedTriggerHasAProbe
	// scans the embedded fence migrations and requires a probe for every `_writer_fence_<op>` trigger
	// they declare, in both directions.
	if got := len(eventing.FenceProbes()); got != 6 {
		t.Fatalf("the probe matrix covers %d governed mutations, want 6 — a probe was removed, which silently shrinks what `verify` covers", got)
	}
	// And the probe leaves NOTHING behind — it is run by an operator against a live deployment, so a
	// row that survived it would be a subscription nobody authored.
	if err := eng.store.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		repo, e := sc.Ext(evtSubscriptionKind)
		if e != nil {
			return e
		}
		rows, _, e := repo.List(ctx, model.Query{Limit: 10})
		if e != nil {
			return e
		}
		for _, r := range rows {
			if r.String(evtColSubName) == eventing.FenceProbeRowName {
				t.Fatal("the probe left a subscription behind: it must roll back whatever it writes")
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
}

// TestTheProbeReportsAnAcceptedWriteAndStillLeavesNothingBehind is the other half of the probe, and
// it exists because the first half could not measure it.
//
// TestTheProbeMeasuresARefusalNotTheCatalog runs against a fresh, armed database where the write is
// always refused — so the branch that handles an ACCEPTED write is never reached there, and a probe
// hard-coded to answer "refused" would have passed it. That is not a hypothetical: it was measured,
// by mutating exactly that branch and watching the test stay green.
//
// Here the fence is moved off enforced, so the write genuinely lands inside the transaction. The
// probe must report it as NOT refused — otherwise a deployment whose enforcement was lost in a
// restore would be reported as enforcing — and it must still leave nothing behind, because a
// transaction that commits its probe row writes a subscription nobody authored.
func TestTheProbeReportsAnAcceptedWriteAndStillLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	eng := bootFence(t)
	rs := eng.store.(store.RolloutStater)
	cur, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: eventing.EgressWriterFenceControlKey, Mode: store.RolloutPolicyOptional,
		Actor: "test", Reason: "let the probe's write actually land", ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("move the fence off enforced: %v", err)
	}

	accepted, err := probeFenceEnforcement(ctx, eng.store)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(accepted) != len(eventing.FenceProbes()) {
		t.Fatalf("with the fence off, the probe reported only %d of %d governed mutations as accepted: a deployment whose enforcement was lost in a restore would then be reported as enforcing", len(accepted), len(eventing.FenceProbes()))
	}
	if err := eng.store.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		repo, e := sc.Ext(evtSubscriptionKind)
		if e != nil {
			return e
		}
		rows, _, e := repo.List(ctx, model.Query{Limit: 10})
		if e != nil {
			return e
		}
		for _, r := range rows {
			if r.String(evtColSubName) == eventing.FenceProbeRowName {
				t.Fatal("the probe COMMITTED its row: an operator running this against a live deployment would be creating a subscription nobody authored")
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
}

// TestOnlyTheFencesOwnRefusalCountsAsEnforcement. The probe used to accept ANY error whose text
// contained "writer fence", so a constraint, a wrapper or an unrelated function carrying those words
// would have counted as a working fence. The signal now requires the engine's own constraint code
// alongside the message.
func TestOnlyTheFencesOwnRefusalCountsAsEnforcement(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"sqlite trigger refusal", errors.New("constraint failed: olivares: eventing egress writer fence: this write carries no capability attestation (1811)"), true},
		{"postgres trigger refusal", errors.New("ERROR: olivares: eventing egress writer fence: no live capability attestation matches this write (SQLSTATE OL441)"), true},
		{"an unrelated error that merely says the words", errors.New("failed to render the eventing egress writer fence status page"), false},
		{"a different constraint entirely", errors.New("constraint failed: eventing_subscription_uniq (1555)"), false},
		{"nothing", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWriterFenceRefusal(tc.err); got != tc.want {
				t.Fatalf("isWriterFenceRefusal(%v) = %v, want %v — a signal this loose reports enforcement that is not there", tc.err, got, tc.want)
			}
		})
	}
}

// dropSQLiteTrigger removes one enforcement object from the store's own database, which is what a
// data-only restore, a hand-repaired database or a mis-promoted replica leaves behind.
//
// It goes through a second connection to the same file deliberately: the store exposes no DDL, and
// an operator's recovery does not go through the store either.
func dropSQLiteTrigger(t *testing.T, dsn, trigger string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open the store's database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("DROP TRIGGER IF EXISTS " + trigger); err != nil {
		t.Fatalf("drop %s: %v", trigger, err)
	}
}

// bootFenceFile is bootFence on a FILE database, so a test can reach the same database from a second
// connection. `:memory:` is private to its pool.
func bootFenceFile(t *testing.T) (*engine, string) {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "fence.db")
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Engine: "sqlite", DSN: dsn, Version: "test",
	})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng, dsn
}

// TestAPartialFenceIsNotReportedAsEnforcing is P0-2 from the implementation contrast, as a property.
//
// The first version of the probe attempted ONE of the governed mutations and the ceremony reported
// ENFORCING for the whole fence. Drop any of the other four triggers — a data-only restore does
// exactly that — and `verify` returned a green in precisely the situation it exists for.
func TestAPartialFenceIsNotReportedAsEnforcing(t *testing.T) {
	ctx := context.Background()
	eng, dsn := bootFenceFile(t)

	// Precondition: whole and enforcing.
	st, err := readFenceStatus(ctx, eng.store)
	if err != nil {
		t.Fatalf("read fence status: %v", err)
	}
	if st.Enforcement != fenceEnforcementLive {
		t.Fatalf("precondition: enforcement is %q, want %q", st.Enforcement, fenceEnforcementLive)
	}

	// Lose ONE trigger, and NOT the one the old probe happened to exercise.
	dropSQLiteTrigger(t, dsn, "eventing_subscription_sink_writer_fence_del")

	st, err = readFenceStatus(ctx, eng.store)
	if err != nil {
		t.Fatalf("read fence status: %v", err)
	}
	if st.Enforcement != fenceEnforcementMissing {
		t.Fatalf("with a governed mutation left unenforced the status is %q, want %q: a probe that only exercises one path reports a green for the four it never touched",
			st.Enforcement, fenceEnforcementMissing)
	}
	if len(st.Unenforced) != 1 || !strings.Contains(st.Unenforced[0], "sink profile DELETE") {
		t.Fatalf("the status does not NAME the mutation that is still accepted (%v), which is what an operator needs to repair it", st.Unenforced)
	}
	// And `verify` exits non-zero, naming a repair that actually works.
	verr := runFenceVerify(t, dsn)
	if verr == nil {
		t.Fatal("`fence verify` reported success with a governed mutation unenforced")
	}
	if !strings.Contains(verr.Error(), "DELETE FROM schema_migrations_mod_eventing") {
		t.Fatalf("the repair advice does not tell the operator to forget the applied migrations, so a restart re-creates nothing: %v", verr)
	}
}

// runFenceVerify runs the ceremony the way an operator does.
func runFenceVerify(t *testing.T, dsn string) error {
	t.Helper()
	cmd := newEventingFenceVerifyCmd()
	cmd.SetArgs([]string{"--data-dir", filepath.Dir(dsn), "--engine", "sqlite", "--dsn", dsn})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return cmd.Execute()
}

// TestAFailedArmingDoesNotBecomeASuccessOnTheSecondRun is P0-3: the transition commits BEFORE the
// verification runs, so an arming whose verification fails leaves a committed decision behind. The
// shortcut that returns "already armed and committed" then turned the operator's retry into a
// success while the database enforced nothing.
//
// Re-arming is also the REPAIR path, and the generation bump it performs is what invalidates a proof
// that a writer committed while a trigger was missing — the P0-4 bypass.
func TestAFailedArmingDoesNotBecomeASuccessOnTheSecondRun(t *testing.T) {
	ctx := context.Background()
	eng, dsn := bootFenceFile(t)
	rs := eng.store.(store.RolloutStater)

	// Commit the arming, the way a first attempt does, then lose an enforcement object.
	cur, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: eventing.EgressWriterFenceControlKey, Mode: store.RolloutEnforced,
		Actor: "test", Reason: "a first arming that committed", ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("arm: %v", err)
	}
	dropSQLiteTrigger(t, dsn, "eventing_subscription_writer_fence_ins")
	_ = eng.Close()

	// The operator runs `arm` again.
	run := func() (string, error) {
		cmd := newEventingFenceArmCmd()
		cmd.SetArgs([]string{
			"--data-dir", filepath.Dir(dsn), "--engine", "sqlite", "--dsn", dsn,
			"--reason", "CHG-9: retry", "--assert-writers-upgraded",
		})
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		// Execute FIRST. `return out.String(), cmd.Execute()` evaluates the operands left to right,
		// so it would capture the buffer before the command ever wrote to it.
		rerr := cmd.Execute()
		return out.String(), rerr
	}
	out, err := run()
	if err == nil {
		t.Fatalf("re-arming a committed fence whose database enforces nothing returned SUCCESS:\n%s", out)
	}
	if !strings.Contains(err.Error(), "subscription INSERT") {
		t.Fatalf("the failure does not name the mutation that is still accepted: %v", err)
	}
	if !strings.Contains(out, "invalidates every proof outstanding") {
		t.Fatalf("re-arming did not say it was moving the generation, which is what invalidates a proof orphaned during the gap:\n%s", out)
	}
}

// TestTheStatusReportsEnforcementAndNeverClaimsTheFleet. An operator reading "armed" would
// reasonably conclude something had checked the fleet. Nothing has, and the surface has to say so
// next to the claim rather than in a design document.
func TestTheStatusReportsEnforcementAndNeverClaimsTheFleet(t *testing.T) {
	ctx := context.Background()
	eng := bootFence(t)

	st, err := readFenceStatus(ctx, eng.store)
	if err != nil {
		t.Fatalf("read fence status: %v", err)
	}
	if !st.Armed || st.Enforcement != fenceEnforcementLive {
		t.Fatalf("a fresh install reports armed=%v enforcement=%q, want true/%q", st.Armed, st.Enforcement, fenceEnforcementLive)
	}
	if st.RequiredCapability != eventing.EgressWriterCapability || st.BinaryCapability != eventing.EgressWriterCapability {
		t.Fatalf("capabilities reported required=%d binary=%d, want %d for both — a refusal cannot be diagnosed without them",
			st.RequiredCapability, st.BinaryCapability, eventing.EgressWriterCapability)
	}
	if st.DestinationMode == "" {
		t.Fatal("the fence status does not report the destination control's mode; the two are read together in every real decision")
	}
	var out strings.Builder
	printFenceStatus(&out, st)
	text := out.String()
	for _, forbidden := range []string{"proved", "fleet verified", "all writers"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the status text claims %q, which arming cannot establish:\n%s", forbidden, text)
		}
	}
	// The disclaimer must name BOTH things arming cannot establish, and it must be tied to what was
	// actually measured: the live branch is the only one allowed to say a violation fails.
	for _, want := range []string{"fleet's composition", "writes already in the"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the status text does not disclaim %q, so an operator reads ARMED as more than it is:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "Armed and enforcing") {
		t.Fatalf("the status text claims a violation fails without tying it to the measurement:\n%s", text)
	}
}

// TestADormantFenceIsReportedAsUnverifiableNotAsBroken. On a deployment whose fleet predates the
// fence, an unproved write is accepted BY DESIGN, so the probe cannot tell a working fence from an
// absent one — and "I could not establish this" must not be delivered as either answer.
func TestADormantFenceIsReportedAsUnverifiableNotAsBroken(t *testing.T) {
	ctx := context.Background()
	eng := bootFence(t)
	rs := eng.store.(store.RolloutStater)

	// Model the dormant deployment by moving the fence's durable disposition, which is what an
	// upgraded estate is classified into.
	cur, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: eventing.EgressWriterFenceControlKey, Mode: store.RolloutPolicyOptional,
		Actor: "test", Reason: "model a deployment that is not armed", ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("move the fence off enforced: %v", err)
	}
	st, err := readFenceStatus(ctx, eng.store)
	if err != nil {
		t.Fatalf("read fence status: %v", err)
	}
	if st.Armed {
		t.Fatal("precondition: the fence still reports armed")
	}
	if st.Enforcement != fenceEnforcementDormant {
		t.Fatalf("enforcement reported %q on a dormant fence, want %q: an unproved write is accepted here by design, so a probe proves nothing either way",
			st.Enforcement, fenceEnforcementDormant)
	}
	if st.RequiredCapability != 0 {
		t.Fatalf("a dormant fence demands capability %d, want 0", st.RequiredCapability)
	}
}

// TestArmingRefusesWithoutAReasonAndWithoutTheAcknowledgement. A rollout decision with no recorded
// reason is the ownerless state these controls exist to eliminate, and arming makes un-upgraded
// nodes fail — which an operator should be doing on purpose.
func TestArmingRefusesWithoutAReasonAndWithoutTheAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no reason", []string{"--assert-writers-upgraded"}, "--reason is required"},
		{"no acknowledgement", []string{"--reason", "CHG-1"}, "--assert-writers-upgraded is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newEventingFenceArmCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(&strings.Builder{})
			cmd.SetErr(&strings.Builder{})
			err := cmd.Execute()
			if err == nil {
				t.Fatal("the ceremony accepted an incomplete arming")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// TestThereIsNoDisarm. The fence exists because an un-upgraded writer can author a destination
// nothing governed; a flag that reopens that would be shipping the hole with a switch on it.
func TestThereIsNoDisarm(t *testing.T) {
	arm := newEventingFenceArmCmd()
	if f := arm.Flags().Lookup("mode"); f != nil {
		t.Fatal("the arming ceremony exposes a --mode flag, which is how a disarm gets added by accident")
	}
	for _, c := range newEventingFenceCmd().Commands() {
		if strings.Contains(c.Use, "disarm") || strings.Contains(strings.ToLower(c.Short), "disarm") {
			t.Fatalf("the fence exposes a disarm command (%q)", c.Use)
		}
	}
}

// TestEnforcingDestinationsRefusesWhileTheFenceIsDormant pins THE ORDER.
//
// --assert-writers-upgraded is the operator signing for something nothing checks. Enforcing
// destinations while the fence is dormant records that signature with nothing to hold anyone to it,
// which is exactly the gap unit H exists to close. Arming first is the safe sequence: interrupted
// between the two steps, the state left behind is more restrictive, never a silent authorization.
func TestEnforcingDestinationsRefusesWhileTheFenceIsDormant(t *testing.T) {
	ctx := context.Background()
	eng := bootFence(t)
	rs := eng.store.(store.RolloutStater)
	cur, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: eventing.EgressWriterFenceControlKey, Mode: store.RolloutPolicyOptional,
		Actor: "test", Reason: "model a fleet that has not converged", ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("move the fence off enforced: %v", err)
	}
	_ = eng.Close()

	// The ceremony opens its own engine over the same data dir, which is what an operator's
	// invocation does. A :memory: DSN would not survive the close, so this uses a file.
	dir := t.TempDir()
	run := func(extra ...string) (string, error) {
		cmd := newEventingEgressActuateCmd()
		args := append([]string{
			"--data-dir", dir, "--engine", "sqlite", "--accept-blocked",
			"--mode", "enforced", "--reason", "CHG-1: enforce destinations",
			"--assert-writers-upgraded",
		}, extra...)
		cmd.SetArgs(args)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}
	// On a FRESH database the fence is armed, so the ordering refusal must NOT fire — the point is
	// to catch an unfenced deployment, not to block every operator.
	//
	// The success is asserted, not just the ABSENCE of the fence's words. The earlier form let any
	// other failure through: the ceremony could have refused for an unrelated reason and this case
	// would still have been green, which makes "the refusal did not fire" true for the wrong reason.
	if _, err := run(); err != nil {
		t.Fatalf("the ceremony failed on a FRESH database, whose fence is armed by classification: on this deployment it must succeed, and an error here is not evidence that the ordering refusal held back: %v", err)
	}

	// Now the dormant case, on its own data dir.
	dir2 := t.TempDir()
	eng2, err := boot(ctx, bootConfig{DataDir: dir2, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	rs2 := eng2.store.(store.RolloutStater)
	cur2, err := rs2.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if _, err := rs2.SetRolloutMode(ctx, store.RolloutTransition{
		Key: eventing.EgressWriterFenceControlKey, Mode: store.RolloutPolicyOptional,
		Actor: "test", Reason: "model a fleet that has not converged", ExpectGeneration: cur2.Generation,
	}); err != nil {
		t.Fatalf("move the fence off enforced: %v", err)
	}
	_ = eng2.Close()

	cmd := newEventingEgressActuateCmd()
	// --accept-blocked clears the destination-diff preflight (this fixture configures no policy
	// file), so what is left to refuse is the ORDER, which is what this test is about.
	cmd.SetArgs([]string{
		"--data-dir", dir2, "--engine", "sqlite", "--accept-blocked",
		"--mode", "enforced", "--reason", "CHG-1: enforce destinations", "--assert-writers-upgraded",
	})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil {
		t.Fatal("enforcing destinations was accepted while the writer fence was dormant: the assertion would be recorded with nothing enforcing it")
	}
	if !strings.Contains(err.Error(), "writer fence") || !strings.Contains(err.Error(), "fence arm") {
		t.Fatalf("the refusal does not name the fence or the remedy: %v", err)
	}
	if !strings.Contains(err.Error(), "--accept-unfenced") {
		t.Fatalf("the refusal offers no way through for a fleet that cannot converge yet: %v", err)
	}
}

// The ceremony's HAPPY PATH, which nothing tested.
//
// Every other case here measures a refusal: a failed arming, a partial fence, a dormant one, a
// missing reason, the absence of a disarm. None of them ever ARMED a dormant fence and checked what
// the deployment was left holding — so the operation the whole unit exists to make possible rested
// on the refusals around it. A ceremony can refuse everything correctly and still leave the wrong
// durable state behind on the one path that succeeds.
//
// What is asserted is the state, not the wording: the mode, the generation MOVING, the enforcement
// being committed, and the operator's reason surviving into the record. Then `verify` is run against
// the same database, because "armed" that does not verify is the exact pair the second contrast
// caught going out of step.
func TestArmingAnUnarmedFenceLeavesItEnforcedAndVerifiable(t *testing.T) {
	ctx := context.Background()
	eng, dsn := bootFenceFile(t)
	rs := eng.store.(store.RolloutStater)

	// Model the un-armed deployment the way the rest of this file does: `legacy_compat` is a
	// CLASSIFICATION, not a transition target — the engine refuses it deliberately, because entering
	// it on purpose would grant every entitlement the deployment had before the control existed.
	// `policy_optional` is the reachable un-armed disposition, and it is the one an operator can
	// actually be sitting on when they decide to arm.
	cur, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: eventing.EgressWriterFenceControlKey, Mode: store.RolloutPolicyOptional,
		Actor: "test", Reason: "model a deployment that is not armed", ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("move the fence off enforced: %v", err)
	}
	unarmed, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read the un-armed state: %v", err)
	}
	_ = eng.Close()

	runCmd := func(cmd *cobra.Command, extra ...string) (string, error) {
		cmd.SetArgs(append([]string{
			"--data-dir", filepath.Dir(dsn), "--engine", "sqlite", "--dsn", dsn,
		}, extra...))
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		rerr := cmd.Execute()
		return out.String(), rerr
	}

	out, err := runCmd(newEventingFenceArmCmd(), "--reason", "CHG-77: every writer replaced", "--assert-writers-upgraded")
	if err != nil {
		t.Fatalf("arming an un-armed fence on a database that enforces failed:\n%s\nerr=%v", out, err)
	}

	eng2, err := boot(ctx, bootConfig{DataDir: filepath.Dir(dsn), Engine: "sqlite", DSN: dsn, Version: "test"})
	if err != nil {
		t.Fatalf("re-boot: %v", err)
	}
	defer func() { _ = eng2.Close() }()
	got, err := eng2.store.(store.RolloutStater).RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read state after arming: %v", err)
	}
	if got.CurrentMode != store.RolloutEnforced {
		t.Fatalf("after a successful arming the fence is %q, want %q", got.CurrentMode, store.RolloutEnforced)
	}
	if got.Generation <= unarmed.Generation {
		t.Fatalf("the generation did not move (%d -> %d): moving it is what invalidates a proof orphaned while the fence was un-armed, so an arming that leaves it put has not closed the gap", unarmed.Generation, got.Generation)
	}
	if !got.EnforcementCommitted {
		t.Fatal("the arming did not record enforcement as committed: a later run would read this deployment as never having armed")
	}
	if !strings.Contains(got.DecidedReason, "CHG-77") {
		t.Fatalf("the operator's reason did not survive into the durable record: got %q — the record is the evidence that a human decided this", got.DecidedReason)
	}
	if got.DecidedBy == "" {
		t.Fatal("the durable record names no decider: a reason with nobody attached is not evidence that a human decided this")
	}
	// ClassifiedMode is immutable: no transition rewrites it, so "how was this deployment
	// classified?" stays answerable after any number of operator decisions. The property is that it
	// is UNCHANGED, not that it differs from CurrentMode — on a fresh database both are `enforced`
	// from the start, and asserting inequality there fails for a reason that has nothing to do with
	// immutability.
	if got.ClassifiedMode != unarmed.ClassifiedMode {
		t.Fatalf("the arming rewrote ClassifiedMode from %q to %q: it is immutable on purpose, and overwriting it destroys the only record of how the engine first met this deployment", unarmed.ClassifiedMode, got.ClassifiedMode)
	}

	// And the ceremony's own verification agrees with the state it just wrote.
	if vout, verr := runCmd(newEventingFenceVerifyCmd()); verr != nil {
		t.Fatalf("`verify` failed on the database `arm` had just reported success for:\n%s\nerr=%v", vout, verr)
	}
}
