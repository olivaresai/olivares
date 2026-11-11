// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/spf13/cobra"
)

func runAuditVerify(cmd *cobra.Command, dataDir, engine, dsn, ownerDSN, tenant, pubAlg string, pubB64s, eventPubB64s []string, fromSeq int64, strict bool) error {
	resolvedTenant, err := resolveTenant(tenant)
	if err != nil {
		return err
	}
	t, err := model.ParseTenantID(resolvedTenant)
	if err != nil {
		return fmt.Errorf("--tenant: %w", err)
	}
	eng, err := auditVerifyBoot(cmd, dataDir, engine, dsn, ownerDSN)
	if err != nil {
		return err
	}
	defer func() { _ = eng.Close() }()

	// Per-event signatures are ALWAYS on-box Ed25519 (the hot path is never
	// routed off-box). The ADVISORY default verifies them against
	// the engine's own key plus the prior generations from the CMEK envelope
	// (`keys rotate`) so a rotated chain self-verifies. When the caller
	// PINS keys (an external, attacker-resistant audit), the pinned set
	// REPLACES the engine's — a pinned check that silently also accepted the
	// on-box key would be a weaker check pretending to be a strong one.
	var pinnedEventKeys []audit.FencedKey
	for _, s := range eventPubB64s {
		fk, perr := parseEventPubKeySpec(s)
		if perr != nil {
			return perr
		}
		pinnedEventKeys = append(pinnedEventKeys, fk)
	}
	eventPinsGiven := len(pinnedEventKeys) > 0

	// Checkpoint verification: by default cover the engine's configured
	// checkpoint key(s) (on-box Ed25519, plus the off-box KMS/HSM key when one
	// is wired) — advisory, since a host-compromise holds those. --pubkey pins
	// EXTERNAL off-box key(s) (the attacker-resistant check, docs/SECURITY-HARDENING.md).
	// Repeatable so a chain whose KMS key ROTATED verifies in one run: each
	// value is a raw base64 Ed25519 key, or "<alg>:<base64 DER SPKI>" for an
	// off-box ECDSA/RSA key (e.g. ecdsa-p256-sha256:MFkw…, an AWS KMS
	// GetPublicKey export). A single bare value may instead pair with
	// --pubkey-alg (the pre form, kept compatible).
	var cpVerifier *audit.CheckpointVerifier
	external := false
	if len(pubB64s) > 0 {
		cpVerifier = audit.NewCheckpointVerifier()
		for _, spec := range pubB64s {
			alg, raw, perr := parsePubKeySpec(spec, pubAlg, len(pubB64s))
			if perr != nil {
				return perr
			}
			switch alg {
			case audit.AlgEd25519:
				if len(raw) != ed25519.PublicKeySize {
					return fmt.Errorf("--pubkey: invalid base64 Ed25519 public key (use \"<alg>:<base64>\" or --pubkey-alg for an off-box ECDSA/RSA DER key)")
				}
				cpVerifier.AddEd25519(ed25519.PublicKey(raw))
				// A pinned Ed25519 key also covers events (the pre
				// advisory case) — and being a PIN, it replaces the on-box default.
				// But if the caller ALSO gave fenced --event-pubkey pins, folding it
				// in here as an UNBOUNDED event key would silently re-widen a retired
				// key the pin just fenced to its epoch (F-07). So add it only when no
				// explicit event pins were given.
				if !eventPinsGiven {
					pinnedEventKeys = append(pinnedEventKeys, audit.FencedKey{Key: ed25519.PublicKey(raw)})
				}
			default:
				if aerr := cpVerifier.AddPublicKey(alg, raw); aerr != nil {
					return fmt.Errorf("--pubkey: %w", aerr)
				}
			}
		}
		external = true
	} else {
		cpVerifier, err = eng.signer.CheckpointVerifier(cmd.Context())
		if err != nil {
			return err
		}
		// Rotated on-box generations also signed past CHECKPOINTS (the
		// on-box default signs both); without the priors as candidates, a
		// healthy rotated chain would report checkpoint-sig-invalid.
		for _, p := range eng.auditPriors {
			cpVerifier.AddEd25519(p)
		}
	}

	// Pins REPLACE the advisory on-box default. Fenced pins (--event-pubkey
	// key@last_seq) restrict a retired generation to its epoch. With no pins the
	// advisory set (current key + the envelope's prior generations) is built
	// INSIDE the View below, deriving each prior's boundary from the in-chain
	// audit.key.rotation markers (F-07).
	advisoryEvents := len(pinnedEventKeys) == 0

	// Custody, not the service door: PROVING a tenant's chain — and repairing
	// it — is custodial work, and it is most needed exactly when service has
	// been withdrawn. Through View an operator could not so much as verify a
	// suspended tenant's ledger without first restoring its service, which is
	// the opposite of what a grace period is for.
	return eng.ledger.ViewAudit(cmd.Context(), t, func(ledger store.AuditLog) error {
		rep, err := ledger.Verify(cmd.Context(), fromSeq)
		if err != nil {
			return err
		}
		cr, err := audit.VerifyCheckpointsWith(cmd.Context(), ledger, cpVerifier)
		if err != nil {
			return err
		}
		// Recovery markers are skipped by the ordinary on-box event-signature
		// pass because they have their own off-box signature domain. Verify every
		// marker here, including markers outside --from: absence is neutral, but
		// any present invalid or displaced marker is an integrity failure. Without
		// --pubkey this uses engine-held keys and is advisory, like checkpoints.
		rr, err := audit.VerifyRecoveryMarkersWith(cmd.Context(), ledger, cpVerifier)
		if err != nil {
			return err
		}
		// F-07: every signing-key epoch boundary (audit.key.rotation) must
		// carry a valid off-box signature and sit at the sequence it declares.
		// Fail-closed, like recovery markers: a forged boundary anywhere fails.
		kr, err := audit.VerifyKeyRotationMarkersWith(cmd.Context(), ledger, cpVerifier)
		if err != nil {
			return err
		}
		// Build the epoch-fenced event key set. Advisory: the current key
		// (unbounded) plus each prior generation FENCED to the boundary its
		// in-chain audit.key.rotation marker declares (0 = unfenced when no marker
		// names it — honestly advisory). Pinned: exactly the caller's fenced pins.
		eventKeys := pinnedEventKeys
		if advisoryEvents {
			fences, ferr := audit.LocateKeyFences(cmd.Context(), ledger, cpVerifier)
			if ferr != nil {
				return ferr
			}
			eventKeys = []audit.FencedKey{{Key: eng.signer.PublicKey()}}
			for _, p := range eng.auditPriors {
				eventKeys = append(eventKeys, audit.FencedKey{Key: p, LastSeq: fences[audit.KeyFingerprint(p)]})
			}
		}
		// Per-event signatures: every non-checkpoint event must verify against
		// the key whose epoch owns its sequence (signing is on by default).
		// Signed==Events proves the tail was not rewritten or stripped even
		// between checkpoints, AND a retired key cannot validate outside its epoch.
		er, err := audit.VerifyEventsFenced(cmd.Context(), ledger, fromSeq, eventKeys)
		if err != nil {
			return err
		}

		// Three answers, not two. "corrupt" is an accusation, and a ledger that
		// nobody has attested YET has not earned it: a freshly installed engine
		// reported `"status": "corrupt"` here (measured on a clean install
		// before the checkpoint scheduler fires), which is exactly how an
		// operator learns the word means nothing. "unattested" is the same word
		// `audit recover` already uses for "no checkpoint covers this".
		//
		// --strict is deliberately NOT relaxed: "unattested" still exits
		// non-zero, so no automation gate loses detection power. Only the word
		// changes — and with it the message that explains why.
		status := "corrupt"
		otherChecksOK := rep.OK && er.OK && rr.OK && kr.OK
		switch {
		case otherChecksOK && cr.OK:
			status = "ok"
		case otherChecksOK && cr.Status() == audit.CheckpointStatusPending:
			status = "unattested"
		}
		var recovery map[string]any
		if external {
			found, recoverSeq, evidence, lerr := audit.LocateRecoveryEvidence(cmd.Context(), ledger, cpVerifier)
			if lerr != nil {
				return lerr
			}
			// An explicit --from at the signed boundary is a green current-epoch
			// proof. A just-created epoch legitimately has no ordinary on-box event
			// signatures yet: the recovery marker's off-box signature is the event.
			if found && fromSeq == recoverSeq && evidence.QuarantinedTo == recoverSeq-1 &&
				rep.OK && cr.OK && cr.LatestAttestedSeq >= recoverSeq && rr.OK && (er.OK || er.Events == 0) {
				status = "epoch_ok"
				recovery = map[string]any{
					"recover_seq": recoverSeq, "reanchor_seq": evidence.ReanchorSeq,
					"approvers": evidence.Approvers, "epoch_start_seq": recoverSeq,
					"epoch_chain": rep, "epoch_event_sigs": er,
				}
			}
			// A valid signature is necessary but not sufficient: the latest marker
			// must describe this exact permanent genesis-walk scar, and the epoch
			// beginning at the marker must itself still be structurally clean. A
			// later fresh corruption therefore remains "corrupt", never hidden by
			// an older recovery marker.
			if rep.BreakAt > 0 && found && evidence.BreakAt == rep.BreakAt && evidence.BreakReason == rep.Reason &&
				evidence.QuarantinedFrom == rep.BreakAt && evidence.QuarantinedTo == recoverSeq-1 &&
				cr.LatestAttestedSeq >= recoverSeq && rr.OK {
				epochChain, eerr := ledger.Verify(cmd.Context(), recoverSeq)
				if eerr != nil {
					return eerr
				}
				epochEvents, eerr := audit.VerifyEventsFenced(cmd.Context(), ledger, recoverSeq, eventKeys)
				if eerr != nil {
					return eerr
				}
				if epochChain.OK && cr.OK && (epochEvents.OK || epochEvents.Events == 0) {
					status = "recovered"
					recovery = map[string]any{
						"recover_seq": recoverSeq, "reanchor_seq": evidence.ReanchorSeq,
						"approvers": evidence.Approvers, "epoch_start_seq": recoverSeq,
						"epoch_chain": epochChain, "epoch_event_sigs": epochEvents,
					}
				}
			}
		}
		// E2: the report is a value, rendered through renderOut.
		report := map[string]any{
			"status":               status,
			"from_seq":             fromSeq,
			"chain":                rep,
			"checkpoints":          cr,
			"event_sigs":           er,
			"recovery_markers":     rr,
			"key_rotation_markers": kr,
			"recovery":             recovery,
			"checkpoint_key":       map[string]any{"external": external, "advisory_only": !external, "off_box_signer": eng.signer.OffBoxCheckpoints()},
			"recovery_key":         map[string]any{"external": external, "advisory_only": !external},
			"event_keys":           map[string]any{"candidates": len(eventKeys), "advisory_only": advisoryEvents, "prior_generations": len(eng.auditPriors), "fenced": eventPinsGiven || advisoryEvents},
		}
		if err := renderReportOut(cmd, report); err != nil {
			return err
		}
		// Default behavior prints the report and exits 0 (status is in the
		// JSON). --strict turns a failed integrity check into a non-zero exit
		// so an on-call cron / CI job can gate on $? instead of having to parse
		// JSON — a green `verify && echo OK` must NOT lie about a tampered chain.
		// A missing signed checkpoint leaves strict verification indeterminate.
		// This state lasts until a checkpoint exists; it does not establish tampering.
		if strict && status == "unattested" {
			return exitcode.New(exitcode.Indeterminate, fmt.Errorf("audit ledger NOT ATTESTED (--strict): chain, per-event signatures and markers all verified, but no signed checkpoint exists yet (%s) — run `olivares audit checkpoint` or let the scheduler fire; this is NOT evidence of tampering", cr.Reason))
		}
		// Recovered and corrupt states retain the established failure outcome.
		if strict && status == "recovered" {
			return fmt.Errorf("audit integrity incident RECOVERED (--strict): genesis chain remains broken at seq %d (%s), current epoch starts at seq %d — see the JSON report above", rep.BreakAt, rep.Reason, recovery["epoch_start_seq"])
		}
		if strict && status != "ok" && status != "epoch_ok" {
			return fmt.Errorf("audit integrity check FAILED (--strict): chain.OK=%v checkpoints.OK=%v event_sigs.OK=%v recovery_markers.OK=%v key_rotation_markers.OK=%v — see the JSON report above", rep.OK, cr.OK, er.OK, rr.OK, kr.OK)
		}
		return nil
	})
}

const auditVerificationAvailable = true

// parseEventPubKeySpec parses one --event-pubkey occurrence into an epoch-fenced
// key (E2). The forms are:
//
//	<b64>            the CURRENT key: no upper bound (signs the tail)
//	<b64>@<last>     a retired generation: valid only for seq <= last
//	<b64>@<lo>:<hi>  an explicit window: valid only for lo <= seq <= hi
//
// A pinned range is what lets an external verifier fence a retired key to the
// sequence it last legitimately signed instead of trusting it forever.
func parseEventPubKeySpec(spec string) (audit.FencedKey, error) {
	spec = strings.TrimSpace(spec)
	b64 := spec
	var firstSeq, lastSeq int64
	if at := strings.IndexByte(spec, '@'); at >= 0 {
		b64 = strings.TrimSpace(spec[:at])
		rng := strings.TrimSpace(spec[at+1:])
		if colon := strings.IndexByte(rng, ':'); colon >= 0 {
			lo, lerr := strconv.ParseInt(strings.TrimSpace(rng[:colon]), 10, 64)
			hi, herr := strconv.ParseInt(strings.TrimSpace(rng[colon+1:]), 10, 64)
			if lerr != nil || herr != nil || lo < 1 || hi < lo {
				return audit.FencedKey{}, fmt.Errorf("--event-pubkey: invalid range %q (want @<lo>:<hi> with 1<=lo<=hi)", rng)
			}
			firstSeq, lastSeq = lo, hi
		} else {
			last, perr := strconv.ParseInt(rng, 10, 64)
			if perr != nil || last < 1 {
				return audit.FencedKey{}, fmt.Errorf("--event-pubkey: invalid boundary %q (want @<last_seq> with last_seq>=1)", rng)
			}
			lastSeq = last
		}
	}
	raw, derr := base64.StdEncoding.DecodeString(b64)
	if derr != nil || len(raw) != ed25519.PublicKeySize {
		return audit.FencedKey{}, fmt.Errorf("--event-pubkey: invalid base64 Ed25519 public key")
	}
	return audit.FencedKey{Key: ed25519.PublicKey(raw), FirstSeq: firstSeq, LastSeq: lastSeq}, nil
}
