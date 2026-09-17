// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Caller tests for the R5 login capability. Every case drives a production function
// against a real store: newLoginCapabilityBoot, observeFollower, recordAtPromotion,
// installAndAssert, or boot/runEngine themselves. No classifier is restated here.

func loginCapStore(t *testing.T) store.Store {
	t.Helper()
	st, err := coreengine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true,
	}, func(store.ExtensionRegistry) error { return nil })
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(context.Background())
		return e
	}); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	return st
}

func loginCapEnv(value string) func(string) string {
	return func(k string) string {
		if k != envLoginEnforcement {
			return ""
		}
		return value
	}
}

// recordPresentComponent gives the deployment a real capability history by taking the
// production path a Wired artifact takes.
func recordPresentComponent(t *testing.T, st store.Store, version string) {
	t.Helper()
	b := loginCapabilityBoot{state: auth.LoginComponentWired, linked: true, artifactVersion: version}
	if err := b.recordAtPromotion(context.Background(), st); err != nil {
		t.Fatalf("seed capability history: %v", err)
	}
}

// setGlobalDemand stores a global/default federation config carrying the operator's
// require-SSO intent, which is the demand GlobalDefaultLoginDemand reads.
func setGlobalDemand(t *testing.T, st store.Store, requireSSO bool) {
	t.Helper()
	ctx := context.Background()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.FederationConfigs().Create(ctx, model.FederationConfig{
			TargetTenantID:         auth.GlobalFederationScope,
			Alias:                  model.DefaultFederationAlias,
			Protocol:               auth.ProtocolOIDC,
			Status:                 model.StatusActive,
			OIDCIssuer:             "https://idp.example",
			OIDCClientSecretSealed: "sealed-fixture-not-a-credential",
			RequireSSO:             requireSSO,
		})
		return e
	}); err != nil {
		t.Fatalf("store the global/default posture: %v", err)
	}
}

func readObservation(t *testing.T, st store.Store) store.LoginCapabilityObservation {
	t.Helper()
	ctx := context.Background()
	var obs store.LoginCapabilityObservation
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		var e error
		obs, e = as.LoginCapability().Read(ctx)
		return e
	}); err != nil {
		t.Fatalf("read the capability observation: %v", err)
	}
	return obs
}

// lastDurableAuditEvent returns the tail event AND the canonical metadata string the
// chain hash actually commits to. Walk alone drops Meta on every read path, so a test
// that asserted the in-memory map would be asserting the draft it just built rather
// than what the ledger stored.
func lastDurableAuditEvent(t *testing.T, st store.Store) (model.AuditEvent, map[string]any) {
	t.Helper()
	ctx := context.Background()
	var last model.AuditEvent
	var canonical string
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		w, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("this store cannot read stored canonical metadata; the durable record cannot be asserted")
		}
		return w.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			last, canonical = ev, meta
			return nil
		})
	}); err != nil {
		t.Fatalf("walk the audit chain: %v", err)
	}
	if last.Seq == 0 {
		t.Fatal("the audit chain holds no event")
	}
	meta := map[string]any{}
	if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
		t.Fatalf("stored canonical metadata %q is not readable: %v", canonical, err)
	}
	return last, meta
}

// TestLoginCapabilityClassifierIsTheAuthSlice pins the boot to the single shared
// classifier: the boot supplies two inputs and holds no second algorithm.
//
// It also asserts the clarified precedence on the one input where the two candidate
// orders differ. An explicit falsey host selection outranks an unlinked artifact, so the
// operator recovery is still recorded on a build that does not carry the component.
func TestLoginCapabilityClassifierIsTheAuthSlice(t *testing.T) {
	t.Parallel()
	linked := loginEnforcementComponentLinked()
	for _, value := range []string{"", "on", "1", "true", "off", "0", "false", "no", "disabled", "DISABLED", " Off "} {
		b := newLoginCapabilityBoot(loginCapEnv(value), "v-test")
		want := auth.ClassifyLoginComponent(linked, loginEnforcementDisabled(value))
		if b.state != want {
			t.Fatalf("newLoginCapabilityBoot(%q).state = %s, auth.ClassifyLoginComponent = %s: the boot must hold no second classifier", value, b.state, want)
		}
		if b.linked != linked {
			t.Fatalf("boot recorded linked=%t, the build predicate says %t", b.linked, linked)
		}
	}
	if got := auth.ClassifyLoginComponent(false, true); got != auth.LoginComponentDisabledByOperator {
		t.Errorf("auth.ClassifyLoginComponent(linked=false, disabled=true) = %s, want disabled_by_operator: the host selection outranks an unlinked artifact, so the operator recovery is still recorded", got)
	}
}

// TestLoginCapabilityFollowerPristineStagingStarts is the Community first-run path: a
// posture is configured and the component was never recorded. It must not refuse.
func TestLoginCapabilityFollowerPristineStagingStarts(t *testing.T) {
	t.Parallel()
	requireAbsentBuild(t)
	st := loginCapStore(t)
	setGlobalDemand(t, st, true)

	b := newLoginCapabilityBoot(loginCapEnv(""), "v-test")
	if err := b.observeFollower(context.Background(), st); err != nil {
		t.Fatalf("follower snapshot: %v", err)
	}
	if !b.snapshotTaken {
		t.Fatal("the absent follower took no snapshot")
	}
	if b.observedPresent {
		t.Fatal("pristine staging reported capability history")
	}
	if !b.demand {
		t.Fatal("the configured require-SSO posture was not read as a demand")
	}
	if b.refusesStartup() {
		t.Fatal("pristine Community staging with a configured posture was refused; the posture was never enforced there, so nothing was lost")
	}
	if err := b.recordAtPromotion(context.Background(), st); err != nil {
		t.Fatalf("promotion refused pristine staging: %v", err)
	}
}

// TestLoginCapabilityFollowerKnownHistoryAndDemandRefuses is the downgrade: this
// deployment recorded the component and a demand is configured.
func TestLoginCapabilityFollowerKnownHistoryAndDemandRefuses(t *testing.T) {
	t.Parallel()
	requireAbsentBuild(t)
	st := loginCapStore(t)
	recordPresentComponent(t, st, "v-previous")
	setGlobalDemand(t, st, true)

	b := newLoginCapabilityBoot(loginCapEnv(""), "v-test")
	if err := b.observeFollower(context.Background(), st); err != nil {
		t.Fatalf("follower snapshot: %v", err)
	}
	if !b.observedPresent || !b.demand {
		t.Fatalf("the snapshot missed a half: present=%t demand=%t", b.observedPresent, b.demand)
	}
	if !b.refusesStartup() {
		t.Fatal("known history plus a configured demand did not refuse")
	}
	if err := b.recordAtPromotion(context.Background(), st); !errors.Is(err, errLoginCapabilityRefused) {
		t.Fatalf("promotion accepted a downgrade: %v", err)
	}
}

// TestLoginCapabilityFollowerHistoryWithoutDemandStarts: a recorded component with no
// configured posture is not a downgrade.
func TestLoginCapabilityFollowerHistoryWithoutDemandStarts(t *testing.T) {
	t.Parallel()
	requireAbsentBuild(t)
	st := loginCapStore(t)
	recordPresentComponent(t, st, "v-previous")

	b := newLoginCapabilityBoot(loginCapEnv(""), "v-test")
	if err := b.observeFollower(context.Background(), st); err != nil {
		t.Fatalf("follower snapshot: %v", err)
	}
	if !b.observedPresent {
		t.Fatal("the seeded capability history was not read back")
	}
	if b.refusesStartup() {
		t.Fatal("a recorded component with no configured posture refused; there is no demand to fail")
	}
	if err := b.recordAtPromotion(context.Background(), st); err != nil {
		t.Fatalf("promotion refused without a demand: %v", err)
	}
}

// TestLoginCapabilitySnapshotErrorPropagates: a failed read is not an observation.
func TestLoginCapabilitySnapshotErrorPropagates(t *testing.T) {
	t.Parallel()
	requireAbsentBuild(t)
	st := loginCapStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	b := newLoginCapabilityBoot(loginCapEnv(""), "v-test")
	if err := b.observeFollower(context.Background(), st); err == nil {
		t.Fatal("a failed snapshot was reported as success")
	}
	if b.snapshotTaken || b.refusesStartup() {
		t.Fatalf("a failed read left an observation: %+v", b)
	}
}

// TestLoginCapabilityWiredObservesOncePerPromotion: each promotion records exactly one
// observation, carrying the artifact version.
func TestLoginCapabilityWiredObservesOncePerPromotion(t *testing.T) {
	t.Parallel()
	st := loginCapStore(t)
	b := loginCapabilityBoot{state: auth.LoginComponentWired, linked: true, artifactVersion: "v-wired"}
	if err := b.recordAtPromotion(context.Background(), st); err != nil {
		t.Fatalf("first promotion: %v", err)
	}
	first := readObservation(t, st)
	if !first.Present || first.LastArtifactVersion != "v-wired" {
		t.Fatalf("observation = %+v, want present with the artifact version", first)
	}
	if first.ObservationCount != 1 {
		t.Fatalf("one promotion produced %d observations", first.ObservationCount)
	}
	if err := b.recordAtPromotion(context.Background(), st); err != nil {
		t.Fatalf("second promotion: %v", err)
	}
	if got := readObservation(t, st); got.ObservationCount != 2 {
		t.Fatalf("two promotions produced %d observations; each promotion records exactly one", got.ObservationCount)
	}
}

// TestLoginCapabilityDisabledAppendsRecoveryAndTakesNoObservation asserts the durable
// recovery record by its STORED canonical metadata, and that a disabled artifact
// creates no capability observation.
func TestLoginCapabilityDisabledAppendsRecoveryAndTakesNoObservation(t *testing.T) {
	t.Parallel()
	st := loginCapStore(t)
	b := loginCapabilityBoot{state: auth.LoginComponentDisabledByOperator, linked: true, artifactVersion: "v-disabled"}
	if err := b.recordAtPromotion(context.Background(), st); err != nil {
		t.Fatalf("disabled promotion: %v", err)
	}
	if got := readObservation(t, st); got.Present || got.ObservationCount != 0 {
		t.Fatalf("a disabled artifact created a capability observation: %+v", got)
	}
	ev, meta := lastDurableAuditEvent(t, st)
	if ev.Action != auditLoginRecovery {
		t.Fatalf("recovery action = %q, want %q", ev.Action, auditLoginRecovery)
	}
	if ev.Actor != auditLoginRecoveryActor {
		t.Fatalf("recovery actor = %q, want %q", ev.Actor, auditLoginRecoveryActor)
	}
	if ev.ActorKind != model.ActorSystem {
		t.Fatalf("recovery actor kind = %q, want %q: the engine applied a host selection and attests no user identity", ev.ActorKind, model.ActorSystem)
	}
	// The record names a capability in its metadata, never a target entity: inventing
	// one would assert an object this event does not act on.
	if ev.TargetKind != "" {
		t.Errorf("recovery target kind = %q, want it unset", ev.TargetKind)
	}
	if ev.TargetID != "" {
		t.Errorf("recovery target id = %q, want it unset", ev.TargetID)
	}
	if ev.Seq <= 0 {
		t.Fatalf("recovery record sequence = %d, want a durable receipt", ev.Seq)
	}
	want := map[string]any{
		"capability":       auditLoginRecoveryCapability,
		"component_state":  auth.LoginComponentDisabledByOperator.String(),
		"component_linked": true,
		"artifact_version": "v-disabled",
	}
	for k, v := range want {
		if meta[k] != v {
			t.Errorf("stored recovery metadata %q = %#v, want %#v", k, meta[k], v)
		}
	}
	for k := range meta {
		if _, declared := want[k]; !declared {
			t.Errorf("the recovery record carries an undeclared key %q; no environment value belongs in it", k)
		}
	}
}

// TestLoginCapabilityPromotionFailurePropagates: a failed promotion transaction is
// never reported as a recorded one, in any declared state.
func TestLoginCapabilityPromotionFailurePropagates(t *testing.T) {
	t.Parallel()
	st := loginCapStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, b := range []loginCapabilityBoot{
		{state: auth.LoginComponentWired, linked: true, artifactVersion: "v"},
		{state: auth.LoginComponentDisabledByOperator, linked: true, artifactVersion: "v"},
		{state: auth.LoginComponentAbsent},
	} {
		if err := b.recordAtPromotion(context.Background(), st); err == nil {
			t.Errorf("%s: a failed promotion transaction reported success", b.state)
		}
	}
}

// TestLoginCapabilityUndeclaredStateRefusesPromotion: an unset state never records.
func TestLoginCapabilityUndeclaredStateRefusesPromotion(t *testing.T) {
	t.Parallel()
	st := loginCapStore(t)
	b := loginCapabilityBoot{state: auth.LoginComponentUnset}
	if err := b.recordAtPromotion(context.Background(), st); !errors.Is(err, errLoginCapabilityUndeclared) {
		t.Fatalf("an undeclared state was promoted: %v", err)
	}
	if got := readObservation(t, st); got.Present {
		t.Fatalf("an undeclared state recorded an observation: %+v", got)
	}
}

// requireAbsentBuild marks the cases that only exist for an artifact that does not
// link the component. This is a build-variant boundary, not a tolerated failure: the
// linked legs are qualified on the private matrix, which compiles the other predicate.
func requireAbsentBuild(t *testing.T) {
	t.Helper()
	if loginEnforcementComponentLinked() {
		t.Skip("this artifact links the login-enforcement component; the absent-follower cases belong to the build that does not")
	}
}

// --- one process per boot caller -----------------------------------------------------
//
// boot() constructs a whole artifact, and a commercial artifact installs PROCESS-scoped
// add-on entitlement license sources inside it. That installer refuses its second call
// in one process on purpose: two engines sharing one license pointer would let an
// unlicensed engine hand out a licensed one's add-ons. It is a production authorization
// invariant, so the boot callers below each take a process of their own rather than ask
// the invariant to bend. Nothing here resets, weakens or bypasses it.
//
// Measured: every case in this file passes when it is the only one in its process; the
// three absent-artifact boot callers abort the binary when they share one. The single
// boot caller that boots twice by design (the startup-rejection case) is unaffected,
// because its second boot refuses at the pre-election follower gate — hundreds of lines
// before the seat seam that installs the sources — and so never installs them.
//
// The isolation is UNCONDITIONAL, and that is deliberate:
//
//   - The open tree has no predicate for "this build installs process-scoped license
//     sources". loginEnforcementComponentLinked() is NOT one. Reading it as one is
//     exactly the conflation that let an add-on-free commercial cut abort this suite
//     while every case passed alone: there, the artifact does not link the login
//     component AND does install the process-scoped sources, and only the first half
//     was being asked about.
//   - A build-variant-dependent execution shape would leave the isolation unexercised
//     in the build most runs use, so it would rot where nobody looks.
//
// Each delegated case runs in a child started at exactly its own anchored name, under a
// bounded timeout, behind a marker that both opens the child's body and closes the loop:
// a process that already carries the marker runs the case in place and never spawns.

// envBootCallerChild names the single case its process was started to run. It is the
// only door into a delegated body and the only thing a second generation would need.
const envBootCallerChild = "OLIVARES_TEST_LOGIN_BOOT_CALLER_CHILD"

// bootCallerChildMaxBudget caps one delegated child run.
const bootCallerChildMaxBudget = 10 * time.Minute

// bootCallerChildDeadlineReserve is kept back from the test deadline so the parent can
// still report the child's outcome before the test binary's own timeout fires.
const bootCallerChildDeadlineReserve = 30 * time.Second

// bootCallerChildGrace is how far the parent's backstop outlives the child's own
// -test.timeout. The ordinary overrun is therefore the CHILD's, which prints a goroutine
// dump the parent relays; the context is only reached by a child that ignored its bound.
const bootCallerChildGrace = 30 * time.Second

var (
	// errBootCallerBudgetExhausted refuses a delegated run that would start with no
	// usable time left before the test deadline.
	errBootCallerBudgetExhausted = errors.New("BOOT_CALLER_BUDGET_EXHAUSTED: the test deadline leaves no time to run this case in its own process")
	// errBootCallerRecursion refuses the one shape this delegation must never take.
	errBootCallerRecursion = errors.New("BOOT_CALLER_RECURSION: an isolated child runs its case, it never starts another one")
)

// bootCallerChildBudget is the time one delegated child may use: min(cap, deadline - now
// - reserve). A deadline that leaves nothing refuses instead of starting a child that
// cannot finish.
func bootCallerChildBudget(now, deadline time.Time, hasDeadline bool) (time.Duration, error) {
	if !hasDeadline {
		return bootCallerChildMaxBudget, nil
	}
	remaining := deadline.Sub(now) - bootCallerChildDeadlineReserve
	if remaining <= 0 {
		return 0, fmt.Errorf("%w (deadline in %s, reserve %s)", errBootCallerBudgetExhausted, deadline.Sub(now), bootCallerChildDeadlineReserve)
	}
	return min(bootCallerChildMaxBudget, remaining), nil
}

// bootCallerRunPattern anchors every element of a test name so the child runs exactly
// that case and nothing else. An unanchored -test.run also selects every longer name,
// which is precisely how two boot callers would end up back in one process.
func bootCallerRunPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// bootCallerIsolated reports whether this process was started to run exactly name, or a
// subtest of it, and nothing else.
func bootCallerIsolated(name string) bool {
	marker := os.Getenv(envBootCallerChild)
	return marker != "" && (name == marker || strings.HasPrefix(name, marker+"/"))
}

// runBootCallerChild re-executes THIS test binary at exactly one case and returns the
// child's combined output together with its completion error. It grades nothing: what
// counts as a pass belongs to the caller, so a child that died for an unrelated reason
// can never be mistaken for a verdict about the case.
//
// selfBudget is the child's OWN -test.timeout, so an overrun reports itself with a
// goroutine dump. hardBudget is the parent's backstop for a child that ignores it.
func runBootCallerChild(name string, selfBudget, hardBudget time.Duration, extraEnv ...string) (string, error) {
	if marker := os.Getenv(envBootCallerChild); marker != "" {
		return "", fmt.Errorf("%w (this process was started for %q)", errBootCallerRecursion, marker)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hardBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run="+bootCallerRunPattern(name),
		"-test.count=1", "-test.v", "-test.timeout="+selfBudget.String())
	cmd.Env = append(os.Environ(),
		// TestMain turns this binary into the real CLI when this is "1". The child has
		// to be a test process, so it is cleared here rather than assumed absent.
		"OLIVARES_CLI_TRAMPOLINE=",
		envBootCallerChild+"="+name,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// bootCallerChildPassed reports whether a child's output carries the OWN pass of the
// case it was started for.
//
// The trailing space is the anchor and it is doing work: without it the pass of any
// LONGER name reads as this case's, which is the same defect as an unanchored
// -test.run, one step later. A child that skipped, failed or selected nothing carries
// no such line, and a delegated case that skips in the child but not in the parent is a
// contradiction rather than a pass.
func bootCallerChildPassed(text, name string) bool {
	return strings.Contains(text, "--- PASS: "+name+" ")
}

// isolateBootCaller gives the calling case a process of its own, and reports whether the
// caller must return without running its body.
//
// In the parent it runs the case in a child and requires that child's OWN PASS line: a
// child that exited 0 having selected nothing is not a pass. In the child it returns
// false and the body runs there, exactly once in that process.
func isolateBootCaller(t *testing.T) bool {
	t.Helper()
	if marker := os.Getenv(envBootCallerChild); marker != "" {
		if !bootCallerIsolated(t.Name()) {
			t.Fatalf("%s=%q but this process is running %s: the marker names the one case its child was started for",
				envBootCallerChild, marker, t.Name())
		}
		return false
	}
	deadline, hasDeadline := t.Deadline()
	budget, err := bootCallerChildBudget(time.Now(), deadline, hasDeadline)
	if err != nil {
		t.Fatalf("%s: %v", t.Name(), err)
	}
	text, runErr := runBootCallerChild(t.Name(), budget, budget+bootCallerChildGrace)
	passed := bootCallerChildPassed(text, t.Name())
	if runErr != nil || !passed {
		t.Fatalf("%s did not pass in its own process within %s (err %v, own PASS %t):\n--- child output ---\n%s--- end child output ---",
			t.Name(), budget, runErr, passed, text)
	}
	t.Logf("%s passed in a process of its own (budget %s): boot() installs process-scoped license sources a commercial artifact refuses to install twice",
		t.Name(), budget)
	return true
}

// --- boot integration ---------------------------------------------------------------

// bootLoginCapability boots a real sqlite engine on dataDir and returns the engine, the
// store boot actually used, and the exact promotion callback boot registered.
//
// It refuses to run outside a delegated process. That refusal is the forcing function:
// a case added later that calls boot() without isolateBootCaller fails here, naming the
// reason, instead of aborting the whole binary in a commercial cut that no open-tree
// gate runs.
func bootLoginCapability(t *testing.T, dataDir string) (*engine, store.Store, func(context.Context) error) {
	t.Helper()
	if !bootCallerIsolated(t.Name()) {
		t.Fatalf("%s calls boot() outside a delegated process: call isolateBootCaller(t) first. boot() installs process-scoped add-on entitlement sources that a commercial artifact refuses to install twice, so two boot callers in one test process abort the binary",
			t.Name())
	}
	var seen store.Store
	var captured func(context.Context) error
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dataDir, Engine: "sqlite", Version: "v-boot", ServeMode: true,
		pdpPromotionRegistered: func(fn func(context.Context) error) { captured = fn },
		pdpListOrgs: func(_ context.Context, st store.Store) ([]model.Org, error) {
			seen = st
			return nil, nil
		},
	})
	if err != nil || eng == nil {
		t.Fatalf("boot = engine:%v err:%v", eng, err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if seen == nil || captured == nil {
		t.Fatalf("boot exposed store:%t promotion:%t", seen != nil, captured != nil)
	}
	return eng, seen, captured
}

// TestLoginCapabilityBootPromotesPristineStaging: the production boot classifies,
// snapshots and runs its own promotion callback on a first-run deployment, and an
// absent artifact records no observation there.
func TestLoginCapabilityBootPromotesPristineStaging(t *testing.T) {
	// The build-variant gate is answered first, so a build this case does not belong to
	// records its skip here instead of paying for a child that would skip too.
	requireAbsentBuild(t)
	if isolateBootCaller(t) {
		return
	}
	t.Setenv(envLoginEnforcement, "")
	_, st, _ := bootLoginCapability(t, t.TempDir())
	if got := readObservation(t, st); got.Present || got.ObservationCount != 0 {
		t.Fatalf("an absent artifact recorded a capability observation at promotion: %+v", got)
	}
}

// TestLoginCapabilityBootRecordsTheLinkedArtifact is the linked leg of the same boot
// path: an artifact that carries the component records exactly one observation per
// promotion, carrying the artifact version, and the declared state agrees with the
// policy the enterprise wiring actually installs. It runs only where the predicate is
// true, which is the private matrix.
func TestLoginCapabilityBootRecordsTheLinkedArtifact(t *testing.T) {
	if !loginEnforcementComponentLinked() {
		t.Skip("this artifact does not link the login-enforcement component; the linked leg belongs to the build that does")
	}
	if isolateBootCaller(t) {
		return
	}
	t.Setenv(envLoginEnforcement, "")
	_, st, promote := bootLoginCapability(t, t.TempDir())
	first := readObservation(t, st)
	if !first.Present || first.LastArtifactVersion != "v-boot" {
		t.Fatalf("observation after promotion = %+v, want present with this artifact version", first)
	}
	if first.ObservationCount != 1 {
		t.Fatalf("one promotion produced %d observations", first.ObservationCount)
	}
	if err := promote(context.Background()); err != nil {
		t.Fatalf("later promotion of a linked artifact: %v", err)
	}
	if got := readObservation(t, st); got.ObservationCount != 2 {
		t.Fatalf("two promotions produced %d observations", got.ObservationCount)
	}
}

// TestLoginCapabilityLaterPromotionRefusalKeepsTheFollowerServing drives the exact
// callback boot registered. A refusal there rejects one acquisition; it must not close
// the store, because the elector will call it again and the node keeps following.
func TestLoginCapabilityLaterPromotionRefusalKeepsTheFollowerServing(t *testing.T) {
	requireAbsentBuild(t)
	if isolateBootCaller(t) {
		return
	}
	t.Setenv(envLoginEnforcement, "")
	ctx := context.Background()
	_, st, promote := bootLoginCapability(t, t.TempDir())

	// An active peer that carries the component records it while this node follows,
	// and an operator configures the demand.
	recordPresentComponent(t, st, "v-peer")
	setGlobalDemand(t, st, true)

	err := promote(ctx)
	if !errors.Is(err, errLoginCapabilityRefused) {
		t.Fatalf("later promotion = %v, want the capability refusal", err)
	}
	if err := st.System(ctx, func(store.SystemScope) error { return nil }); err != nil {
		t.Fatalf("a refused later promotion closed the follower store: %v", err)
	}
	if err := st.AuthView(ctx, func(store.AuthScope) error { return nil }); err != nil {
		t.Fatalf("a refused later promotion left the follower unable to read: %v", err)
	}
}

// TestLoginCapabilityStartupRejectionBindsNoListener: when the follower snapshot
// refuses, runEngine returns the refusal before any serve listener is acquired and
// before the console announcement, so a refused node never serves.
func TestLoginCapabilityStartupRejectionBindsNoListener(t *testing.T) {
	requireAbsentBuild(t)
	// This case boots TWICE by design: once to seed the durable facts and once through
	// runEngine. Both stay inside the one delegated process, because the second boot
	// refuses at the pre-election follower gate and never reaches the seat seam that
	// installs the process-scoped sources.
	if isolateBootCaller(t) {
		return
	}
	t.Setenv(envLoginEnforcement, "")
	dataDir := t.TempDir()

	// Seed the same durable facts a downgraded deployment holds, through the
	// production promotion path, then shut that engine down.
	eng, st, _ := bootLoginCapability(t, dataDir)
	recordPresentComponent(t, st, "v-peer")
	setGlobalDemand(t, st, true)
	if err := eng.Close(); err != nil {
		t.Fatalf("close the seeding engine: %v", err)
	}

	opts := bindAnnounceInsecureOpts(dataDir, bindAnnounceFreeAddr(t), bindAnnounceFreeAddr(t))
	binds, announcements := 0, 0
	opts.bindListener = func(ctx context.Context, addr string, reuse bool) (net.Listener, error) {
		binds++
		return bindServeListener(ctx, addr, reuse)
	}
	err := runEngine(context.Background(), io.Discard, opts, func(context.Context, io.Writer, *engine, consoleAddress) error {
		announcements++
		return nil
	})
	if err == nil {
		t.Fatal("a downgraded deployment started")
	}
	if !strings.Contains(err.Error(), "login enforcement") {
		t.Fatalf("startup error = %v, want the login-enforcement refusal", err)
	}
	if binds != 0 {
		t.Errorf("%d serve listeners were acquired on a refused startup, want none", binds)
	}
	if announcements != 0 {
		t.Errorf("%d console announcements ran on a refused startup, want none", announcements)
	}
}

// TestLoginCapabilityInstallAssertsAgainstTheWiredPolicy: the declared state and the
// policy actually wired must agree before any surface is exposed. A node whose own
// record contradicts what it serves must not reach API construction.
func TestLoginCapabilityInstallAssertsAgainstTheWiredPolicy(t *testing.T) {
	t.Parallel()
	st := loginCapStore(t)
	authn := auth.NewAuthenticator(st, nil)
	fed := auth.NewFederationService(st, nil, newFederationBuilder(), newFederation(loginCapEnv(""), slog.Default()), newFederationMultiIDP())

	// No login policy is wired here, so a Wired declaration contradicts the artifact.
	wired := loginCapabilityBoot{state: auth.LoginComponentWired, linked: true}
	if err := wired.installAndAssert(authn, fed); !errors.Is(err, auth.ErrLoginComponentState) {
		t.Fatalf("install accepted a Wired declaration with no login policy: %v", err)
	}
	// An undeclared state is never installed.
	unset := loginCapabilityBoot{state: auth.LoginComponentUnset}
	if err := unset.installAndAssert(authn, fed); !errors.Is(err, auth.ErrLoginComponentState) {
		t.Fatalf("install accepted an undeclared state: %v", err)
	}
	// Both services are required: installing on one half would leave them disagreeing.
	if err := wired.installAndAssert(authn, nil); !errors.Is(err, auth.ErrLoginComponentState) {
		t.Fatalf("install accepted a nil federation service: %v", err)
	}
	// The declaration this artifact actually makes installs.
	absent := loginCapabilityBoot{state: auth.LoginComponentAbsent}
	if err := absent.installAndAssert(authn, fed); err != nil {
		t.Fatalf("install refused the state this artifact declares: %v", err)
	}
}

// --- durable-receipt controls -------------------------------------------------------
//
// Two separate behaviours have to be observable, and a test that only reads a positive
// sequence back proves neither: an Append that FAILS, and an Append that SUCCEEDS but
// returns the store's explicit degrade answer (a zero-valued event with a nil error,
// meaning the evidence was dropped). The wrappers below are narrow: they delegate to the
// REAL store and the REAL audit port and change exactly one return value, so the
// transaction, the capability lock and the ledger write are all the production ones.

type auditFaultStore struct {
	store.Store
	fault func(store.AuditLog) store.AuditLog
}

func (s auditFaultStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(auditFaultScope{AuthScope: as, audit: s.fault(as.Audit())})
	})
}

type auditFaultScope struct {
	store.AuthScope
	audit store.AuditLog
}

func (s auditFaultScope) Audit() store.AuditLog { return s.audit }

// failingAuditLog refuses the append. Nothing is written.
type failingAuditLog struct {
	store.AuditLog
	err   error
	calls *int
}

func (l failingAuditLog) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	*l.calls++
	return model.AuditEvent{}, l.err
}

// droppedReceiptAuditLog performs the REAL append and then hands back the impossible
// receipt. The row therefore exists inside the transaction, so a chain that holds no
// recovery event afterwards is proof the caller rolled the transaction back rather than
// proof that nothing was ever attempted.
type droppedReceiptAuditLog struct {
	store.AuditLog
	calls *int
}

func (l droppedReceiptAuditLog) Append(ctx context.Context, d model.AuditDraft) (model.AuditEvent, error) {
	*l.calls++
	if _, err := l.AuditLog.Append(ctx, d); err != nil {
		return model.AuditEvent{}, err
	}
	return model.AuditEvent{}, nil
}

// countRecoveryEvents walks the durable chain and counts the operator-recovery records.
func countRecoveryEvents(t *testing.T, st store.Store) int {
	t.Helper()
	ctx := context.Background()
	n := 0
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == auditLoginRecovery {
				n++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk the audit chain: %v", err)
	}
	return n
}

// TestLoginCapabilityRecoveryAppendFailureRefusesAndRollsBack: the append fails, the
// promotion fails with that error, and nothing is left in the ledger.
func TestLoginCapabilityRecoveryAppendFailureRefusesAndRollsBack(t *testing.T) {
	t.Parallel()
	st := loginCapStore(t)
	injected := errors.New("audit sink refused the recovery record")
	calls := 0
	faulted := auditFaultStore{Store: st, fault: func(real store.AuditLog) store.AuditLog {
		return failingAuditLog{AuditLog: real, err: injected, calls: &calls}
	}}

	b := loginCapabilityBoot{state: auth.LoginComponentDisabledByOperator, linked: true, artifactVersion: "v-disabled"}
	if err := b.recordAtPromotion(context.Background(), faulted); !errors.Is(err, injected) {
		t.Fatalf("promotion = %v, want the append failure propagated", err)
	}
	if calls != 1 {
		t.Fatalf("the recovery append ran %d times, want exactly one attempt", calls)
	}
	if got := countRecoveryEvents(t, st); got != 0 {
		t.Fatalf("%d recovery records survived a failed append", got)
	}
	if got := readObservation(t, st); got.Present || got.ObservationCount != 0 {
		t.Fatalf("a failed recovery append left a capability observation: %+v", got)
	}
}

// TestLoginCapabilityRecoveryZeroReceiptRefusesAndRollsBack: the append succeeds and the
// store returns its explicit degrade answer. The promotion must refuse, and the row the
// real port wrote must not survive the rollback.
func TestLoginCapabilityRecoveryZeroReceiptRefusesAndRollsBack(t *testing.T) {
	t.Parallel()
	st := loginCapStore(t)
	calls := 0
	faulted := auditFaultStore{Store: st, fault: func(real store.AuditLog) store.AuditLog {
		return droppedReceiptAuditLog{AuditLog: real, calls: &calls}
	}}

	b := loginCapabilityBoot{state: auth.LoginComponentDisabledByOperator, linked: true, artifactVersion: "v-disabled"}
	err := b.recordAtPromotion(context.Background(), faulted)
	if !errors.Is(err, errLoginCapabilityAudit) {
		t.Fatalf("promotion = %v, want the dropped-receipt refusal", err)
	}
	if calls != 1 {
		t.Fatalf("the recovery append ran %d times, want exactly one attempt", calls)
	}
	if got := countRecoveryEvents(t, st); got != 0 {
		t.Fatalf("%d recovery records survived a refused promotion; the real append was not rolled back", got)
	}
	if got := readObservation(t, st); got.Present || got.ObservationCount != 0 {
		t.Fatalf("a dropped recovery receipt left a capability observation: %+v", got)
	}

	// The control positive: the same store, unfaulted, does persist the record, so the
	// two assertions above are about the rollback and not about a store that never writes.
	if err := b.recordAtPromotion(context.Background(), st); err != nil {
		t.Fatalf("the unfaulted promotion refused: %v", err)
	}
	if got := countRecoveryEvents(t, st); got != 1 {
		t.Fatalf("the unfaulted promotion left %d recovery records, want 1", got)
	}
}

// --- isolation controls ---------------------------------------------------------------
//
// The delegation above is only worth having if a child that FAILS, a child that OUTRUNS
// its budget and a child that would start a second generation are each reported as such.
// An isolation helper that swallowed a child's outcome would look exactly like one that
// works, and every delegated case would report a pass nobody ran.

// envBootCallerControl is the only door into the two child helpers below.
const envBootCallerControl = "OLIVARES_TEST_LOGIN_BOOT_CALLER_CONTROL"

// bootCallerControlDiagnostic is what a deliberately failing control child prints, so
// the parent grades the child's own words rather than the mere absence of a PASS line.
const bootCallerControlDiagnostic = "this control child failed on purpose"

// TestLoginCapabilityBootCallerChildFails is a helper, not a case: without its own
// marker it has nothing to do and skips. It exists so the propagation control has a
// child that really fails.
func TestLoginCapabilityBootCallerChildFails(t *testing.T) {
	if os.Getenv(envBootCallerControl) != "fail" {
		t.Skipf("child helper: reached only through %s, which the propagation control sets", envBootCallerControl)
	}
	t.Fatal(bootCallerControlDiagnostic)
}

// TestLoginCapabilityBootCallerChildOverrunsItsBudget is the same kind of helper for the
// timeout paths: it outlives any budget a control gives it, and the control gives it a
// small one, so no run pays this constant.
func TestLoginCapabilityBootCallerChildOverrunsItsBudget(t *testing.T) {
	if os.Getenv(envBootCallerControl) != "overrun" {
		t.Skipf("child helper: reached only through %s, which the propagation control sets", envBootCallerControl)
	}
	time.Sleep(bootCallerChildMaxBudget)
}

// TestLoginCapabilityBootCallerRunPatternSelectsExactlyOneCase pins the anchoring. An
// unanchored -test.run selects every longer name too, and two boot callers in one child
// is the defect this whole section exists to prevent.
func TestLoginCapabilityBootCallerRunPatternSelectsExactlyOneCase(t *testing.T) {
	t.Parallel()
	if got := bootCallerRunPattern("TestLoginCapabilityBootPromotesPristineStaging"); got != "^TestLoginCapabilityBootPromotesPristineStaging$" {
		t.Fatalf("pattern = %q", got)
	}
	if got := bootCallerRunPattern("TestA/v1.2+x"); got != `^TestA$/^v1\.2\+x$` {
		t.Fatalf("subtest pattern = %q: every element is anchored and quoted", got)
	}
	// The real boot callers, against each other and against longer names built from them.
	callers := []string{
		"TestLoginCapabilityBootPromotesPristineStaging",
		"TestLoginCapabilityBootRecordsTheLinkedArtifact",
		"TestLoginCapabilityLaterPromotionRefusalKeepsTheFollowerServing",
		"TestLoginCapabilityStartupRejectionBindsNoListener",
	}
	candidates := append([]string{}, callers...)
	for _, c := range callers {
		candidates = append(candidates, c+"Extra", "Extra"+c)
	}
	for _, want := range callers {
		re := regexp.MustCompile(bootCallerRunPattern(want))
		selected := []string{}
		for _, c := range candidates {
			if re.MatchString(c) {
				selected = append(selected, c)
			}
		}
		if len(selected) != 1 || selected[0] != want {
			t.Fatalf("the pattern for %s selects %v, want exactly [%s]", want, selected, want)
		}
	}
}

// TestLoginCapabilityBootCallerChildBudget pins the arithmetic that bounds a child,
// including the refusal: a deadline with nothing left must not start a child that cannot
// finish and would then be graded as a failure of the case.
func TestLoginCapabilityBootCallerChildBudget(t *testing.T) {
	t.Parallel()
	now := time.Now()
	if got, err := bootCallerChildBudget(now, time.Time{}, false); err != nil || got != bootCallerChildMaxBudget {
		t.Fatalf("no deadline = (%s, %v), want the cap %s", got, err, bootCallerChildMaxBudget)
	}
	if got, err := bootCallerChildBudget(now, now.Add(time.Hour), true); err != nil || got != bootCallerChildMaxBudget {
		t.Fatalf("a distant deadline = (%s, %v), want the cap %s", got, err, bootCallerChildMaxBudget)
	}
	want := 2*time.Minute - bootCallerChildDeadlineReserve
	if got, err := bootCallerChildBudget(now, now.Add(2*time.Minute), true); err != nil || got != want {
		t.Fatalf("a near deadline = (%s, %v), want %s", got, err, want)
	}
	if got, err := bootCallerChildBudget(now, now.Add(bootCallerChildDeadlineReserve), true); !errors.Is(err, errBootCallerBudgetExhausted) {
		t.Fatalf("an exhausted deadline = (%s, %v), want the refusal", got, err)
	}
}

// TestLoginCapabilityBootCallerGradesTheChildsOwnPass pins what the parent accepts as a
// delegated pass. Every false case here is one a real child can produce.
func TestLoginCapabilityBootCallerGradesTheChildsOwnPass(t *testing.T) {
	t.Parallel()
	const name = "TestLoginCapabilityBootPromotesPristineStaging"
	for _, ok := range []string{
		"=== RUN   " + name + "\n--- PASS: " + name + " (1.21s)\nPASS\n",
		"--- PASS: " + name + " (0.00s)\n",
	} {
		if !bootCallerChildPassed(ok, name) {
			t.Fatalf("a child that passed was not graded as one:\n%s", ok)
		}
	}
	for _, bad := range []string{
		"",
		"--- SKIP: " + name + " (0.00s)\n",
		"--- FAIL: " + name + " (1.21s)\n",
		"testing: warning: no tests to run\nPASS\n",
		// The longer-name hazard, one step later than the -test.run anchoring.
		"--- PASS: " + name + "Extra (0.01s)\n",
		// A neighbour's pass is not this case's.
		"--- PASS: TestLoginCapabilityBootRecordsTheLinkedArtifact (0.01s)\n",
	} {
		if bootCallerChildPassed(bad, name) {
			t.Fatalf("a child that did not pass %s was graded as one:\n%s", name, bad)
		}
	}
}

// TestLoginCapabilityBootCallerPropagatesChildFailureAndTimeout drives the three ways a
// delegated run can fail to be a pass, plus the one shape it must never take.
func TestLoginCapabilityBootCallerPropagatesChildFailureAndTimeout(t *testing.T) {
	t.Run("a failing child carries its own diagnostic back", func(t *testing.T) {
		text, err := runBootCallerChild("TestLoginCapabilityBootCallerChildFails",
			time.Minute, time.Minute+bootCallerChildGrace, envBootCallerControl+"=fail")
		if err == nil {
			t.Fatalf("a failing child was reported as a completed run:\n%s", text)
		}
		if !strings.Contains(text, bootCallerControlDiagnostic) {
			t.Fatalf("the child's own diagnostic did not reach the parent:\n%s", text)
		}
		if strings.Contains(text, "--- PASS: TestLoginCapabilityBootCallerChildFails ") {
			t.Fatalf("a failing child produced the PASS line the delegation grades on:\n%s", text)
		}
	})

	t.Run("the child's own budget ends it and says so", func(t *testing.T) {
		text, err := runBootCallerChild("TestLoginCapabilityBootCallerChildOverrunsItsBudget",
			2*time.Second, time.Minute, envBootCallerControl+"=overrun")
		if err == nil {
			t.Fatalf("a child that outran its budget was reported as a completed run:\n%s", text)
		}
		if !strings.Contains(text, "test timed out after 2s") {
			t.Fatalf("the child's own timeout did not report itself, so the parent has no dump to relay:\n%s", text)
		}
	})

	t.Run("the parent backstop ends a child that outlives its own bound", func(t *testing.T) {
		started := time.Now()
		text, err := runBootCallerChild("TestLoginCapabilityBootCallerChildOverrunsItsBudget",
			bootCallerChildMaxBudget, 2*time.Second, envBootCallerControl+"=overrun")
		elapsed := time.Since(started)
		if err == nil {
			t.Fatalf("a child the backstop had to end was reported as a completed run:\n%s", text)
		}
		if elapsed > time.Minute {
			t.Fatalf("the backstop took %s to end a child whose own bound was %s", elapsed, bootCallerChildMaxBudget)
		}
	})

	t.Run("an isolated process never starts a second generation", func(t *testing.T) {
		t.Setenv(envBootCallerChild, "TestSomeCaseAlreadyDelegated")
		text, err := runBootCallerChild("TestLoginCapabilityBootCallerChildFails",
			time.Second, time.Second, envBootCallerControl+"=fail")
		if !errors.Is(err, errBootCallerRecursion) {
			t.Fatalf("a child was allowed to start another child: err=%v\n%s", err, text)
		}
		if text != "" {
			t.Fatalf("the refused generation still produced output:\n%s", text)
		}
	})
}
