// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// verifyBundleScratch proves an uploaded DR bundle restores to a ledger-continuity-
// safe state in a THROWAWAY directory, WITHOUT touching the live store. It is the gate
// the console restore blocks on before it overwrites production: the snapshot is
// copied into a scratch SQLite db, a signer is built from the bundle's own restored
// audit key, and dr.RestoreVerify re-checks every tenant chain + per-event signatures
// + checkpoints + tip continuity + key fingerprint. A non-OK report means the bundle
// is NOT safe to promote — the caller must refuse and leave the live data dir intact.
//
// tmpDir is a bundle already ExtractBundle'd (snapshot at tmpDir/<m.Store.File>, each
// sealed key at tmpDir/<KeyRef.File>); cipher is the operator's opened KEK.
func verifyBundleScratch(ctx context.Context, tmpDir string, m *dr.Manifest, cipher *dr.KeyCipher) (*dr.RestoreReport, error) {
	if m.EngineKind != "sqlite" {
		// The console DR surface is SQLite-only (Postgres uses the CLI + pg_restore);
		// a scratch chain verify for Postgres needs a scratch Postgres (see the runbook).
		return nil, fmt.Errorf("scratch verify supports the sqlite engine only, bundle is %q", m.EngineKind)
	}
	priv, err := decryptAuditKey(tmpDir, m, cipher)
	if err != nil {
		return nil, err
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		return nil, fmt.Errorf("build signer from restored key: %w", err)
	}

	scratch := filepath.Join(tmpDir, "scratch-verify.db")
	if err := dr.CopyFile(filepath.Join(tmpDir, filepath.FromSlash(m.Store.File)), scratch); err != nil {
		return nil, fmt.Errorf("stage scratch snapshot: %w", err)
	}
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: scratch, SignEvent: signer.SignEvent}, nil)
	if err != nil {
		return nil, fmt.Errorf("open scratch store: %w", err)
	}
	defer func() { _ = st.Close() }()

	cpv, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		return nil, err
	}
	return dr.RestoreVerify(ctx, st, m, signer.PublicKey(), cpv)
}

// decryptAuditKey decrypts the bundle's audit-role signing key and parses the
// Ed25519 private key (the data-dir format is base64 of the 64-byte private key).
func decryptAuditKey(tmpDir string, m *dr.Manifest, cipher *dr.KeyCipher) (ed25519.PrivateKey, error) {
	var ref *dr.KeyRef
	for i := range m.Keys {
		if m.Keys[i].Role == dr.RoleAudit {
			ref = &m.Keys[i]
			break
		}
	}
	if ref == nil {
		return nil, fmt.Errorf("bundle manifest records no audit-role signing key")
	}
	sealed, err := os.ReadFile(filepath.Join(tmpDir, filepath.FromSlash(ref.File)))
	if err != nil {
		return nil, fmt.Errorf("read sealed audit key: %w", err)
	}
	plain, err := cipher.Open(sealed)
	if err != nil {
		return nil, fmt.Errorf("decrypt audit key (wrong passphrase?): %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(plain)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("restored audit key is not a base64 Ed25519 private key")
	}
	return ed25519.PrivateKey(raw), nil
}
