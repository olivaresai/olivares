// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// estate is a file-backed SQLite store wired with a per-event signer — the
// realistic shape a DR backup operates on (the engine always signs per event).
type estate struct {
	st     store.Store
	signer *audit.Signer
	priv   ed25519.PrivateKey
	dir    string
	dbPath string
	keyB64 []byte // the data-dir audit-signing.key file contents
}

func genKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return pub, priv
}

// keyFileBytes is the data-dir signing-key file format (base64 + newline), the
// exact format core/secure writes and core/dr.PubFingerprintFromSigningKey reads.
func keyFileBytes(priv ed25519.PrivateKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(priv) + "\n")
}

// newEstate opens a file-backed SQLite store in a fresh dir with a signer built
// from a known key, and persists that key in the data-dir format so the DR code
// can fingerprint it the way it would in production.
func newEstate(t *testing.T) *estate {
	t.Helper()
	_, priv := genKey(t)
	return newEstateWithKey(t, priv)
}

func newEstateWithKey(t *testing.T, priv ed25519.PrivateKey) *estate {
	t.Helper()
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "olivares.db")
	st, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dbPath, Debug: true, SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(context.Background())
		return e
	}); err != nil {
		t.Fatalf("system tenant: %v", err)
	}
	return &estate{st: st, signer: signer, priv: priv, dir: dir, dbPath: dbPath, keyB64: keyFileBytes(priv)}
}

func (e *estate) pub() ed25519.PublicKey { return e.signer.PublicKey() }

func (e *estate) cpVerifier(t *testing.T) *audit.CheckpointVerifier {
	t.Helper()
	v, err := e.signer.CheckpointVerifier(context.Background())
	if err != nil {
		t.Fatalf("cpverifier: %v", err)
	}
	return v
}

var tenantSeq int

func (e *estate) newTenant(t *testing.T) model.TenantID {
	t.Helper()
	tenantSeq++
	slug := "t" + strconv.Itoa(tenantSeq)
	var id model.TenantID
	if err := e.st.System(context.Background(), func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		id = o.TenantID
		return err
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	return id
}

func (e *estate) appendN(t *testing.T, tenant model.TenantID, n int) {
	t.Helper()
	if err := e.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			if _, err := sc.Audit().Append(context.Background(), model.AuditDraft{
				Actor: "user:x", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func (e *estate) checkpointAll(t *testing.T) {
	t.Helper()
	if err := e.signer.CheckpointAll(context.Background(), e.st); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}

// snapshotInto VACUUMs the live db into dst and returns dst.
func (e *estate) snapshotInto(t *testing.T, dst string) string {
	t.Helper()
	if err := dr.SnapshotSQLite(context.Background(), e.dbPath, dst); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return dst
}

// openRestored opens a restored data dir (a db file + an audit signing key file)
// as a store wired with the signer built from that restored key, mirroring boot.
func openRestored(t *testing.T, dbPath string, priv ed25519.PrivateKey) *estate {
	t.Helper()
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	st, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dbPath, Debug: true, SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &estate{st: st, signer: signer, priv: priv, dbPath: dbPath, keyB64: keyFileBytes(priv)}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
