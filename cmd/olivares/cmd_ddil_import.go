// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/ddil"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/sigbundle"
	"github.com/olivaresai/olivares/modules/governance"
)

type ddilVerifyPolicyReport struct {
	Carried      bool   `json:"carried"`
	Revision     string `json:"revision"`
	MaxStaleness string `json:"max_staleness"`
}

type ddilVerifyReport struct {
	Verified     bool                   `json:"verified"`
	Tenant       string                 `json:"tenant"`
	CreatedAt    time.Time              `json:"created_at"`
	ExpiresAt    *time.Time             `json:"expires_at,omitempty"`
	ExpiresState string                 `json:"expires_state"`
	Policy       ddilVerifyPolicyReport `json:"policy"`
	Segments     []ddil.SegmentRef      `json:"segments"`
	Evidence     []string               `json:"evidence"`
}

func newDDILVerifyCmd() *cobra.Command {
	var bundle, pubkey string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify and inspect a DDIL courier bundle without applying it",
		Long: "verify authenticates a DDIL courier bundle with the pinned transport public key and\n" +
			"reports its tenant, expiry, policy snapshot, audit segments and evidence without applying it.",
		Example:      "  olivares ddil verify --bundle transfer.ddil --pubkey @ddil-public.b64 -o json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pub, err := loadDDILPublicKey(pubkey)
			if err != nil {
				return err
			}
			// Imported intentionally exposes only DDIL-domain data. Re-open the same
			// descriptor to surface authenticated envelope expiry without loading an
			// unbounded courier file or allowing a path-swap between the two reads.
			now := time.Now().UTC()
			im, opened, err := inspectDDILBundle(bundle, pub, now)
			if err != nil {
				return err
			}
			report, err := makeDDILVerifyReport(im, opened.Manifest)
			if err != nil {
				return err
			}
			return writeDDILVerifyReport(cmd, report)
		},
	}
	cmd.Flags().StringVar(&bundle, "bundle", "", "DDIL courier bundle file")
	cmd.Flags().StringVar(&pubkey, "pubkey", "", "pinned raw Ed25519 public key (base64, or @file)")
	addDeprecatedJSONFlag(cmd)
	_ = cmd.MarkFlagRequired("bundle")
	_ = cmd.MarkFlagRequired("pubkey")
	return cmd
}

func makeDDILVerifyReport(im ddil.Imported, manifest sigbundle.Manifest) (ddilVerifyReport, error) {
	report := ddilVerifyReport{
		Verified: true, Tenant: im.Index.Tenant, CreatedAt: im.CreatedAt,
		ExpiresState: "not_set",
		Policy: ddilVerifyPolicyReport{
			Carried: len(im.PolicySnapshot()) > 0, Revision: im.Index.PolicyRevision,
			MaxStaleness: time.Duration(im.Index.PolicyMaxStaleness).String(),
		},
		Segments: append([]ddil.SegmentRef(nil), im.Index.Segments...),
		Evidence: append([]string(nil), im.Index.Evidence...),
	}
	if manifest.Expires != nil {
		expires, err := time.Parse(time.RFC3339, *manifest.Expires)
		if err != nil {
			return ddilVerifyReport{}, fmt.Errorf("DDIL bundle expires_at %q is invalid after verification: %w", *manifest.Expires, err)
		}
		report.ExpiresAt = &expires
		report.ExpiresState = "valid"
	}
	return report, nil
}

func writeDDILVerifyReport(cmd *cobra.Command, report ddilVerifyReport) error {
	return renderOut(cmd, func(out io.Writer) error {
		if _, err := fmt.Fprintln(out, "DDIL bundle verified"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "tenant: %s\ncreated_at: %s\n", report.Tenant, report.CreatedAt.Format(time.RFC3339)); err != nil {
			return err
		}
		if report.ExpiresAt == nil {
			if _, err := fmt.Fprintln(out, "expires: not set"); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(out, "expires: valid until %s\n", report.ExpiresAt.Format(time.RFC3339)); err != nil {
			return err
		}
		if report.Policy.Carried {
			if _, err := fmt.Fprintf(out, "policy: revision=%s max_staleness=%s\n", report.Policy.Revision, report.Policy.MaxStaleness); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintln(out, "policy: not carried"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "audit segments: %d\n", len(report.Segments)); err != nil {
			return err
		}
		for _, segment := range report.Segments {
			if _, err := fmt.Fprintf(out, "  - %d..%d\n", segment.FromSeq, segment.ToSeq); err != nil {
				return err
			}
		}
		if len(report.Evidence) == 0 {
			_, err := fmt.Fprintln(out, "evidence: none")
			return err
		}
		_, err := fmt.Fprintf(out, "evidence: %s\n", strings.Join(report.Evidence, ", "))
		return err
	}, report)
}

type ddilImportGapReport struct {
	Expected int64 `json:"expected"`
	Got      int64 `json:"got"`
}

type ddilImportAuditReport struct {
	CursorBefore    int64                `json:"cursor_before"`
	AppliedSegments int                  `json:"applied_segments"`
	SkippedSegments int                  `json:"skipped_segments"`
	CursorAfter     int64                `json:"cursor_after"`
	Gap             *ddilImportGapReport `json:"gap,omitempty"`
	Error           string               `json:"error,omitempty"`
}

type ddilImportPolicyReport struct {
	Carried      bool                    `json:"carried"`
	Revision     string                  `json:"revision"`
	Advances     bool                    `json:"advances"`
	MaxStaleness string                  `json:"max_staleness"`
	Adoption     *governance.AdoptReport `json:"adoption,omitempty"`
	Error        string                  `json:"error,omitempty"`
	OperatorNote string                  `json:"operator_note,omitempty"`
}

type ddilImportEvidenceReport struct {
	Written []string `json:"written"`
	Skipped []string `json:"skipped"`
	Warning string   `json:"warning,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type ddilImportBundleReport struct {
	Tenant    string    `json:"tenant"`
	CreatedAt time.Time `json:"created_at"`
	Revision  string    `json:"revision"`
}

type ddilImportReport struct {
	Bundle   ddilImportBundleReport   `json:"bundle"`
	Audit    ddilImportAuditReport    `json:"audit"`
	Policy   ddilImportPolicyReport   `json:"policy"`
	Evidence ddilImportEvidenceReport `json:"evidence"`
}

func newDDILImportCmd() *cobra.Command {
	var dataDir, engineName, dsn string
	var bundle, pubkey, tenant, auditOut, evidenceOut string
	var eventPubkeys, checkpointPubkeys []string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Verify, reconcile and apply a DDIL courier bundle fail-closed",
		Long: "import verifies a DDIL courier bundle, rejects tenant or audit-sequence mismatches,\n" +
			"stages carried audit segments, extracts evidence read-only and adopts a newer signed policy snapshot.",
		Example: `  olivares ddil import --bundle transfer.ddil --pubkey @ddil-public.b64 \
    --tenant t_abc123 --audit-out /srv/ddil/audit --evidence-out /srv/ddil/evidence`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pub, err := loadDDILPublicKey(pubkey)
			if err != nil {
				return err
			}
			im, err := readDDILBundle(bundle, pub, time.Now().UTC())
			if err != nil {
				return err
			}
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			parsedTenant, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			if parsedTenant.String() != im.Index.Tenant {
				return fmt.Errorf("--tenant %q does not match DDIL bundle tenant %q", parsedTenant, im.Index.Tenant)
			}
			if len(im.Index.Segments) > 0 && strings.TrimSpace(auditOut) == "" {
				return fmt.Errorf("--audit-out is required because the DDIL bundle carries %d audit segment(s)", len(im.Index.Segments))
			}
			verifyOpts, err := ddilArchiveVerifyOptions(checkpointPubkeys, eventPubkeys)
			if err != nil {
				return err
			}

			cursorBefore := int64(0)
			if auditOut != "" {
				cursorBefore, err = deriveDDILArchiveCursor(cmd.Context(), auditOut, im.Index.Tenant)
				if err != nil {
					return err
				}
			}

			localPolicyRev := ""
			var eng *engine
			if len(im.PolicySnapshot()) > 0 {
				eng, err = auditBoot(cmd, dataDir, engineName, dsn)
				if err != nil {
					return err
				}
				defer func() { _ = eng.Close() }()
				freshness, _, readErr := governance.PolicyFreshness(cmd.Context(), eng.store, parsedTenant)
				if readErr != nil {
					return fmt.Errorf("read local DDIL policy adoption state: %w", readErr)
				}
				localPolicyRev = freshness.AdoptedRevision
			}

			plan := im.Reconcile(cursorBefore, localPolicyRev)
			report := ddilImportReport{
				Bundle: ddilImportBundleReport{Tenant: im.Index.Tenant, CreatedAt: im.CreatedAt, Revision: im.Index.PolicyRevision},
				Audit: ddilImportAuditReport{
					CursorBefore: cursorBefore, CursorAfter: cursorBefore,
					SkippedSegments: len(plan.SkippedSegments),
				},
				Policy: ddilImportPolicyReport{
					Carried: len(im.PolicySnapshot()) > 0, Revision: im.Index.PolicyRevision,
					Advances:     plan.PolicyAdvances,
					MaxStaleness: time.Duration(im.Index.PolicyMaxStaleness).String(),
				},
				Evidence: ddilImportEvidenceReport{Written: []string{}, Skipped: []string{}},
			}

			var planeErrors []error
			if plan.GapBeforeApply {
				got := plan.NewSegments[0].FromSeq
				report.Audit.Gap = &ddilImportGapReport{Expected: cursorBefore + 1, Got: got}
				report.Audit.Error = fmt.Sprintf("audit gap before apply: expected seq %d, got %d", cursorBefore+1, got)
				planeErrors = append(planeErrors, errors.New(report.Audit.Error))
			} else if len(plan.NewSegments) > 0 {
				applied, cursorAfter, applyErr := applyDDILAuditSegments(
					cmd.Context(), auditOut, im.Index.Tenant, cursorBefore, plan.NewSegments, im.Payloads, verifyOpts,
				)
				report.Audit.AppliedSegments = applied
				report.Audit.CursorAfter = cursorAfter
				if applyErr != nil {
					report.Audit.Error = applyErr.Error()
					planeErrors = append(planeErrors, applyErr)
				}
			}

			evidenceReport, evidenceErr := extractDDILEvidence(im, evidenceOut)
			report.Evidence = evidenceReport
			if evidenceErr != nil {
				report.Evidence.Error = evidenceErr.Error()
				planeErrors = append(planeErrors, evidenceErr)
			}

			if report.Policy.Carried {
				// Always route through AdoptBundlePolicy, NOT gated on plan.PolicyAdvances:
				// a re-attestation carries UNCHANGED policy (revision equals the last
				// adopted, so PolicyAdvances is false) yet a newer signed created_at must
				// still refresh the offline-trust clock. The package function classifies
				// adopt / re-attest / no-op / replay-refuse against the durable record.
				adoption, adoptErr := governance.AdoptBundlePolicy(cmd.Context(), eng.store, parsedTenant, governance.PolicyAdoption{
					Snapshot: im.PolicySnapshot(), Revision: im.Index.PolicyRevision,
					BundleCreatedAt: im.CreatedAt,
					MaxStaleness:    time.Duration(im.Index.PolicyMaxStaleness),
					Actor:           "ddil-import",
				}, time.Now().UTC())
				if adoptErr != nil {
					refused := governance.AdoptReport{Reason: "refused"}
					report.Policy.Adoption = &refused
					report.Policy.Error = adoptErr.Error()
					planeErrors = append(planeErrors, adoptErr)
				} else {
					report.Policy.Adoption = &adoption
					report.Policy.OperatorNote = "policy persisted; the live engine adopts it on the next restart/reload, with the durable signed freshness clock restored rather than re-stamped"
				}
			}

			if writeErr := writeDDILImportReport(cmd, report); writeErr != nil {
				planeErrors = append(planeErrors, writeErr)
			}
			return errors.Join(planeErrors...)
		},
	}
	addStoreFlags(cmd, &dataDir, &engineName, &dsn)
	cmd.Flags().StringVar(&bundle, "bundle", "", "DDIL courier bundle file")
	cmd.Flags().StringVar(&pubkey, "pubkey", "", "pinned raw Ed25519 bundle public key (base64, or @file)")
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant that is allowed to receive the bundle (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&auditOut, "audit-out", "", "local WORM archive directory for carried audit segments")
	cmd.Flags().StringVar(&evidenceOut, "evidence-out", "", "directory under which carried evidence is extracted read-only")
	cmd.Flags().StringArrayVar(&eventPubkeys, "event-pubkey", nil, "per-event Ed25519 public key pin for staged archive verification (repeatable), optionally epoch-FENCED as \"<base64>@<last_seq>\" or \"<base64>@<lo>:<hi>\"; a bare key is the current generation (pin every retired generation with its boundary to fence it)")
	cmd.Flags().StringArrayVar(&checkpointPubkeys, "checkpoint-pubkey", nil, "checkpoint public key pin for staged archive verification (repeatable; raw Ed25519 or <alg>:<base64 DER SPKI>)")
	addDeprecatedJSONFlag(cmd)
	_ = cmd.MarkFlagRequired("bundle")
	_ = cmd.MarkFlagRequired("pubkey")
	return cmd
}

func loadDDILPublicKey(flag string) (ed25519.PublicKey, error) {
	raw := strings.TrimSpace(flag)
	if strings.HasPrefix(raw, "@") {
		body, err := os.ReadFile(raw[1:])
		if err != nil {
			return nil, fmt.Errorf("read --pubkey file: %w", err)
		}
		raw = strings.TrimSpace(string(body))
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("--pubkey: invalid base64 Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

func readDDILBundle(file string, pub ed25519.PublicKey, now time.Time) (ddil.Imported, error) {
	bundle, err := os.Open(file)
	if err != nil {
		return ddil.Imported{}, fmt.Errorf("open DDIL bundle %q: %w", file, err)
	}
	im, importErr := ddil.Import(bundle, pub, now)
	closeErr := bundle.Close()
	if importErr != nil {
		return ddil.Imported{}, fmt.Errorf("verify DDIL bundle %q: %w", file, importErr)
	}
	if closeErr != nil {
		return ddil.Imported{}, fmt.Errorf("close DDIL bundle %q: %w", file, closeErr)
	}
	return im, nil
}

func inspectDDILBundle(file string, pub ed25519.PublicKey, now time.Time) (ddil.Imported, sigbundle.Opened, error) {
	bundle, err := os.Open(file)
	if err != nil {
		return ddil.Imported{}, sigbundle.Opened{}, fmt.Errorf("open DDIL bundle %q: %w", file, err)
	}
	im, importErr := ddil.Import(bundle, pub, now)
	if importErr != nil {
		_ = bundle.Close()
		return ddil.Imported{}, sigbundle.Opened{}, fmt.Errorf("verify DDIL bundle %q: %w", file, importErr)
	}
	if _, err := bundle.Seek(0, 0); err != nil {
		_ = bundle.Close()
		return ddil.Imported{}, sigbundle.Opened{}, fmt.Errorf("rewind DDIL bundle %q: %w", file, err)
	}
	opened, readErr := sigbundle.Read(bundle, sigbundle.TagDDILBundle, pub, now)
	closeErr := bundle.Close()
	if readErr != nil {
		return ddil.Imported{}, sigbundle.Opened{}, fmt.Errorf("verify DDIL bundle %q envelope metadata: %w", file, readErr)
	}
	if closeErr != nil {
		return ddil.Imported{}, sigbundle.Opened{}, fmt.Errorf("close DDIL bundle %q: %w", file, closeErr)
	}
	return im, opened, nil
}

func ddilArchiveVerifyOptions(checkpointPubkeys, eventPubkeys []string) (audit.ArchiveVerifyOptions, error) {
	if len(eventPubkeys) > 0 && len(checkpointPubkeys) == 0 {
		return audit.ArchiveVerifyOptions{}, fmt.Errorf("--event-pubkey without --checkpoint-pubkey: checkpoint lines would be unverifiable; pin both key sets, or pin none for structural verification")
	}
	if len(checkpointPubkeys) == 0 {
		return audit.ArchiveVerifyOptions{}, nil
	}
	opts, _, err := archiveVerifyOptions("", "", checkpointPubkeys, eventPubkeys)
	if err != nil {
		return audit.ArchiveVerifyOptions{}, fmt.Errorf("DDIL staged archive key pins: %w", err)
	}
	return opts, nil
}

func deriveDDILArchiveCursor(ctx context.Context, dir, tenant string) (int64, error) {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect local archive %q: %w", dir, err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("local archive %q is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("inspect local archive %q: %w", dir, err)
	}
	if len(entries) == 0 {
		return 0, nil
	}
	report, err := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{})
	if err != nil {
		return 0, fmt.Errorf("local archive corrupt: verify %q: %w", dir, err)
	}
	if !report.OK {
		return 0, fmt.Errorf("local archive corrupt: reason=%s tenant=%s segment=%s seq=%d", report.Reason, report.BreakTenant, report.BreakSegment, report.BreakAt)
	}
	return report.Ranges[tenant].ToSeq, nil
}

type ddilStagedSegment struct {
	ref         ddil.SegmentRef
	manifest    audit.SegmentManifest
	eventsKey   string
	manifestKey string
}

func applyDDILAuditSegments(
	ctx context.Context,
	auditOut, tenant string,
	cursorBefore int64,
	refs []ddil.SegmentRef,
	payloads map[string][]byte,
	verifyOpts audit.ArchiveVerifyOptions,
) (applied int, cursorAfter int64, err error) {
	cursorAfter = cursorBefore
	if err := os.MkdirAll(auditOut, 0o755); err != nil {
		return 0, cursorAfter, fmt.Errorf("create audit archive %q: %w", auditOut, err)
	}
	// Stage OUTSIDE the WORM archive root. VerifyArchiveDir and the cursor/fork checks
	// walk auditOut RECURSIVELY (filepath.WalkDir), so a staging dir left inside it by an
	// unclean crash — power loss is the design-basis failure of a DDIL edge — would be
	// counted as durably-held evidence: the derived cursor would jump past segments that
	// never committed and they would be skipped forever (silent loss). The commit loop
	// below COPIES bytes into the real sink (os.ReadFile → Put), never renames, so staging
	// on a different filesystem is fine; a crash now only orphans a dir under the system
	// temp, which the OS reclaims and which no verifier ever walks.
	staging, err := os.MkdirTemp("", fmt.Sprintf("olivares-ddil-staging-%d-", os.Getpid()))
	if err != nil {
		return 0, cursorAfter, fmt.Errorf("create DDIL audit staging directory: %w", err)
	}
	defer func() {
		if staging != "" {
			_ = os.RemoveAll(staging)
		}
	}()
	stageSink, err := audit.NewDirSink(staging)
	if err != nil {
		return 0, cursorAfter, err
	}

	staged := make([]ddilStagedSegment, 0, len(refs))
	for _, ref := range refs {
		manifestBody, manifestOK := payloads[ref.ManifestName]
		eventsBody, eventsOK := payloads[ref.EventsName]
		if !manifestOK || !eventsOK {
			return 0, cursorAfter, fmt.Errorf("DDIL bundle omitted payloads for audit segment %d..%d after verification", ref.FromSeq, ref.ToSeq)
		}
		var manifest audit.SegmentManifest
		if err := json.Unmarshal(manifestBody, &manifest); err != nil {
			return 0, cursorAfter, fmt.Errorf("decode staged audit manifest %q: %w", ref.ManifestName, err)
		}
		if err := matchDDILSegmentReference(tenant, ref, manifest); err != nil {
			return 0, cursorAfter, err
		}
		eventsKey := audit.SegmentKey(tenant, ref.FromSeq, ref.ToSeq)
		manifestKey := audit.SegmentManifestKey(tenant, ref.FromSeq, ref.ToSeq)
		if _, err := stageSink.Put(ctx, eventsKey, eventsBody, audit.ArchivePutOptions{ContentSHA256: manifest.EventsSHA256}); err != nil {
			return 0, cursorAfter, fmt.Errorf("stage audit events %d..%d: %w", ref.FromSeq, ref.ToSeq, err)
		}
		manifestSum := sha256.Sum256(manifestBody)
		if _, err := stageSink.Put(ctx, manifestKey, manifestBody, audit.ArchivePutOptions{ContentSHA256: hex.EncodeToString(manifestSum[:])}); err != nil {
			return 0, cursorAfter, fmt.Errorf("stage audit manifest %d..%d: %w", ref.FromSeq, ref.ToSeq, err)
		}
		staged = append(staged, ddilStagedSegment{ref: ref, manifest: manifest, eventsKey: eventsKey, manifestKey: manifestKey})
	}

	stageReport, err := audit.VerifyArchiveDir(ctx, staging, verifyOpts)
	if err != nil {
		return 0, cursorAfter, fmt.Errorf("verify staged DDIL audit archive: %w", err)
	}
	if !stageReport.OK {
		return 0, cursorAfter, fmt.Errorf("staged DDIL audit archive verification failed: reason=%s tenant=%s segment=%s seq=%d", stageReport.Reason, stageReport.BreakTenant, stageReport.BreakSegment, stageReport.BreakAt)
	}
	wantFrom, wantTo := refs[0].FromSeq, refs[len(refs)-1].ToSeq
	gotRange, ok := stageReport.Ranges[tenant]
	if !ok || gotRange.FromSeq != wantFrom || gotRange.ToSeq != wantTo {
		return 0, cursorAfter, fmt.Errorf("staged DDIL audit archive covers %d..%d for tenant %q, want exactly %d..%d", gotRange.FromSeq, gotRange.ToSeq, tenant, wantFrom, wantTo)
	}
	if cursorBefore > 0 {
		localLast, err := readDDILLastManifest(auditOut, tenant, cursorBefore)
		if err != nil {
			return 0, cursorAfter, err
		}
		stagedPrev := staged[0].manifest.PrevSegmentLastHash
		if stagedPrev != localLast.LastHash {
			return 0, cursorAfter, fmt.Errorf("audit fork detected: hash mismatch at cursor %d: staged prev_segment_last_hash=%s local last_hash=%s", cursorBefore, stagedPrev, localLast.LastHash)
		}
	}

	realSink, err := audit.NewDirSink(auditOut)
	if err != nil {
		return 0, cursorAfter, err
	}
	for _, segment := range staged {
		eventsBody, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(segment.eventsKey)))
		if err != nil {
			return applied, cursorAfter, fmt.Errorf("read verified staged audit events %q: %w", segment.eventsKey, err)
		}
		manifestBody, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(segment.manifestKey)))
		if err != nil {
			return applied, cursorAfter, fmt.Errorf("read verified staged audit manifest %q: %w", segment.manifestKey, err)
		}
		if _, err := realSink.Put(ctx, segment.eventsKey, eventsBody, audit.ArchivePutOptions{ContentSHA256: segment.manifest.EventsSHA256}); err != nil {
			return applied, cursorAfter, fmt.Errorf("commit audit events %d..%d: %w", segment.ref.FromSeq, segment.ref.ToSeq, err)
		}
		manifestSum := sha256.Sum256(manifestBody)
		if _, err := realSink.Put(ctx, segment.manifestKey, manifestBody, audit.ArchivePutOptions{ContentSHA256: hex.EncodeToString(manifestSum[:])}); err != nil {
			return applied, cursorAfter, fmt.Errorf("commit audit manifest %d..%d: %w", segment.ref.FromSeq, segment.ref.ToSeq, err)
		}
		applied++
	}
	if err := os.RemoveAll(staging); err != nil {
		return applied, cursorAfter, fmt.Errorf("remove DDIL audit staging directory: %w", err)
	}
	staging = ""
	cursorAfter, err = deriveDDILArchiveCursor(ctx, auditOut, tenant)
	if err != nil {
		return applied, cursorBefore, fmt.Errorf("verify committed DDIL audit archive: %w", err)
	}
	if cursorAfter != wantTo {
		return applied, cursorAfter, fmt.Errorf("committed DDIL audit cursor is %d, want %d", cursorAfter, wantTo)
	}
	return applied, cursorAfter, nil
}

func matchDDILSegmentReference(tenant string, ref ddil.SegmentRef, manifest audit.SegmentManifest) error {
	if manifest.Tenant != tenant || manifest.FromSeq != ref.FromSeq || manifest.ToSeq != ref.ToSeq ||
		manifest.FirstHash != ref.FirstHash || manifest.LastHash != ref.LastHash ||
		manifest.PrevSegmentLastHash != ref.PrevSegmentLastHash {
		return fmt.Errorf("DDIL audit manifest %q does not match its signed index reference for %d..%d", ref.ManifestName, ref.FromSeq, ref.ToSeq)
	}
	return nil
}

func readDDILLastManifest(dir, tenant string, cursor int64) (audit.SegmentManifest, error) {
	var found *audit.SegmentManifest
	err := filepath.WalkDir(dir, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(file, ".jsonl.manifest.json") {
			return nil
		}
		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var manifest audit.SegmentManifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			return err
		}
		if manifest.Tenant != tenant || manifest.ToSeq != cursor {
			return nil
		}
		if found != nil {
			return fmt.Errorf("local archive has multiple last manifests for tenant %q at seq %d", tenant, cursor)
		}
		snapshot := manifest
		found = &snapshot
		return nil
	})
	if err != nil {
		return audit.SegmentManifest{}, fmt.Errorf("read local archive last manifest: %w", err)
	}
	if found == nil {
		return audit.SegmentManifest{}, fmt.Errorf("local archive cursor is %d but its last manifest for tenant %q is missing", cursor, tenant)
	}
	return *found, nil
}

func extractDDILEvidence(im ddil.Imported, evidenceOut string) (ddilImportEvidenceReport, error) {
	report := ddilImportEvidenceReport{Written: []string{}, Skipped: []string{}}
	if len(im.Index.Evidence) == 0 {
		return report, nil
	}
	if strings.TrimSpace(evidenceOut) == "" {
		report.Skipped = append(report.Skipped, im.Index.Evidence...)
		report.Warning = fmt.Sprintf("bundle carries %d evidence file(s), but --evidence-out was not set; evidence was not extracted and can be re-imported", len(im.Index.Evidence))
		return report, nil
	}
	absRoot, err := filepath.Abs(evidenceOut)
	if err != nil {
		return report, fmt.Errorf("resolve --evidence-out %q: %w", evidenceOut, err)
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return report, fmt.Errorf("create --evidence-out %q: %w", evidenceOut, err)
	}
	for _, name := range im.Index.Evidence {
		clean, err := safeDDILEvidenceName(name)
		if err != nil {
			return report, err
		}
		body, ok := im.Payloads[name]
		if !ok {
			return report, fmt.Errorf("DDIL evidence payload %q is missing after verification", name)
		}
		target := filepath.Join(absRoot, filepath.FromSlash(clean))
		rel, err := filepath.Rel(absRoot, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return report, fmt.Errorf("DDIL evidence name %q escapes --evidence-out", name)
		}
		written, err := writeDDILEvidenceFile(target, body)
		if err != nil {
			return report, fmt.Errorf("extract DDIL evidence %q: %w", name, err)
		}
		if written {
			report.Written = append(report.Written, name)
		} else {
			report.Skipped = append(report.Skipped, name)
		}
	}
	sort.Strings(report.Written)
	sort.Strings(report.Skipped)
	return report, nil
}

func safeDDILEvidenceName(name string) (string, error) {
	native := filepath.FromSlash(name)
	if name == "" || strings.ContainsRune(name, '\\') || path.IsAbs(name) ||
		filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", fmt.Errorf("DDIL evidence name %q is not a safe relative path", name)
	}
	clean := path.Clean(name)
	if clean != name || clean == "." {
		return "", fmt.Errorf("DDIL evidence name %q is not clean", name)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("DDIL evidence name %q escapes --evidence-out", name)
		}
	}
	return clean, nil
}

func writeDDILEvidenceFile(target string, body []byte) (bool, error) {
	if existing, err := os.ReadFile(target); err == nil {
		if bytes.Equal(existing, body) {
			return false, nil
		}
		return false, fmt.Errorf("existing file has different content; refusing to overwrite")
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return false, err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if os.IsExist(err) {
		if existing, readErr := os.ReadFile(target); readErr == nil && bytes.Equal(existing, body) {
			return false, nil
		}
		return false, fmt.Errorf("existing file has different content; refusing to overwrite")
	}
	if err != nil {
		return false, err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(target)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	if err := file.Chmod(0o400); err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	remove = false
	return true, nil
}

func writeDDILImportReport(cmd *cobra.Command, report ddilImportReport) error {
	return renderOut(cmd, func(out io.Writer) error {
		if _, err := fmt.Fprintf(out, "DDIL bundle import\ntenant: %s\ncreated_at: %s\nrevision: %s\n", report.Bundle.Tenant, report.Bundle.CreatedAt.Format(time.RFC3339), printableDDILRevision(report.Bundle.Revision)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "audit: cursor %d -> %d; applied_segments=%d skipped_segments=%d\n", report.Audit.CursorBefore, report.Audit.CursorAfter, report.Audit.AppliedSegments, report.Audit.SkippedSegments); err != nil {
			return err
		}
		if report.Audit.Gap != nil {
			if _, err := fmt.Fprintf(out, "audit: REFUSED gap before apply (expected seq %d, got %d)\n", report.Audit.Gap.Expected, report.Audit.Gap.Got); err != nil {
				return err
			}
		} else if report.Audit.Error != "" {
			if _, err := fmt.Fprintf(out, "audit: ERROR %s\n", report.Audit.Error); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(out, "policy: carried=%t revision=%s advances=%t max_staleness=%s\n", report.Policy.Carried, printableDDILRevision(report.Policy.Revision), report.Policy.Advances, report.Policy.MaxStaleness); err != nil {
			return err
		}
		if report.Policy.Adoption != nil {
			if _, err := fmt.Fprintf(out, "policy adoption: %s; adopted=%t surface_revision=%d\n", report.Policy.Adoption.Reason, report.Policy.Adoption.Adopted, report.Policy.Adoption.SurfaceRevision); err != nil {
				return err
			}
		}
		if report.Policy.Error != "" {
			if _, err := fmt.Fprintf(out, "policy: ERROR %s\n", report.Policy.Error); err != nil {
				return err
			}
		}
		if report.Policy.OperatorNote != "" {
			if _, err := fmt.Fprintf(out, "operator note: %s\n", report.Policy.OperatorNote); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(out, "evidence: written=%d skipped=%d\n", len(report.Evidence.Written), len(report.Evidence.Skipped)); err != nil {
			return err
		}
		if len(report.Evidence.Written) > 0 {
			if _, err := fmt.Fprintf(out, "  written: %s\n", strings.Join(report.Evidence.Written, ", ")); err != nil {
				return err
			}
		}
		if len(report.Evidence.Skipped) > 0 {
			if _, err := fmt.Fprintf(out, "  skipped: %s\n", strings.Join(report.Evidence.Skipped, ", ")); err != nil {
				return err
			}
		}
		if report.Evidence.Warning != "" {
			if _, err := fmt.Fprintf(out, "WARNING: %s\n", report.Evidence.Warning); err != nil {
				return err
			}
		}
		if report.Evidence.Error != "" {
			if _, err := fmt.Fprintf(out, "evidence: ERROR %s\n", report.Evidence.Error); err != nil {
				return err
			}
		}
		return nil
	}, report)
}

func printableDDILRevision(revision string) string {
	if revision == "" {
		return "-"
	}
	return revision
}
