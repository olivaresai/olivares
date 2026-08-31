// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// newAuditCmd offers offline operations on the evidence ledger: verify the chain
// + signed checkpoints, write a new checkpoint, export to a SIEM format, and
// export/verify the immutable archive.
func newAuditCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "audit",
		Short: "Inspect and checkpoint the evidence ledger",
		Long: "audit is how the evidence ledger is proved, anchored and handed over: verify a\n" +
			"tenant's hash chain against its signed checkpoints, write a new checkpoint,\n" +
			"export the ledger to a SIEM format or to a verifiable offline archive, record a\n" +
			"signing-key epoch boundary, and — after a corruption — seal the bad tail and\n" +
			"open a governed recovery epoch.\n\n" +
			"Reading the ledger is read-only and never mints keys or writes to the data dir.",
		Example: "  olivares audit verify --tenant t_abc123\n" +
			"  olivares audit checkpoint --tenant t_abc123\n" +
			"  olivares audit export --tenant t_abc123 --format cef",
	}
	root.AddCommand(auditVerifyCmd(), auditRecoverCmd(), auditKeyTransitionCmd(), auditCheckpointCmd(), auditExportCmd(), auditArchiveCmd(), auditObserveReportCmd())
	return root
}

// auditBoot wires a minimal engine (store + signer) for an audit CLI command
// that MUTATES state (checkpoint, key-transition, recover, secrets put/rm, …).
// It initializes the data directory when absent, exactly as `serve` does.
func auditBoot(cmd *cobra.Command, dataDir, engine, dsn string) (*engine, error) {
	return auditBootCrossTenant(cmd, dataDir, engine, dsn, "")
}

// auditBootCrossTenant is auditBoot for the ceremonies that ENUMERATE TENANTS, and
// it exists because auditBoot could not carry an --admin-dsn at all.
//
// On Postgres, a cross-tenant System read runs RLS-limited unless a dedicated
// BYPASSRLS admin pool is configured, and store.SystemScope.ListOrgs now refuses to
// report such a read as the whole estate. A ceremony booted WITHOUT the admin DSN
// therefore fails closed rather than sweeping a short list — correct, but useless:
// the operator has no flag to give it the pool it needs. This is that flag's wiring.
//
// `serve` has carried --admin-dsn since R2; the audit CLI never did, so
// `audit key-transition` on a hardened Postgres could not cover the estate even when
// the admin role existed.
func auditBootCrossTenant(cmd *cobra.Command, dataDir, engine, dsn, adminDSN string) (*engine, error) {
	return boot(cmd.Context(), bootConfig{
		DataDir: dataDir, Engine: engine, DSN: dsn, AdminDSN: adminDSN,
		Version: version, Logger: slog.Default(),
		NoImplicitInstall: true,
	})
}

// auditBootRO is the same wiring for a command that only READS: it creates no
// data directory, mints no signing key and creates no store file, so running one
// in a directory that holds no installation reports NotFound instead of building
// one there. Everything that lists, shows, verifies or exports uses this.
func auditBootRO(cmd *cobra.Command, dataDir, engine, dsn string) (*engine, error) {
	return boot(cmd.Context(), bootConfig{
		DataDir: dataDir, Engine: engine, DSN: dsn, Version: version, Logger: slog.Default(),
		ReadOnly: true,
	})
}

// rosterReadBoot is auditBootRO for a command that only reads the SOURCE ROSTER
// (`sources plan`/`validate`/`test`, and anything later that previews).
//
// It adds NoIngest, and the difference is not a detail. auditBootRO's read-only
// stance stops the boot MANUFACTURING an installation; it does not stop the rest
// of the boot, and the rest of the boot starts the runtime and reconciles the
// roster — which PREPARES, OPENS and WIRES every enabled connector. A command
// whose whole promise is "this changes nothing" cannot dial a dozen third-party
// systems on the way to printing a diff.
//
// What this still does NOT do, stated because the alternative is a comforting
// half-truth: the store still opens and migrates, leadership still bootstraps,
// and absent sealer keys are still created. Those writes belong to boot() itself
// and are the same for every read-only CLI verb in this binary; closing them
// needs a genuinely minimal read path, not another flag. It is written up as a
// named, reproduced defect in the session record rather than left for
// somebody to rediscover.
func rosterReadBoot(cmd *cobra.Command, dataDir, engine, dsn string) (*engine, error) {
	return boot(cmd.Context(), bootConfig{
		DataDir: dataDir, Engine: engine, DSN: dsn, Version: version, Logger: slog.Default(),
		ReadOnly: true, NoIngest: true,
	})
}

func auditVerifyCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, pubAlg string
	var pubB64s, eventPubB64s []string
	var fromSeq int64
	var strict bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a tenant's chain and its signed checkpoints",
		Long: "verify checks a tenant's audit hash chain, signed checkpoints and per-event signatures.\n" +
			"Without pinned keys the check is advisory; pass off-box public keys and --strict for an\n" +
			"attacker-resistant automation gate that exits non-zero on any integrity failure.",
		Example: `  # Advisory verification against the engine's own keys
  olivares audit verify --tenant t_abc123

  # Attacker-resistant verification with a pinned off-box public key
  olivares audit verify --tenant t_abc123 --pubkey "ecdsa-p256-sha256:MFkw..." --strict

  # Verify a Postgres-backed ledger
  olivares audit verify --tenant t_abc123 --engine postgres --dsn "env:DATABASE_URL"`,
		Args: cobra.NoArgs,
		// The JSON report always prints; usage noise on a --strict integrity
		// failure would only obscure it, so suppress the cobra usage dump.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
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
			return eng.store.Custody(cmd.Context(), t, func(sc store.CustodyScope) error {
				rep, err := sc.Audit().Verify(cmd.Context(), fromSeq)
				if err != nil {
					return err
				}
				cr, err := audit.VerifyCheckpointsWith(cmd.Context(), sc.Audit(), cpVerifier)
				if err != nil {
					return err
				}
				// Recovery markers are skipped by the ordinary on-box event-signature
				// pass because they have their own off-box signature domain. Verify every
				// marker here, including markers outside --from: absence is neutral, but
				// any present invalid or displaced marker is an integrity failure. Without
				// --pubkey this uses engine-held keys and is advisory, like checkpoints.
				rr, err := audit.VerifyRecoveryMarkersWith(cmd.Context(), sc.Audit(), cpVerifier)
				if err != nil {
					return err
				}
				// F-07: every signing-key epoch boundary (audit.key.rotation) must
				// carry a valid off-box signature and sit at the sequence it declares.
				// Fail-closed, like recovery markers: a forged boundary anywhere fails.
				kr, err := audit.VerifyKeyRotationMarkersWith(cmd.Context(), sc.Audit(), cpVerifier)
				if err != nil {
					return err
				}
				// Build the epoch-fenced event key set. Advisory: the current key
				// (unbounded) plus each prior generation FENCED to the boundary its
				// in-chain audit.key.rotation marker declares (0 = unfenced when no marker
				// names it — honestly advisory). Pinned: exactly the caller's fenced pins.
				eventKeys := pinnedEventKeys
				if advisoryEvents {
					fences, ferr := audit.LocateKeyFences(cmd.Context(), sc.Audit(), cpVerifier)
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
				er, err := audit.VerifyEventsFenced(cmd.Context(), sc.Audit(), fromSeq, eventKeys)
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
					found, recoverSeq, evidence, lerr := audit.LocateRecoveryEvidence(cmd.Context(), sc.Audit(), cpVerifier)
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
						epochChain, eerr := sc.Audit().Verify(cmd.Context(), recoverSeq)
						if eerr != nil {
							return eerr
						}
						epochEvents, eerr := audit.VerifyEventsFenced(cmd.Context(), sc.Audit(), recoverSeq, eventKeys)
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
				// An unattested ledger still fails --strict (the anchor an automation
				// gate asked for does not exist), but it is not accused of tampering:
				// everything that COULD be verified verified.
				if strict && status == "unattested" {
					return fmt.Errorf("audit ledger NOT ATTESTED (--strict): chain, per-event signatures and markers all verified, but no signed checkpoint exists yet (%s) — run `olivares audit checkpoint` or let the scheduler fire; this is NOT evidence of tampering", cr.Reason)
				}
				if strict && status == "recovered" {
					return fmt.Errorf("audit integrity incident RECOVERED (--strict): genesis chain remains broken at seq %d (%s), current epoch starts at seq %d — see the JSON report above", rep.BreakAt, rep.Reason, recovery["epoch_start_seq"])
				}
				if strict && status != "ok" && status != "epoch_ok" {
					return fmt.Errorf("audit integrity check FAILED (--strict): chain.OK=%v checkpoints.OK=%v event_sigs.OK=%v recovery_markers.OK=%v key_rotation_markers.OK=%v — see the JSON report above", rep.OK, cr.OK, er.OK, rr.OK, kr.OK)
				}
				return nil
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id to verify (default $OLIVARES_TENANT)")
	cmd.Flags().Int64Var(&fromSeq, "from", 1, "first sequence of the structural walk (a recovered epoch begins at its recover_seq; genesis remains the default)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero if any integrity check fails (chain/checkpoints/event_sigs); for on-call cron/CI. The default exits 0 and reports status only in the JSON")
	cmd.Flags().StringArrayVar(&pubB64s, "pubkey", nil, "checkpoint public key pin, repeatable (key rotation): raw base64 Ed25519, or \"<alg>:<base64 DER SPKI>\" for an off-box key (default: the engine's own keys — advisory only; pin OFF-BOX keys for an attacker-resistant check, docs/SECURITY-HARDENING.md §5)")
	cmd.Flags().StringVar(&pubAlg, "pubkey-alg", "", "algorithm of a SINGLE bare --pubkey (compat form): ed25519 (raw, default) | ecdsa-p256-sha256 | ecdsa-p384-sha384 | rsa-pkcs1-sha256 | rsa-pss-sha256 (DER SubjectPublicKeyInfo); with multiple --pubkey use the \"<alg>:<base64>\" form")
	cmd.Flags().StringArrayVar(&eventPubB64s, "event-pubkey", nil, "per-event Ed25519 public key pin, repeatable (raw base64), optionally epoch-FENCED as \"<base64>@<last_seq>\" (retired generation, valid only up to that sequence) or \"<base64>@<lo>:<hi>\" (explicit window); a bare key is the current key. Pins REPLACE the advisory defaults — pin EVERY generation with its boundary (`keys status` lists prior_public_keys; the boundary is the audit.key.rotation marker's prior_last_seq). Without a boundary a retired key is trusted for every sequence")
	_ = cmd.RegisterFlagCompletionFunc("pubkey-alg", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"ed25519", "ecdsa-p256-sha256", "ecdsa-p384-sha384", "rsa-pkcs1-sha256", "rsa-pss-sha256"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

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

// parsePubKeySpec parses one --pubkey occurrence: "<alg>:<base64>" names its
// algorithm inline; a bare base64 value is Ed25519 unless the single-key compat
// flag --pubkey-alg overrides it (only unambiguous with exactly one --pubkey).
func parsePubKeySpec(spec, compatAlg string, n int) (audit.SigAlg, []byte, error) {
	spec = strings.TrimSpace(spec)
	if i := strings.IndexByte(spec, ':'); i > 0 {
		alg := audit.SigAlg(strings.TrimSpace(spec[:i]))
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(spec[i+1:]))
		if err != nil {
			return "", nil, fmt.Errorf("--pubkey %q: invalid base64 after the algorithm prefix", spec[:i])
		}
		return alg, raw, nil
	}
	raw, err := base64.StdEncoding.DecodeString(spec)
	if err != nil {
		return "", nil, fmt.Errorf("--pubkey: invalid base64")
	}
	if compatAlg == "" {
		return audit.AlgEd25519, raw, nil
	}
	if n > 1 {
		return "", nil, fmt.Errorf("--pubkey-alg is ambiguous with multiple --pubkey values — use the \"<alg>:<base64>\" form per key")
	}
	return audit.SigAlg(strings.TrimSpace(compatAlg)), raw, nil
}

func auditCheckpointCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant string
	cmd := &cobra.Command{
		Use:     "checkpoint",
		Short:   "Write a signed checkpoint (all tenants, or one with --tenant)",
		Long:    "checkpoint appends a signed audit checkpoint for one tenant, or for every tenant when --tenant is omitted.",
		Example: "  olivares audit checkpoint --tenant t_abc123",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			// -o/--output IS Honored (2026-08-05). This RunE used to Fprintln its
			// three outcomes and return, so `audit checkpoint -o json` printed prose
			// and exited 0 — a caller piping it to jq got a parse error from a
			// command that reported success. The three outcomes are DISTINCT and stay
			// distinct in the JSON: an empty chain is not a written checkpoint, and
			// neither is the all-tenants sweep, which reports no sequence at all.
			if tenant == "" {
				if err := eng.signer.CheckpointAll(cmd.Context(), eng.store); err != nil {
					return err
				}
				return renderOut(cmd, func(out io.Writer) error {
					_, err := fmt.Fprintln(out, "checkpointed all tenants")
					return err
				}, map[string]any{"scope": "all-tenants", "outcome": "checkpointed"})
			}
			t, err := model.ParseTenantID(tenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			ev, ok, err := eng.signer.Checkpoint(cmd.Context(), eng.store, t)
			if err != nil {
				return err
			}
			if !ok {
				return renderOut(cmd, func(out io.Writer) error {
					_, err := fmt.Fprintln(out, "empty chain; nothing to checkpoint")
					return err
				}, map[string]any{"scope": "tenant", "tenant": tenant, "outcome": "empty-chain"})
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "checkpoint written at seq %d\n", ev.Seq)
				return err
			}, map[string]any{"scope": "tenant", "tenant": tenant, "outcome": "checkpointed", "seq": ev.Seq})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (empty = all tenants)")
	return cmd
}

func auditExportCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, format string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a tenant's ledger to a SIEM format (" + audit.FormatList() + ")",
		Long: "export walks one tenant's audit ledger in sequence order and writes each event to stdout in " +
			"ArcSight CEF, IBM QRadar LEEF 2.0, RFC 5424 syslog, a complete OTLP/HTTP export request " +
			"(otlp; otlp_envelope is an exact alias), the bare OTLP LogRecord projection " +
			"(otlp_log_record, one LogRecord JSON per line, not postable) or OCSF JSON. Every format " +
			"carries the chain-integrity fields verbatim, so the copy re-verifies offline.",
		Example: `  # Export as CEF (ArcSight-compatible) to stdout
  olivares audit export --tenant t_abc123 --format cef

  # Export as syslog and pipe to a file
  olivares audit export --tenant t_abc123 --format syslog > /var/log/olivares-audit.log

  # Export as LEEF 2.0 for a QRadar collector
  olivares audit export --tenant t_abc123 --format leef

  # Export postable OTLP/HTTP request bodies (one per line) for a collector
  olivares audit export --tenant t_abc123 --format otlp

  # Export the bare OTLP LogRecord projection (NDJSON, not postable)
  olivares audit export --tenant t_abc123 --format otlp_log_record

  # Export as OCSF from a Postgres store
  olivares audit export --tenant t_abc123 --format ocsf --engine postgres --dsn "env:DATABASE_URL"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			f := audit.Format(format)
			if !audit.ValidFormat(f) {
				return fmt.Errorf("unknown --format %q (%s)", format, audit.FormatList())
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			out := cmd.OutOrStdout()
			return eng.store.Custody(cmd.Context(), t, func(sc store.CustodyScope) error {
				return sc.Audit().Walk(cmd.Context(), 1, func(ev model.AuditEvent) error {
					line, ferr := audit.FormatEvent(ev, f)
					if ferr != nil {
						return ferr
					}
					_, werr := fmt.Fprintln(out, line)
					return werr
				})
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id to export (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&format, "format", string(audit.DefaultFormat()),
		"export format: "+audit.FormatList()+
			" (this selects the SIEM export format and is fully supported — it is not the "+
			"deprecated -o/--output alias other commands spell the same way)")
	_ = cmd.RegisterFlagCompletionFunc("format", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		// Derived from the engine's registry, not copied from it: a literal here is
		// exactly how the CLI came to hide two working formats from every operator.
		known := audit.Formats()
		out := make([]string, len(known))
		for i, f := range known {
			out[i] = string(f)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// auditArchiveCmd groups the archival export/verify operations (§8.3):
// the JSONL+manifest segment format an external WORM copy re-verifies offline.
func auditArchiveCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "archive",
		Short: "Export and verify the immutable ledger archive",
		Long: "archive writes the ledger out as self-describing JSONL segments plus a signed\n" +
			"manifest, and verifies such a directory again later.\n\n" +
			"The point of the split is that verify needs neither the store nor the network:\n" +
			"a WORM copy on someone else's media can be re-proved years afterwards with\n" +
			"nothing but the binary and the archive itself.",
		Example: "  olivares audit archive export --tenant t_abc123 --out /mnt/worm/audit/t_abc123\n" +
			"  olivares audit archive verify --dir /mnt/worm/audit/t_abc123 --strict",
	}
	root.AddCommand(auditArchiveExportCmd(), auditArchiveVerifyCmd())
	// enterprise-only archive subcommands (the examiner-grade `bundle`). The default
	// (AGPL) build adds none (enterpriseArchiveCommands returns nil in wire_noenterprise.go),
	// so the open export/verify subcommands are unchanged — no rug-pull.
	root.AddCommand(enterpriseArchiveCommands()...)
	return root
}

func auditArchiveExportCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, out string
	var fromSeq int64
	var segmentEvents int
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a tenant's ledger as verifiable archive segments to a directory",
		Long: "export writes immutable JSONL audit segments, manifests and advisory verification keys\n" +
			"to a directory suitable for external WORM storage. Use --from-seq to resume an export.",
		Example: "  olivares audit archive export --tenant t_abc123 --out /mnt/worm/audit/t_abc123",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			sink, err := audit.NewDirSink(out)
			if err != nil {
				return err
			}
			// The operator/air-gap export path: read-only against the ledger. The
			// in-chain audit.archive.segment anchors are the engine archival LOOP's
			// job, which mutates the chain it drains; an offline export
			// must not.
			rep, err := audit.ExportSegments(cmd.Context(), eng.store, t, sink,
				audit.ExportOptions{FromSeq: fromSeq, SegmentEvents: segmentEvents}, nil)
			if err != nil {
				return err
			}
			// Advisory keys.json: the engine's own public keys so a casual verify
			// works out of the box. Verifier pins REPLACE these (an archive written
			// by a compromised host carries the attacker's keys, docs/SECURITY-HARDENING.md) — so a
			// write failure is a WARNING, never a failed export (the §8.5 loop's
			// writeKeysOnce posture): on a re-export into an existing --out the
			// WORM dir sink refuses keys.json (its created_at differs) while the
			// re-put segments, being byte-identical, are absorbed.
			eventKeys := []string{base64.StdEncoding.EncodeToString(eng.signer.PublicKey())}
			for _, p := range eng.auditPriors {
				eventKeys = append(eventKeys, base64.StdEncoding.EncodeToString(p))
			}
			checkpointKeys := append([]string(nil), eventKeys...) // on-box signs checkpoints too
			if ck := eng.signer.CheckpointKey(); ck != nil {
				raw, kerr := ck.PublicKey(cmd.Context())
				if kerr != nil {
					return fmt.Errorf("off-box checkpoint public key: %w", kerr)
				}
				spec := base64.StdEncoding.EncodeToString(raw)
				if ck.Algorithm() != audit.AlgEd25519 {
					spec = string(ck.Algorithm()) + ":" + spec
				}
				checkpointKeys = append(checkpointKeys, spec)
			}
			keysWritten := true
			if _, err := audit.WriteArchiveKeys(cmd.Context(), sink, audit.ArchiveKeys{
				EventPubKeys: eventKeys, CheckpointKeys: checkpointKeys,
				CreatedAt: model.SystemClock{}.Now().String(),
			}); err != nil {
				keysWritten = false
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: advisory %s not written (the export itself succeeded; verifier pins replace it anyway): %v\n", audit.ArchiveKeysName, err)
			}
			// E2: honor -o instead of always printing JSON.
			return renderReportOut(cmd, map[string]any{
				"export": rep,
				"out":    sink.Root(),
				"keys":   map[string]any{"file": audit.ArchiveKeysName, "advisory_only": true, "written": keysWritten},
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id to export (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&out, "out", "", "output directory (files are written read-only; WORM when the substrate is)")
	cmd.Flags().Int64Var(&fromSeq, "from-seq", 1, "first sequence number to export (resume an earlier export at its last to_seq+1)")
	cmd.Flags().IntVar(&segmentEvents, "segment-events", audit.DefaultSegmentEvents, "maximum events per segment")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func auditArchiveVerifyCmd() *cobra.Command {
	var dir, pubAlg string
	var pubSpecs, eventPubB64s []string
	var strict bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify an exported archive directory offline (no store, no network)",
		Long: "verify checks an exported archive's segment hashes, chain continuity and signatures without\n" +
			"opening a store or using the network. Pin trusted keys and use --strict for an automation gate.",
		Example: "  olivares audit archive verify --dir /mnt/worm/audit/t_abc123 --strict",
		Args:    cobra.NoArgs,
		// As in `audit verify`: the JSON report always prints; suppress the cobra
		// usage dump on a --strict integrity failure.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, pinned, err := archiveVerifyOptions(dir, pubAlg, pubSpecs, eventPubB64s)
			if err != nil {
				return err
			}
			rep, err := audit.VerifyArchiveDir(cmd.Context(), dir, opts)
			if err != nil {
				return err
			}
			// E2 (sol-max contrast): this sibling of `archive export` was
			// still marshaling directly, so `-o text` did nothing here.
			if rerr := renderReportOut(cmd, map[string]any{
				"archive": rep,
				"event_keys": map[string]any{
					"candidates": len(opts.EventKeys), "pinned": pinned,
					"advisory_only": !pinned && len(opts.EventKeys) > 0,
					"checked":       len(opts.EventKeys) > 0,
				},
				"checkpoint_keys": map[string]any{
					"pinned": pinned, "advisory_only": !pinned && opts.Checkpoints != nil,
					"checked": opts.Checkpoints != nil,
				},
			}); rerr != nil {
				return rerr
			}
			// Same exit-code contract as `audit verify`: default exits 0 (status is
			// in the JSON); --strict gates $? for cron/CI.
			if strict && !rep.OK {
				return fmt.Errorf("archive integrity check FAILED (--strict): reason=%s segment=%s seq=%d — see the JSON report above", rep.Reason, rep.BreakSegment, rep.BreakAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "archive directory to verify (the export's --out)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero if the archive fails to verify; for on-call cron/CI. The default exits 0 and reports status only in the JSON")
	cmd.Flags().StringArrayVar(&pubSpecs, "pubkey", nil, "checkpoint public key pin, repeatable: raw base64 Ed25519, or \"<alg>:<base64 DER SPKI>\" for an off-box key. Pins REPLACE the archive's advisory keys.json (docs/SECURITY-HARDENING.md §5)")
	cmd.Flags().StringVar(&pubAlg, "pubkey-alg", "", "algorithm of a SINGLE bare --pubkey (compat form, as in `audit verify`)")
	_ = cmd.RegisterFlagCompletionFunc("pubkey-alg", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"ed25519", "ecdsa-p256-sha256", "ecdsa-p384-sha384", "rsa-pkcs1-sha256", "rsa-pss-sha256"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringArrayVar(&eventPubB64s, "event-pubkey", nil, "per-event Ed25519 public key pin, repeatable (raw base64), optionally epoch-FENCED as \"<base64>@<last_seq>\" (retired generation, valid only up to that sequence) or \"<base64>@<lo>:<hi>\" (explicit window); a bare key is the current generation. Pins REPLACE the archive's advisory keys.json — pin EVERY generation with its boundary (the audit.key.rotation marker's prior_last_seq) for the attacker-resistant fenced check; without a boundary a retired key is trusted for every sequence")
	_ = cmd.MarkFlagRequired("dir")
	return cmd
}

// archiveVerifyOptions resolves the pinned (or, absent pins, the archive's advisory keys.json)
// verification keys into ArchiveVerifyOptions. It is the SINGLE source of truth for the
// security-critical key-pinning logic shared by `audit archive verify` and the enterprise
// `audit archive bundle` command. pinned reports whether the caller pinned keys (vs the
// advisory keys.json fallback). Pins REPLACE the archive's keys.json — a keys file that rode
// with the archive proves nothing against whoever wrote it (docs/SECURITY-HARDENING.md).
func archiveVerifyOptions(dir, pubAlg string, pubSpecs, eventPubB64s []string) (audit.ArchiveVerifyOptions, bool, error) {
	var opts audit.ArchiveVerifyOptions
	pinned := len(pubSpecs) > 0 || len(eventPubB64s) > 0
	// Event pins without a checkpoint pin would leave every checkpoint line unverifiable
	// (the verifier fails them, "checkpoint-unverifiable"): an attacker who re-derives a
	// forged chain could dress events as checkpoints to dodge the per-event check. Refuse up
	// front with the fix. Pinning NO keys remains the advisory chain-structure-only mode.
	if len(eventPubB64s) > 0 && len(pubSpecs) == 0 {
		return opts, pinned, fmt.Errorf("--event-pubkey without --pubkey: checkpoint lines would be unverifiable (a forged archive could dress events as checkpoints); pin BOTH the event key(s) and the checkpoint key(s), or pin none for the structural advisory mode")
	}
	eventPinsGiven := len(eventPubB64s) > 0
	cpSpecs, evSpecs := pubSpecs, eventPubB64s
	if !pinned {
		if keys, ok, kerr := audit.LoadArchiveKeys(dir); kerr != nil {
			return opts, pinned, kerr
		} else if ok {
			cpSpecs, evSpecs = keys.CheckpointKeys, keys.EventPubKeys
			pubAlg = "" // spec form only in keys.json
		}
	}
	// Per-event keys are epoch-FENCED (F-07): --event-pubkey accepts the SAME
	// grammar as the live `audit verify` path — key · key@last · key@lo:hi — so an
	// operator can fence a retired generation to the sequence it last legitimately
	// signed (parseEventPubKeySpec, reused, is the single parser). The archive's
	// keys.json entries are bare keys, which parse to UNBOUNDED FencedKeys: that
	// reproduces the pre-fix single-generation behavior and keeps the advisory
	// verify honestly advisory (the boundaries an archive cannot self-authenticate
	// must be supplied by the operator's out-of-band pins, not by the keys.json
	// that rode with the archive — docs/SECURITY-HARDENING.md).
	for _, s := range evSpecs {
		fk, perr := parseEventPubKeySpec(s)
		if perr != nil {
			return opts, pinned, perr
		}
		opts.EventKeys = append(opts.EventKeys, fk)
	}
	if len(cpSpecs) > 0 {
		v := audit.NewCheckpointVerifier()
		for _, spec := range cpSpecs {
			alg, raw, perr := parsePubKeySpec(spec, pubAlg, len(cpSpecs))
			if perr != nil {
				return opts, pinned, perr
			}
			if alg == audit.AlgEd25519 {
				if aerr := v.AddEd25519Raw(raw); aerr != nil {
					return opts, pinned, fmt.Errorf("--pubkey: %w", aerr)
				}
				// A pinned Ed25519 checkpoint key also covers events — but fold it in as
				// an UNBOUNDED event key ONLY when the caller PINNED it AND gave no
				// explicit --event-pubkey pins. Folding it when fenced pins exist would
				// silently re-widen a retired key the pins just fenced to its epoch (F-07);
				// in advisory mode the keys.json already lists its event keys separately.
				if pinned && !eventPinsGiven {
					opts.EventKeys = append(opts.EventKeys, audit.FencedKey{Key: ed25519.PublicKey(raw)})
				}
			} else if aerr := v.AddPublicKey(alg, raw); aerr != nil {
				return opts, pinned, fmt.Errorf("--pubkey: %w", aerr)
			}
		}
		opts.Checkpoints = v
	}
	return opts, pinned, nil
}

// addStoreFlags adds the shared store-location flags to an audit subcommand.
func addStoreFlags(cmd *cobra.Command, dataDir, engine, dsn *string) {
	cmd.Flags().StringVar(dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().StringVar(engine, "engine", "sqlite", "store engine: sqlite or postgres")
	cmd.Flags().StringVar(dsn, "dsn", "", "store DSN (default a SQLite file in the data dir)")
	_ = cmd.RegisterFlagCompletionFunc("engine", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"sqlite", "postgres"}, cobra.ShellCompDirectiveNoFileComp
	})
}
