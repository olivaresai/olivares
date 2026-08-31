// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type recoveryTestKMS struct {
	private *ecdsa.PrivateKey
	keyID   string
	calls   atomic.Int64
	failAt  atomic.Int64
}

func newRecoveryTestKMS(t *testing.T) *recoveryTestKMS {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &recoveryTestKMS{private: key, keyID: "test-kms://audit-recovery"}
}

func (k *recoveryTestKMS) SignCheckpoint(_ context.Context, preimage []byte) ([]byte, error) {
	call := k.calls.Add(1)
	if failAt := k.failAt.Load(); failAt > 0 && call == failAt {
		return nil, errors.New("injected off-box signer failure")
	}
	digest := sha256.Sum256(preimage)
	return ecdsa.SignASN1(rand.Reader, k.private, digest[:])
}
func (k *recoveryTestKMS) Algorithm() audit.SigAlg { return audit.AlgECDSAP256SHA256 }
func (k *recoveryTestKMS) KeyID() string           { return k.keyID }
func (k *recoveryTestKMS) PublicKey(context.Context) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(&k.private.PublicKey)
}

type recoveryCLIFixture struct {
	dataDir    string
	tenant     model.TenantID
	signer     *audit.Signer
	kms        *recoveryTestKMS
	pubSpec    string
	reanchor   int64
	breakAt    int64
	checkpoint bool
}

func newRecoveryCLIFixture(t *testing.T, checkpoint, corrupt bool) recoveryCLIFixture {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dataDir, Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatal(err)
	}
	tenant := eng.demoTenant
	_, onBoxPrivate, _ := ed25519.GenerateKey(nil)
	kms := newRecoveryTestKMS(t)
	signer, err := audit.NewSigner(onBoxPrivate, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	var checkpointEvent model.AuditEvent
	if checkpoint {
		var ok bool
		checkpointEvent, ok, err = signer.Checkpoint(ctx, eng.store, tenant)
		if err != nil || !ok {
			t.Fatalf("checkpoint: ok=%v err=%v", ok, err)
		}
	}
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:tail", ActorKind: model.ActorUser, Action: "agent.update", TargetKind: "core.agent",
		})
		return aerr
	}); err != nil {
		t.Fatal(err)
	}
	var head store.HeadRef
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		var ok bool
		head, ok, err = sc.Audit().Head(ctx)
		if !ok && err == nil {
			err = errors.New("fixture has no head")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = eng.Close()

	breakAt := head.Seq
	if corrupt {
		raw, err := sql.Open("sqlite", filepath.Join(dataDir, "olivares.db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec("DROP TRIGGER audit_events_no_update"); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec("UPDATE audit_events SET action = 'tampered' WHERE tenant_id = ? AND seq = ?", tenant.String(), breakAt); err != nil {
			t.Fatal(err)
		}
		_ = raw.Close()
	}
	der, _ := kms.PublicKey(ctx)
	reanchor := int64(0)
	if checkpoint {
		reanchor = checkpointEvent.Seq - 1
	}
	return recoveryCLIFixture{
		dataDir: dataDir, tenant: tenant, signer: signer, kms: kms,
		pubSpec:  string(kms.Algorithm()) + ":" + base64.StdEncoding.EncodeToString(der),
		reanchor: reanchor, breakAt: breakAt, checkpoint: checkpoint,
	}
}

func approvedRecoveryDeps(f recoveryCLIFixture, offBoxSigner bool) auditRecoverDeps {
	return auditRecoverDeps{
		boot: func(cmd *cobra.Command, dataDir, engineKind, dsn string) (*engine, error) {
			eng, err := auditBoot(cmd, dataDir, engineKind, dsn)
			if err == nil && offBoxSigner {
				eng.signer = f.signer
			}
			return eng, err
		},
		gate: func(_ context.Context, _ *engine, _ model.TenantID, planHash, _, _ string) (string, string, string, approverEvidence, error) {
			// Two credentials AND two distinct people: the ceremony's happy path is two
			// humans, and the fixture has to say so in both forms or it would be
			// asserting the very confusion removed.
			return "apr_recovery", nbApproved, planHash, approverEvidence{
				Actors:  []string{"user:alice", "user:bob"},
				Persons: []string{"alice", "bob"},
			}, nil
		},
	}
}

func runRecoveryCLI(t *testing.T, f recoveryCLIFixture, deps auditRecoverDeps, args ...string) (string, error) {
	t.Helper()
	cmd := auditRecoverCmdWithDeps(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("yes\n"))
	cmd.SetContext(context.Background())
	base := []string{"--tenant", f.tenant.String(), "--data-dir", f.dataDir, "--pubkey", f.pubSpec}
	cmd.SetArgs(append(base, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestAuditRecoverForkForwardHappyAndEpochAwareVerify(t *testing.T) {
	f := newRecoveryCLIFixture(t, true, true)
	ctx := context.Background()

	// Before recovery, strict verification reports a fresh corruption.
	freshOut, freshErr := runAuditVerifyForRecovery(t, f, true)
	if freshErr == nil || !strings.Contains(freshOut, `"status": "corrupt"`) || strings.Contains(freshErr.Error(), "RECOVERED") {
		t.Fatalf("fresh corruption was not distinct: err=%v\n%s", freshErr, freshOut)
	}

	out, err := runRecoveryCLI(t, f, approvedRecoveryDeps(f, true),
		"--dry-run=false", "--reason", "tail hash incident", "--requested-by", "svc:recovery")
	if err != nil {
		t.Fatalf("recover: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"status": "recovered"`) || !strings.Contains(out, `"mutated": true`) || !strings.Contains(out, `"recovery_signature_valid": true`) {
		t.Fatalf("recovery report incomplete:\n%s", out)
	}
	recoveryReport := decodeRecoveryCommandReport(t, out)
	if recoveryReport.Checkpoints.LatestAttestedSeq < recoveryReport.RecoverSeq {
		t.Fatalf("recovery head was not checkpointed: %+v", recoveryReport)
	}

	eng, err := auditBoot(auditCommandForTest(ctx), f.dataDir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	verifier, _, err := recoveryCheckpointVerifier([]string{f.pubSpec}, "")
	if err != nil {
		t.Fatal(err)
	}
	var recoverSeq int64
	if err := eng.store.View(ctx, f.tenant, func(sc store.Scope) error {
		genesis, verr := sc.Audit().Verify(ctx, 1)
		if verr != nil {
			return verr
		}
		if genesis.OK || genesis.BreakAt != f.breakAt {
			t.Fatalf("genesis scar was re-greened: %+v", genesis)
		}
		found, seq, evidence, verr := audit.LocateRecoveryEvidence(ctx, sc.Audit(), verifier)
		if verr != nil {
			return verr
		}
		if !found || evidence.ReanchorSeq != f.reanchor || evidence.OffBoxCheckpointSeq != f.reanchor ||
			evidence.QuarantinedFrom != f.breakAt || evidence.QuarantinedSHA256 == "" || len(evidence.Approvers) != 2 ||
			evidence.Reason != "tail hash incident" || evidence.RequestedBy != "svc:recovery" {
			t.Fatalf("recovery evidence incomplete: found=%v seq=%d evidence=%+v", found, seq, evidence)
		}
		recoverSeq = seq
		epoch, verr := sc.Audit().Verify(ctx, recoverSeq)
		if verr != nil || !epoch.OK {
			t.Fatalf("recovery epoch is not green: %+v err=%v", epoch, verr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The reserved-action guard rejects a forged unsigned marker.
	err = eng.store.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, model.AuditDraft{Action: store.ActionAuditRecover})
		return aerr
	})
	if !errors.Is(err, store.ErrReservedAuditAction) {
		t.Fatalf("unsigned audit.recover = %v, want ErrReservedAuditAction", err)
	}

	_ = eng.Close()
	epochOut, epochErr := runAuditVerifyForRecovery(t, f, true, "--from", strconv.FormatInt(recoverSeq, 10))
	if epochErr != nil || !strings.Contains(epochOut, `"status": "epoch_ok"`) {
		t.Fatalf("strict current-epoch proof failed: err=%v\n%s", epochErr, epochOut)
	}
	recoveredOut, recoveredErr := runAuditVerifyForRecovery(t, f, true)
	if recoveredErr == nil || !strings.Contains(recoveredErr.Error(), "RECOVERED") || !strings.Contains(recoveredOut, `"status": "recovered"`) ||
		!strings.Contains(recoveredOut, `"recover_seq"`) || !strings.Contains(recoveredOut, `"reanchor_seq"`) {
		t.Fatalf("epoch-aware verify did not surface recovery distinctly: err=%v\n%s", recoveredErr, recoveredOut)
	}
}

func TestAuditRecoverRejectsReplayForwardMarker(t *testing.T) {
	f := newRecoveryCLIFixture(t, true, true)
	recovery := completeRecoveryForAttack(t, f)
	carrierSeq := appendRecoveryAttackCarrier(t, f)
	copyRecoveryMarkerOntoCarrier(t, f, recovery.RecoverSeq, carrierSeq)

	ctx := context.Background()
	eng, err := auditBoot(auditCommandForTest(ctx), f.dataDir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	verifier, _, err := recoveryCheckpointVerifier([]string{f.pubSpec}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.store.View(ctx, f.tenant, func(sc store.Scope) error {
		found, seq, _, lerr := audit.LocateRecoveryEvidence(ctx, sc.Audit(), verifier)
		if lerr == nil && (!found || seq != recovery.RecoverSeq) {
			t.Fatalf("LocateRecoveryEvidence honored replay seq %d instead of original seq %d", seq, recovery.RecoverSeq)
		}
		return lerr
	}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := runAuditVerifyForRecovery(t, f, true, "--from", strconv.FormatInt(carrierSeq, 10))
	if err == nil || !strings.Contains(out, `"status": "corrupt"`) ||
		!strings.Contains(out, `"reason": "recovery-position-invalid"`) ||
		strings.Contains(out, `"status": "epoch_ok"`) || strings.Contains(out, `"status": "recovered"`) {
		t.Fatalf("replayed recovery marker was honored: err=%v\n%s", err, out)
	}
}

func TestAuditRecoverCheckpointFailureLeavesUnattestedMarker(t *testing.T) {
	f := newRecoveryCLIFixture(t, true, true)
	// The fixture's initial trusted checkpoint is call 1; recovery marker signing
	// is call 2; fail call 3, the checkpoint intended to attest that marker.
	f.kms.failAt.Store(3)
	out, err := runRecoveryCLI(t, f, approvedRecoveryDeps(f, true),
		"--dry-run=false", "--reason", "kms outage regression", "--requested-by", "svc:test")
	if err == nil || !strings.Contains(out, `"status": "unattested"`) ||
		!strings.Contains(out, `"mutated": true`) || !strings.Contains(out, "retry `olivares audit checkpoint`") ||
		strings.Contains(out, `"status": "recovered"`) {
		t.Fatalf("checkpoint failure reported a completed recovery: err=%v\n%s", err, out)
	}

	raw := openRecoveryAttackDB(t, f)
	defer raw.Close()
	var markers int
	if err := raw.QueryRow("SELECT COUNT(*) FROM audit_events WHERE tenant_id = ? AND action = ?", f.tenant.String(), store.ActionAuditRecover).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("checkpoint failure left %d recovery markers, want the one durable marker", markers)
	}
}

func TestAuditRecoverRejectsTruncationToUnattestedMarker(t *testing.T) {
	f := newRecoveryCLIFixture(t, true, true)
	recovery := completeRecoveryForAttack(t, f)
	truncateRecoveryLedgerToMarker(t, f, recovery.RecoverSeq)

	out, err := runAuditVerifyForRecovery(t, f, true, "--from", strconv.FormatInt(recovery.RecoverSeq, 10))
	if err == nil || !strings.Contains(out, `"status": "corrupt"`) ||
		strings.Contains(out, `"status": "epoch_ok"`) {
		t.Fatalf("unattested truncated recovery epoch was honored: err=%v\n%s", err, out)
	}
	var report struct {
		Checkpoints audit.CheckpointReport `json:"checkpoints"`
	}
	if jerr := json.NewDecoder(strings.NewReader(out)).Decode(&report); jerr != nil {
		t.Fatalf("decode verify report: %v\n%s", jerr, out)
	}
	if report.Checkpoints.LatestAttestedSeq >= recovery.RecoverSeq {
		t.Fatalf("attack fixture retained a checkpoint over marker seq %d: %+v", recovery.RecoverSeq, report.Checkpoints)
	}
}

func TestAuditVerifyStrictRejectsForgedRecoveryMarker(t *testing.T) {
	f := newRecoveryCLIFixture(t, true, false)
	carrierSeq := appendRecoveryAttackCarrier(t, f)
	evidence := audit.RecoveryEvidence{
		Tenant: f.tenant.String(), BreakReason: "hash-mismatch", BreakAt: 2,
		ReanchorSeq: 1, OffBoxCheckpointSeq: 1, OffBoxKeyID: "attacker",
		QuarantinedFrom: 2, QuarantinedTo: carrierSeq - 1,
		QuarantinedSHA256: strings.Repeat("ab", 32),
		Approvers:         []string{"user:alice", "user:bob"}, Reason: "forged", RequestedBy: "attacker",
	}
	meta, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	rewriteRecoveryAttackCarrier(t, f, carrierSeq, string(meta), []byte("garbage-signature"))

	out, verifyErr := runAuditVerifyForRecovery(t, f, true)
	if verifyErr == nil || !strings.Contains(out, `"status": "corrupt"`) ||
		!strings.Contains(out, `"reason": "recovery-sig-invalid"`) {
		t.Fatalf("forged recovery marker survived strict verify: err=%v\n%s", verifyErr, out)
	}
}

func TestAuditRecoverDenyClosed(t *testing.T) {
	t.Run("no checkpoint", func(t *testing.T) {
		f := newRecoveryCLIFixture(t, false, true)
		out, err := runRecoveryCLI(t, f, approvedRecoveryDeps(f, true))
		if err == nil || !strings.Contains(out, "no-checkpoints") || !strings.Contains(out, auditRecoveryRunbook) {
			t.Fatalf("no-checkpoint refusal: err=%v\n%s", err, out)
		}
	})

	t.Run("wrong pin", func(t *testing.T) {
		f := newRecoveryCLIFixture(t, true, true)
		wrong := newRecoveryTestKMS(t)
		der, _ := wrong.PublicKey(context.Background())
		f.pubSpec = string(wrong.Algorithm()) + ":" + base64.StdEncoding.EncodeToString(der)
		out, err := runRecoveryCLI(t, f, approvedRecoveryDeps(f, true))
		if err == nil || !strings.Contains(out, "checkpoint-sig-invalid") {
			t.Fatalf("wrong-pin refusal: err=%v\n%s", err, out)
		}
	})

	t.Run("healthy ledger", func(t *testing.T) {
		f := newRecoveryCLIFixture(t, true, false)
		out, err := runRecoveryCLI(t, f, approvedRecoveryDeps(f, true))
		if err == nil || !strings.Contains(out, "no structural break") {
			t.Fatalf("healthy-ledger refusal: err=%v\n%s", err, out)
		}
	})

	t.Run("on-box signer", func(t *testing.T) {
		f := newRecoveryCLIFixture(t, true, true)
		out, err := runRecoveryCLI(t, f, approvedRecoveryDeps(f, false))
		if err == nil || !strings.Contains(out, "no off-box checkpoint signer") {
			t.Fatalf("on-box refusal: err=%v\n%s", err, out)
		}
	})
}

func runAuditVerifyForRecovery(t *testing.T, f recoveryCLIFixture, strict bool, extraArgs ...string) (string, error) {
	t.Helper()
	cmd := newAuditCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	args := []string{"verify", "--tenant", f.tenant.String(), "--data-dir", f.dataDir, "--pubkey", f.pubSpec}
	args = append(args, extraArgs...)
	if strict {
		args = append(args, "--strict")
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func auditCommandForTest(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	return cmd
}

func completeRecoveryForAttack(t *testing.T, f recoveryCLIFixture) auditRecoveryReport {
	t.Helper()
	out, err := runRecoveryCLI(t, f, approvedRecoveryDeps(f, true),
		"--dry-run=false", "--reason", "adversarial regression", "--requested-by", "svc:test")
	if err != nil {
		t.Fatalf("complete recovery fixture: %v\n%s", err, out)
	}
	report := decodeRecoveryCommandReport(t, out)
	if report.Status != "recovered" || report.RecoverSeq < 1 || report.Checkpoints.LatestAttestedSeq < report.RecoverSeq {
		t.Fatalf("incomplete recovery fixture: %+v", report)
	}
	return report
}

func decodeRecoveryCommandReport(t *testing.T, out string) auditRecoveryReport {
	t.Helper()
	start := strings.IndexByte(out, '{')
	if start < 0 {
		t.Fatalf("recovery output contains no JSON report:\n%s", out)
	}
	var report auditRecoveryReport
	if err := json.Unmarshal([]byte(out[start:]), &report); err != nil {
		t.Fatalf("decode recovery report: %v\n%s", err, out)
	}
	return report
}

func appendRecoveryAttackCarrier(t *testing.T, f recoveryCLIFixture) int64 {
	t.Helper()
	ctx := context.Background()
	eng, err := auditBoot(auditCommandForTest(ctx), f.dataDir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	var carrier model.AuditEvent
	err = eng.store.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		var aerr error
		carrier, aerr = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "attacker", ActorKind: model.ActorUser, Action: "attack.carrier", TargetKind: "core.agent",
		})
		return aerr
	})
	if closeErr := eng.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return carrier.Seq
}

func copyRecoveryMarkerOntoCarrier(t *testing.T, f recoveryCLIFixture, sourceSeq, carrierSeq int64) {
	t.Helper()
	raw := openRecoveryAttackDB(t, f)
	defer raw.Close()
	var meta string
	var sig []byte
	if err := raw.QueryRow(
		"SELECT meta, sig FROM audit_events WHERE tenant_id = ? AND seq = ?", f.tenant.String(), sourceSeq,
	).Scan(&meta, &sig); err != nil {
		t.Fatal(err)
	}
	rewriteRecoveryAttackCarrierWithDB(t, raw, f, carrierSeq, meta, sig)
}

func rewriteRecoveryAttackCarrier(t *testing.T, f recoveryCLIFixture, carrierSeq int64, meta string, sig []byte) {
	t.Helper()
	raw := openRecoveryAttackDB(t, f)
	defer raw.Close()
	rewriteRecoveryAttackCarrierWithDB(t, raw, f, carrierSeq, meta, sig)
}

func rewriteRecoveryAttackCarrierWithDB(t *testing.T, raw *sql.DB, f recoveryCLIFixture, carrierSeq int64, meta string, sig []byte) {
	t.Helper()
	if _, err := raw.Exec("DROP TRIGGER IF EXISTS audit_events_no_update"); err != nil {
		t.Fatal(err)
	}
	var occurredAt, actor, actorKind, targetKind, targetID string
	var payloadHash, prevHash []byte
	if err := raw.QueryRow(`SELECT occurred_at, actor, actor_kind, target_kind, target_id, payload_hash, prev_hash
		FROM audit_events WHERE tenant_id = ? AND seq = ?`, f.tenant.String(), carrierSeq).Scan(
		&occurredAt, &actor, &actorKind, &targetKind, &targetID, &payloadHash, &prevHash,
	); err != nil {
		t.Fatal(err)
	}
	hash := recoveryAttackEventHash(f.tenant.String(), carrierSeq, occurredAt, actor, actorKind,
		store.ActionAuditRecover, targetKind, targetID, meta, payloadHash, prevHash)
	if _, err := raw.Exec(`UPDATE audit_events SET action = ?, meta = ?, sig = ?, hash = ?
		WHERE tenant_id = ? AND seq = ?`, store.ActionAuditRecover, meta, sig, hash, f.tenant.String(), carrierSeq); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("UPDATE audit_heads SET seq = ?, hash = ? WHERE tenant_id = ?", carrierSeq, hash, f.tenant.String()); err != nil {
		t.Fatal(err)
	}
}

func truncateRecoveryLedgerToMarker(t *testing.T, f recoveryCLIFixture, recoverSeq int64) {
	t.Helper()
	raw := openRecoveryAttackDB(t, f)
	defer raw.Close()
	if _, err := raw.Exec("DROP TRIGGER IF EXISTS audit_events_no_delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("DELETE FROM audit_events WHERE tenant_id = ? AND seq > ?", f.tenant.String(), recoverSeq); err != nil {
		t.Fatal(err)
	}
	var markerHash []byte
	if err := raw.QueryRow("SELECT hash FROM audit_events WHERE tenant_id = ? AND seq = ?", f.tenant.String(), recoverSeq).Scan(&markerHash); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("UPDATE audit_heads SET seq = ?, hash = ? WHERE tenant_id = ?", recoverSeq, markerHash, f.tenant.String()); err != nil {
		t.Fatal(err)
	}
}

func openRecoveryAttackDB(t *testing.T, f recoveryCLIFixture) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(f.dataDir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func recoveryAttackEventHash(tenant string, seq int64, occurredAt, actor, actorKind, action, targetKind, targetID, meta string, payloadHash, prevHash []byte) []byte {
	metaInput := recoveryAttackLP(nil, []byte("olivares.audit.meta.v1"))
	metaInput = append(metaInput, []byte(meta)...)
	metaDigest := sha256.Sum256(metaInput)

	var preimage []byte
	preimage = recoveryAttackLP(preimage, []byte("olivares.audit.v1"))
	preimage = recoveryAttackLP(preimage, []byte(tenant))
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], uint64(seq))
	preimage = append(preimage, seqBytes[:]...)
	for _, value := range []string{occurredAt, actor, actorKind, action, targetKind, targetID} {
		preimage = recoveryAttackLP(preimage, []byte(value))
	}
	preimage = append(preimage, metaDigest[:]...)
	preimage = append(preimage, recoveryAttackFixed(payloadHash)...)
	preimage = append(preimage, recoveryAttackFixed(prevHash)...)
	sum := sha256.Sum256(preimage)
	return sum[:]
}

func recoveryAttackLP(dst, value []byte) []byte {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	dst = append(dst, size[:]...)
	return append(dst, value...)
}

func recoveryAttackFixed(value []byte) []byte {
	fixed := make([]byte, sha256.Size)
	copy(fixed, value)
	return fixed
}
