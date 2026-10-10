// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/dr/opgate"
	coreengine "github.com/olivaresai/olivares/core/engine"
)

func TestDRBackupRefusesSQLiteCoordinationRelation(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	db, err := sql.Open("sqlite", filepath.Join(src, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE olv_dr_restore_control_v1 (state TEXT)")
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatalf("fixture: %v, close: %v", err, closeErr)
	}
	out := filepath.Join(t.TempDir(), "refused.drbundle")
	_, err = runDR("backup", "--data-dir", src, "--out", out, "--kek-key-file", drPayloadTestKEK(t))
	if err == nil || !strings.Contains(err.Error(), "operational restore coordination") {
		t.Fatalf("backup must classify and refuse coordination in the payload, got %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("refused backup published a bundle: %v", err)
	}
}

func TestDRRestoreClassifiesAuthenticatedAndLegacyPayloadBeforeEffects(t *testing.T) {
	previous := version
	version = "26.1001"
	t.Cleanup(func() { version = previous })
	src, dst := t.TempDir(), t.TempDir()
	seedDataDir(t, src)
	seedDataDir(t, dst)
	keyFile := drPayloadTestKEK(t)
	clean := filepath.Join(t.TempDir(), "clean.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--out", clean, "--kek-key-file", keyFile); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
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
	path := filepath.Join(work, m.Store.File)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE olv_dr_restore_control_v1 (state TEXT)")
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatalf("fixture: %v, close: %v", err, closeErr)
	}
	m.Store.SHA256, m.Store.SizeBytes, err = dr.FileSHA256(path)
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
	before := map[string]string{}
	for _, name := range []string{"olivares.db", "audit-signing.key", "catalog-signing.key", "policy-signing.key"} {
		before[name], _, err = dr.FileSHA256(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "authenticated", true: "legacy"}[legacy], func(t *testing.T) {
			bundle := filepath.Join(t.TempDir(), "contaminated.drbundle")
			f, err := os.Create(bundle)
			if err != nil {
				t.Fatal(err)
			}
			if !legacy {
				err = dr.WriteAuthenticatedBundle(f, dr.BundleInput{Manifest: m, KEK: kek, SnapshotPath: path, SealedKeys: sealed}, cipher)
			} else {
				m.Authentication = dr.ManifestAuthentication{}
				m.Files = nil
				gz := gzip.NewWriter(f)
				tw := tar.NewWriter(gz)
				mb, e := json.Marshal(m)
				if e != nil {
					t.Fatal(e)
				}
				kb, e := json.Marshal(kek)
				if e != nil {
					t.Fatal(e)
				}
				payload, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				entries := map[string][]byte{"manifest.json": mb, "keys/kek.json": kb, m.Store.File: payload}
				for name, body := range sealed {
					entries[name] = body
				}
				for name, body := range entries {
					if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); e != nil {
						t.Fatal(e)
					}
					if _, e := tw.Write(body); e != nil {
						t.Fatal(e)
					}
				}
				if e := tw.Close(); e != nil {
					t.Fatal(e)
				}
				err = gz.Close()
			}
			if closeErr := f.Close(); err != nil || closeErr != nil {
				t.Fatalf("bundle: %v, close: %v", err, closeErr)
			}
			args := []string{"restore", "--in", bundle, "--data-dir", dst, "--force", "--operator", "fixture", "--reason", "test contaminated payload", "--kek-key-file", keyFile}
			if legacy {
				args = append(args, "--allow-legacy-unsigned")
			}
			if _, err := runDR(args...); err == nil || !strings.Contains(err.Error(), "operational restore coordination") {
				t.Fatalf("restore must classify before effects: %v", err)
			}
			for name, want := range before {
				got, _, err := dr.FileSHA256(filepath.Join(dst, name))
				if err != nil || got != want {
					t.Fatalf("refusal changed %s: %v", name, err)
				}
			}
			if matches, err := filepath.Glob(filepath.Join(dst, "*.pre-restore-*")); err != nil || len(matches) != 0 {
				t.Fatalf("refusal preserved/replaced live files: %v %v", matches, err)
			}
		})
	}
}

func TestDRBackupClassifiesSuppliedSnapshotBeforeDialing(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	snapshot := filepath.Join(t.TempDir(), "plain.sql")
	if err := os.WriteFile(snapshot, []byte("CREATE TABLE customer_data (id int);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "refused.drbundle")
	_, err := runDR("backup", "--engine", "postgres", "--dsn", "host=127.0.0.1 port=1 dbname=fixture connect_timeout=1", "--data-dir", src, "--snapshot-file", snapshot, "--out", out, "--kek-key-file", drPayloadTestKEK(t))
	if err == nil || !strings.Contains(err.Error(), "unsupported PostgreSQL archive") {
		t.Fatalf("supplied payload must be classified before a database dial, got %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("refused backup published a bundle: %v", err)
	}
}

func TestDRSQLitePayloadKeepsDeclarationAndExcludesSidecars(t *testing.T) {
	previous := version
	version = "26.1001"
	t.Cleanup(func() { version = previous })
	src := t.TempDir()
	seedDataDir(t, src)
	eng, err := drBoot(t.Context(), drFlags{dataDir: src, engineKind: "sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	err = recordRestoreDeclaration(t.Context(), eng.store, restoreDeclaration{operator: "fixture", reason: "historical restore"}, restoreEvidence{engine: "sqlite", bundle: "previous.drbundle"})
	if closeErr := eng.Close(); err != nil || closeErr != nil {
		t.Fatalf("declaration: %v, close: %v", err, closeErr)
	}
	anchor := installDataDirControl(t, src, opgate.StatePending)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	keyFile := drPayloadTestKEK(t)
	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--kek-key-file", keyFile); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	work := t.TempDir()
	m, _, err := openAndCheckBundle(bundle, work)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range m.Files {
		if filepath.Base(entry.Path) == filepath.Base(anchor.RecordPath()) || filepath.Base(entry.Path) == filepath.Base(anchor.LockPath()) {
			t.Fatalf("operational sidecar entered the bundle: %s", entry.Path)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(work, m.Store.File))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM audit_events WHERE action='dr.restore.cli'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("declaration history lost: count=%d, err=%v", count, err)
	}
	if out, err := runDR("verify", "--in", bundle, "--kek-key-file", keyFile); err != nil {
		t.Fatalf("verify retained declaration chain: %v\n%s", err, out)
	}
}

func drPayloadTestKEK(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture-kek")
	if err := os.WriteFile(path, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const drTOCHeader = ";     Format: CUSTOM\n;     Dumped by pg_dump version: 16.15\n"

func checkDRPayloadVersionHeader(t *testing.T, version string) {
	t.Helper()
	for _, suffix := range []string{"", " (Debian " + version + "-1.pgdg12+2)"} {
		header := strings.Replace(drTOCHeader, "16.15", version+suffix, 1)
		if err := classifyPGTOC(strings.NewReader(header+"1; 0 0 TABLE public customer_data owner\n"), nil); err != nil {
			t.Fatalf("published PostgreSQL version support must survive classification: %v", err)
		}
	}
}

func TestDRPayloadClassifierAcceptsPostgres15Header(t *testing.T) {
	checkDRPayloadVersionHeader(t, "15.19")
}

func TestDRPayloadClassifierAcceptsPostgres16Header(t *testing.T) {
	checkDRPayloadVersionHeader(t, "16.15")
}

func TestDRPayloadClassifierAcceptsPostgres17Header(t *testing.T) {
	checkDRPayloadVersionHeader(t, "17.11")
}

func TestDRPayloadClassifierAcceptsPostgres18Header(t *testing.T) {
	checkDRPayloadVersionHeader(t, "18.6")
}

func TestDRPayloadClassifierRefusesUnsupportedOrAmbiguousVersionHeader(t *testing.T) {
	for _, version := range []string{"14.20", "19.0", "16", "16.x", "016.15", "16.15 17.11", "16.15 (unterminated", "16.15\n;     Dumped by pg_dump version: 17.11"} {
		t.Run(version, func(t *testing.T) {
			header := strings.Replace(drTOCHeader, "16.15", version, 1)
			err := classifyPGTOC(strings.NewReader(header+"1; 0 0 TABLE public customer_data owner\n"), nil)
			if err == nil || !strings.Contains(err.Error(), "PostgreSQL 15, 16, 17, or 18") {
				t.Fatalf("unsupported or ambiguous header must name supported versions: %v", err)
			}
		})
	}
}

func TestDRPayloadClassifierRefusesCoordinationObjects(t *testing.T) {
	// Data and constraints must refuse even without a TABLE entry.
	for _, object := range coreengine.DRCoordinationObjects() {
		t.Run(object.String(), func(t *testing.T) {
			tag := object.Name
			if object.Parent != "" {
				tag = object.Parent + " " + tag
			}
			entry := "1; 0 0 " + string(object.Kind) + " " + object.Schema + " " + tag + " owner\n"
			if err := classifyPGTOC(strings.NewReader(drTOCHeader+entry), nil); err == nil || !strings.Contains(err.Error(), "operational restore coordination") {
				t.Fatalf("isolated coordination entry must refuse: %v", err)
			}
		})
	}
	for name, toc := range map[string]string{
		"similar customer table": drTOCHeader + "1; 0 0 TABLE public olv_dr_restore_control_v1_customer owner\n",
		"same name other schema": drTOCHeader + "1; 0 0 TABLE customer olv_dr_restore_control_v1 owner\n",
		"declaration history":    drTOCHeader + "1; 0 0 TABLE DATA public audit_events owner\n",
		"routine signature":      drTOCHeader + "1; 0 0 FUNCTION public routine(text, integer) owner\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := classifyPGTOC(strings.NewReader(toc), nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDRPayloadClassifierRefusesDataAndConstraintWithoutTable(t *testing.T) {
	// These fixed identities also detect an incomplete inventory.
	for _, entry := range []string{
		"1; 0 0 TABLE DATA public olv_dr_restore_control_v1 owner\n",
		"1; 0 0 CONSTRAINT public olv_dr_restore_control_v1 olv_dr_restore_control_v1_pkey owner\n",
	} {
		if err := classifyPGTOC(strings.NewReader(drTOCHeader+entry), nil); err == nil || !strings.Contains(err.Error(), "operational restore coordination") {
			t.Fatalf("coordination survived omission of its table entry: %v", err)
		}
	}
}

func TestDRPayloadClassifierRefusesAmbiguousTOC(t *testing.T) {
	for name, toc := range map[string]string{
		"missing format":            "1; 0 0 TABLE public safe owner\n",
		"plain format":              strings.ReplaceAll(drTOCHeader, "CUSTOM", "PLAIN") + "1; 0 0 TABLE public safe owner\n",
		"unsupported client":        strings.ReplaceAll(drTOCHeader, "16.15", "14.20") + "1; 0 0 TABLE public safe owner\n",
		"duplicate ids":             drTOCHeader + "1; 0 0 TABLE public safe owner\n1; 0 0 TABLE DATA public safe owner\n",
		"quoted identifier":         drTOCHeader + "1; 0 0 TABLE \"public\" safe owner\n",
		"whitespace identifier":     drTOCHeader + "1; 0 0 TABLE public unsafe name owner\n",
		"trailing space identifier": drTOCHeader + "1; 0 0 TABLE public safe  owner\n",
		"leading space identifier":  drTOCHeader + "1; 0 0 TABLE public  safe owner\n",
		"tab identifier":            drTOCHeader + "1; 0 0 TABLE public safe\t owner\n",
		"unknown descriptor":        drTOCHeader + "1; 0 0 FUTURE TYPE public safe owner\n",
		"broken entry":              drTOCHeader + "bad; 0 0 TABLE public safe owner\n",
		"overlong line":             drTOCHeader + strings.Repeat("x", 65537) + "\n",
		"too much metadata":         drTOCHeader + strings.Repeat("; metadata\n", 800000),
	} {
		t.Run(name, func(t *testing.T) {
			if err := classifyPGTOC(strings.NewReader(toc), nil); err == nil {
				t.Fatal("ambiguous metadata was accepted")
			}
		})
	}
}

func TestDRPayloadClassifierBindsStagedBytes(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	path := filepath.Join(src, "olivares.db")
	proof, err := classifyDRPayload(t.Context(), "sqlite", dr.MethodVacuumInto, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.unchanged(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 1
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	// Even preserving inode, size and timestamp does not preserve the proof.
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := proof.unchanged(); err == nil {
		t.Fatal("changed payload bytes retained their classification proof")
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := proof.unchanged(); err == nil {
		t.Fatal("replacement payload retained its classification proof")
	}
}

func TestDRPayloadClassifierRejectsChangesDuringListing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dump.pgcustom")
	stamp := filepath.Join(dir, "stamp")
	for _, file := range []string{path, stamp} {
		if err := os.WriteFile(file, []byte("PGDMP"+strings.Repeat("a", 32)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stamp, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "pg_restore")
	body := "#!/bin/sh\nprintf 'PGDMPbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' > \"$DRW_LIST_PAYLOAD\"\ntouch -r \"$DRW_LIST_STAMP\" \"$DRW_LIST_PAYLOAD\"\nprintf ';     Format: CUSTOM\\n;     Dumped by pg_dump version: 16.15\\n1; 0 0 TABLE public customer_data owner\\n'\n"
	if err := os.WriteFile(bin, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRW_LIST_PAYLOAD", path)
	t.Setenv("DRW_LIST_STAMP", stamp)
	if _, err := classifyDRPayload(t.Context(), "postgres", dr.MethodPgDump, path, bin); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("listing must not attest bytes changed during classification: %v", err)
	}
}

func TestDRInPlaceRestoreRefusesChangedClassifiedPayload(t *testing.T) {
	previous := version
	version = "26.1001"
	t.Cleanup(func() { version = previous })
	src, dst := t.TempDir(), t.TempDir()
	seedDataDir(t, src)
	seedDataDir(t, dst)
	bundle := filepath.Join(t.TempDir(), "clean.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--kek-key-file", drPayloadTestKEK(t)); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	work := t.TempDir()
	m, kek, err := openAndCheckBundle(bundle, work)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := dr.OpenCipher(make([]byte, 32), kek)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, m.Store.File)
	proof, err := classifyDRPayload(t.Context(), "sqlite", m.Store.Method, path, "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE changed_payload (id INTEGER)")
	if closeErr := db.Close(); err != nil || closeErr != nil {
		t.Fatalf("fixture: %v, close: %v", err, closeErr)
	}
	before := map[string]string{}
	for _, name := range []string{"olivares.db", "audit-signing.key", "catalog-signing.key", "policy-signing.key"} {
		before[name], _, err = dr.FileSHA256(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	cmd := newDRCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err = restoreInPlaceSQLite(t.Context(), cmd, work, dst, m, cipher, "fixture", declaredRestore{}, proof)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("in-place restore must recheck its classified bytes before consuming them: %v", err)
	}
	for name, want := range before {
		got, _, err := dr.FileSHA256(filepath.Join(dst, name))
		if err != nil || got != want {
			t.Fatalf("refusal changed live %s: %v", name, err)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(dst, "*.pre-restore-*")); err != nil || len(matches) != 0 {
		t.Fatalf("refusal promoted or preserved the live estate: %v %v", matches, err)
	}
}

func TestDRPayloadListingCopyKeepsMetadataBound(t *testing.T) {
	const maxMetadata = 8 << 20 // Supported archive metadata bound.
	var output drTOCBuffer
	n, err := io.Copy(&output, io.LimitReader(strings.NewReader(strings.Repeat("x", maxMetadata+1)), maxMetadata+1))
	if err == nil || n > maxMetadata || output.buffer.Len() > maxMetadata {
		t.Fatalf("native metadata copy exceeded its byte bound: copied=%d, error=%v", n, err)
	}
}

func TestDRPayloadClassifierBoundsAndReapsListing(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "oversized"}[oversized], func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			bin := filepath.Join(dir, "pg_restore")
			body := "#!/bin/sh\nprintf '%s' \"$$\" > \"$DRW_LIST_PID\"\nprintf 'fixture-secret' >&2\nexec sleep 30\n"
			if oversized {
				body = strings.Replace(body, "exec sleep 30", "exec head -c 8388609 /dev/zero", 1)
			}
			if err := os.WriteFile(bin, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DRW_LIST_PID", pidFile)
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := classifyPGArchive(ctx, bin, filepath.Join(dir, "unused-fixture-path"), nil)
			if err == nil || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("listing refusal leaked metadata/stderr or succeeded: %v", err)
			}
			if oversized && !strings.Contains(err.Error(), "metadata is unreadable or exceeds the supported bound") {
				t.Fatalf("oversized native output reached parsing instead of refusing at the byte cap: %v", err)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("listing was not bounded by cancellation")
			}
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(pidBytes))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, 0); err == nil {
				t.Fatal("owned listing child was not reaped")
			}
		})
	}
}

func TestDRPayloadRecognizesDirectoryInventoryAuthority(t *testing.T) {
	for _, entry := range []string{
		"1; 1255 123 FUNCTION public olivares_directory_inventory_v1() source_inventory_owner\n",
		"1; 1255 123 FUNCTION public \"olivares_directory_inventory_v1\"() source_inventory_owner\n",
	} {
		proof := &drPayloadProof{}
		if err := classifyPGTOC(strings.NewReader(drTOCHeader+entry), proof); err != nil || !proof.inventory {
			t.Fatalf("inventory not recognized: %v", err)
		}
	}
}
