// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const keyRotationRunbook = "deploy/runbooks/key-rotation.md"

type keyTransitionResult struct {
	Tenant       string `json:"tenant"`
	MarkerSeq    int64  `json:"marker_seq,omitempty"`
	PriorLastSeq int64  `json:"prior_last_seq,omitempty"`
	Skipped      string `json:"skipped,omitempty"`
}

type keyTransitionReport struct {
	OK               bool                  `json:"ok"`
	PriorFingerprint string                `json:"prior_fingerprint"`
	NewFingerprint   string                `json:"new_fingerprint"`
	OffBoxKeyID      string                `json:"offbox_key_id"`
	Markers          []keyTransitionResult `json:"markers"`
	Runbook          string                `json:"runbook"`
}

// auditKeyTransitionCmd records the signing-key epoch boundary of a rotation
// (F-07): the SECOND step of the on-box key-rotation ceremony, run with the
// engine STOPPED, AFTER `keys rotate` has minted and swapped in the new sealed
// envelope. For each tenant it appends one off-box-signed audit.key.rotation marker
// that binds the RETIRING key's fingerprint to the last sequence it legitimately
// signed (this tenant's current tail), so `audit verify` fences the retired key to
// its epoch instead of trusting it for every sequence forever.
func auditKeyTransitionCmd() *cobra.Command {
	var dataDir, engineKind, dsn, adminDSN, tenant, priorPubB64 string
	var yes bool
	cmd := &cobra.Command{
		Use:   "key-transition",
		Short: "Record the off-box-signed signing-key epoch boundary after `keys rotate`",
		Long: "key-transition appends one off-box-signed audit.key.rotation marker per tenant that fences the\n" +
			"RETIRED per-event key to the sequence it last signed (this tenant's tail). Run it with the engine\n" +
			"STOPPED, after `keys rotate` has swapped in the new sealed envelope and before serving resumes.\n" +
			"It REQUIRES an off-box checkpoint signer (OLIVARES_LEDGER_SIGNER): the boundary is exactly the\n" +
			"control that revokes a retired on-box key, so it must not be signed by an on-box key. Without an\n" +
			"off-box signer, fence externally instead with `audit verify --event-pubkey <key>@<last_seq>`.",
		Example:      "  olivares audit key-transition --data-dir /var/lib/olivares",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := auditBootCrossTenant(cmd, dataDir, engineKind, dsn, adminDSN)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			// The boundary is the control that revokes a retired on-box key; it must be
			// sealed off-box, never by an on-box key the same attacker could hold.
			if eng.signer == nil || !eng.signer.OffBoxCheckpoints() || eng.signer.CheckpointKey() == nil {
				return fmt.Errorf("audit key-transition requires an off-box checkpoint signer (OLIVARES_LEDGER_SIGNER); without one, fence externally with `audit verify --event-pubkey <key>@<last_seq>` (see %s)", keyRotationRunbook)
			}
			keyID := eng.signer.CheckpointKey().KeyID()
			newPub := eng.signer.PublicKey()

			// The retiring key: an explicit --prior-pubkey, else the most recent prior
			// generation the sealed envelope carries (the one `keys rotate` just retired).
			var priorPub ed25519.PublicKey
			switch {
			case priorPubB64 != "":
				raw, derr := base64.StdEncoding.DecodeString(priorPubB64)
				if derr != nil || len(raw) != ed25519.PublicKeySize {
					return fmt.Errorf("--prior-pubkey: invalid base64 Ed25519 public key")
				}
				priorPub = ed25519.PublicKey(raw)
			case len(eng.auditPriors) > 0:
				priorPub = eng.auditPriors[len(eng.auditPriors)-1]
			default:
				return fmt.Errorf("no rotation history in the sealed envelope and no --prior-pubkey given — run `keys rotate` first, or pass the retired key explicitly")
			}
			if priorPub.Equal(newPub) {
				return fmt.Errorf("the retired key equals the current key — nothing to fence (did `keys rotate` run and get swapped in?)")
			}
			report := keyTransitionReport{
				PriorFingerprint: audit.KeyFingerprint(priorPub),
				NewFingerprint:   audit.KeyFingerprint(newPub),
				OffBoxKeyID:      keyID,
				Runbook:          keyRotationRunbook,
			}

			tenants, err := keyTransitionTenants(cmd.Context(), eng.store, tenant)
			if err != nil {
				return err
			}
			if !yes && !confirm(cmd, fmt.Sprintf("Record the signing-key epoch boundary (retired %s -> current %s) across %d chain(s)?",
				report.PriorFingerprint[:12], report.NewFingerprint[:12], len(tenants))) {
				return fmt.Errorf("operator confirmation was not granted")
			}

			// Record and CONTINUE, rather than returning on the first tenant that
			// fails. The sweep used to abandon mid-estate and emit NO report at all,
			// so an operator learned neither which tenants were fenced before the
			// failure nor which were left open — and the natural response, re-running
			// the command, was the de-revocation path (see RecordKeyRotation).
			//
			// Continuing is safe precisely because RecordKeyRotation is idempotent on
			// the retired/new pair: a re-run re-visits every tenant, suppresses the
			// markers already recorded, and only fences the ones still missing. That
			// is the whole resumability mechanism — the immutable ledger IS the
			// cursor, so there is no cursor file to lose, desync or forge.
			var failures []error
			for _, t := range tenants {
				res := keyTransitionResult{Tenant: t.String()}
				// Custody, not Mutate: fencing the signing-key epoch is a custodial act
				// on the evidence chain, so it must also cover a tenant whose service is
				// withdrawn. Through Mutate it did not — that tenant came back
				// "FAILED: tenant suspended", which made the report claim a failure that
				// was not one AND left that chain's epoch boundary unfenced, on exactly
				// the evidence a grace period exists to preserve.
				err := eng.store.Custody(cmd.Context(), t, func(sc store.CustodyScope) error {
					log := sc.Audit()
					if locker, ok := log.(store.AuditAppendLocker); ok {
						if lerr := locker.LockAppends(cmd.Context()); lerr != nil {
							return lerr
						}
					}
					head, ok, herr := log.Head(cmd.Context())
					if herr != nil {
						return herr
					}
					if !ok {
						res.Skipped = "empty chain (no boundary to fence)"
						return nil
					}
					marker, existed, rerr := audit.RecordKeyRotation(cmd.Context(), log, eng.signer, audit.KeyRotationEvidence{
						Tenant:           t.String(),
						PriorFingerprint: report.PriorFingerprint,
						PriorLastSeq:     head.Seq,
						NewFingerprint:   report.NewFingerprint,
						OffBoxKeyID:      keyID,
					})
					if rerr != nil {
						return rerr
					}
					res.MarkerSeq = marker.Seq
					if existed {
						// Report the boundary the EXISTING marker declares, never the
						// head this run happened to read: the recorded one is the
						// honest boundary, and printing the current head would show
						// the operator the widened number the fix exists to prevent.
						res.PriorLastSeq = marker.Seq - 1
						res.Skipped = "already fenced by an earlier run (same retired/new key pair)"
						return nil
					}
					res.PriorLastSeq = head.Seq
					return nil
				})
				if err != nil {
					res.Skipped = "FAILED: " + err.Error()
					failures = append(failures, fmt.Errorf("record key-transition marker for tenant %s: %w", t, err))
				}
				report.Markers = append(report.Markers, res)
			}
			report.OK = len(failures) == 0
			// E2: honor -o instead of always printing JSON. The report is
			// rendered even when a tenant failed: which chains got their boundary is
			// exactly what the operator needs before deciding what to do next, and a
			// non-zero exit with no report is what made the old failure path opaque.
			if rerr := renderReportOut(cmd, report); rerr != nil {
				return rerr
			}
			if len(failures) > 0 {
				return fmt.Errorf("key-transition covered %d of %d chain(s); re-running is safe and resumes the rest (%s): %w",
					len(tenants)-len(failures), len(tenants), keyRotationRunbook, errors.Join(failures...))
			}
			return nil
		},
	}
	addStoreFlags(cmd, &dataDir, &engineKind, &dsn)
	cmd.Flags().StringVar(&adminDSN, "admin-dsn", "", "Postgres only: DSN of the dedicated NOSUPERUSER BYPASSRLS role used for the cross-tenant org enumeration. Without it the default (every tenant) sweep CANNOT enumerate the estate and this command fails closed rather than fencing a short list; --tenant needs no enumeration and so needs no admin pool")
	cmd.Flags().StringVar(&tenant, "tenant", "", "record only this tenant's boundary (default: every tenant + the system chain)")
	cmd.Flags().StringVar(&priorPubB64, "prior-pubkey", "", "retired key (raw base64 Ed25519); default: the most recent prior generation in the sealed envelope")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// keyTransitionTenants resolves the chains to fence: one --tenant, or every org
// plus the system chain (the CheckpointAll enumeration — a rotation touches all).
func keyTransitionTenants(ctx context.Context, st store.Store, one string) ([]model.TenantID, error) {
	if one != "" {
		resolved, err := resolveTenant(one)
		if err != nil {
			return nil, err
		}
		t, err := model.ParseTenantID(resolved)
		if err != nil {
			return nil, fmt.Errorf("--tenant: %w", err)
		}
		return []model.TenantID{t}, nil
	}
	var tenants []model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			tenants = append(tenants, o.TenantID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	tenants = append(tenants, model.SystemTenantID)
	seen := map[model.TenantID]bool{}
	out := tenants[:0]
	for _, t := range tenants {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out, nil
}
