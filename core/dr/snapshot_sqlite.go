// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver "sqlite" (same as the store)
)

// SnapshotSQLite makes a consistent, single-file copy of the SQLite database at
// srcPath into dstPath using `VACUUM INTO`. Unlike copying the database file,
// VACUUM INTO:
//   - is transactionally consistent (it runs in its own read transaction), so it
//     never captures a torn write — audit_events and audit_heads are copied at the
//     SAME instant, preserving the chain's tail-truncation detection;
//   - works while the engine is LIVE (WAL mode allows a concurrent reader), so a
//     scheduled backup needs no downtime;
//   - emits a fully-checkpointed single file with NO -wal/-shm sidecar — exactly
//     the self-contained snapshot a DR bundle needs.
//
// It opens its OWN read connection (it does not touch the engine's pinned
// connection). dstPath must not already exist (VACUUM INTO refuses to overwrite).
func SnapshotSQLite(ctx context.Context, srcPath, dstPath string) error {
	if srcPath == "" || srcPath == ":memory:" {
		return fmt.Errorf("dr: SnapshotSQLite needs a file-backed source, got %q", srcPath)
	}
	if err := os.Remove(dstPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("dr: clear snapshot target: %w", err)
	}
	db, err := sql.Open("sqlite", srcPath)
	if err != nil {
		return fmt.Errorf("dr: open source for snapshot: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 30000"); err != nil {
		return fmt.Errorf("dr: snapshot busy_timeout: %w", err)
	}
	// The destination is a server-side path the operator controls; quote it as a
	// SQL string literal with single-quotes doubled (VACUUM INTO takes a literal,
	// not a bound parameter).
	q := "VACUUM INTO '" + strings.ReplaceAll(dstPath, "'", "''") + "'" // #nosec G202 -- VACUUM INTO takes a literal, not a bound param; dstPath is an operator-controlled server path with single-quotes doubled
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("dr: VACUUM INTO: %w", err)
	}
	return nil
}

// FileSHA256 returns the lowercase-hex SHA-256 of the file at path, streaming it
// so a large snapshot is never held in memory.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// CopyFile copies src to dst (0600), creating dst. It is used to stage a snapshot
// copy for an out-of-band manifest boot without mutating the bundled bytes.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// PubFingerprintFromSigningKey reads a base64 Ed25519 PRIVATE signing key file
// (the data-dir format written by core/secure.LoadOrCreateSigningKey), derives
// its PUBLIC key and returns the lowercase-hex SHA-256 of the 32 public bytes.
// This is the non-secret fingerprint a manifest records and a restore checks the
// restored key against — it never exposes the private material.
func PubFingerprintFromSigningKey(b []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("dr: not a base64 Ed25519 private key")
	}
	pub := ed25519.PrivateKey(raw).Public().(ed25519.PublicKey)
	return PubFingerprint(pub), nil
}

// PubFingerprint returns the lowercase-hex SHA-256 of an Ed25519 public key.
func PubFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}
