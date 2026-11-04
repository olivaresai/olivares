// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/dr/opgate"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// installedPackageBinary is the path the packages (deb, rpm, apk, AUR) install the
// binary at: the file the package manager's database owns. Both readers of "this is the
// installed package's binary" decide from it — installedPackageSnapshotRequest
// (the serve path's snapshot request) and `olivares upgrade`'s refusal to swap
// it in place. A variable so a test can stand a fake path in for the
// package-owned binary, the way runningBinary stands in for the image binary.
var installedPackageBinary = "/usr/bin/olivares"

// packageManagerRoute is how the package-owned binary is upgraded. The appliance unit
// is named because it renews the entitled-channel credential before it runs dnf, which a
// bare package-manager command would skip. One copy, for every refusal that names the route.
const packageManagerRoute = "upgrade it with the host's package manager (on the Olivares appliance: systemctl start olivares-appliance-update.service)"

// packageSnapshotSQLite is the indirection the snapshot-deadline test replaces.
var packageSnapshotSQLite = dr.SnapshotSQLite

func installedPackageSnapshotRequest() string {
	// os.Executable resolves a symlink used to invoke the installed binary.
	// A development binary on the same host must not consume package requests.
	exe, err := os.Executable()
	if err != nil || exe != installedPackageBinary {
		return ""
	}
	return "/var/lib/olivares-package/upgrade-snapshot"
}

// snapshotPackageUpgrade runs under boot's publication admission, before the
// store opens. Only the installed package's serve path requests it. No engine
// boot is used to capture or inspect the snapshot: that could migrate the source.
func snapshotPackageUpgrade(ctx context.Context, request, dataDir string, cfg store.Config) (string, error) {
	return snapshotPackageUpgradeWithSync(ctx, request, dataDir, cfg, syncDir)
}

func snapshotPackageUpgradeWithSync(ctx context.Context, request, dataDir string, cfg store.Config, syncDirectory func(string) error) (string, error) {
	info, err := os.Lstat(request)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil // Fresh package installation: there is no upgrade request.
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 128 {
		return "", fmt.Errorf("invalid snapshot request %s", request)
	}
	token, err := os.ReadFile(request)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(token), "upgrade-snapshot.") {
		return "", fmt.Errorf("invalid snapshot request %s", request)
	}
	// No time limit: the snapshot of a large store on a slow disk takes as long as it takes,
	// and a start that cut it would never migrate. The unit (Type=simple) has no start timeout,
	// and a stop ends the unit's whole control group, pg_dump included. The log names the file
	// the operator can watch grow meanwhile.
	var identity, name string
	fresh := false // a store the package installed but never started: nothing to preserve
	switch cfg.Engine {
	case store.EngineSQLite:
		target, err := opgate.ResolveSQLiteTarget(cfg.DSN)
		if err != nil {
			return "", err
		}
		if target.Memory() {
			return "", fmt.Errorf("package upgrade requires a file-backed SQLite store")
		}
		if _, err := os.Stat(target.CanonicalPath()); errors.Is(err, os.ErrNotExist) {
			fresh = true
		} else if err != nil {
			return "", fmt.Errorf("inspect snapshot source: %w", err)
		}
		identity, name = target.CanonicalPath(), "olivares.db"
		cfg.DSN = target.DriverDSN()
	case store.EnginePostgres:
		// Identity excludes credentials: rotating a password does not replace the
		// snapshot, and no password-derived hash is exposed in a filename.
		auth, err := coreengine.ProbeConnAuthority(ctx, cfg)
		if err != nil {
			return "", err
		}
		if !auth.Posture.Reachable || auth.SystemIdentifier == "" {
			return "", fmt.Errorf("cannot establish PostgreSQL snapshot source identity; run olivares db check")
		}
		identity, name = auth.SystemIdentifier+"/"+auth.Database, "dump.pgcustom"
		if cfg.AdminDSN == "" && cfg.AllowPrivilegedRole && auth.Posture.RLSUnsafe() {
			// The operator already permitted this proven cross-tenant connection.
			// Never fall back to an ordinary FORCE-RLS application/owner role.
			cfg.AdminDSN = cfg.DSN
		}
	default:
		return "", fmt.Errorf("unsupported snapshot engine %q", cfg.Engine)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(string(token)+"\x00"+string(cfg.Engine)+"\x00"+identity)))
	if upgradeSnapshotConsumed(dataDir, key) {
		// This request's migration already ran: a copy taken now would be of the migrated store.
		return "", nil
	}
	if fresh {
		// This start creates the store, so the request's migration runs on no data.
		slog.Info("package upgrade: no existing SQLite store to snapshot")
		return key, nil
	}
	parent := filepath.Join(dataDir, "backups", "pre-upgrade")
	dest := filepath.Join(parent, key)
	complete := func() bool {
		st, err := os.Lstat(filepath.Join(dest, name))
		return err == nil && st.Mode().IsRegular() && st.Size() > 0
	}
	// MkdirAll may introduce both backups and pre-upgrade. Persist their
	// directory entries as well as the snapshot's rename. Repeat all three
	// syncs on retries: existence does not prove a prior sync succeeded.
	syncParents := func() error {
		for _, dir := range []string{parent, filepath.Dir(parent), filepath.Clean(dataDir)} {
			if err := syncDirectory(dir); err != nil {
				return fmt.Errorf("persist upgrade snapshot directory %s: %w", dir, err)
			}
		}
		return nil
	}
	if complete() {
		return key, syncParents()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".snapshot-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	snapshot := filepath.Join(stage, name)
	started := time.Now()
	slog.Info("package upgrade: taking the pre-migration snapshot; migrations wait for it, with no time limit", "path", snapshot)
	if cfg.Engine == store.EngineSQLite {
		err = packageSnapshotSQLite(ctx, cfg.DSN, snapshot)
	} else {
		if cfg.AdminDSN == "" {
			// Configuration only an operator can add: starting again fails the same way.
			return "", exitcode.New(exitcode.Refused, fmt.Errorf("PostgreSQL upgrade snapshot requires --admin-dsn and pg_dump; see docs/UPGRADE-AND-ROLLBACK.md"))
		}
		// The DR preflight verifies role authority AND that all pools reach the
		// same live database; a dump of a different admin database is no backup.
		if err = preflightPostgresDRWithPrivilege(ctx, drFlags{engineKind: "postgres", dsn: cfg.DSN,
			ownerDSN: cfg.OwnerDSN, adminDSN: cfg.AdminDSN}, "backup", cfg.AllowPrivilegedRole); err == nil {
			err = runPgDump(ctx, "pg_dump", cfg.AdminDSN, snapshot)
		}
	}
	if err != nil {
		return "", err
	}
	st, err := os.Stat(snapshot)
	if err != nil {
		return "", fmt.Errorf("inspect upgrade snapshot: %w", err)
	}
	if st.Size() == 0 {
		return "", fmt.Errorf("snapshot tool did not produce a nonempty file")
	}
	if err := os.Chmod(snapshot, 0o600); err != nil {
		return "", err
	}
	if err := syncSnapshotPath(snapshot); err != nil {
		return "", err
	}
	if err := syncDirectory(stage); err != nil {
		return "", err
	}
	// A nonempty destination directory cannot be replaced. Competing boots
	// may capture concurrently, but only the first complete snapshot wins;
	// neither may migrate until that snapshot has been published.
	if err := os.Rename(stage, dest); err != nil && !complete() {
		return "", fmt.Errorf("publish upgrade snapshot: %w", err)
	}
	if err := syncParents(); err != nil {
		return "", err
	}
	slog.Info("package upgrade: pre-migration snapshot ready", "path", filepath.Join(dest, name),
		"bytes", st.Size(), "took", time.Since(started).Round(time.Second))
	return key, nil
}

// upgradeSnapshotConsumedPath records the digest of the last request whose migration ran.
// The engine cannot mark the request itself: the package owns it (root, read-only to the
// engine), and the snapshot is no record either, since operators delete old ones.
func upgradeSnapshotConsumedPath(dataDir string) string {
	return filepath.Join(dataDir, "upgrade-snapshot.consumed")
}

// upgradeSnapshotConsumed reads that record. A missing or unreadable record means
// "not consumed": the start then takes the snapshot, as it did before the record existed.
func upgradeSnapshotConsumed(dataDir, key string) bool {
	done, err := os.ReadFile(upgradeSnapshotConsumedPath(dataDir))
	return err == nil && strings.TrimSpace(string(done)) == key
}

// recordUpgradeSnapshotConsumed takes the digest snapshotPackageUpgrade returned and runs once
// the store has opened, so the request's migration ran. A record lost to a crash only repeats
// the snapshot on the next start.
func recordUpgradeSnapshotConsumed(dataDir, key string) error {
	return os.WriteFile(upgradeSnapshotConsumedPath(dataDir), []byte(key+"\n"), 0o600)
}

func syncSnapshotPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
