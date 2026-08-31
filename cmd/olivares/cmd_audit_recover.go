// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	auditRecoveryAction  = "audit.ledger.recover"
	auditRecoveryRunbook = "deploy/runbooks/ledger-recovery.md"
)

type auditRecoverBootFunc func(*cobra.Command, string, string, string) (*engine, error)

type auditRecoverGateFunc func(context.Context, *engine, model.TenantID, string, string, string) (ref, status, boundHash string, approvers approverEvidence, err error)

type auditRecoverDeps struct {
	boot auditRecoverBootFunc
	gate auditRecoverGateFunc
}

type auditRecoveryReport struct {
	OK            bool                       `json:"ok"`
	Status        string                     `json:"status"`
	Tenant        string                     `json:"tenant"`
	DryRun        bool                       `json:"dry_run"`
	Mutated       bool                       `json:"mutated"`
	Chain         store.VerifyReport         `json:"chain"`
	Checkpoints   audit.CheckpointReport     `json:"checkpoints"`
	OffBoxKeyID   string                     `json:"offbox_key_id,omitempty"`
	Archive       *audit.ArchiveVerifyReport `json:"archive,omitempty"`
	Gate          auditRecoveryGateReport    `json:"gate"`
	Evidence      *audit.RecoveryEvidence    `json:"evidence,omitempty"`
	RecoverSeq    int64                      `json:"recover_seq,omitempty"`
	ReanchorSeq   int64                      `json:"reanchor_seq,omitempty"`
	EpochStartSeq int64                      `json:"epoch_start_seq,omitempty"`
	Proof         *auditRecoveryProof        `json:"proof,omitempty"`
	Runbook       string                     `json:"runbook"`
	Problems      []string                   `json:"problems,omitempty"`
}

type auditRecoveryGateReport struct {
	ApprovalRef string `json:"approval_ref,omitempty"`
	Status      string `json:"status,omitempty"`
	BoundHash   string `json:"bound_hash,omitempty"`
	PlanHash    string `json:"plan_hash,omitempty"`
	// Approvers are the CREDENTIALS that approved (the provenance that rides the signed
	// evidence); Persons are the distinct ACCOUNTS, which is what the two-approver bar
	// counts — not two provably distinct humans (core/auth/person.go).
	// Unattributed reports approvals with no account behind them, so an operator reading
	// this report sees the difference between "one approver short" and "an approval I
	// cannot attribute to an account".
	//
	// The JSON key stays `approver_persons`: it rides SIGNED evidence and a wire rename
	// would invalidate receipts already issued. What it holds is documented here.
	Approvers    []string `json:"approvers,omitempty"`
	Persons      []string `json:"approver_persons,omitempty"`
	Unattributed int      `json:"unattributed_approvals,omitempty"`
}

type auditRecoveryProof struct {
	OK                     bool                       `json:"ok"`
	FromSeq                int64                      `json:"from_seq"`
	Chain                  store.VerifyReport         `json:"chain"`
	Checkpoints            audit.CheckpointReport     `json:"checkpoints"`
	RecoveryMarkers        audit.RecoveryMarkerReport `json:"recovery_markers"`
	RecoverySignatureValid bool                       `json:"recovery_signature_valid"`
	Command                string                     `json:"command"`
}

type recoveryPinnedKey struct {
	alg audit.SigAlg
	raw []byte
}

func auditRecoverCmd() *cobra.Command {
	return auditRecoverCmdWithDeps(auditRecoverDeps{boot: auditBoot, gate: gateAuditRecovery})
}

func auditRecoverCmdWithDeps(deps auditRecoverDeps) *cobra.Command {
	var dataDir, engineKind, dsn, tenant, pubAlg, archiveDir, reason, requestedBy string
	var pubSpecs []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Seal a corrupt audit tail and start a governed recovery epoch",
		Long: "recover performs a deny-closed, dual-control ceremony around a corrupt audit tail.\n" +
			"It never repairs, moves or deletes audit rows: it appends an off-box-signed audit.recover\n" +
			"marker that preserves the genesis scar and starts a new verifiable epoch.",
		Example: `  # Report the proposed boundary without appending it (default)
  olivares audit recover --tenant t_abc123 --pubkey "ecdsa-p256-sha256:MFkw..." \
    --reason "tail hash mismatch" --requested-by "svc:ledger-recovery"

  # After the CRITICAL two-person approval is effective, append with confirmation
  olivares audit recover --tenant t_abc123 --pubkey "ecdsa-p256-sha256:MFkw..." \
    --reason "tail hash mismatch" --requested-by "svc:ledger-recovery" --dry-run=false`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			report := auditRecoveryReport{
				Status: "refused", Tenant: t.String(), DryRun: dryRun,
				Runbook: auditRecoveryRunbook,
			}
			refuse := func(problem string) error {
				report.OK = false
				report.Status = "refused"
				report.Problems = append(report.Problems, problem)
				if rerr := printAuditRecoveryReport(cmd, report); rerr != nil {
					return rerr
				}
				return fmt.Errorf("audit recovery refused: %s (follow %s)", problem, auditRecoveryRunbook)
			}

			if deps.boot == nil || deps.gate == nil {
				return refuse("internal recovery dependencies are not configured")
			}
			eng, err := deps.boot(cmd, dataDir, engineKind, dsn)
			if err != nil {
				return refuse("engine boot failed: " + err.Error())
			}
			defer func() { _ = eng.Close() }()

			// 1. The genesis walk must show a real structural break. Recovery is
			// never a way to rotate an otherwise healthy ledger into a new epoch.
			// Custody, not the service door: PROVING a tenant's chain — and repairing
			// it — is custodial work, and it is most needed exactly when service has
			// been withdrawn. Through View an operator could not so much as verify a
			// suspended tenant's ledger without first restoring its service, which is
			// the opposite of what a grace period is for.
			err = eng.store.Custody(cmd.Context(), t, func(sc store.CustodyScope) error {
				var verr error
				report.Chain, verr = sc.Audit().Verify(cmd.Context(), 1)
				return verr
			})
			if err != nil {
				return refuse("verify the ledger from genesis: " + err.Error())
			}
			if report.Chain.BreakAt < 1 {
				return refuse("the ledger has no structural break")
			}

			// 2. Recovery accepts only caller-pinned off-box verification keys. It
			// never falls back to the engine's own key material.
			verifier, pinnedKeys, err := recoveryCheckpointVerifier(pubSpecs, pubAlg)
			if err != nil {
				return refuse(err.Error())
			}
			if verifier == nil || verifier.Empty() {
				return refuse("no pinned off-box checkpoint key was configured")
			}

			// 3. At least one valid off-box checkpoint must precede the break. A
			// young estate without a checkpoint cannot be recovered under A+.
			err = eng.store.Custody(cmd.Context(), t, func(sc store.CustodyScope) error {
				var cerr error
				report.Checkpoints, cerr = audit.VerifyCheckpointsWith(cmd.Context(), sc.Audit(), verifier)
				return cerr
			})
			if err != nil {
				return refuse("verify pinned off-box checkpoints: " + err.Error())
			}
			if !report.Checkpoints.OK || report.Checkpoints.Checkpoints == 0 {
				return refuse(fmt.Sprintf("off-box checkpoints are not valid (count=%d reason=%s)", report.Checkpoints.Checkpoints, report.Checkpoints.Reason))
			}
			if report.Checkpoints.LatestAttestedSeq >= report.Chain.BreakAt {
				return refuse(fmt.Sprintf("latest off-box checkpoint seq %d does not precede break seq %d", report.Checkpoints.LatestAttestedSeq, report.Chain.BreakAt))
			}
			report.ReanchorSeq = report.Checkpoints.LatestAttestedSeq

			// 4. The live signer must itself be off-box, and its exact public key
			// must be one of the caller's pins. Otherwise the command could append
			// a marker that the same pinned verification invocation cannot honor.
			if eng.signer == nil || !eng.signer.OffBoxCheckpoints() || eng.signer.CheckpointKey() == nil {
				return refuse("the engine has no off-box checkpoint signer")
			}
			checkpointKey := eng.signer.CheckpointKey()
			report.OffBoxKeyID = checkpointKey.KeyID()
			currentPub, err := checkpointKey.PublicKey(cmd.Context())
			if err != nil {
				return refuse("read the off-box signer's public key: " + err.Error())
			}
			if !recoveryPinIncludes(pinnedKeys, checkpointKey.Algorithm(), currentPub) {
				return refuse("the pinned keys do not include the engine's current off-box signer")
			}

			// 5. An optional WORM/off-box archive must verify and explicitly cover
			// the complete trusted prefix [1..reanchor_seq].
			if archiveDir != "" {
				opts, pinned, oerr := archiveVerifyOptions(archiveDir, pubAlg, pubSpecs, nil)
				if oerr != nil {
					return refuse("build pinned archive verifier: " + oerr.Error())
				}
				if !pinned || opts.Checkpoints == nil || opts.Checkpoints.Empty() {
					return refuse("off-box archive verification requires a pinned checkpoint key")
				}
				// Recovery's flag contract pins the OFF-BOX checkpoint key only; an
				// Ed25519 checkpoint pin is not implicitly the distinct on-box event
				// key. archiveVerifyOptions supplies the shared parsing/pinning rules,
				// while this check deliberately limits the archive signature proof to
				// its checkpoints plus the always-on structural verification.
				opts.EventKeys = nil
				opts.Checkpoints = verifier
				archiveReport, aerr := audit.VerifyArchiveDir(cmd.Context(), archiveDir, opts)
				report.Archive = &archiveReport
				if aerr != nil {
					return refuse("verify the off-box archive: " + aerr.Error())
				}
				rangeReport, covered := archiveReport.Ranges[t.String()]
				if !archiveReport.OK || !covered || rangeReport.FromSeq != 1 || rangeReport.ToSeq < report.ReanchorSeq {
					return refuse(fmt.Sprintf("off-box archive does not verify and cover [1..%d] for tenant %s", report.ReanchorSeq, t))
				}
			}

			// Snapshot the quarantine evidence before opening/reusing the approval.
			// A second read inside the eventual write transaction closes the TOCTOU
			// window before the marker is appended.
			var quarantineTo int64
			var quarantineDigest string
			err = eng.store.Custody(cmd.Context(), t, func(sc store.CustodyScope) error {
				head, ok, herr := sc.Audit().Head(cmd.Context())
				if herr != nil {
					return herr
				}
				if !ok || head.Seq < report.Chain.BreakAt {
					return fmt.Errorf("corrupt range has no physical tail at or above break seq %d", report.Chain.BreakAt)
				}
				quarantineTo = head.Seq
				quarantineDigest, herr = auditRecoveryRangeSHA256(cmd.Context(), sc.Audit(), report.Chain.BreakAt, head.Seq)
				return herr
			})
			if err != nil {
				return refuse("hash the quarantined range: " + err.Error())
			}

			// 6. The one-shot gate deliberately has no break-glass fallback. Its
			// plan binds exactly the four contract fields and governance floors this
			// action at two distinct human approvers.
			planHash := auditRecoveryPlanHash(t, report.Chain.BreakAt, report.ReanchorSeq, report.OffBoxKeyID)
			ref, status, boundHash, approvers, err := deps.gate(cmd.Context(), eng, t, planHash, reason, requestedBy)
			report.Gate = auditRecoveryGateReport{
				ApprovalRef: ref, Status: status, BoundHash: boundHash, PlanHash: planHash,
				Approvers:    append([]string(nil), approvers.Actors...),
				Persons:      append([]string(nil), approvers.Persons...),
				Unattributed: approvers.Unattributed,
			}
			if err != nil {
				return refuse("dual-control gate failed: " + err.Error())
			}
			if status != nbApproved || boundHash != planHash {
				return refuse(fmt.Sprintf("dual-control approval is not approved and plan-bound (status=%s)", status))
			}
			// The quorum is counted on PEOPLE, never on the credentials: Actor() renders
			// "user:<id>" for a session and "token:<id>" for a token, so one human
			// holding both would otherwise clear a two-human bar alone. This is the
			// OUTER half of the check — audit.validateRecoveryEvidence re-counts the
			// signed Approvers as an inner backstop, but that list is credentials, so it
			// cannot be the quorum. It is also why the signed evidence below keeps
			// carrying the credentials: they are the provenance, and they are inside
			// recoverPreimage, which no already-signed marker may have changed under it.
			credentials := distinctPrincipals(approvers.Actors)
			persons := distinctPrincipals(approvers.Persons)
			report.Gate.Approvers, report.Gate.Persons = credentials, persons
			// Counting accounts, not credentials, is the whole bar: an approval with no
			// account behind it is already absent from Persons, so it can never make up
			// one of the two approvers — it needs no separate refusal, only the report
			// above so the operator can see it happened. Two accounts is the ceiling this
			// binary can verify, and the refusal says so rather than claiming humans.
			if len(persons) < 2 {
				return refuse("dual-control approver evidence has fewer than two distinct approver accounts")
			}

			evidence := audit.RecoveryEvidence{
				Tenant: t.String(), BreakReason: report.Chain.Reason,
				BreakAt: report.Chain.BreakAt, ReanchorSeq: report.ReanchorSeq,
				OffBoxCheckpointSeq: report.ReanchorSeq, OffBoxKeyID: report.OffBoxKeyID,
				QuarantinedFrom: report.Chain.BreakAt, QuarantinedTo: quarantineTo,
				QuarantinedSHA256: quarantineDigest, Approvers: credentials,
				Reason: reason, RequestedBy: requestedBy,
			}
			report.Evidence = &evidence
			if dryRun {
				report.OK = true
				report.Status = "dry_run"
				return printAuditRecoveryReport(cmd, report)
			}
			if !confirm(cmd, fmt.Sprintf("Append the signed audit.recover epoch marker for tenant %s at break seq %d?", t, report.Chain.BreakAt)) {
				return refuse("operator confirmation was not granted")
			}

			// Re-check the plan-bound facts in the same transaction that appends the
			// signed marker. No audit row is modified; a failure rolls back the sole
			// INSERT. The structurally clean epoch starts at recover_seq, not at the
			// historical checkpoint: starting at reanchor_seq would necessarily walk
			// the immutable corrupt rows the marker quarantines.
			var recoveryEvent model.AuditEvent
			err = eng.store.Custody(cmd.Context(), t, func(sc store.CustodyScope) error {
				log := sc.Audit()
				// Freeze this tenant's append tail before any re-check. The sqlstore
				// implements this with the same xact-scoped Postgres advisory lock used
				// by Append (SQLite is already single-writer). The capability is optional
				// so decorators remain compatible; RecordRecovery's post-Append position
				// assertion is the independent safety backstop and rolls back on a race.
				if locker, ok := log.(store.AuditAppendLocker); ok {
					if lerr := locker.LockAppends(cmd.Context()); lerr != nil {
						return fmt.Errorf("lock the tenant audit tail: %w", lerr)
					}
				}
				freshChain, verr := log.Verify(cmd.Context(), 1)
				if verr != nil {
					return verr
				}
				if freshChain.BreakAt != report.Chain.BreakAt || freshChain.Reason != report.Chain.Reason {
					return fmt.Errorf("ledger break changed after approval (was %d/%s, now %d/%s)", report.Chain.BreakAt, report.Chain.Reason, freshChain.BreakAt, freshChain.Reason)
				}
				freshCheckpoints, verr := audit.VerifyCheckpointsWith(cmd.Context(), log, verifier)
				if verr != nil {
					return verr
				}
				if !freshCheckpoints.OK || freshCheckpoints.LatestAttestedSeq != report.ReanchorSeq {
					return fmt.Errorf("off-box checkpoint anchor changed after approval")
				}
				head, ok, verr := log.Head(cmd.Context())
				if verr != nil {
					return verr
				}
				if !ok || head.Seq < freshChain.BreakAt {
					return fmt.Errorf("corrupt range has no physical tail")
				}
				freshDigest, verr := auditRecoveryRangeSHA256(cmd.Context(), log, freshChain.BreakAt, head.Seq)
				if verr != nil {
					return verr
				}
				evidence.QuarantinedTo = head.Seq
				evidence.QuarantinedSHA256 = freshDigest
				recoveryEvent, verr = audit.RecordRecovery(cmd.Context(), log, eng.signer, evidence)
				if verr != nil {
					return verr
				}
				return nil
			})
			if err != nil {
				return refuse("append the recovery marker: " + err.Error())
			}

			// The marker is now durable. A distinct off-box checkpoint must attest
			// that exact new head before the epoch can be called recovered. This is
			// deliberately a second Mutate: a remote KMS failure leaves an explicit
			// marker to retry, never a false success or a rolled-back incident record.
			report.Mutated = true
			report.Evidence = &evidence
			report.RecoverSeq = recoveryEvent.Seq
			report.EpochStartSeq = recoveryEvent.Seq
			_, checkpointed, checkpointErr := eng.signer.Checkpoint(cmd.Context(), eng.store, t)
			if checkpointErr != nil || !checkpointed {
				problem := "recovery marker was appended but its epoch head is not attested; retry `olivares audit checkpoint`"
				if checkpointErr != nil {
					problem += ": " + checkpointErr.Error()
				}
				report.OK = false
				report.Status = "unattested"
				report.Problems = append(report.Problems, problem)
				if rerr := printAuditRecoveryReport(cmd, report); rerr != nil {
					return rerr
				}
				return fmt.Errorf("audit recovery incomplete: %s", problem)
			}

			// Only after notarizing the marker do the epoch proof, exhaustive marker
			// pass and checkpoint freshness check. A copied or forged marker anywhere
			// in the ledger fails this proof even when --from begins at this boundary.
			var proofChain store.VerifyReport
			var proofCheckpoints audit.CheckpointReport
			var proofRecoveries audit.RecoveryMarkerReport
			var recoverySigOK bool
			err = eng.store.Custody(cmd.Context(), t, func(sc store.CustodyScope) error {
				var verr error
				proofChain, verr = sc.Audit().Verify(cmd.Context(), recoveryEvent.Seq)
				if verr != nil {
					return verr
				}
				proofCheckpoints, verr = audit.VerifyCheckpointsWith(cmd.Context(), sc.Audit(), verifier)
				if verr != nil {
					return verr
				}
				proofRecoveries, verr = audit.VerifyRecoveryMarkersWith(cmd.Context(), sc.Audit(), verifier)
				if verr != nil {
					return verr
				}
				found, seq, located, verr := audit.LocateRecoveryEvidence(cmd.Context(), sc.Audit(), verifier)
				if verr != nil {
					return verr
				}
				recoverySigOK = found && seq == recoveryEvent.Seq && located.BreakAt == report.Chain.BreakAt &&
					located.QuarantinedTo == recoveryEvent.Seq-1
				if !proofChain.OK || !proofCheckpoints.OK || proofCheckpoints.LatestAttestedSeq < recoveryEvent.Seq ||
					!proofRecoveries.OK || !recoverySigOK {
					return fmt.Errorf("new recovery epoch failed strict attested proof")
				}
				return nil
			})
			if err != nil {
				return refuse("verify the attested recovery epoch: " + err.Error())
			}

			report.OK = true
			report.Status = "recovered"
			report.Checkpoints = proofCheckpoints
			report.Proof = &auditRecoveryProof{
				OK: proofChain.OK && proofCheckpoints.OK && proofCheckpoints.LatestAttestedSeq >= recoveryEvent.Seq &&
					proofRecoveries.OK && recoverySigOK,
				FromSeq: recoveryEvent.Seq,
				Chain:   proofChain, Checkpoints: proofCheckpoints, RecoveryMarkers: proofRecoveries,
				RecoverySignatureValid: recoverySigOK,
				Command:                fmt.Sprintf("olivares audit verify --tenant %s --from %d --pubkey <pinned-off-box-key> --strict", t, recoveryEvent.Seq),
			}
			return printAuditRecoveryReport(cmd, report)
		},
	}
	addStoreFlags(cmd, &dataDir, &engineKind, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id whose corrupt audit tail will be sealed (default $OLIVARES_TENANT)")
	cmd.Flags().StringArrayVar(&pubSpecs, "pubkey", nil, "required pinned off-box checkpoint public key, repeatable: raw base64 Ed25519 or \"<alg>:<base64 DER SPKI>\"")
	cmd.Flags().StringVar(&pubAlg, "pubkey-alg", "", "algorithm of a SINGLE bare --pubkey (compat form, as in `audit verify`)")
	cmd.Flags().StringVar(&archiveDir, "archive-dir", "", "optional off-box archive directory that must verify and cover the trusted prefix")
	cmd.Flags().StringVar(&reason, "reason", "", "operator reason recorded in the signed recovery evidence")
	cmd.Flags().StringVar(&requestedBy, "requested-by", "", "non-secret requester identity recorded in the signed recovery evidence")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "run every deny-closed check and print the plan without appending the recovery marker")
	_ = cmd.MarkFlagRequired("pubkey")
	return cmd
}

func gateAuditRecovery(ctx context.Context, eng *engine, tenant model.TenantID, planHash, reason, requestedBy string) (ref, status, boundHash string, approvers approverEvidence, err error) {
	if eng == nil || eng.approvalBridge == nil {
		return noGateRefPrefix + planHash, nbNoGate, planHash, approverEvidence{}, nil
	}
	ref, status, boundHash, err = eng.approvalBridge.gateOnceNoBreakGlass(
		ctx, tenant, auditRecoveryAction, "audit_ledger", tenant.String(), planHash, reason, requestedBy,
	)
	if err != nil || status != nbApproved {
		return ref, status, boundHash, approverEvidence{}, err
	}
	if cred, ok := eng.approvalBridge.cred(tenant); ok {
		approvers = eng.approvalBridge.approvalApproverEvidence(ctx, cred, ref)
	}
	return ref, status, boundHash, approvers, nil
}

func recoveryCheckpointVerifier(specs []string, compatAlg string) (*audit.CheckpointVerifier, []recoveryPinnedKey, error) {
	v := audit.NewCheckpointVerifier()
	keys := make([]recoveryPinnedKey, 0, len(specs))
	for _, spec := range specs {
		alg, raw, err := parsePubKeySpec(spec, compatAlg, len(specs))
		if err != nil {
			return nil, nil, err
		}
		if alg == audit.AlgEd25519 {
			if err := v.AddEd25519Raw(raw); err != nil {
				return nil, nil, fmt.Errorf("--pubkey: %w", err)
			}
		} else if err := v.AddPublicKey(alg, raw); err != nil {
			return nil, nil, fmt.Errorf("--pubkey: %w", err)
		}
		keys = append(keys, recoveryPinnedKey{alg: alg, raw: append([]byte(nil), raw...)})
	}
	return v, keys, nil
}

func recoveryPinIncludes(keys []recoveryPinnedKey, alg audit.SigAlg, raw []byte) bool {
	for _, key := range keys {
		if key.alg == alg && bytes.Equal(key.raw, raw) {
			return true
		}
	}
	return false
}

func auditRecoveryPlanHash(tenant model.TenantID, breakAt, reanchorSeq int64, keyID string) string {
	preimage, _ := json.Marshal(struct {
		Tenant      string `json:"tenant"`
		BreakAt     int64  `json:"break_at"`
		ReanchorSeq int64  `json:"reanchor_seq"`
		OffBoxKeyID string `json:"offbox_key_id"`
	}{tenant.String(), breakAt, reanchorSeq, keyID})
	sum := sha256.Sum256(preimage)
	return hex.EncodeToString(sum[:])
}

func auditRecoveryRangeSHA256(ctx context.Context, log store.AuditLog, fromSeq, toSeq int64) (string, error) {
	if fromSeq < 1 || toSeq < fromSeq {
		return "", fmt.Errorf("invalid quarantine range [%d..%d]", fromSeq, toSeq)
	}
	h := sha256.New()
	auditRecoveryHashPart(h, []byte("olivares.audit.quarantine.v1"))
	count := int64(0)
	walk := func(ev model.AuditEvent, meta string) error {
		if ev.Seq > toSeq {
			return nil
		}
		line, err := json.Marshal(struct {
			ID          string `json:"id"`
			Tenant      string `json:"tenant"`
			Seq         int64  `json:"seq"`
			OccurredAt  string `json:"occurred_at"`
			Actor       string `json:"actor"`
			ActorKind   string `json:"actor_kind"`
			Action      string `json:"action"`
			TargetKind  string `json:"target_kind"`
			TargetID    string `json:"target_id"`
			Meta        string `json:"meta"`
			PayloadHash string `json:"payload_hash"`
			PrevHash    string `json:"prev_hash"`
			Hash        string `json:"hash"`
			Sig         string `json:"sig"`
		}{
			ID: ev.ID.String(), Tenant: ev.TenantID.String(), Seq: ev.Seq,
			OccurredAt: ev.OccurredAt.String(), Actor: ev.Actor, ActorKind: ev.ActorKind,
			Action: ev.Action, TargetKind: string(ev.TargetKind), TargetID: ev.TargetID.String(), Meta: meta,
			PayloadHash: base64.StdEncoding.EncodeToString(ev.PayloadHash),
			PrevHash:    base64.StdEncoding.EncodeToString(ev.PrevHash), Hash: base64.StdEncoding.EncodeToString(ev.Hash),
			Sig: base64.StdEncoding.EncodeToString(ev.Sig),
		})
		if err != nil {
			return err
		}
		auditRecoveryHashPart(h, line)
		count++
		return nil
	}
	if canonical, ok := log.(store.CanonicalWalker); ok {
		if err := canonical.WalkCanonical(ctx, fromSeq, func(ev model.AuditEvent, meta string, _ []byte) error { return walk(ev, meta) }); err != nil {
			return "", err
		}
	} else if err := log.Walk(ctx, fromSeq, func(ev model.AuditEvent) error {
		meta, merr := json.Marshal(ev.Meta)
		if merr != nil {
			return merr
		}
		return walk(ev, string(meta))
	}); err != nil {
		return "", err
	}
	if count == 0 {
		return "", fmt.Errorf("quarantine range [%d..%d] contains no physical events", fromSeq, toSeq)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func auditRecoveryHashPart(h hash.Hash, value []byte) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}

func distinctPrincipals(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, principal := range in {
		principal = strings.TrimSpace(principal)
		if principal == "" {
			continue
		}
		if _, ok := seen[principal]; ok {
			continue
		}
		seen[principal] = struct{}{}
		out = append(out, principal)
	}
	return out
}

// printAuditRecoveryReport renders through the shared renderer so -o is honored
// (it printed JSON regardless before), and RETURNS the error rather than
// discarding it: a recovery report that failed to render is a command that said
// nothing about an audit-integrity incident.
func printAuditRecoveryReport(cmd *cobra.Command, report auditRecoveryReport) error {
	return renderReportOut(cmd, report)
}
