// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/spf13/cobra"
)

// newAuditCmd offers offline operations on the evidence ledger: verify the chain
// + signed checkpoints, write a new checkpoint, export to a SIEM format, and
// export/verify the immutable archive.
func newAuditCmd() *cobra.Command {
	root := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "audit",
		Short:       "Read and maintain the signed audit ledger",
		Long: "Read signed audit events and maintain their checkpoints.\n\n" +
			"Reading the ledger is read-only and never mints keys or writes to the data dir.",
		Example: "  olivares audit ls\n" +
			"  olivares audit checkpoint --tenant t_abc123",
	}
	root.AddCommand(auditListCmd(), auditVerifyCmd(), auditRecoverCmd(), auditKeyTransitionCmd(), auditCheckpointCmd(), auditTreeCmd(), auditExportCmd(), auditArchiveCmd(), auditObserveReportCmd())
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
		Version: version, Logger: cliBootLogger(slog.LevelWarn),
		NoImplicitInstall:   true,
		storeEngineExplicit: cmd.Flags().Changed("engine"),
	})
}

// auditBootRO is the same wiring for a command that only READS: it creates no
// data directory, mints no signing key and creates no store file, so running one
// in a directory that holds no installation reports NotFound instead of building
// one there. Everything that lists, shows, verifies or exports uses this.
func auditBootRO(cmd *cobra.Command, dataDir, engine, dsn string) (*engine, error) {
	return boot(cmd.Context(), bootConfig{
		DataDir: dataDir, Engine: engine, DSN: dsn, Version: version, Logger: cliBootLogger(slog.LevelError),
		ReadOnly:            true,
		storeEngineExplicit: cmd.Flags().Changed("engine"),
	})
}

// sourceProbeBoot prepares only the explicitly requested source probe.
func sourceProbeBoot(cmd *cobra.Command, dataDir, engine, dsn string) (*engine, error) {
	return boot(cmd.Context(), bootConfig{
		DataDir: dataDir, Engine: engine, DSN: dsn, Version: version, Logger: cliBootLogger(slog.LevelError),
		ReadOnly: true, NoIngest: true,
		storeEngineExplicit: cmd.Flags().Changed("engine"),
	})
}

func auditVerifyCmd() *cobra.Command {
	var dataDir, engine, dsn, ownerDSN, tenant, pubAlg string
	var pubB64s, eventPubB64s []string
	var fromSeq int64
	var strict bool
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "verify",
		Short:       "Verify a tenant's chain and its signed checkpoints",
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
			return runAuditVerify(cmd, dataDir, engine, dsn, ownerDSN, tenant, pubAlg, pubB64s, eventPubB64s, fromSeq, strict)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "Postgres owner-role DSN for topology verification (accepts file:/env: references; never runs DDL)")
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id to verify (default $OLIVARES_TENANT)")
	cmd.Flags().Int64Var(&fromSeq, "from", 1, "first sequence of the structural walk (a recovered epoch begins at its recover_seq; genesis remains the default)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero if any integrity check fails (chain/checkpoints/event_sigs); for on-call cron/CI. The default exits 0 and reports status only in the JSON")
	cmd.Flags().StringArrayVar(&pubB64s, "pubkey", nil, "checkpoint public key pin, repeatable (key rotation): raw base64 Ed25519, or \"<alg>:<base64 DER SPKI>\" for an off-box key (default: the engine's own keys — advisory only; pin OFF-BOX keys for an attacker-resistant check, docs/SECURITY-HARDENING.md §5)")
	cmd.Flags().StringVar(&pubAlg, "pubkey-alg", "", "algorithm of a SINGLE bare --pubkey (compat form): ed25519 (raw, default) | ecdsa-p256-sha256 | ecdsa-p384-sha384 | rsa-pkcs1-sha256 | rsa-pss-sha256 (DER SubjectPublicKeyInfo); with multiple --pubkey use the \"<alg>:<base64>\" form")
	cmd.Flags().StringArrayVar(&eventPubB64s, "event-pubkey", nil, "per-event Ed25519 public key pin, repeatable (raw base64), optionally epoch-FENCED as \"<base64>@<last_seq>\" (retired generation, valid only up to that sequence) or \"<base64>@<lo>:<hi>\" (explicit window); a bare key is the current key. Pins REPLACE the advisory defaults — pin EVERY generation with its boundary ('keys status' lists prior_public_keys; the boundary is the audit.key.rotation marker's prior_last_seq). Without a boundary a retired key is trusted for every sequence")
	_ = cmd.RegisterFlagCompletionFunc("pubkey-alg", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"ed25519", "ecdsa-p256-sha256", "ecdsa-p384-sha384", "rsa-pkcs1-sha256", "rsa-pss-sha256"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
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
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "checkpoint",
		Short:       "Write a signed checkpoint (all tenants, or one with --tenant)",
		Long:        "checkpoint appends a signed audit checkpoint for one tenant, or for every tenant when --tenant is omitted.",
		Example:     "  olivares audit checkpoint --tenant t_abc123",
		Args:        cobra.NoArgs,
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
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "export",
		Short:       "Export a tenant's ledger to a SIEM format (" + audit.FormatList() + ")",
		Long: "export walks one tenant's audit ledger in sequence order and writes each event to stdout in " +
			"ArcSight CEF, IBM QRadar LEEF 2.0, RFC 5424 syslog, a complete OTLP/HTTP export request " +
			"(otlp; otlp_envelope is an exact alias), the bare OTLP LogRecord projection " +
			"(otlp_log_record, one LogRecord JSON per line, not postable) or OCSF JSON. Every format " +
			"carries the chain-integrity fields verbatim, so the copy re-verifies offline. Requires Business.",
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
			return runAuditExport(cmd, dataDir, engine, dsn, tenant, format)
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
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "archive",
		Short:       "Export and verify the immutable ledger archive",
		Long: "archive writes the ledger out as self-describing JSONL segments plus a signed\n" +
			"manifest, and verifies such a directory again later.\n\n" +
			"The point of the split is that verify needs neither the store nor the network:\n" +
			"a WORM copy on someone else's media can be re-proved years afterwards with\n" +
			"nothing but the binary and the archive itself.",
		Example: "  olivares audit archive export --tenant t_abc123 --out /mnt/worm/audit/t_abc123\n" +
			"  olivares audit archive verify --dir /mnt/worm/audit/t_abc123 --strict",
	}
	root.AddCommand(auditArchiveExportCmd(), auditArchiveVerifyCmd())
	// The private edition adds further archive commands through the existing port.
	root.AddCommand(thisEdition.archiveCommands.get()...)
	return root
}

func auditArchiveExportCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, out string
	var fromSeq int64
	var segmentEvents int
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "write"},
		Use:         "export",
		Short:       "Export a tenant's ledger as verifiable archive segments to a directory",
		Long: "export writes immutable JSONL audit segments, manifests and advisory verification keys\n" +
			"to a directory suitable for external WORM storage. Use --from-seq to resume an export. Requires Business.",
		Example: "  olivares audit archive export --tenant t_abc123 --out /mnt/worm/audit/t_abc123",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuditArchiveExport(cmd, dataDir, engine, dsn, tenant, out, fromSeq, segmentEvents)
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
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "verify",
		Short:       "Verify an exported archive directory offline (no store, no network)",
		Long: "verify checks an exported archive's segment hashes, chain continuity and signatures without\n" +
			"opening a store or using the network. Pin trusted keys and use --strict for an automation gate. Requires Business.",
		Example: "  olivares audit archive verify --dir /mnt/worm/audit/t_abc123 --strict",
		Args:    cobra.NoArgs,
		// As in `audit verify`: the JSON report always prints; suppress the cobra
		// usage dump on a --strict integrity failure.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuditArchiveVerify(cmd, dir, pubAlg, pubSpecs, eventPubB64s, strict)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "archive directory to verify (the export's --out)")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero if the archive fails to verify; for on-call cron/CI. The default exits 0 and reports status only in the JSON")
	cmd.Flags().StringArrayVar(&pubSpecs, "pubkey", nil, "checkpoint public key pin, repeatable: raw base64 Ed25519, or \"<alg>:<base64 DER SPKI>\" for an off-box key. Pins REPLACE the archive's advisory keys.json (docs/SECURITY-HARDENING.md §5)")
	cmd.Flags().StringVar(&pubAlg, "pubkey-alg", "", "algorithm of a SINGLE bare --pubkey (compat form, as in 'audit verify')")
	_ = cmd.RegisterFlagCompletionFunc("pubkey-alg", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"ed25519", "ecdsa-p256-sha256", "ecdsa-p384-sha384", "rsa-pkcs1-sha256", "rsa-pss-sha256"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringArrayVar(&eventPubB64s, "event-pubkey", nil, "per-event Ed25519 public key pin, repeatable (raw base64), optionally epoch-FENCED as \"<base64>@<last_seq>\" (retired generation, valid only up to that sequence) or \"<base64>@<lo>:<hi>\" (explicit window); a bare key is the current generation. Pins REPLACE the archive's advisory keys.json — pin EVERY generation with its boundary (the audit.key.rotation marker's prior_last_seq) for the attacker-resistant fenced check; without a boundary a retired key is trusted for every sequence")
	_ = cmd.MarkFlagRequired("dir")
	return cmd
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
