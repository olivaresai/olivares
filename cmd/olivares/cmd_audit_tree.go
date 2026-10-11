// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// auditTreeCmd is the operator surface of the audit ledger's RFC 6962 Merkle tree:
// publish a C2SP signed checkpoint, prove one event is inside it, and verify both
// offline from a saved checkpoint and the signing public key.
func auditTreeCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "tree",
		Short: "Publish and verify C2SP Merkle checkpoints of the audit ledger",
		Long: "tree is the audit ledger's second, independent proof beside the hash chain: a signed C2SP\n" +
			"checkpoint (origin, size, RFC 6962 root) an operator saves, and an inclusion proof for one event.\n" +
			"From a saved checkpoint and the signing public key, `tree verify` proves offline that the\n" +
			"ledger still holds the history the checkpoint committed to, and that one event was inside it.",
		Example: "  olivares audit tree checkpoint --tenant t_abc123 > t_abc123.checkpoint\n" +
			"  olivares audit tree verify --tenant t_abc123 --checkpoint t_abc123.checkpoint --pubkey <base64>",
	}
	root.AddCommand(auditTreeCheckpointCmd(), auditTreeProveCmd(), auditTreeVerifyCmd())
	return root
}

func auditTreeCheckpointCmd() *cobra.Command {
	var dataDir, engine, dsn, ownerDSN, tenant string
	cmd := &cobra.Command{
		Use:   "checkpoint",
		Short: "Print the signed C2SP checkpoint of a tenant's Merkle tree",
		Long: "checkpoint prints the tenant's current tree head as a C2SP signed note (origin, size, root,\n" +
			"Ed25519 signature) on standard output. Save it: it is what `tree verify` checks later.\n" +
			"With -o json it also reports the public key and the C2SP verifier key to publish.\n" +
			"On PostgreSQL, stop the engine before checkpointing; this ceremony can complete a pre-v24 tree.\n" +
			"Supply --owner-dsn in an owner/application split.",
		Example: "  olivares audit tree checkpoint --tenant t_abc123 > t_abc123.checkpoint",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := tenantFlag(tenant)
			if err != nil {
				return err
			}
			eng, err := boot(cmd.Context(), bootConfig{
				DataDir: dataDir, Engine: engine, DSN: dsn, OwnerDSN: ownerDSN,
				Version: version, Logger: cliBootLogger(slog.LevelWarn),
				NoImplicitInstall:   true,
				storeEngineExplicit: cmd.Flags().Changed("engine"),
			})
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			signed, head, ok, err := eng.signer.TreeCheckpoint(cmd.Context(), eng.store, t)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("tenant %s has no audit events; there is no tree to checkpoint", t)
			}
			pub := eng.signer.PublicKey()
			if eng.signer.OffBoxCheckpoints() {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: this checkpoint is signed with the on-box key, not the off-box checkpoint key; keep it and the public key off this host")
			}
			vkey, err := audit.TreeVerifierKey(t, pub)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := out.Write(signed)
				return err
			}, map[string]any{
				"tenant": t.String(), "size": head.Size,
				"root":         base64.StdEncoding.EncodeToString(head.Root),
				"checkpoint":   string(signed),
				"public_key":   base64.StdEncoding.EncodeToString(pub),
				"verifier_key": vkey,
				"signed_by":    "on-box key",
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "Postgres: the owner role, required in a split-role deployment (the app role has no schema CREATE)")
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id")
	return cmd
}

func auditTreeProveCmd() *cobra.Command {
	var dataDir, engine, dsn, ownerDSN, tenant, checkpointPath, pubkey string
	var seq int64
	cmd := &cobra.Command{
		Use:   "prove",
		Short: "Prove one event is inside a saved checkpoint (JSON proof on standard output)",
		Long: "prove prints the inclusion proof of the event with --seq in the tree a saved checkpoint\n" +
			"commits to. The checkpoint is verified against --pubkey first. Give the proof, the checkpoint and\n" +
			"the public key to an auditor: `tree verify --proof` needs no access to this installation.",
		Example: "  olivares audit tree prove --tenant t_abc123 --checkpoint t_abc123.checkpoint --pubkey <base64> --seq 42 > seq42.proof",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := tenantFlag(tenant)
			if err != nil {
				return err
			}
			if seq < 1 {
				return errors.New("--seq: want the audit sequence number of the event (>= 1)")
			}
			cp, err := loadTreeCheckpoint(checkpointPath, t, pubkey, cmd.InOrStdin())
			if err != nil {
				return err
			}
			eng, err := auditVerifyBoot(cmd, dataDir, engine, dsn, ownerDSN)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			var proof audit.TreeInclusion
			var found bool
			err = eng.ledger.ViewAudit(cmd.Context(), t, func(log store.AuditLog) error {
				var perr error
				proof, found, perr = audit.ProveInclusion(cmd.Context(), log, seq, cp.Size)
				return perr
			})
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("event seq %d is not in the %d-event tree the checkpoint commits to", seq, cp.Size)
			}
			// The proof comes from the stored tree and the event from the ledger; a
			// damaged tree would give a proof that only fails later, at the auditor.
			if err := audit.VerifyInclusion(proof, cp); err != nil {
				return fmt.Errorf("the stored tree does not reproduce the checkpoint: %w", err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(proof)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "PostgreSQL owner DSN, when the application role cannot read the ledger")
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id")
	cmd.Flags().StringVar(&checkpointPath, "checkpoint", "", "saved checkpoint file (- for stdin)")
	cmd.Flags().StringVar(&pubkey, "pubkey", "", "base64 Ed25519 public key that signed the checkpoint")
	cmd.Flags().Int64Var(&seq, "seq", 0, "audit sequence number of the event")
	return cmd
}

func auditTreeVerifyCmd() *cobra.Command {
	var dataDir, engine, dsn, ownerDSN, tenant, checkpointPath, pubkey, proofPath string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a saved checkpoint, offline, against the ledger or an inclusion proof",
		Long: "verify checks a saved C2SP checkpoint with the signing public key and then one of two things.\n" +
			"With --proof it checks that one event was inside the checkpoint's tree, using nothing but the\n" +
			"checkpoint, the proof and the key. Without it, it recomputes the root from the events of the ledger\n" +
			"in --data-dir (a restored backup works) and fails if events were removed or rewritten.\n" +
			"The hash chain itself is `olivares audit verify`.",
		Example: "  olivares audit tree verify --tenant t_abc123 --checkpoint t_abc123.checkpoint --pubkey <base64>\n" +
			"  olivares audit tree verify --tenant t_abc123 --checkpoint t_abc123.checkpoint --pubkey <base64> --proof seq42.proof",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := tenantFlag(tenant)
			if err != nil {
				return err
			}
			cp, err := loadTreeCheckpoint(checkpointPath, t, pubkey, cmd.InOrStdin())
			if err != nil {
				return err
			}
			result := map[string]any{"tenant": t.String(), "size": cp.Size, "signature": "ok"}
			what := "the first events of the ledger hash to the checkpoint (the chain itself: olivares audit verify)"
			if proofPath != "" {
				raw, err := readSmallFile(proofPath)
				if err != nil {
					return fmt.Errorf("--proof: %w", err)
				}
				var proof audit.TreeInclusion
				if err := json.Unmarshal(raw, &proof); err != nil {
					return fmt.Errorf("--proof: %w", err)
				}
				if err := audit.VerifyInclusion(proof, cp); err != nil {
					return err
				}
				result["inclusion"] = map[string]any{"seq": proof.Seq, "outcome": "ok"}
				what = fmt.Sprintf("the event with hash %x, claimed as seq %d, is in the tree", proof.EventHash[:min(8, len(proof.EventHash))], proof.Seq)
			} else {
				eng, err := auditVerifyBoot(cmd, dataDir, engine, dsn, ownerDSN)
				if err != nil {
					return err
				}
				defer func() { _ = eng.Close() }()
				err = eng.ledger.ViewAudit(cmd.Context(), t, func(log store.AuditLog) error {
					return audit.VerifyTreeAgainstLedger(cmd.Context(), log, cp)
				})
				if err != nil {
					return err
				}
				result["ledger"] = "ok"
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "checkpoint of %d events for %s: signature ok, %s\n", cp.Size, t, what)
				return err
			}, result)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&ownerDSN, "owner-dsn", "", "PostgreSQL owner DSN, when the application role cannot read the ledger")
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id")
	cmd.Flags().StringVar(&checkpointPath, "checkpoint", "", "saved checkpoint file (- for stdin)")
	cmd.Flags().StringVar(&pubkey, "pubkey", "", "base64 Ed25519 public key that signed the checkpoint")
	cmd.Flags().StringVar(&proofPath, "proof", "", "inclusion proof file from 'tree prove' (offline; no ledger is read)")
	return cmd
}

func tenantFlag(tenant string) (model.TenantID, error) {
	resolved, err := resolveTenant(tenant)
	if err != nil {
		return "", err
	}
	t, err := model.ParseTenantID(resolved)
	if err != nil {
		return "", fmt.Errorf("--tenant: %w", err)
	}
	return t, nil
}

// loadTreeCheckpoint reads a saved checkpoint and verifies it against the key.
func loadTreeCheckpoint(path string, tenant model.TenantID, pubB64 string, stdin io.Reader) (audit.TreeCheckpoint, error) {
	if path == "" || pubB64 == "" {
		return audit.TreeCheckpoint{}, errors.New("--checkpoint and --pubkey are required")
	}
	var raw []byte
	var err error
	if path == "-" {
		raw, err = readSmallInput(stdin, "stdin")
	} else {
		raw, err = readSmallFile(path)
	}
	if err != nil {
		return audit.TreeCheckpoint{}, fmt.Errorf("--checkpoint: %w", err)
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return audit.TreeCheckpoint{}, errors.New("--pubkey: invalid base64 Ed25519 public key")
	}
	return audit.VerifyTreeCheckpoint(raw, tenant, ed25519.PublicKey(pub))
}

// maxTreeFileBytes bounds the files the tree commands read: a checkpoint is a few
// hundred bytes and a proof a few kilobytes.
const maxTreeFileBytes = 1 << 20

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readSmallInput(f, path)
}

func readSmallInput(input io.Reader, name string) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(input, maxTreeFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxTreeFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, maxTreeFileBytes)
	}
	return raw, nil
}
