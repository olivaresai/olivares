// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// newDRCmd is the disaster-recovery command group (docs/DR-RUNBOOK.md): a
// LEDGER-CONTINUITY-SAFE backup/restore for the evidence ledger. Unlike a raw
// database dump it (1) captures the signing keys under the operator's KEK so the
// restored ledger is verifiable, (2) records the per-tenant chain tips so a
// restore is provably complete, and (3) re-verifies the whole chain + per-event
// signatures + checkpoints after a restore. `restore` and `verify` exit non-zero
// unless the restored ledger is green.
func newDRCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "dr",
		Short: "Disaster recovery: ledger-continuity-safe backup and restore",
		Example: "  olivares dr inspect --in /srv/backups/olivares-2026-07-14.drbundle\n" +
			"  olivares dr verify --in /srv/backups/olivares-2026-07-14.drbundle --passphrase-file /run/secrets/dr-passphrase\n" +
			"  olivares dr drill --events 100",
		Long: "dr backs up and restores the control plane in a way that preserves the audit\n" +
			"ledger's hash-chain continuity and signing-key custody — not a naive database\n" +
			"dump (docs/DR-RUNBOOK.md). The backup bundle carries the store snapshot, the\n" +
			"signing keys encrypted under your key-encryption key (KEK), and a manifest of\n" +
			"the per-tenant chain tips; restore re-verifies the chain end to end.",
	}
	root.AddCommand(drBackupCmd(), drRestoreCmd(), drVerifyCmd(), drInspectCmd(),
		drPushCmd(), drPullCmd(), drListCmd(), drDrillCmd())
	return root
}

// drFlags are the store-location flags shared with the audit/serve commands.
type drFlags struct {
	dataDir, engineKind, dsn, adminDSN string
}

func addDRStoreFlags(cmd *cobra.Command, f *drFlags) {
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().StringVar(&f.engineKind, "engine", "sqlite", "store engine: sqlite or postgres")
	cmd.Flags().StringVar(&f.dsn, "dsn", "", "store DSN (default a SQLite file in the data dir)")
	cmd.Flags().StringVar(&f.adminDSN, "admin-dsn", "", "Postgres only: NOSUPERUSER BYPASSRLS role DSN. REQUIRED to run pg_dump directly (it keeps row_security=off and ABORTS as the application role under FORCE RLS); also used for the cross-tenant org list, without which a backup may MISS tenants — see deploy/postgres/01-app-role.sql")
}

// kekFlags carry the operator's key-encryption-key source. Exactly one of a
// passphrase (Argon2id-derived) or a raw 32-byte key file (the KMS-wrapped path)
// must be supplied.
type kekFlags struct {
	passphraseFile string
	keyFile        string
}

func addKEKFlags(cmd *cobra.Command, f *kekFlags) {
	cmd.Flags().StringVar(&f.passphraseFile, "passphrase-file", os.Getenv("OLIVARES_DR_PASSPHRASE_FILE"), "file holding the backup passphrase (Argon2id-derived KEK); or $OLIVARES_DR_PASSPHRASE_FILE")
	cmd.Flags().StringVar(&f.keyFile, "kek-key-file", os.Getenv("OLIVARES_DR_KEK_FILE"), "file holding a raw/base64 32-byte key-encryption key (the KMS-unwrapped path); or $OLIVARES_DR_KEK_FILE")
}

// backupCipher builds a fresh KeyCipher for a backup (new salt for a passphrase).
func (f kekFlags) backupCipher() (*dr.KeyCipher, error) {
	switch {
	case f.passphraseFile != "" && f.keyFile != "":
		return nil, fmt.Errorf("use exactly one of --passphrase-file or --kek-key-file")
	case f.passphraseFile != "":
		pass, err := readPassphrase(f.passphraseFile)
		if err != nil {
			return nil, err
		}
		return dr.NewPassphraseCipher(pass)
	case f.keyFile != "":
		key, err := readRawKEK(f.keyFile)
		if err != nil {
			return nil, err
		}
		return dr.NewRawKeyCipher(key)
	default:
		return nil, fmt.Errorf("a KEK is required: pass --passphrase-file or --kek-key-file (DR bundles never store keys in the clear)")
	}
}

// restoreCipher rebuilds the KeyCipher from the bundle's recorded KDF params.
func (f kekFlags) restoreCipher(params dr.KDFParams) (*dr.KeyCipher, error) {
	switch {
	case f.passphraseFile != "" && f.keyFile != "":
		return nil, fmt.Errorf("use exactly one of --passphrase-file or --kek-key-file")
	case f.passphraseFile != "":
		pass, err := readPassphrase(f.passphraseFile)
		if err != nil {
			return nil, err
		}
		return dr.OpenCipher(pass, params)
	case f.keyFile != "":
		key, err := readRawKEK(f.keyFile)
		if err != nil {
			return nil, err
		}
		return dr.OpenCipher(key, params)
	default:
		return nil, fmt.Errorf("a KEK is required to decrypt the bundle's signing keys: pass --passphrase-file or --kek-key-file")
	}
}

func readPassphrase(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read passphrase file: %w", err)
	}
	// Trim only a trailing newline so a passphrase may contain spaces.
	return []byte(strings.TrimRight(string(b), "\r\n")), nil
}

// readRawKEK reads a 32-byte KEK from a file, accepting raw bytes or base64.
func readRawKEK(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read kek file: %w", err)
	}
	if len(b) == 32 {
		return b, nil
	}
	if dec, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b))); derr == nil && len(dec) == 32 {
		return dec, nil
	}
	return nil, fmt.Errorf("kek file must hold 32 raw bytes or base64 of 32 bytes")
}

// drBoot wires a full engine (store + signer + registered modules) on a data dir,
// the same composition root the serve/audit commands use.
func drBoot(ctx context.Context, f drFlags) (*engine, error) {
	return boot(ctx, bootConfig{
		DataDir: f.dataDir, Engine: f.engineKind, DSN: f.dsn, AdminDSN: f.adminDSN,
		Version: version, Logger: slog.Default(),
	})
}

// drBackupRetention is what retention ACTUALLY did during this backup, per tier.
// All three slices are always present and never null, so a caller can read
// `.retention.local | length` without first testing for the key.
//
// Local entries are full PATHS in the --out directory and offsite entries are
// OBJECT names on the mirror — one shape per field, whichever retention flag the
// caller passed (see applyGFSLocal for why that had to be said out loud).
//
// Skipped names the tiers whose prune COULD NOT BE ATTEMPTED, and it is the field
// that makes an empty list readable. Without it, "retention ran and removed
// nothing" and "retention was asked for and never ran" are the same document, and
// the second is the one that matters: it is how a volume fills up under a policy
// its operator believes is being enforced. It is NOT inferrable from the flags the
// caller passed — that was the claim this type used to make, and it is false the
// moment the offsite mirror cannot be listed. The human pane says which and why in
// its `warning: … prune skipped` line; this says THAT it happened, in a field.
type drBackupRetention struct {
	Local   []string `json:"local"`
	Offsite []string `json:"offsite"`
	Skipped []string `json:"skipped"`
}

// recordLocal and recordOffsite fold one tier's outcome into the document. There
// are two methods, one per tier, rather than one taking the tier as an argument:
// with a single entry point the pairing of "which slice" with "which tier name"
// becomes a parameter, and a swapped pair is invisible on the happy path.
func (r *drBackupRetention) recordLocal(o retentionOutcome) {
	r.Local = append(r.Local, o.Deleted...)
	r.noteSkip(o, "local")
}

func (r *drBackupRetention) recordOffsite(o retentionOutcome) {
	r.Offsite = append(r.Offsite, o.Deleted...)
	r.noteSkip(o, "offsite")
}

func (r *drBackupRetention) noteSkip(o retentionOutcome, tier string) {
	if o.Skipped {
		r.Skipped = append(r.Skipped, tier)
	}
}

// drBackupResult is what `dr backup -o json` reports, and it is the most valuable
// document in this lot.
//
// The text form packs FIVE facts into one Fprintf (cmd_dr.go, the line that begins
// "DR bundle written:"): the bundle path, the instant the snapshot was taken, the
// engine kind, the tenant count and the key count. TakenAt is the one that matters
// most and is the least parseable: it is the RPO BASIS — at a disaster, the
// recovery point objective actually achieved is (time of disaster − TakenAt of the
// newest good bundle). A scheduled backup job that wants to alarm on "the newest
// bundle is older than our RPO" has, today, to regex a sentence for it.
//
// The counts are counts, not the lists: the manifest inside the bundle carries the
// per-tenant chain tips and `dr inspect -o json` prints it. Duplicating the tenant
// ids here would create a second place for them to be wrong.
type drBackupResult struct {
	// THE KEY IS `out`, THE FLAG THAT NAMED IT. Measured across cmd/olivares: every
	// other leaf that reports a file it just wrote keys it by the flag the caller
	// passed — keys wrap, keys rotate, keys rewrap, keys seal and ddil export all
	// spell it `out`, and `dr pull` (same lot, same family, a bundle written at
	// --out) spells it `out` too, with `dr push` spelling its source `in`. `bundle`
	// was the one place that broke the rule, so following a bundle through
	// backup → push → pull meant reading .bundle, then .in, then .out for the same
	// kind of value. It also reads better beside its sibling: `out` is the local
	// copy, `offsite` is the off-box one.
	Bundle  string `json:"out"`
	TakenAt string `json:"taken_at"`
	Engine  string `json:"engine"`
	Tenants int    `json:"tenants"`
	Keys    int    `json:"keys"`
	// ExternalKeyCustody is true when the data dir held NO signing key because the
	// customer custodies it (BYOK/CMEK) and the bundle therefore escrows no key
	// material — the condition the text form reports as a "note:" line. A restore
	// from such a bundle needs the key provisioned from the customer's Secret/KMS
	// envelope FIRST, so a fleet-wide check for bundles that cannot be verified
	// stand-alone is exactly the query this field answers.
	ExternalKeyCustody bool `json:"external_key_custody"`
	// Offsite is null when no offsite target was configured. When it is not null,
	// the replication SUCCEEDED: a failed push fails the whole command (an operator
	// who asked for offsite must not be left believing a local-only copy is 3-2-1).
	Offsite   *drOffsiteRef     `json:"offsite"`
	Retention drBackupRetention `json:"retention"`
}

func drBackupCmd() *cobra.Command {
	var sf drFlags
	var kf kekFlags
	var of offsiteFlags
	var gf gfsFlags
	var out, notes string
	var allowUnverified bool
	var pgDumpPath, snapshotFile, pitrRef string
	var retainDays int
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write a ledger-continuity-safe DR bundle",
		Long: "backup snapshots the configured store, verifies tenant audit chains, seals local signing\n" +
			"keys under the supplied KEK and writes a continuity-safe DR bundle. Optional offsite and\n" +
			"retention flags replicate and prune the completed backup.",
		Example: `  olivares dr backup --out /srv/backups/olivares-$(date +%F).drbundle \
    --passphrase-file /run/secrets/dr-passphrase --data-dir /var/lib/olivares`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return fmt.Errorf("--out is required")
			}
			// Resolved ONCE, before anything is written: every human line this
			// command emits goes here — stdout in text mode (byte-for-byte what it
			// printed before) and stderr under -o json, where stdout carries only the
			// JSON document. Deciding it per-line is how one forgotten Fprintf would
			// corrupt the document.
			human, err := commentaryOut(cmd)
			if err != nil {
				return err
			}
			cipher, err := kf.backupCipher()
			if err != nil {
				return err
			}
			dataDir, err := resolveDataDir(sf.dataDir)
			if err != nil {
				return err
			}
			sf.dataDir = dataDir

			// Seal every signing key in the data dir (audit + catalog).
			sealed, keyRefs, err := sealSigningKeys(dataDir, cipher)
			if err != nil {
				return err
			}
			if len(keyRefs) == 0 {
				// Under EXTERNAL custody (BYOK shared Secret/env; CMEK sealed
				// envelope) the data dir holds no key BY DESIGN and the bundle
				// must NOT escrow it — the customer custodies the key, and the
				// manifest signer still resolves it from the environment. Without
				// external custody, zero keys means the data dir is wrong: fail.
				if !externalKeyCustodyConfigured() {
					return fmt.Errorf("no *-signing.key found in %s: nothing to put the ledger's custody on (run the engine once to mint keys, point --data-dir at the live data dir, or configure the external-custody env if the key is BYOK/CMEK-custodied)", dataDir)
				}
				fmt.Fprintln(human, "note: signing keys are externally custodied (BYOK/CMEK) — the bundle escrows NO key material; at restore time provision the key from your Secret/KMS envelope before verifying")
			}
			// The SAME predicate the note above is printed under, so the document and
			// the note cannot disagree about whether this bundle escrows key material.
			// Zero key refs is enough here: the branch above already REFUSED the run
			// when there were none and no external custody was configured, so reaching
			// this line with zero refs means the custody is external.
			externalKeyCustody := len(keyRefs) == 0

			work, err := os.MkdirTemp("", "olivares-dr-backup-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(work) }()

			var (
				m            *dr.Manifest
				snapshotPath string
			)
			now := time.Now()
			switch store.Engine(sf.engineKind) {
			case store.EngineSQLite:
				m, snapshotPath, err = backupSQLite(cmd.Context(), sf, work, keyRefs, notes, now)
			case store.EnginePostgres:
				m, snapshotPath, err = backupPostgres(cmd.Context(), sf, work, keyRefs, notes, now, pgOpts{
					pgDumpPath: pgDumpPath, snapshotFile: snapshotFile, pitrRef: pitrRef,
				})
			default:
				return fmt.Errorf("unknown --engine %q (sqlite|postgres)", sf.engineKind)
			}
			if err != nil {
				return err
			}

			// Refuse to certify a backup over a chain that is NOT already green — a
			// corrupt ledger must never be captured as if it were a good restore point.
			if bad := unverifiedTenants(m); len(bad) > 0 && !allowUnverified {
				return fmt.Errorf("backup ABORTED: %d tenant chain(s) did not verify at backup time (%s); fix the ledger or pass --allow-unverified to capture anyway", len(bad), strings.Join(bad, ", "))
			}

			f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if err := dr.WriteBundle(f, dr.BundleInput{
				Manifest: m, KEK: cipher.Params(), SnapshotPath: snapshotPath, SealedKeys: sealed,
			}); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Fprintf(human, "DR bundle written: %s\n  taken: %s (RPO basis)\n  engine: %s  tenants: %d  keys: %d\n",
				out, m.CreatedAt, m.EngineKind, len(m.Tenants), len(m.Keys))
			// Built from the SAME values the line above formats, at the same point in
			// the run. Populated here rather than at the end because the offsite and
			// retention steps below can fail or be skipped, and the five facts of the
			// bundle are true the moment the file closed.
			result := drBackupResult{
				Bundle: out, TakenAt: m.CreatedAt, Engine: m.EngineKind,
				Tenants: len(m.Tenants), Keys: len(m.Keys),
				ExternalKeyCustody: externalKeyCustody,
				Retention: drBackupRetention{
					Local: []string{}, Offsite: []string{}, Skipped: []string{},
				},
			}

			// Offsite replication (the "1" of 3-2-1): push the just-written bundle
			// off-box. A push failure FAILS the command — an operator who asked for
			// offsite must not be left with a false sense of safety (only a local copy).
			var offsiteCli *dr.OffsiteClient
			if of.configured() {
				offsiteCli, err = of.client()
				if err != nil {
					return fmt.Errorf("offsite: %w", err)
				}
				if err := pushBundleOffsite(cmd.Context(), offsiteCli, out); err != nil {
					return fmt.Errorf("offsite replication: %w", err)
				}
				fmt.Fprintf(human, "replicated offsite: %s → bucket %s\n", filepath.Base(out), of.bucket)
				// Set only AFTER the push succeeded, which is what makes a non-null
				// offsite in the document mean "this bundle IS off-box".
				result.Offsite = &drOffsiteRef{Object: filepath.Base(out), Bucket: of.bucket, Prefix: of.prefix}
			}

			// Retention: a GFS policy (grandfather-father-son) applied to BOTH the
			// local directory and the offsite mirror, or the legacy age-based
			// --retain-days. GFS takes precedence when any tier is set. Pruning is
			// self-contained (no shell): the engine image is distroless, so a scheduled
			// backup Job runs this single binary.
			if gf.any() {
				policy := gf.policy()
				result.Retention.recordLocal(applyGFSLocal(filepath.Dir(out), filepath.Base(out), policy, now, human))
				if offsiteCli != nil {
					result.Retention.recordOffsite(applyGFSOffsite(cmd.Context(), offsiteCli, filepath.Base(out), policy, now, human))
				}
			} else if retainDays > 0 {
				result.Retention.recordLocal(pruneOldBundles(filepath.Dir(out), out, retainDays, now, human))
			}
			return renderJSONOnly(cmd, result)
		},
	}
	addDRStoreFlags(cmd, &sf)
	addKEKFlags(cmd, &kf)
	addOffsiteFlags(cmd, &of)
	addGFSFlags(cmd, &gf)
	cmd.Flags().StringVar(&out, "out", "", "path to write the DR bundle to (required)")
	cmd.Flags().StringVar(&notes, "notes", "", "free-form operator note recorded in the manifest (no secrets)")
	cmd.Flags().BoolVar(&allowUnverified, "allow-unverified", false, "capture even if a tenant chain fails verification at backup time (NOT recommended)")
	cmd.Flags().StringVar(&pgDumpPath, "pg-dump", "pg_dump", "pg_dump executable (Postgres engine only)")
	cmd.Flags().StringVar(&snapshotFile, "snapshot-file", "", "Postgres only: use this pre-made dump as the store snapshot instead of running pg_dump (e.g. produced by a postgres-client sidecar)")
	cmd.Flags().StringVar(&pitrRef, "pitr-ref", "", "Postgres only: build a keys+manifest companion bundle for a point-in-time-recovery archive (no store bytes); the value is a human pointer to the WAL archive")
	cmd.Flags().IntVar(&retainDays, "retain-days", 0, "after a successful write, prune sibling *.drbundle files older than N days in the --out directory (0 = keep all). The offsite mirror keeps longer (3-2-1)")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

// pruneOldBundles deletes sibling *.drbundle files in dir whose mtime is older
// than retainDays days, never touching the bundle just written (keep). It is the
// shell-free, distroless-safe replacement for `find -mtime +N -delete`. Errors are
// non-fatal: a backup must not be reported as failed because a stale file could
// not be pruned — the run already produced a valid bundle.
//
// It returns the paths it deleted (VER-06), for the reason written on
// applyGFSLocal: retention that only reports itself in prose cannot be checked by
// the scheduled job that ran it. The printed lines are unchanged.
//
// retainDays <= 0 is NOT a skip: it means no retention was asked for on this tier,
// and the only caller already guards on it. A skip is a tier the caller DID ask for
// that could not run — here, a listing that failed.
func pruneOldBundles(dir, keep string, retainDays int, now time.Time, out io.Writer) retentionOutcome {
	if retainDays <= 0 {
		return retentionOutcome{}
	}
	cutoff := now.Add(-time.Duration(retainDays) * 24 * time.Hour)
	matches, err := filepath.Glob(filepath.Join(dir, "*.drbundle"))
	if err != nil {
		fmt.Fprintf(out, "warning: bundle prune skipped (%v)\n", err)
		return retentionOutcome{Skipped: true}
	}
	var pruned []string
	keepAbs, _ := filepath.Abs(keep)
	for _, m := range matches {
		if mAbs, _ := filepath.Abs(m); mAbs == keepAbs {
			continue
		}
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(m); err != nil {
				fmt.Fprintf(out, "warning: could not prune %s (%v)\n", m, err)
				continue
			}
			fmt.Fprintf(out, "pruned bundle older than %dd: %s\n", retainDays, m)
			pruned = append(pruned, m)
		}
	}
	return retentionOutcome{Deleted: pruned}
}

// pgOpts selects how a Postgres backup obtains its store snapshot.
type pgOpts struct {
	// pgDumpPath is the pg_dump executable (used only when snapshotFile and pitrRef
	// are both empty).
	pgDumpPath string
	// snapshotFile, when set, is a pre-made dump to bundle as-is (no pg_dump run).
	snapshotFile string
	// pitrRef, when set, produces a keys+manifest companion bundle for PITR (no
	// store bytes); its value is a pointer to the external WAL archive.
	pitrRef string
}

// backupSQLite snapshots the SQLite store with VACUUM INTO and builds the manifest
// from a boot of that exact snapshot, so the recorded tips match the bundle bytes
// (TipExact). It never opens the LIVE engine, so it is safe to run while serve is
// up (WAL allows the concurrent read).
func backupSQLite(ctx context.Context, sf drFlags, work string, keyRefs []dr.KeyRef, notes string, now time.Time) (*dr.Manifest, string, error) {
	srcDB := sf.dsn
	if srcDB == "" {
		srcDB = filepath.Join(sf.dataDir, "olivares.db")
	}
	snap := filepath.Join(work, "snapshot.db")
	if err := dr.SnapshotSQLite(ctx, srcDB, snap); err != nil {
		return nil, "", err
	}
	sum, size, err := dr.FileSHA256(snap)
	if err != nil {
		return nil, "", err
	}

	// Build the manifest by booting a COPY of the snapshot with COPIES of the
	// signing keys (so the engine's signer matches the source key). The copy is
	// throwaway; the original snapshot's bytes are what get bundled and digested.
	mdir, err := os.MkdirTemp(work, "manifest-")
	if err != nil {
		return nil, "", err
	}
	if err := dr.CopyFile(snap, filepath.Join(mdir, "olivares.db")); err != nil {
		return nil, "", err
	}
	if err := copySigningKeys(sf.dataDir, mdir); err != nil {
		return nil, "", err
	}
	eng, err := drBoot(ctx, drFlags{dataDir: mdir, engineKind: string(store.EngineSQLite)})
	if err != nil {
		return nil, "", fmt.Errorf("open snapshot to build manifest: %w", err)
	}
	defer func() { _ = eng.Close() }()

	cpv, err := eng.signer.CheckpointVerifier(ctx)
	if err != nil {
		return nil, "", err
	}
	m, err := dr.BuildManifest(ctx, eng.store, eng.signer.PublicKey(), cpv, dr.BuildOptions{
		EngineKind: string(store.EngineSQLite), Version: version,
		Store:    dr.StoreSnapshot{Method: dr.MethodVacuumInto, File: "store/olivares.db", SizeBytes: size, SHA256: sum},
		Keys:     keyRefs,
		TipMatch: dr.TipExact, Now: now, Notes: notes,
	})
	if err != nil {
		return nil, "", err
	}
	return m, snap, nil
}

// backupPostgres dumps the Postgres store with pg_dump (custom format, a single
// consistent snapshot) and builds the manifest from the LIVE store (TipAdvisory:
// the live read may trail the dump by the online-backup window; the chain
// self-verification on restore is the real guarantee). For point-in-time recovery
// (near-zero RPO) the runbook uses pg_basebackup + WAL archiving instead; this
// command captures the logical-dump tier.
func backupPostgres(ctx context.Context, sf drFlags, work string, keyRefs []dr.KeyRef, notes string, now time.Time, opt pgOpts) (*dr.Manifest, string, error) {
	if sf.dsn == "" {
		return nil, "", fmt.Errorf("--dsn is required for a Postgres backup")
	}

	// Pick the store snapshot source: a PITR companion (no bytes), a pre-made dump,
	// or a pg_dump we run here.
	var (
		snapMeta     dr.StoreSnapshot
		snapshotPath string
	)
	switch {
	case opt.pitrRef != "":
		// Keys + manifest companion for point-in-time recovery: the store is
		// recovered out of band from the WAL archive; this bundle carries the
		// signing keys and the per-tenant tips needed to verify continuity after.
		if err := validatePITRRef(opt.pitrRef); err != nil {
			return nil, "", err
		}
		snapMeta = dr.StoreSnapshot{Method: dr.MethodPITR, File: opt.pitrRef}
	case opt.snapshotFile != "":
		sum, size, err := dr.FileSHA256(opt.snapshotFile)
		if err != nil {
			return nil, "", fmt.Errorf("read --snapshot-file: %w", err)
		}
		snapMeta = dr.StoreSnapshot{Method: dr.MethodPgDump, File: "store/dump.pgcustom", SizeBytes: size, SHA256: sum}
		snapshotPath = opt.snapshotFile
	default:
		// The direct dump runs on the ADMIN (BYPASSRLS) DSN, never the application
		// DSN: pg_dump keeps row_security=off by default and ABORTS as a role that
		// cannot bypass the FORCE ROW LEVEL SECURITY policies on every tenant
		// table — the app-DSN invocation cannot produce a dump at all. This is the
		// same invariant the Helm chart and the Operator hold for their pg_dump
		// initContainers; requiring it here keeps the binary's own default path
		// from being the one broken variant.
		if sf.adminDSN == "" {
			return nil, "", fmt.Errorf("--admin-dsn is required to run pg_dump directly: pg_dump aborts under FORCE ROW LEVEL SECURITY as the NOBYPASSRLS application role (alternatively, supply --snapshot-file with a dump produced by deploy/postgres/backup/pg-dump.sh)")
		}
		snap := filepath.Join(work, "dump.pgcustom")
		if err := pgDumpRunner(ctx, opt.pgDumpPath, sf.adminDSN, snap); err != nil {
			return nil, "", err
		}
		sum, size, err := dr.FileSHA256(snap)
		if err != nil {
			return nil, "", err
		}
		snapMeta = dr.StoreSnapshot{Method: dr.MethodPgDump, File: "store/dump.pgcustom", SizeBytes: size, SHA256: sum}
		snapshotPath = snap
	}

	eng, err := drBoot(ctx, sf)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = eng.Close() }()
	if sf.adminDSN == "" {
		slog.Default().Warn("postgres backup without --admin-dsn: the org list (hence the tenant set captured in the manifest) may be RLS-limited and INCOMPLETE; provision a BYPASSRLS admin role for a complete backup")
	}
	cpv, err := eng.signer.CheckpointVerifier(ctx)
	if err != nil {
		return nil, "", err
	}
	m, err := dr.BuildManifest(ctx, eng.store, eng.signer.PublicKey(), cpv, dr.BuildOptions{
		EngineKind: string(store.EnginePostgres), Version: version,
		Store:    snapMeta,
		Keys:     keyRefs,
		TipMatch: dr.TipAdvisory, Now: now, Notes: notes,
	})
	if err != nil {
		return nil, "", err
	}
	return m, snapshotPath, nil
}

func drRestoreCmd() *cobra.Command {
	var sf drFlags
	var kf kekFlags
	var decl restoreDeclaration
	var in string
	var force, inPlace bool
	var pgRestorePath string
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a DR bundle and verify ledger continuity (non-zero exit if not safe)",
		Long: "restore validates a DR bundle, decrypts its signing keys, restores the matching store\n" +
			"snapshot and verifies the complete ledger before reporting success. For SQLite, --in-place\n" +
			"stages and verifies the restore before replacing a live data directory.",
		Example: `  olivares dr restore --in /srv/backups/olivares-2026-07-14.drbundle \
    --passphrase-file /run/secrets/dr-passphrase --data-dir /srv/olivares-restored`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if in == "" {
				return fmt.Errorf("--in is required")
			}
			resolvedDataDir, err := resolveDataDir(sf.dataDir)
			if err != nil {
				return err
			}
			sf.dataDir = resolvedDataDir

			// BEFORE anything is read, decrypted or written: if this restore would
			// REPLACE an existing estate, it is the one destructive act in the
			// product that the console's two-person gate cannot see. It has
			// to name an operator and a reason, which are sealed into the restored
			// ledger once the restore is proven safe. A restore into a clean target
			// destroys nothing and is deliberately left untouched.
			//
			// The verdict asks the TARGET, not only the filesystem, and fails closed
			// when it cannot be read: the filesystem says nothing about a
			// Postgres estate at the far end of a DSN, and believing it did let a
			// live database be modified with nothing required and nothing sealed.
			replaces, why := restoreTarget{
				engineKind: sf.engineKind, dsn: sf.dsn, dataDir: sf.dataDir,
			}.replacesAnEstate(cmd.Context())
			if replaces {
				if err := decl.require(why); err != nil {
					return err
				}
			}

			work, err := os.MkdirTemp("", "olivares-dr-restore-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(work) }()
			m, kek, err := openAndCheckBundle(in, work)
			if err != nil {
				return err
			}
			if m.EngineKind != sf.engineKind {
				return fmt.Errorf("bundle engine is %q but --engine is %q; restore to the matching engine", m.EngineKind, sf.engineKind)
			}
			cipher, err := kf.restoreCipher(kek)
			if err != nil {
				return err
			}

			// --in-place: replace a LIVE data dir SAFELY. Stage the restore, verify the
			// staged ledger BEFORE touching production, auto-preserve the current
			// store/keys, then promote atomically. A failed verify leaves the live data
			// dir untouched. (SQLite only; Postgres restores into a live DB via pg_restore.)
			if inPlace {
				if store.Engine(sf.engineKind) != store.EngineSQLite {
					return fmt.Errorf("--in-place is supported for the sqlite engine only (postgres restores into a live DB with pg_restore; see docs/DR-RUNBOOK.md)")
				}
				if m.Store.Method == dr.MethodPITR {
					return fmt.Errorf("--in-place does not apply to a PITR companion bundle")
				}
				ts := time.Now().UTC().Format("20060102-150405")
				return restoreInPlaceSQLite(cmd.Context(), cmd, work, sf.dataDir, m, cipher, ts,
					declaredRestore{required: replaces, decl: decl, bundle: in, verdict: why})
			}

			// 0) Everything below this point is destructive and IRREVERSIBLE, and the
			// declaration is only sealed at the very end — so a failure in between
			// (a full audit spool in block mode is the measured one) ends with the
			// estate replaced and no record of it. The staged discipline that makes
			// --in-place safe cannot be lifted here; what CAN be is the loss. Take the
			// same automatic pre-restore copy --in-place takes, so the previous state
			// stays recoverable (the P2 of the sol-max contrast).
			//
			// Gated on --force because the guards below are what refuse an
			// unintentional overwrite: preserving first would move the very files
			// those guards test for, and a safety copy must not disable a refusal.
			// With --force the operator has already said "overwrite", so the only
			// question left is whether the old bytes survive it.
			var preserved map[string]string
			if replaces && force {
				ts := time.Now().UTC().Format("20060102-150405")
				var perr error
				preserved, perr = preserveCurrent(sf.dataDir, m, ts)
				if perr != nil {
					rollbackPreserved(sf.dataDir, preserved)
					return fmt.Errorf("preserve the state this restore would overwrite: %w", perr)
				}
				if len(preserved) > 0 {
					drNote(cmd, fmt.Sprintf(
						"previous state preserved as *.pre-restore-%s in %s (remove once satisfied)", ts, sf.dataDir))
				}
			}

			// undoIfTheStoreWasNeverTouched puts the preserved custody back. It is only
			// ever called BEFORE the store bytes are written, which is what makes it
			// safe: rolling keys back after the store was replaced would leave custody
			// that does not match the estate, which is worse than either end state.
			//
			// It exists because the keys are installed FIRST and the store second, so a
			// store restore that fails — most sharply a pg_restore that conflicts and
			// rolls its own transaction back — used to leave the live estate running on
			// the BUNDLE's signing keys, with no rollback and no sealed record. The
			// database was untouched and the custody was silently replaced (the
			// contrast's B-03).
			undoIfTheStoreWasNeverTouched := func(err error) error {
				if len(preserved) == 0 {
					return err
				}
				rollbackPreserved(sf.dataDir, preserved)
				return fmt.Errorf("%w; the store was NOT touched, so the previous signing keys were put back", err)
			}

			// 1) Restore the signing keys under custody (fail-closed on overwrite).
			if err := restoreKeys(work, sf.dataDir, m, cipher, force); err != nil {
				return undoIfTheStoreWasNeverTouched(err)
			}
			// 2) Restore the store snapshot. A PITR companion bundle carries no store
			// bytes — the operator recovered Postgres out of band via WAL replay — so
			// we skip straight to the continuity verification against that live store.
			if m.Store.Method == dr.MethodPITR {
				fmt.Fprintln(cmd.OutOrStdout(), "PITR companion bundle: keys restored; verifying against the out-of-band-recovered store…")
			}
			switch store.Engine(sf.engineKind) {
			case store.EngineSQLite:
				if m.Store.Method == dr.MethodPITR {
					return fmt.Errorf("PITR companion bundle is for the Postgres engine, not sqlite")
				}
				dest := sf.dsn
				if dest == "" {
					dest = filepath.Join(sf.dataDir, "olivares.db")
				}
				if _, err := os.Stat(dest); err == nil && !force {
					return undoIfTheStoreWasNeverTouched(fmt.Errorf("%s already exists; restore into an empty data dir or pass --force", dest))
				}
				if err := dr.CopyFile(filepath.Join(work, m.Store.File), dest); err != nil {
					return err
				}
			case store.EnginePostgres:
				if sf.dsn == "" {
					return undoIfTheStoreWasNeverTouched(fmt.Errorf("--dsn is required for a Postgres restore"))
				}
				if m.Store.Method != dr.MethodPITR {
					if err := runPgRestore(cmd.Context(), pgRestorePath, sf.dsn, filepath.Join(work, m.Store.File)); err != nil {
						// --single-transaction means no object and no row of the backup
						// reached the database, so the estate is exactly where it was —
						// EXCEPT for the custody this command already replaced.
						return undoIfTheStoreWasNeverTouched(err)
					}
				}
			default:
				return fmt.Errorf("unknown --engine %q", sf.engineKind)
			}

			// 3) Boot the restored store and PROVE continuity before returning.
			eng, err := drBoot(cmd.Context(), sf)
			if err != nil {
				return fmt.Errorf("boot restored store: %w", err)
			}
			defer func() { _ = eng.Close() }()
			cpv, err := eng.signer.CheckpointVerifier(cmd.Context())
			if err != nil {
				return err
			}
			rep, err := dr.RestoreVerify(cmd.Context(), eng.store, m, eng.signer.PublicKey(), cpv)
			if err != nil {
				return err
			}
			if err := printReport(cmd, rep); err != nil {
				return err
			}
			if !rep.OK {
				return fmt.Errorf("restore is NOT ledger-continuity-safe: %s", strings.Join(rep.Problems, "; "))
			}
			// Seal the declaration into the estate that now exists. It goes AFTER the
			// continuity verification so it never perturbs the tip the verifier
			// compares against the manifest, and it goes into the RESTORED store
			// because anything written before promotion is overwritten by the
			// promotion itself.
			if replaces {
				if err := sealRestoreDeclaration(cmd.Context(), cmd, eng.store, decl, restoreEvidence{
					engine: sf.engineKind, bundle: filepath.Base(in), takenAt: m.CreatedAt,
					targetVerdict: why,
				}); err != nil {
					if len(preserved) > 0 {
						return fmt.Errorf("%w. The state this replaced is still on disk as *.pre-restore-* in %s, "+
							"so this is recoverable — do not delete those files until the record exists", err, sf.dataDir)
					}
					return err
				}
			}
			drNote(cmd, "restore verified: ledger continuity and key custody intact")
			return nil
		},
	}
	addDRStoreFlags(cmd, &sf)
	addKEKFlags(cmd, &kf)
	addRestoreDeclarationFlags(cmd, &decl)
	cmd.Flags().StringVar(&in, "in", "", "DR bundle to restore (required)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing keys / store file in the data dir")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "replace a LIVE data dir safely: stage + verify BEFORE promoting, auto-preserving the current store/keys as *.pre-restore-<ts> (sqlite only)")
	cmd.Flags().StringVar(&pgRestorePath, "pg-restore", "pg_restore", "pg_restore executable (Postgres engine only)")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

func drVerifyCmd() *cobra.Command {
	var kf kekFlags
	var in string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Test a DR bundle WITHOUT touching the live data dir (the DR drill)",
		Long: "verify extracts a bundle to a throwaway location, decrypts its keys and (for\n" +
			"SQLite) restores + verifies the full chain there, so you can prove a backup is\n" +
			"restorable on a cadence (docs/DR-RUNBOOK.md) without disturbing production.",
		Example: "  olivares dr verify --in /srv/backups/olivares-2026-07-14.drbundle --passphrase-file /run/secrets/dr-passphrase",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if in == "" {
				return fmt.Errorf("--in is required")
			}
			work, err := os.MkdirTemp("", "olivares-dr-verify-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(work) }()
			m, kek, err := openAndCheckBundle(in, work)
			if err != nil {
				return err
			}
			cipher, err := kf.restoreCipher(kek)
			if err != nil {
				return err
			}
			if m.Store.Method == dr.MethodPITR {
				// A PITR companion carries no store bytes; verifying its chain needs the
				// out-of-band-recovered Postgres, so here we prove only that the bundle is
				// intact and the keys decrypt.
				if _, _, derr := decryptAllKeys(work, m, cipher); derr != nil {
					return derr
				}
				drNote(cmd, "PITR companion bundle: integrity OK (keys decrypt). Full chain verification requires the out-of-band-recovered Postgres (see docs/DR-RUNBOOK.md).")
				return printManifest(cmd, m)
			}
			if m.EngineKind != string(store.EngineSQLite) {
				// A Postgres chain check needs a scratch Postgres; here we prove the
				// bundle is intact and the keys decrypt, and surface the manifest.
				if _, _, err := decryptAllKeys(work, m, cipher); err != nil {
					return err
				}
				drNote(cmd, "bundle integrity OK (digest + keys decrypt). Full chain verification for a Postgres bundle requires restoring to a scratch Postgres (see docs/DR-RUNBOOK.md).")
				return printManifest(cmd, m)
			}
			vdir, err := os.MkdirTemp(work, "scratch-")
			if err != nil {
				return err
			}
			if err := restoreKeys(work, vdir, m, cipher, true); err != nil {
				return err
			}
			if err := dr.CopyFile(filepath.Join(work, m.Store.File), filepath.Join(vdir, "olivares.db")); err != nil {
				return err
			}
			eng, err := drBoot(cmd.Context(), drFlags{dataDir: vdir, engineKind: string(store.EngineSQLite)})
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			cpv, err := eng.signer.CheckpointVerifier(cmd.Context())
			if err != nil {
				return err
			}
			rep, err := dr.RestoreVerify(cmd.Context(), eng.store, m, eng.signer.PublicKey(), cpv)
			if err != nil {
				return err
			}
			if err := printReport(cmd, rep); err != nil {
				return err
			}
			if !rep.OK {
				return fmt.Errorf("DR drill FAILED: bundle is not restorable to a safe ledger: %s", strings.Join(rep.Problems, "; "))
			}
			drNote(cmd, "DR drill PASSED: bundle restores to a continuity-safe ledger")
			return nil
		},
	}
	addKEKFlags(cmd, &kf)
	cmd.Flags().StringVar(&in, "in", "", "DR bundle to verify (required)")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

func drInspectCmd() *cobra.Command {
	var in string
	cmd := &cobra.Command{
		Use:     "inspect",
		Short:   "Print a DR bundle's manifest (no KEK needed; no secrets shown)",
		Long:    "inspect extracts and prints a DR bundle's non-secret manifest without requiring or decrypting its KEK-protected signing keys.",
		Example: "  olivares dr inspect --in /srv/backups/olivares-2026-07-14.drbundle",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if in == "" {
				return fmt.Errorf("--in is required")
			}
			work, err := os.MkdirTemp("", "olivares-dr-inspect-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(work) }()
			f, err := os.Open(in)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			m, _, err := dr.ExtractBundle(f, work)
			if err != nil {
				return err
			}
			return printManifest(cmd, m)
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "DR bundle to inspect (required)")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

// --- shared helpers ---------------------------------------------------------

// validatePITRRef rejects a PITR pointer that looks like a filesystem path
// escape. The value is metadata (a pointer to an external WAL archive), never used
// as a path on restore, but keeping it inert defends a future consumer and an
// operator from a surprising manifest. Control characters are also rejected.
func validatePITRRef(ref string) error {
	if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "../") || strings.Contains(ref, "/../") || ref == ".." {
		return fmt.Errorf("--pitr-ref must be a pointer (e.g. a URI), not a filesystem path: %q", ref)
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("--pitr-ref contains a control character")
		}
	}
	return nil
}

// resolveDataDir returns d, or the engine's default when d is empty. It returns
// an error rather than a guess: since the default is a per-user directory,
// and the one location that is never an acceptable fallback is the working
// directory the operator happened to be standing in (see defaultDataDir).
func resolveDataDir(d string) (string, error) {
	if d != "" {
		return d, nil
	}
	return defaultDataDir()
}

// signingKeyFiles returns the *-signing.key files in dir (audit + catalog). It
// deliberately excludes TLS material and the setup token — only the ledger's
// signing keys belong in the DR custody set (minimal data, docs/SECURITY-HARDENING.md).
func signingKeyFiles(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*-signing.key"))
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func roleForKey(name string) string {
	switch {
	case strings.HasPrefix(name, "audit-"):
		return dr.RoleAudit
	case strings.HasPrefix(name, "catalog-"):
		return dr.RoleCatalog
	default:
		return dr.RoleOther
	}
}

func sealSigningKeys(dir string, cipher *dr.KeyCipher) (map[string][]byte, []dr.KeyRef, error) {
	files, err := signingKeyFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	sealed := map[string][]byte{}
	var refs []dr.KeyRef
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		fp, err := dr.PubFingerprintFromSigningKey(b)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		blob, err := cipher.Seal(b)
		if err != nil {
			return nil, nil, err
		}
		name := filepath.Base(path)
		bundlePath := "keys/" + name + ".enc"
		sealed[bundlePath] = blob
		refs = append(refs, dr.KeyRef{File: bundlePath, Name: name, Role: roleForKey(name), PubSHA256: fp})
	}
	return sealed, refs, nil
}

func copySigningKeys(srcDir, dstDir string) error {
	files, err := signingKeyFiles(srcDir)
	if err != nil {
		return err
	}
	for _, path := range files {
		if err := dr.CopyFile(path, filepath.Join(dstDir, filepath.Base(path))); err != nil {
			return err
		}
	}
	return nil
}

// restoreInPlaceSQLite replaces a LIVE SQLite data dir safely: it stages the
// restored keys + store in a sibling dir on the SAME filesystem, boots and fully
// re-verifies that staged ledger BEFORE touching production, and only then promotes
// — first moving the current store/keys aside as *.pre-restore-<ts> (an automatic
// pre-restore backup), then atomically renaming the staged files into place. If the
// staged verification fails, the live data dir is left completely untouched; if a
// promotion rename fails, the preserved files are rolled back. This is the
// production restore path for an operator who cannot take the dir empty first.
func restoreInPlaceSQLite(ctx context.Context, cmd *cobra.Command, work, dataDir string, m *dr.Manifest, cipher *dr.KeyCipher, ts string, dr601 declaredRestore) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	// Stage on the SAME filesystem as dataDir so the promotion renames are atomic.
	staging, err := os.MkdirTemp(dataDir, ".dr-staging-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := restoreKeys(work, staging, m, cipher, true); err != nil {
		return err
	}
	if err := dr.CopyFile(filepath.Join(work, m.Store.File), filepath.Join(staging, "olivares.db")); err != nil {
		return err
	}

	// Prove the STAGED restore is continuity-safe before promoting anything.
	eng, err := drBoot(ctx, drFlags{dataDir: staging, engineKind: string(store.EngineSQLite)})
	if err != nil {
		return fmt.Errorf("boot staged restore: %w", err)
	}
	cpv, err := eng.signer.CheckpointVerifier(ctx)
	if err != nil {
		_ = eng.Close()
		return err
	}
	rep, err := dr.RestoreVerify(ctx, eng.store, m, eng.signer.PublicKey(), cpv)
	if err != nil {
		_ = eng.Close()
		return err
	}
	if rep.OK && dr601.required {
		// Seal the declaration into the STAGED estate, after its verification and
		// before it becomes the live one: promotion renames these very bytes into
		// place, so a record written anywhere else would be overwritten by it. A
		// staged restore that cannot be accounted for is not promoted at all.
		if derr := sealRestoreDeclaration(ctx, cmd, eng.store, dr601.decl, restoreEvidence{
			engine: string(store.EngineSQLite), bundle: filepath.Base(dr601.bundle),
			takenAt: m.CreatedAt, inPlace: true,
		}); derr != nil {
			_ = eng.Close()
			return fmt.Errorf("%w; the live data dir was left UNTOUCHED", derr)
		}
	}
	_ = eng.Close()
	// The report is checked here for the same reason the other two call sites check it
	// (:656, :764) and this one did not: it is the ONLY account of the staged restore the
	// operator gets, and the next thing this function does is PROMOTE — move the live data
	// files aside and replace them. A failed render that is discarded means promoting on
	// an unseen verdict, so a render failure has to stop the promote, not decorate it.
	if err := printReport(cmd, rep); err != nil {
		return fmt.Errorf("%w; the live data dir was left UNTOUCHED (the staged restore was not promoted, because its report could not be rendered)", err)
	}
	if !rep.OK {
		return fmt.Errorf("staged restore is NOT ledger-continuity-safe; the live data dir was left UNTOUCHED: %s", strings.Join(rep.Problems, "; "))
	}

	// Promote. Move the current live files aside first (auto pre-restore backup).
	moved, err := preserveCurrent(dataDir, m, ts)
	if err != nil {
		return fmt.Errorf("preserve current state: %w", err)
	}
	if err := promoteStaged(staging, dataDir, m); err != nil {
		rollbackPreserved(dataDir, moved)
		return fmt.Errorf("promote staged restore (rolled back to the pre-restore state): %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "restore verified and promoted in place: ledger continuity and key custody intact\n")
	fmt.Fprintf(cmd.OutOrStdout(), "previous state preserved as *.pre-restore-%s in %s (remove once satisfied)\n", ts, dataDir)
	return nil
}

// preserveCurrent renames the current store file and every existing signing key in
// dataDir to <name>.pre-restore-<ts>, returning original→preserved for rollback.
func preserveCurrent(dataDir string, m *dr.Manifest, ts string) (map[string]string, error) {
	moved := map[string]string{}
	preserve := func(name string) error {
		orig := filepath.Join(dataDir, name)
		if _, err := os.Stat(orig); os.IsNotExist(err) {
			return nil // nothing to preserve (fresh dir)
		} else if err != nil {
			return err
		}
		dst := orig + ".pre-restore-" + ts
		if err := os.Rename(orig, dst); err != nil {
			return err
		}
		moved[orig] = dst
		return nil
	}
	if err := preserve("olivares.db"); err != nil {
		return moved, err
	}
	for _, kr := range m.Keys {
		if err := preserve(kr.Name); err != nil {
			return moved, err
		}
	}
	// Preserve any signing key present in the dir but NOT in the manifest, so a key
	// the bundle does not carry is not silently shadowed by the restore.
	if extra, _ := signingKeyFiles(dataDir); extra != nil {
		for _, p := range extra {
			name := filepath.Base(p)
			if strings.Contains(name, ".pre-restore-") {
				continue
			}
			if err := preserve(name); err != nil {
				return moved, err
			}
		}
	}
	return moved, nil
}

// promoteStaged atomically renames the staged store + keys into dataDir. Each rename
// is atomic on the same filesystem; the current files were already moved aside, so
// the destinations are free.
func promoteStaged(staging, dataDir string, m *dr.Manifest) error {
	if err := os.Rename(filepath.Join(staging, "olivares.db"), filepath.Join(dataDir, "olivares.db")); err != nil {
		return err
	}
	for _, kr := range m.Keys {
		if err := os.Rename(filepath.Join(staging, kr.Name), filepath.Join(dataDir, kr.Name)); err != nil {
			return err
		}
	}
	return nil
}

// rollbackPreserved best-effort restores the preserved files after a failed promote.
func rollbackPreserved(dataDir string, moved map[string]string) {
	for orig, preserved := range moved {
		_ = os.Rename(preserved, orig)
	}
}

// openAndCheckBundle extracts a bundle and verifies the snapshot digest against
// the manifest (the first gate of any restore: detect a corrupt/tampered bundle).
func openAndCheckBundle(in, work string) (*dr.Manifest, dr.KDFParams, error) {
	f, err := os.Open(in)
	if err != nil {
		return nil, dr.KDFParams{}, err
	}
	defer func() { _ = f.Close() }()
	m, kek, err := dr.ExtractBundle(f, work)
	if err != nil {
		return nil, dr.KDFParams{}, err
	}
	if m.Store.SHA256 != "" {
		sum, _, err := dr.FileSHA256(filepath.Join(work, m.Store.File))
		if err != nil {
			return nil, dr.KDFParams{}, err
		}
		if sum != m.Store.SHA256 {
			return nil, dr.KDFParams{}, fmt.Errorf("bundle snapshot digest mismatch (corrupt or tampered bundle): got %s, manifest %s", sum, m.Store.SHA256)
		}
	}
	return m, kek, nil
}

func decryptAllKeys(work string, m *dr.Manifest, cipher *dr.KeyCipher) (map[string][]byte, []dr.KeyRef, error) {
	out := map[string][]byte{}
	for _, kr := range m.Keys {
		blob, err := os.ReadFile(filepath.Join(work, kr.File))
		if err != nil {
			return nil, nil, err
		}
		plain, err := cipher.Open(blob)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", kr.Name, err)
		}
		out[kr.Name] = plain
	}
	return out, m.Keys, nil
}

// restoreKeys decrypts each bundled signing key into dataDir (0600), fail-closed
// on an existing file unless force.
func restoreKeys(work, dataDir string, m *dr.Manifest, cipher *dr.KeyCipher, force bool) error {
	// EnsureDataDir rather than a bare MkdirAll: this function writes PRIVATE KEYS,
	// and it runs before the boot that would otherwise have created the directory's
	// VCS exclusion. A restore that fails after this point — or an --in-place
	// promotion, which moves the keys but not the marker — used to leave that key
	// material with nothing excluding it (found by the sol-max contrast).
	if err := secure.EnsureDataDir(dataDir); err != nil {
		return err
	}
	keys, _, err := decryptAllKeys(work, m, cipher)
	if err != nil {
		return err
	}
	for name, plain := range keys {
		dest := filepath.Join(dataDir, name)
		if _, err := os.Stat(dest); err == nil && !force {
			return fmt.Errorf("%s already exists; restore into an empty data dir or pass --force", dest)
		}
		if err := os.WriteFile(dest, plain, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func unverifiedTenants(m *dr.Manifest) []string {
	var bad []string
	for _, t := range m.Tenants {
		if !t.VerifiedAtBackup {
			bad = append(bad, fmt.Sprintf("%s:%s", short(t.Tenant), t.VerifyReason))
		}
	}
	return bad
}

func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// printReport renders the DR report through the shared renderer, and RETURNS the
// error rather than discarding it: a report that failed to render is a command
// that produced nothing, and swallowing that turns it into a silent success.
func printReport(cmd *cobra.Command, rep *dr.RestoreReport) error {
	return renderReportOut(cmd, rep)
}

// printManifest goes through the renderer too. It marshaled directly until the
// sol-max contrast pointed out that `-o text` did nothing here.
func printManifest(cmd *cobra.Command, m *dr.Manifest) error {
	return renderReportOut(cmd, m)
}

// drNote writes a human status sentence to STDERR.
//
// It exists because these commands used to render the machine report to stdout
// and then append a sentence like "restore verified: …" to the SAME stream — so
// `dr restore -o json | jq` received a JSON document followed by prose and could
// not parse it. The sentence is worth keeping; it just is not part of the
// payload.
func drNote(cmd *cobra.Command, msg string) {
	fmt.Fprintln(cmd.ErrOrStderr(), msg)
}

// pgDumpRunner is the indirection the credential-selection test replaces. It
// exists so a test can assert WHICH DSN reaches pg_dump, not merely that the
// missing-flag guard fires: a mutation that keeps the guard and passes sf.dsn
// again would otherwise go unnoticed, which is the whole defect being fixed.
var pgDumpRunner = runPgDump

// runPgDump captures a consistent Postgres logical dump (custom format). pg_dump's
// default single transaction is consistent, so the chain and its head are dumped
// at one instant. NOT exercised by CI here (no live Postgres); the mechanism is
// the standard, supported one (docs/DR-RUNBOOK.md).
func runPgDump(ctx context.Context, bin, dsn, out string) error {
	cmd := exec.CommandContext(ctx, bin, "--format=custom", "--no-owner", "--no-privileges", "--file", out, "--dbname", dsn) // #nosec G204 -- bin is the operator-configured pg_dump path; all other args are fixed flags
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w (is %q on PATH?)", err, bin)
	}
	return nil
}

// runPgRestore restores a custom-format dump into the target database. The target
// should be an empty database (a fresh DR target).
//
// --single-transaction is a SAFETY flag here, not a tuning one, and it was added
// because the absence of it had been reasoned about instead of measured.
// pg_restore is not atomic by default: without it, a restore that hits conflicts
// applies whatever did not conflict and exits non-zero. MEASURED against
// PostgreSQL 16.14 with the previous argv, against a live database already holding
// the dump's schema plus rows created after the copy — rc 1, four errors, and the
// backup's rows INSERTED into both pre-existing tables, live rows still there. The
// command reported a failure and had already replaced part of an estate.
//
// With --single-transaction the same run exits 1 and NONE of the dump's objects or
// rows land, and a restore into an empty target still exits 0 with everything
// restored (both measured on the same server; the two properties are pinned by
// cmd_dr_postgres_test.go against a real one, because no double reproduces
// pg_restore's partial-effect semantics).
//
// THE EXACT CLAIM, because the first version of this comment overreached and an
// external contrast measured it down. "Single transaction" is not "the target is
// untouched": a few Postgres effects are not transactional and survive the
// rollback — sequence advances (nextval/setval) most sharply, and anything a
// pre-existing event trigger or a VOLATILE side effect does while the restore
// runs. The contrast forced exactly that: an event trigger on the target bumped a
// live sequence, pg_restore exited 1, the dump's table was rolled back and the
// SEQUENCE STAYED ADVANCED. So what this buys is precise and worth having — no
// object and no row of the backup is written into a live estate by a failed
// restore — and it is not a promise that nothing at all happened.
//
// What it costs: --single-transaction implies --exit-on-error, so a restore that
// used to limp to a partial success now fails whole. That is the intended trade —
// a DR restore that "mostly worked" is the worst outcome available, because the
// ledger it produces is neither the old estate nor the backup. It also rules out
// parallel restore (--jobs), which this wrapper never asked for.
func runPgRestore(ctx context.Context, bin, dsn, in string) error {
	cmd := exec.CommandContext(ctx, bin, "--no-owner", "--no-privileges", "--single-transaction", "--dbname", dsn, in) // #nosec G204 -- bin is the operator-configured pg_restore path; all other args are fixed flags
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %w (is %q on PATH?). "+
			"The restore ran in a SINGLE TRANSACTION, so NONE of the backup's objects or rows were written; "+
			"effects Postgres does not roll back (an advanced sequence, anything a pre-existing event trigger did) can still have happened", err, bin)
	}
	return nil
}
