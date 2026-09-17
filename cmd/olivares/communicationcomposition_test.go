// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// writeCommunicationCustodyForTest writes a plaintext content keyring and a
// cursor keyring with the strict permissions the loaders demand. The KEK
// custody path is exercised by communicationsealerboot_test.go; this file
// measures composition, not envelope decoding.
func writeCommunicationCustodyForTest(t *testing.T) (contentPath, cursorPath string) {
	t.Helper()
	dir := t.TempDir()
	contentPath = filepath.Join(dir, "content-keyring.json")
	if err := os.WriteFile(contentPath, communicationContentTestKeyring(t, "seal-v1", "digest-v1",
		communicationContentTestRoot{"seal-v1", communicationContentTestRootBytes(0x41)},
		communicationContentTestRoot{"digest-v1", communicationContentTestRootBytes(0x42)},
	), 0o600); err != nil {
		t.Fatal(err)
	}
	cursorPath = filepath.Join(dir, "cursor-keyring.json")
	if err := os.WriteFile(cursorPath, cursorKeyringDocument("cursor-k1",
		cursorKeyringEntry("cursor-k1", 0x51, time.Time{}),
	), 0o600); err != nil {
		t.Fatal(err)
	}
	return contentPath, cursorPath
}

func bootForComposition(t *testing.T, dataDir string) *engine {
	t.Helper()
	// These fixtures restart only after closing the prior engine. An edition
	// can reset its test process state without weakening its production guards.
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dataDir, Engine: "sqlite", Version: "test", NoIngest: true,
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	return eng
}

func TestBootWithoutActivationBindsRealCompositionAndKeepsK3Off(t *testing.T) {
	t.Setenv(envKeyWrap, "")
	t.Setenv(envCommunicationActivation, "")
	t.Setenv(envCommunicationContentKeyringFile, "")
	t.Setenv(envCommunicationCursorKeyringFile, "")
	eng := bootForComposition(t, t.TempDir())
	defer eng.Close() //nolint:errcheck
	readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(context.Background())
	if err != nil {
		t.Fatalf("readiness: %v", err)
	}
	if eng.sessionsMod.CommunicationSessionCredentialsEnabled() || readiness.Effective ||
		readiness.Components.PumpReady || !readiness.Components.ResolverReady || !readiness.Components.PermissionsReady {
		t.Fatalf("activation-off readiness = %+v enabled=%t", readiness,
			eng.sessionsMod.CommunicationSessionCredentialsEnabled())
	}
	if eng.communicationPump == nil || eng.communicationPump.communication != nil {
		t.Fatal("activation-off boot did not register the pump without a K3 witness")
	}
	if eng.sessionsMod.CommunicationCursorTokenKeyringBound() {
		t.Fatal("no cursor keyring was configured, yet one is bound")
	}
}

// bootstrapCompositionTenant creates a real tenant, its bootstrapped owner and
// the default workspace through the booted engine, and returns the trusted
// internal principal the lot A acceptance uses for Apply.
func bootstrapCompositionTenant(t *testing.T, eng *engine, slug string) (model.TenantID, model.ID, model.ID, sessions.WorkPrincipal) {
	t.Helper()
	ctx := context.Background()
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	user, _, err := eng.authr.BootstrapSuperadminOwning(ctx, "owner@"+slug+".test", "k3-acceptance-password", tenant)
	if err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}
	var workspace model.ID
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	principal := sessions.WorkPrincipal{
		ActorKind: model.ActorUser, ActorRef: user.ID.String(), Actor: "user:" + user.ID.String(), Admin: true,
	}
	return tenant, user.ID, workspace, principal
}

// applyCompositionCreate runs the production Apply service for one item.create
// command: a normal K1 command whose post-commit nudge is a production drain.
func applyCompositionCreate(
	t *testing.T, eng *engine, tenant model.TenantID, principal sessions.WorkPrincipal, workspace, owner model.ID, title string,
) sessions.CommandResult {
	t.Helper()
	created, err := eng.sessionsMod.Apply(context.Background(), tenant, principal, sessions.WorkCommand{
		Command: "item.create", WorkspaceID: workspace, WorkKind: "implementation", Title: title,
		BriefMD: "A K1 command must keep flowing while K3 is held.", ContextRefs: []sessions.ContextRef{}, Priority: "p1",
		OwnerKind: "user", OwnerRef: owner.String(), ProvenanceKind: "human", ProvenanceRef: "test:k3-authority",
		Acceptance:     []sessions.AcceptanceInput{{Key: "hold", Ordinal: 0, Statement: "K3 stays held", Required: true}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost, CommandScope: "POST /work-items",
	})
	if err != nil {
		t.Fatalf("real K1 Apply %q: %v", title, err)
	}
	return created
}

// assertOutboxPublished is the K1/K2 progress witness: the row settled as
// published with its claim cleared and exactly one durable capture.
func assertOutboxPublished(t *testing.T, eng *engine, tenant model.TenantID, eventID model.ID, what string) {
	t.Helper()
	row := outboxRowByEvent(t, eng.store, tenant, eventID)
	if row.String("state") != "published" || row.String("claim_owner") != "" {
		t.Fatalf("%s: expected published, got %v", what, row)
	}
	if n := eventingCaptureCount(t, eng.store, tenant, eventID); n != 1 {
		t.Fatalf("%s: captures = %d, want 1", what, n)
	}
}

// assertOutboxHeld is the zero-effect witness for a K3 row: never claimed
// (pending, zero attempts, no claim owner) and never captured.
func assertOutboxHeld(t *testing.T, eng *engine, tenant model.TenantID, eventID model.ID, what string) {
	t.Helper()
	row := outboxRowByEvent(t, eng.store, tenant, eventID)
	if row.String("state") != "pending" || row.Int("attempts") != 0 || row.String("claim_owner") != "" {
		t.Fatalf("%s: K3 row was touched: state=%s attempts=%d owner=%q", what,
			row.String("state"), row.Int("attempts"), row.String("claim_owner"))
	}
	if n := eventingCaptureCount(t, eng.store, tenant, eventID); n != 0 {
		t.Fatalf("%s: captures for held K3 event = %d, want 0", what, n)
	}
}

func mustCompositionReadiness(t *testing.T, eng *engine) sessions.CommunicationReadiness {
	t.Helper()
	readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(context.Background())
	if err != nil {
		t.Fatalf("readiness: %v", err)
	}
	return readiness
}

func readinessMissing(readiness sessions.CommunicationReadiness, dependency sessions.CommunicationReadinessDependency) bool {
	for _, missing := range readiness.Missing {
		if missing == dependency {
			return true
		}
	}
	return false
}

// TestBootActivationRequestedWithoutCustodyHoldsK3AndServesK1 is the R3
// correction of the independent review: custody that belongs to K3 alone
// being absent, inaccessible or unusable keeps K3 OFF with a specific visible
// cause while core, K1 and K2 boot and serve. The requested posture is
// retained (credentials enabled, issuance decided by non-effective readiness),
// the K3 lane is not composed, no key is minted and no durable row is touched.
// Invalid activation syntax stays a configuration error.
func TestBootActivationRequestedWithoutCustodyHoldsK3AndServesK1(t *testing.T) {
	t.Setenv(envKeyWrap, "")
	t.Setenv(envCommunicationActivation, "on")
	t.Setenv(envCommunicationContentKeyringFile, "")
	t.Setenv(envCommunicationCursorKeyringFile, "")
	ctx := context.Background()
	dir := t.TempDir()

	// Missing custody: neither keyring declared.
	eng := bootForComposition(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "olivares.db")); err != nil {
		t.Fatalf("degraded boot did not open the estate: %v", err)
	}
	if !eng.sessionsMod.CommunicationSessionCredentialsEnabled() {
		t.Fatal("requested activation was silently downgraded to work-only")
	}
	if eng.communicationPump == nil || eng.communicationPump.communication != nil || eng.communicationPump.authority == nil {
		t.Fatal("K3 lane was composed without custody, or the authority is unbound")
	}
	if !eng.sessionsMod.WorkOutboxClaimAuthorityBound() {
		t.Fatal("outbox authority not bound on the module")
	}
	_, blockers := eng.communicationPump.authority.lane()
	if len(blockers) != 2 || !strings.Contains(blockers[0], envCommunicationContentKeyringFile) ||
		!strings.Contains(blockers[1], envCommunicationCursorKeyringFile) {
		t.Fatalf("custody blockers = %q, want both keyrings named", blockers)
	}
	if verdict := eng.communicationPump.authority.current(ctx); verdict.allow ||
		!strings.HasPrefix(verdict.reason, "custody_unavailable: ") ||
		!strings.Contains(verdict.reason, envCommunicationCursorKeyringFile) {
		t.Fatalf("authority verdict without custody = %+v", verdict)
	}
	readiness := mustCompositionReadiness(t, eng)
	if readiness.Effective || readiness.Components.PumpReady || readiness.Components.SealerReady ||
		!readinessMissing(readiness, sessions.CommunicationReadinessSealer) ||
		!readinessMissing(readiness, sessions.CommunicationReadinessPump) {
		t.Fatalf("readiness without custody = %+v, want non-effective with sealer and pump missing", readiness)
	}
	if eng.sessionsMod.CommunicationCursorTokenKeyringBound() {
		t.Fatal("no cursor custody, yet a cursor keyring is bound")
	}
	// Core and K1 serve: a real tenant, owner and item through Apply; the K1
	// event publishes through the real sink. A K3 row on the same estate is
	// held by every production path.
	tenant, owner, workspace, principal := bootstrapCompositionTenant(t, eng, "k3-nocustody")
	item := applyCompositionCreate(t, eng, tenant, principal, workspace, owner, "K1 without K3 custody")
	assertOutboxPublished(t, eng, tenant, item.EventID, "K1 create on degraded boot")
	heldK3 := insertOutboxEvent(t, eng.store, tenant, workspace, item.ResultID, 2, "work.handoff.offered")
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick without custody: %v", err)
	}
	assertOutboxHeld(t, eng, tenant, heldK3, "after pump tick without custody")
	second := applyCompositionCreate(t, eng, tenant, principal, workspace, owner, "second K1 without K3 custody")
	assertOutboxPublished(t, eng, tenant, second.EventID, "second K1 create on degraded boot")
	assertOutboxHeld(t, eng, tenant, heldK3, "after K1 Apply nudge without custody")
	if err := eng.sessionsMod.DrainWorkOutbox(ctx, tenant, 100); err != nil {
		t.Fatalf("public drain without custody: %v", err)
	}
	assertOutboxHeld(t, eng, tenant, heldK3, "after public drain without custody")
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	// Unusable custody on the SAME estate: a content keyring that is not a
	// keyring and a cursor keyring with world-readable permissions. Both are
	// declared, both fail their strict loaders, neither is worked around.
	contentPath, cursorPath := writeCommunicationCustodyForTest(t)
	if err := os.WriteFile(contentPath, []byte("{\"not\": \"a keyring\""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cursorPath, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envCommunicationContentKeyringFile, contentPath)
	t.Setenv(envCommunicationCursorKeyringFile, cursorPath)
	eng = bootForComposition(t, dir)
	_, blockers = eng.communicationPump.authority.lane()
	if len(blockers) != 2 || !strings.Contains(blockers[0], envCommunicationContentKeyringFile) ||
		!strings.Contains(blockers[1], envCommunicationCursorKeyringFile) || !strings.Contains(blockers[1], "0644") {
		t.Fatalf("unusable custody blockers = %q, want both loaders' causes", blockers)
	}
	if eng.communicationPump.communication != nil || eng.sessionsMod.CommunicationCursorTokenKeyringBound() {
		t.Fatal("unusable custody composed the K3 lane or bound the cursor keyring")
	}
	if readiness := mustCompositionReadiness(t, eng); readiness.Effective || readiness.Components.SealerReady {
		t.Fatalf("readiness with unusable custody = %+v", readiness)
	}
	// Durable data preserved and K1 still serves; the earlier K3 row is still
	// exactly as it was.
	assertOutboxHeld(t, eng, tenant, heldK3, "after reboot with unusable custody")
	third := applyCompositionCreate(t, eng, tenant, principal, workspace, owner, "K1 with unusable K3 custody")
	assertOutboxPublished(t, eng, tenant, third.EventID, "K1 create with unusable custody")
	assertOutboxHeld(t, eng, tenant, heldK3, "after K1 Apply with unusable custody")
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick with unusable custody: %v", err)
	}
	assertOutboxHeld(t, eng, tenant, heldK3, "after pump tick with unusable custody")
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	// Invalid activation syntax remains a configuration error, before the store.
	t.Setenv(envCommunicationActivation, "maybe")
	if _, err := loadCommunicationActivationConfig(osGetenv); err == nil {
		t.Fatal("unrecognized activation value accepted")
	}
	if eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test", NoIngest: true}); err == nil ||
		!strings.Contains(err.Error(), envCommunicationActivation) {
		if eng != nil {
			_ = eng.Close()
		}
		t.Fatalf("boot with invalid activation syntax = %v, want a configuration error naming the setting", err)
	}
}

// TestK1ApplyAndPublicDrainHoldK3OnEveryProductionPath is the R1 correction:
// the composed K3 authority is bound on the module, so the post-commit nudge
// of a normal K1 Apply, the public DrainWorkOutbox and the periodic pump all
// hold a K3 row while K3 is requested-but-not-effective and while it is not
// requested at all, with independent K1 progress on another aggregate and the
// ordering rule kept explicit for a K1 fact queued behind the held K3 fact.
// Once activation becomes effective the same rows drain, which proves the hold
// was the authority and not a broken row.
func TestK1ApplyAndPublicDrainHoldK3OnEveryProductionPath(t *testing.T) {
	contentPath, cursorPath := writeCommunicationCustodyForTest(t)
	t.Setenv(envKeyWrap, "")
	t.Setenv(envCommunicationActivation, "on")
	t.Setenv(envCommunicationContentKeyringFile, contentPath)
	t.Setenv(envCommunicationCursorKeyringFile, cursorPath)
	ctx := context.Background()
	dir := t.TempDir()

	// Requested ON, effective OFF: the store is not activated yet.
	eng := bootForComposition(t, dir)
	readiness := mustCompositionReadiness(t, eng)
	if !eng.sessionsMod.CommunicationSessionCredentialsEnabled() || readiness.Effective || !readiness.Components.PumpReady {
		t.Fatalf("requested-on posture before activation = %+v", readiness)
	}
	if !eng.sessionsMod.WorkOutboxClaimAuthorityBound() {
		t.Fatal("outbox authority not bound on the module")
	}
	tenant, owner, workspace, principal := bootstrapCompositionTenant(t, eng, "k3-paths")
	one := applyCompositionCreate(t, eng, tenant, principal, workspace, owner, "item one")
	assertOutboxPublished(t, eng, tenant, one.EventID, "K1 create before activation")
	heldK3 := insertOutboxEvent(t, eng.store, tenant, workspace, one.ResultID, 2, "work.handoff.offered")
	k1Behind := insertOutboxEvent(t, eng.store, tenant, workspace, one.ResultID, 3, "work.lease.ended")

	// (a) A normal K1 Apply on a SEPARATE aggregate: its nudge publishes its
	// own fact and leaves the held K3 row untouched.
	two := applyCompositionCreate(t, eng, tenant, principal, workspace, owner, "item two")
	assertOutboxPublished(t, eng, tenant, two.EventID, "independent K1 create, requested-on/effective-off")
	assertOutboxHeld(t, eng, tenant, heldK3, "after K1 Apply nudge, requested-on/effective-off")
	// (b) The public drain API on the composed module.
	k1Two := insertOutboxEvent(t, eng.store, tenant, workspace, two.ResultID, 2, "work.item.transitioned")
	if err := eng.sessionsMod.DrainWorkOutbox(ctx, tenant, 100); err != nil {
		t.Fatalf("public drain: %v", err)
	}
	assertOutboxPublished(t, eng, tenant, k1Two, "K1 through the public drain, requested-on/effective-off")
	assertOutboxHeld(t, eng, tenant, heldK3, "after public drain, requested-on/effective-off")
	// (c) The periodic pump.
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick: %v", err)
	}
	assertOutboxHeld(t, eng, tenant, heldK3, "after pump tick, requested-on/effective-off")
	// The K1 fact queued behind the held K3 fact on the SAME aggregate waits by
	// the existing ordering rule: it is pending, not claimed, and not lost.
	if row := outboxRowByEvent(t, eng.store, tenant, k1Behind); row.String("state") != "pending" || row.Int("attempts") != 0 {
		t.Fatalf("K1 behind held K3 = %v, want pending by the aggregate ordering rule", row)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	// Explicit ceremony and reopen: the same rows drain in order.
	if out, err := runDB(t, "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1",
		"--actor", "acceptance", "--reason", "serve stopped for activation", "--writers-upgraded", "--writers-drained"); err != nil {
		t.Fatalf("activation ceremony: %v\n%s", err, out)
	}
	eng = bootForComposition(t, dir)
	if readiness := mustCompositionReadiness(t, eng); !readiness.Effective {
		t.Fatalf("post-activation readiness = %+v", readiness)
	}
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick after activation: %v", err)
	}
	assertOutboxPublished(t, eng, tenant, heldK3, "held K3 after activation")
	assertOutboxPublished(t, eng, tenant, k1Behind, "K1 behind K3 after activation")
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	// Activation withdrawn (requested OFF, effective OFF): the review's R1
	// sequence. The pump holds K3, then a normal K1 Apply must hold it too.
	t.Setenv(envCommunicationActivation, "off")
	eng = bootForComposition(t, dir)
	defer eng.Close() //nolint:errcheck
	if eng.sessionsMod.CommunicationSessionCredentialsEnabled() {
		t.Fatal("activation off still enabled credentials")
	}
	if !eng.sessionsMod.WorkOutboxClaimAuthorityBound() || eng.communicationPump.authority == nil {
		t.Fatal("outbox authority not bound with activation off")
	}
	offK3 := insertOutboxEvent(t, eng.store, tenant, workspace, one.ResultID, 4, "work.handoff.offered")
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick with activation off: %v", err)
	}
	assertOutboxHeld(t, eng, tenant, offK3, "after pump tick, activation off")
	before := mustCompositionReadiness(t, eng)
	three := applyCompositionCreate(t, eng, tenant, principal, workspace, owner, "independent K1 with activation off")
	after := mustCompositionReadiness(t, eng)
	assertOutboxPublished(t, eng, tenant, three.EventID, "K1 create with activation off")
	row := outboxRowByEvent(t, eng.store, tenant, offK3)
	captures := eventingCaptureCount(t, eng.store, tenant, offK3)
	t.Logf("K1 Apply on real boot/store: requested=false effective_before=%t effective_after=%t K3_state=%s attempts=%d captures=%d",
		before.Effective, after.Effective, row.String("state"), row.Int("attempts"), captures)
	assertOutboxHeld(t, eng, tenant, offK3, "after K1 Apply nudge, activation off")
	if err := eng.sessionsMod.DrainWorkOutbox(ctx, tenant, 100); err != nil {
		t.Fatalf("public drain with activation off: %v", err)
	}
	assertOutboxHeld(t, eng, tenant, offK3, "after public drain, activation off")
	if verdict := eng.communicationPump.authority.current(ctx); verdict.allow || verdict.reason != "communication_pump_unbound" {
		t.Fatalf("authority verdict with activation off = %+v", verdict)
	}
}

// stoppingWorkSink forwards to the REAL boot sink and, after the named event
// was captured, stops the real pump: the review's R2 sequence.
type stoppingWorkSink struct {
	inner sessions.WorkEventSink
	after func(sessions.WorkEventEnvelope)
}

func (s stoppingWorkSink) IngestDurable(ctx context.Context, event sessions.WorkEventEnvelope) error {
	if err := s.inner.IngestDurable(ctx, event); err != nil {
		return err
	}
	s.after(event)
	return nil
}

// TestStoppedPumpHoldsSubsequentK3ClaimsWithinTheSameTick is the R2
// correction: the authority is sampled per candidate at the claim boundary
// and at the effect boundary, never cached for a tick. The real pump is
// stopped by a forwarding sink right after a real K1 intake; the K3 row next
// in the same aggregate and the same drain stays untouched, and it is
// recovered by the existing FSM on the next authorized tick after a restart.
func TestStoppedPumpHoldsSubsequentK3ClaimsWithinTheSameTick(t *testing.T) {
	contentPath, cursorPath := writeCommunicationCustodyForTest(t)
	t.Setenv(envKeyWrap, "")
	t.Setenv(envCommunicationActivation, "on")
	t.Setenv(envCommunicationContentKeyringFile, contentPath)
	t.Setenv(envCommunicationCursorKeyringFile, cursorPath)
	ctx := context.Background()
	dir := t.TempDir()

	eng := bootForComposition(t, dir)
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := runDB(t, "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1",
		"--actor", "acceptance", "--reason", "serve stopped for activation", "--writers-upgraded", "--writers-drained"); err != nil {
		t.Fatalf("activation ceremony: %v\n%s", err, out)
	}
	eng = bootForComposition(t, dir)
	if readiness := mustCompositionReadiness(t, eng); !readiness.Effective {
		t.Fatalf("post-activation readiness = %+v", readiness)
	}
	tenant, owner, workspace, principal := bootstrapCompositionTenant(t, eng, "k3-stop")
	item := applyCompositionCreate(t, eng, tenant, principal, workspace, owner, "stop mid-tick")
	k1 := insertOutboxEvent(t, eng.store, tenant, workspace, item.ResultID, 2, "work.item.transitioned")
	k3 := insertOutboxEvent(t, eng.store, tenant, workspace, item.ResultID, 3, "work.handoff.offered")
	eng.sessionsMod.UseWorkEventSink(stoppingWorkSink{inner: eng.workSink, after: func(event sessions.WorkEventEnvelope) {
		if event.EventID == k1 {
			eng.communicationPump.stop()
		}
	}})
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick: %v", err)
	}
	assertOutboxPublished(t, eng, tenant, k1, "K1 delivered before the stop")
	readiness := mustCompositionReadiness(t, eng)
	row := outboxRowByEvent(t, eng.store, tenant, k3)
	captures := eventingCaptureCount(t, eng.store, tenant, k3)
	t.Logf("real pump stopped after real K1 intake: effective=%t pump_ready=%t K3_state=%s attempts=%d captures=%d",
		readiness.Effective, readiness.Components.PumpReady, row.String("state"), row.Int("attempts"), captures)
	if readiness.Components.PumpReady || readiness.Effective {
		t.Fatal("stop fact did not withdraw the real witness")
	}
	assertOutboxHeld(t, eng, tenant, k3, "K3 next in the same tick after the stop")
	// Both boundaries of the real authority refuse now, without any cache.
	candidate := sessions.WorkOutboxCandidate{TenantID: tenant, EventID: k3, Type: "work.handoff.offered", Family: sessions.WorkEventFamilyCommunication}
	if allow, err := eng.communicationPump.authority.AllowWorkOutboxClaim(ctx, candidate); allow || err != nil {
		t.Fatalf("claim boundary after stop = %t %v", allow, err)
	}
	if allow, err := eng.communicationPump.authority.AllowWorkOutboxEffect(ctx, candidate); allow || err != nil {
		t.Fatalf("effect boundary after stop = %t %v", allow, err)
	}
	if verdict := eng.communicationPump.authority.current(ctx); verdict.allow ||
		!strings.HasPrefix(verdict.reason, "pump_witness_off: ") || !strings.Contains(verdict.reason, "stopped=true") {
		t.Fatalf("authority verdict after stop = %+v", verdict)
	}
	// A later tick of the stopped pump still holds it; a K1 Apply nudge still
	// holds it; the public drain still holds it.
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("second tick after stop: %v", err)
	}
	assertOutboxHeld(t, eng, tenant, k3, "second tick after the stop")
	other := applyCompositionCreate(t, eng, tenant, principal, workspace, owner, "K1 after the stop")
	assertOutboxPublished(t, eng, tenant, other.EventID, "K1 create after the stop")
	assertOutboxHeld(t, eng, tenant, k3, "after K1 Apply nudge following the stop")
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	// Recovery through the existing FSM: the untouched pending row drains on
	// the first authorized tick after a restart, exactly once.
	eng = bootForComposition(t, dir)
	defer eng.Close() //nolint:errcheck
	if ready, err := eng.communicationPump.communication.CommunicationPumpReady(ctx); !ready || err != nil {
		t.Fatalf("pump witness after restart = %t %v (%s)", ready, err, eng.communicationPump.communication.status())
	}
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick after restart: %v", err)
	}
	assertOutboxPublished(t, eng, tenant, k3, "held K3 after restart")
}

// eventingCaptureCount counts eventing rows that carry the given event id in
// any column: the capture table is the durable dedupe boundary.
func eventingCaptureCount(t *testing.T, st store.Store, tenant model.TenantID, eventID model.ID) int {
	t.Helper()
	count := 0
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("eventing.event")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: 500})
		if err != nil {
			return err
		}
		for _, row := range rows {
			for _, value := range row {
				if s, ok := value.(string); ok && s == eventID.String() {
					count++
					break
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("count captures: %v", err)
	}
	return count
}

func outboxRowByEvent(t *testing.T, st store.Store, tenant model.TenantID, eventID model.ID) model.Record {
	t.Helper()
	var row model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.work_outbox")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: "event_id", Op: model.OpEq, Value: eventID.String(),
		}}, Limit: 2})
		if err != nil || len(rows) != 1 {
			t.Fatalf("outbox rows for %s = %d err=%v", eventID, len(rows), err)
		}
		row = rows[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return row
}

// storedEventEnvelope builds the envelope the pump itself delivers for one
// stored event: every field comes from the durable row, so a capture taken
// through the real sink carries exactly the content a later re-delivery will.
func storedEventEnvelope(t *testing.T, st store.Store, tenant model.TenantID, eventID model.ID) sessions.WorkEventEnvelope {
	t.Helper()
	var envelope sessions.WorkEventEnvelope
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.work_event")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{{
			Column: "event_id", Op: model.OpEq, Value: eventID.String(),
		}}, Limit: 2})
		if err != nil || len(rows) != 1 {
			t.Fatalf("event rows for %s = %d err=%v", eventID, len(rows), err)
		}
		row := rows[0]
		envelope = sessions.WorkEventEnvelope{
			TenantID: tenant, WorkspaceID: model.ID(row.String("workspace_id")), EventID: eventID,
			AggregateKind: row.String("aggregate_kind"), AggregateID: model.ID(row.String("aggregate_id")),
			Sequence: row.Int("seq"), Type: row.String("event_type"), OccurredAt: row.String("occurred_at"),
			Payload: []byte(row.String("payload_json")),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return envelope
}

// setOutboxRow rewrites one outbox row's durable state. The row is read before
// the mutation opens: a View nested inside a Mutate competes for the store's
// connections and can wait forever on a small pool.
func setOutboxRow(t *testing.T, st store.Store, tenant model.TenantID, eventID model.ID, state string, claimUntil *time.Time) {
	t.Helper()
	row := outboxRowByEvent(t, st, tenant, eventID)
	row["state"] = state
	row["next_attempt_at"] = model.NewTimestamp(time.Unix(0, 0).UTC()).String()
	row["claim_owner"], row["claim_until"], row["published_at"] = nil, nil, nil
	if claimUntil != nil {
		// An in-flight row is one a pump already attempted: the evidence
		// validator requires attempts >= 1 alongside the claim.
		row["claim_owner"] = "node-before-restart"
		row["claim_until"] = model.NewTimestamp(*claimUntil).String()
		row["attempts"] = max(int64(1), row.Int("attempts"))
	}
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.work_outbox")
		if err != nil {
			return err
		}
		_, err = repo.Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatalf("set outbox row: %v", err)
	}
}

// insertOutboxEvent seeds one durable event of any family on an EXISTING
// work-item aggregate with its pending outbox row, exactly as an apply path
// leaves them behind. The outbox delivers an aggregate in sequence order, so
// callers hand out consecutive seqs.
func insertOutboxEvent(t *testing.T, st store.Store, tenant model.TenantID, workspace, aggregate model.ID, seq int64, eventType string) model.ID {
	t.Helper()
	eventID := model.NewID()
	payload := []byte(`{"event_type":"` + eventType + `","schema_version":1}`)
	digest := sha256.Sum256(payload)
	audit := sha256.Sum256([]byte("audit"))
	now := model.NewTimestamp(time.Now().UTC())
	// Due one second ago: the SQLite transaction clock has millisecond
	// precision, so a row due "now" with nanoseconds is not yet claimable by a
	// drain that runs within the same millisecond.
	due := model.NewTimestamp(time.Now().UTC().Add(-time.Second))
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		events, err := sc.Ext("sessions.work_event")
		if err != nil {
			return err
		}
		if _, err := events.Create(context.Background(), model.Record{
			"workspace_id": workspace.String(), "event_id": eventID.String(),
			"aggregate_kind": "sessions.work_item", "aggregate_id": aggregate.String(), "seq": seq,
			"event_type": eventType, "actor_kind": "user", "actor_ref": model.NewID().String(),
			"occurred_at": now.String(), "payload_json": string(payload), "payload_hash": digest[:],
			"command_id": model.NewID().String(), "audit_seq": int64(1), "audit_hash": audit[:],
		}); err != nil {
			return err
		}
		outbox, err := sc.Ext("sessions.work_outbox")
		if err != nil {
			return err
		}
		_, err = outbox.Create(context.Background(), model.Record{
			"workspace_id": workspace.String(), "event_id": eventID.String(), "state": "pending",
			"attempts": int64(0), "next_attempt_at": due.String(), "claim_owner": nil, "claim_until": nil,
			"published_at": nil, "last_outcome": nil,
		})
		return err
	}); err != nil {
		t.Fatalf("insert %s outbox event: %v", eventType, err)
	}
	return eventID
}

// TestBootActivationBecomesEffectiveOnlyAfterExplicitWriterActivation is the
// lot A acceptance path on one owned file-backed SQLite estate: requested
// activation binds real witnesses but stays non-effective until the operator
// ceremony runs and the store is reopened; then the registered local pump
// drains K1 and K3 through the real Eventing sink, resumes an expired pending
// claim after a restart, and never captures one event id twice. With
// activation off again, the same pump keeps K1 flowing and holds K3.
func TestBootActivationBecomesEffectiveOnlyAfterExplicitWriterActivation(t *testing.T) {
	contentPath, cursorPath := writeCommunicationCustodyForTest(t)
	t.Setenv(envKeyWrap, "")
	t.Setenv(envCommunicationActivation, "on")
	t.Setenv(envCommunicationContentKeyringFile, contentPath)
	t.Setenv(envCommunicationCursorKeyringFile, cursorPath)
	ctx := context.Background()
	dir := t.TempDir()

	eng := bootForComposition(t, dir)
	readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(ctx)
	if err != nil {
		t.Fatalf("readiness before activation: %v", err)
	}
	c := readiness.Components
	if !eng.sessionsMod.CommunicationSessionCredentialsEnabled() || !c.IssuerReady || !c.SealerReady ||
		!c.ResolverReady || !c.PermissionsReady || !c.PumpReady || c.StoreReady || readiness.Effective {
		t.Fatalf("pre-activation readiness = %+v", readiness)
	}
	if !eng.sessionsMod.CommunicationCursorTokenKeyringBound() {
		t.Fatal("cursor keyring custody was configured but is not bound")
	}
	proofWitness, ok := eng.communicationPump.communication, true
	if proofWitness == nil || !ok {
		t.Fatal("pump witness not attached")
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	// Explicit operator ceremony, then reopen: the only path to StoreReady.
	if out, err := runDB(t, "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1",
		"--actor", "acceptance", "--reason", "serve stopped for activation", "--writers-upgraded", "--writers-drained"); err != nil {
		t.Fatalf("activation ceremony: %v\n%s", err, out)
	}
	eng = bootForComposition(t, dir)
	readiness, err = eng.sessionsMod.EvaluateCommunicationReadiness(ctx)
	if err != nil || !readiness.Effective || readiness.Verdict != sessions.VerdictClean {
		t.Fatalf("post-activation readiness = %+v err=%v", readiness, err)
	}

	// A real tenant, user and work item through the composed kernel.
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "k3", Slug: "k3", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	user, _, err := eng.authr.BootstrapSuperadminOwning(ctx, "owner@k3.test", "k3-acceptance-password", tenant)
	if err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}
	var workspace model.ID
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		workspace = ws.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	principal := sessions.WorkPrincipal{
		ActorKind: model.ActorUser, ActorRef: user.ID.String(), Actor: "user:" + user.ID.String(), Admin: true,
	}
	created, err := eng.sessionsMod.Apply(ctx, tenant, principal, sessions.WorkCommand{
		Command: "item.create", WorkspaceID: workspace, WorkKind: "implementation", Title: "K3 lot A acceptance",
		BriefMD: "Prove the local pump lanes.", ContextRefs: []sessions.ContextRef{}, Priority: "p1",
		OwnerKind: "user", OwnerRef: user.ID.String(), ProvenanceKind: "human", ProvenanceRef: "test:k3-lot-a",
		Acceptance:     []sessions.AcceptanceInput{{Key: "pump", Ordinal: 0, Statement: "both lanes drain", Required: true}},
		IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost, CommandScope: "POST /work-items",
	})
	if err != nil {
		t.Fatalf("create work item: %v", err)
	}
	// The request path published seq 1 itself, post-commit. Everything the pump
	// must deliver is seeded as LATER events on the same aggregate: a published
	// row is terminal under the outbox guard, so nothing is ever reset.
	item := created.ResultID
	k1 := insertOutboxEvent(t, eng.store, tenant, workspace, item, 2, "work.item.transitioned")
	k3 := insertOutboxEvent(t, eng.store, tenant, workspace, item, 3, "work.handoff.offered")
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick: %v", err)
	}
	for _, id := range []model.ID{k1, k3} {
		if row := outboxRowByEvent(t, eng.store, tenant, id); row.String("state") != "published" {
			t.Fatalf("event %s after effective tick = %v", id, row)
		}
		if n := eventingCaptureCount(t, eng.store, tenant, id); n != 1 {
			t.Fatalf("captures for %s = %d, want 1", id, n)
		}
	}

	// Crash simulation, the window the outbox exists for: the durable intake
	// captured a K3 event but the process died before the outbox settled, so the
	// row is still in flight under an expired claim, with a K1 row queued behind
	// it. The capture is taken through the SAME sink boot bound. After a restart
	// the pump must reclaim the expired claim, re-deliver, and the intake must
	// keep exactly one capture (event-id dedupe), then drain the K1 row in order.
	k3Crashed := insertOutboxEvent(t, eng.store, tenant, workspace, item, 4, "work.handoff.withdrawn")
	if err := eng.workSink.IngestDurable(ctx, storedEventEnvelope(t, eng.store, tenant, k3Crashed)); err != nil {
		t.Fatalf("capture through the real sink: %v", err)
	}
	if n := eventingCaptureCount(t, eng.store, tenant, k3Crashed); n != 1 {
		t.Fatalf("captures before the crash = %d, want 1", n)
	}
	k1Behind := insertOutboxEvent(t, eng.store, tenant, workspace, item, 5, "work.lease.ended")
	expired := time.Now().UTC().Add(-time.Minute)
	setOutboxRow(t, eng.store, tenant, k3Crashed, "delivering", &expired)
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	eng = bootForComposition(t, dir)
	if ready, err := eng.communicationPump.communication.CommunicationPumpReady(ctx); !ready || err != nil {
		t.Fatalf("pump witness after restart = %t %v (%s)", ready, err, eng.communicationPump.communication.status())
	}
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick after restart: %v", err)
	}
	for _, id := range []model.ID{k3Crashed, k1Behind} {
		if row := outboxRowByEvent(t, eng.store, tenant, id); row.String("state") != "published" || row.String("claim_owner") != "" {
			t.Fatalf("event %s after restart tick = %v", id, row)
		}
		if n := eventingCaptureCount(t, eng.store, tenant, id); n != 1 {
			t.Fatalf("captures for %s after restart = %d, want exactly 1 (dedupe on re-delivery)", id, n)
		}
	}
	// Stop withdraws pump readiness at once.
	eng.communicationPump.stop()
	if ready, _ := eng.communicationPump.communication.CommunicationPumpReady(ctx); ready {
		t.Fatal("stopped pump still reports ready")
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	// Activation withdrawn: the same estate keeps K1 recovery and holds K3 with
	// zero effects (no claim, no attempt, no capture).
	t.Setenv(envCommunicationActivation, "off")
	eng = bootForComposition(t, dir)
	defer eng.Close() //nolint:errcheck
	if eng.sessionsMod.CommunicationSessionCredentialsEnabled() {
		t.Fatal("activation off still enabled credentials")
	}
	k1Off := insertOutboxEvent(t, eng.store, tenant, workspace, item, 6, "work.lease.acquired")
	k3Off := insertOutboxEvent(t, eng.store, tenant, workspace, item, 7, "work.handoff.offered")
	if err := eng.communicationPump.runOnce(ctx); err != nil {
		t.Fatalf("pump tick with activation off: %v", err)
	}
	if row := outboxRowByEvent(t, eng.store, tenant, k1Off); row.String("state") != "published" {
		t.Fatalf("K1 event held while activation off: %v", row)
	}
	if row := outboxRowByEvent(t, eng.store, tenant, k3Off); row.String("state") != "pending" ||
		row.Int("attempts") != 0 || row.String("claim_owner") != "" {
		t.Fatalf("K3 event claimed while activation off: %v", row)
	}
	if n := eventingCaptureCount(t, eng.store, tenant, k3Off); n != 0 {
		t.Fatalf("captures for held K3 event = %d, want 0", n)
	}
}
