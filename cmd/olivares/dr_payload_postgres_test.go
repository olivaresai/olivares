// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

func TestDRBackupExcludesExactCoordinationInventory(t *testing.T) {
	previous := version
	version = "26.1001"
	t.Cleanup(func() { version = previous })
	src := newPGSplitFixture(t, "drw_payload", true)
	dst := newPGSplitFixture(t, "drw_payload_dst", true)
	protectDRPayloadFixtureDSNs(t, src, dst)
	dir := t.TempDir()
	src.seed(t, dir)
	eng, err := drBoot(t.Context(), drFlags{dataDir: dir, engineKind: "postgres", dsn: src.appDSN, ownerDSN: src.ownerDSN, adminDSN: src.adminDSN})
	if err != nil {
		t.Fatal(err)
	}
	err = recordRestoreDeclaration(t.Context(), eng.store, restoreDeclaration{operator: "fixture", reason: "historical restore"}, restoreEvidence{engine: "postgres", bundle: "previous.drbundle"})
	if closeErr := eng.Close(); err != nil || closeErr != nil {
		t.Fatalf("declaration: %v, close: %v", err, closeErr)
	}
	src.superExecInDB(t, "CREATE TABLE public.olv_dr_restore_control_v1_customer (id int); INSERT INTO public.olv_dr_restore_control_v1_customer VALUES (7); GRANT SELECT ON public.olv_dr_restore_control_v1_customer TO "+src.adminRole)
	keyFile := drPayloadTestKEK(t)
	clean := filepath.Join(t.TempDir(), "clean.drbundle")
	args := append([]string{"backup", "--data-dir", dir, "--out", clean, "--kek-key-file", keyFile, "--pg-dump", src.bin("pg_dump")}, src.drArgs()...)
	if out, err := runDR(args...); err != nil {
		t.Fatalf("real split-role backup: %v\n%s", err, out)
	}
	if _, err := coreengine.InstallPendingRestoreControl(t.Context(), store.Config{Engine: store.EnginePostgres, DSN: src.appDSN, OwnerDSN: src.ownerDSN, AdminDSN: src.adminDSN},
		coreengine.PendingRestoreSpec{OpID: "cafebabecafebabecafebabecafebabe", PlanSHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	excluded := filepath.Join(t.TempDir(), "excluded.pgcustom")
	if err := runPgDump(t.Context(), src.bin("pg_dump"), src.adminDSN, excluded); err != nil {
		t.Fatal(err)
	}
	if _, err := classifyDRPayload(t.Context(), "postgres", dr.MethodPgDump, excluded, src.bin("pg_restore")); err != nil {
		t.Fatal(err)
	}
	if err := runPgRestore(t.Context(), dst.bin("pg_restore"), dst.ownerDSN, excluded); err != nil {
		t.Fatal(err)
	}
	if got := dst.superScalar(t, "SELECT count(*) FROM pg_catalog.pg_class WHERE relname = 'olv_dr_restore_control_v1'"); got != 0 {
		t.Fatalf("restored operational control: %d", got)
	}
	if got := dst.superScalar(t, "SELECT count(*) FROM public.olv_dr_restore_control_v1_customer WHERE id=7"); got != 1 {
		t.Fatalf("lost similarly named customer data: %d", got)
	}
	if got := dst.superScalar(t, "SELECT count(*) FROM public.audit_events WHERE action='dr.restore.cli'"); got != 1 {
		t.Fatalf("lost declaration history: %d", got)
	}
	if got := dst.superScalar(t, "SELECT count(*) FROM public.audit_events"); got != src.superScalar(t, "SELECT count(*) FROM public.audit_events") {
		t.Fatal("lost ledger events")
	}

	// An externally produced archive carrying the real control is refused
	// both by the supplied-snapshot producer and authenticated CLI consumer.
	contaminated := filepath.Join(t.TempDir(), "contaminated.pgcustom")
	cmd := exec.CommandContext(t.Context(), src.bin("pg_dump"), "--format=custom", "--no-owner", "--no-privileges", "--file", contaminated, "--dbname", src.adminDSN) // #nosec G204 -- password-free fixture DSN; credentials use PGPASSFILE
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture archive: %v\n%s", err, out)
	}
	before, _, err := dr.FileSHA256(contaminated)
	if err != nil {
		t.Fatal(err)
	}
	refused := filepath.Join(t.TempDir(), "refused.drbundle")
	args = append([]string{"backup", "--data-dir", dir, "--out", refused, "--kek-key-file", keyFile, "--pg-dump", src.bin("pg_dump"), "--snapshot-file", contaminated}, src.drArgs()...)
	if _, err := runDR(args...); err == nil || !strings.Contains(err.Error(), "operational restore coordination") {
		t.Fatalf("supplied contaminated archive accepted: %v", err)
	}
	if _, err := os.Stat(refused); !os.IsNotExist(err) {
		t.Fatalf("refused backup published a bundle: %v", err)
	}
	work := t.TempDir()
	m, kek, err := openAndCheckBundle(clean, work)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := dr.OpenCipher(make([]byte, 32), kek)
	if err != nil {
		t.Fatal(err)
	}
	m.Store.SHA256, m.Store.SizeBytes, err = dr.FileSHA256(contaminated)
	if err != nil {
		t.Fatal(err)
	}
	sealed := map[string][]byte{}
	for _, ref := range m.Keys {
		sealed[ref.File], err = os.ReadFile(filepath.Join(work, ref.File))
		if err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(t.TempDir(), "contaminated.drbundle")
	f, err := os.Create(bundle)
	if err != nil {
		t.Fatal(err)
	}
	err = dr.WriteAuthenticatedBundle(f, dr.BundleInput{Manifest: m, KEK: kek, SnapshotPath: contaminated, SealedKeys: sealed}, cipher)
	if closeErr := f.Close(); err != nil || closeErr != nil {
		t.Fatalf("bundle: %v, close: %v", err, closeErr)
	}
	restoreDir := t.TempDir()
	sentinel := filepath.Join(restoreDir, "audit-signing.key")
	if err := os.WriteFile(sentinel, []byte("incumbent fixture key"), 0o600); err != nil {
		t.Fatal(err)
	}
	events := dst.superScalar(t, "SELECT count(*) FROM public.audit_events")
	args = append([]string{"restore", "--data-dir", restoreDir, "--in", bundle, "--kek-key-file", keyFile, "--force", "--operator", "fixture", "--reason", "reject contamination", "--pg-restore", dst.bin("pg_restore")}, dst.drArgs()...)
	if _, err := runDR(args...); err == nil || !strings.Contains(err.Error(), "operational restore coordination") {
		t.Fatalf("authenticated contaminated archive accepted: %v", err)
	}
	if got := dst.superScalar(t, "SELECT count(*) FROM public.audit_events"); got != events {
		t.Fatal("refusal changed destination ledger")
	}
	if got := dst.superScalar(t, "SELECT count(*) FROM pg_catalog.pg_class WHERE relname='olv_dr_restore_control_v1'"); got != 0 {
		t.Fatal("refusal enrolled the destination")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "incumbent fixture key" {
		t.Fatalf("refusal changed custody: %v", err)
	}
	after, _, err := dr.FileSHA256(contaminated)
	if err != nil || before != after {
		t.Fatalf("classification rewrote supplied archive: %v", err)
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		out, err := exec.CommandContext(t.Context(), src.bin(tool), "--version").Output() // #nosec G204 -- native client version only
		if err != nil {
			t.Fatal(err)
		}
		t.Log(strings.TrimSpace(string(out)))
	}
	t.Logf("server_version_num=%d; split app/owner/admin backup, exact exclusion, declaration/customer preservation and no-effect refusal proved", src.superScalar(t, "SELECT current_setting('server_version_num')::int"))
}

// Both pgx and native libpq clients read the private password file. All DSNs
// used by this root, including the product's dump/restore callers, stay safe
// to pass as native arguments. This never reads a host credential file.
func protectDRPayloadFixtureDSNs(t *testing.T, fixtures ...*pgSplitFixture) {
	t.Helper()
	escape := strings.NewReplacer(`\`, `\\`, ":", `\:`)
	var entries strings.Builder
	for _, fixture := range fixtures {
		for _, dsn := range []*string{&fixture.appDSN, &fixture.ownerDSN, &fixture.adminDSN} {
			if *dsn == "" {
				continue
			}
			u, err := url.Parse(*dsn)
			if err != nil || u.User == nil {
				t.Fatal("fixture connection is not a valid credential URL")
			}
			password, present := u.User.Password()
			query := u.Query()
			if query.Has("password") {
				password, present = query.Get("password"), true
				query.Del("password")
			}
			if !present || strings.ContainsAny(fixture.db+u.User.Username()+password, "\r\n") {
				t.Fatal("fixture credentials cannot be stored in a native password file")
			}
			entries.WriteString("*:*:" + escape.Replace(fixture.db) + ":" +
				escape.Replace(u.User.Username()) + ":" + escape.Replace(password) + "\n")
			u.User = url.User(u.User.Username())
			u.RawQuery = query.Encode()
			*dsn = u.String()
		}
	}
	path := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(path, []byte(entries.String()), 0o600); err != nil {
		t.Fatal("write private fixture password file")
	}
	t.Setenv("PGPASSFILE", path)
}
