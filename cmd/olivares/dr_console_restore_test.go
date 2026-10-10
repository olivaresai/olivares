// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/store"
)

// restoreOwnBackup boots a SQLite engine, takes a console backup of it and restores that
// bundle through the console (upload, apply), waiting for the restore job. A job that does
// not complete fails the test. It returns the engine, still running, and its boot config.
func restoreOwnBackup(t *testing.T) (*engine, bootConfig) {
	t.Helper()
	ctx := context.Background()
	const passphrase = "console restore passphrase"
	// A restore compares the bundle's engine version, so the boot needs a stamped one.
	cfg := bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), Version: "26.1001"}
	eng, err := boot(ctx, cfg)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	admin, err := eng.authr.BootstrapSuperadmin(ctx, demoEmail, demoPassword)
	if err != nil {
		t.Fatalf("bootstrap superadmin: %v", err)
	}
	handler := eng.api.Handler()
	token := consoleLogin(t, handler)

	bundle := consoleBackupBundle(t, handler, token, passphrase)
	// The restored account must come from the backup rather than the newer live
	// database. A no-op promotion must fail the restart assertions below.
	if err := eng.store.AuthMutate(ctx, func(sc store.AuthScope) error {
		admin.DisplayName = "Changed after backup"
		_, err := sc.Users().Update(ctx, admin)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := eng.authr.Authenticate(ctx, token)
	if err != nil || p.DisplayName != "Changed after backup" {
		t.Fatalf("post-backup mutation: name=%q err=%v", p.DisplayName, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/console/dr/restore/upload", bytes.NewReader(bundle))
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var uploaded struct {
		UploadID string `json:"upload_id"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &uploaded) != nil || uploaded.UploadID == "" {
		t.Fatalf("restore upload = %d: %s", rec.Code, rec.Body.String())
	}

	code, apply, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/console/dr/restore/"+uploaded.UploadID+"/apply", token, "", map[string]any{
		"passphrase": passphrase,
	})
	jobID, _ := apply["job_id"].(string)
	if code != http.StatusAccepted || jobID == "" {
		t.Fatalf("restore apply = %d: %s", code, raw)
	}

	waitConsoleDRJob(t, handler, token, jobID)
	// A late subscription must still read the terminal job after the pool closed.
	waitConsoleDRJobStream(t, handler, token, jobID)
	if err := waitConsoleRestoreFence(t, cfg.DataDir).Release(); err != nil {
		t.Fatal(err)
	}
	return eng, cfg
}

func consoleBackupBundle(t *testing.T, handler http.Handler, token, passphrase string) []byte {
	t.Helper()
	code, triggered, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/console/dr/backup", token, "", map[string]any{
		"passphrase": passphrase, "notes": "console restore test",
	})
	backupID, _ := triggered["job_id"].(string)
	if code != http.StatusAccepted || backupID == "" {
		t.Fatalf("backup trigger = %d: %s", code, raw)
	}
	backup := waitConsoleDRJob(t, handler, token, backupID)
	bundleID, _ := backup["bundle_id"].(string)
	if bundleID == "" {
		t.Fatal("completed backup has no bundle_id")
	}
	download := httptest.NewRequest(http.MethodGet, "/v1/console/dr/backups/"+bundleID+"/download", nil)
	download.RemoteAddr = "10.0.0.1:1234"
	download.Header.Set("Authorization", "Bearer "+token)
	downloaded := httptest.NewRecorder()
	handler.ServeHTTP(downloaded, download)
	if downloaded.Code != http.StatusOK {
		t.Fatalf("download = %d", downloaded.Code)
	}
	return downloaded.Body.Bytes()
}

func waitConsoleDRJob(t *testing.T, handler http.Handler, token, jobID string) map[string]any {
	t.Helper()
	return waitConsoleDRJobResult(t, handler, token, jobID, "completed")
}

func waitConsoleDRJobResult(t *testing.T, handler http.Handler, token, jobID, wantStatus string) map[string]any {
	t.Helper()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		code, jobs, raw := doDemoViewJSON(t, handler, http.MethodGet, "/v1/console/dr/jobs", token, "", nil)
		if code != http.StatusOK {
			t.Fatalf("list jobs = %d: %s", code, raw)
		}
		for _, item := range demoViewItems(t, jobs) {
			job, _ := item.(map[string]any)
			if job["id"] != jobID {
				continue
			}
			switch job["status"] {
			case "completed", "failed":
				if job["status"] != wantStatus {
					t.Fatalf("DR job status=%v want=%s at %v: %v", job["status"], wantStatus, job["phase"], job["error"])
				}
				return job
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("DR job %s did not finish: %s", jobID, raw)
		case <-ticker.C:
		}
	}
}

// Reconnect after completion: the restore receipt remains readable while the
// live store is closed, and it reports the required restart.
func waitConsoleDRJobStream(t *testing.T, handler http.Handler, token, jobID string) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/console/dr/jobs/"+jobID+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job stream status %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var job map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &job); err != nil {
			t.Fatal(err)
		}
		if job["status"] == "failed" {
			t.Fatalf("restore failed at %v: %v", job["phase"], job["error"])
		}
		if job["status"] == "completed" {
			if job["phase"] != "restart_required" {
				t.Fatalf("restore did not report restart: %v", job)
			}
			return
		}
	}
	t.Fatalf("stream ended before completion: %v", scanner.Err())
}

func consoleLogin(t *testing.T, handler http.Handler) string {
	t.Helper()
	code, login, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/auth/login", "", "", map[string]any{
		"email": demoEmail, "password": demoPassword,
	})
	token, _ := login["token"].(string)
	if code != http.StatusOK || token == "" {
		t.Fatalf("login = %d: %s", code, raw)
	}
	return token
}

// A console restore of the installation's own backup must pass the scratch
// verification. The scratch store has to open with the schema the live engine
// registers (the modules' tables), or its guard edition differs from the one the
// snapshot records and the restore is refused for every real install.
func TestConsoleRestoreOfOwnBackupPassesScratchVerification(t *testing.T) {
	restoreOwnBackup(t)
}

// The restore promotes the snapshot while the engine keeps running, and the operator
// restarts it afterwards. The restored database must open and the restored administrator
// sign in, after a normal stop and after a hard stop that leaves the engine's -wal and
// -shm behind.
func TestConsoleRestoreLeavesADatabaseTheEngineRestartsOn(t *testing.T) {
	eng, cfg := restoreOwnBackup(t)

	// The data dir as a crash leaves it: copied while the engine still holds its files open.
	crashed := t.TempDir()
	entries, err := os.ReadDir(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(cfg.DataDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(crashed, entry.Name()), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hard := cfg
	hard.DataDir = crashed
	restartAndLogin(t, hard)

	if err := eng.Close(); err != nil {
		t.Fatalf("normal stop: %v", err)
	}
	restartAndLogin(t, cfg)
}

func restartAndLogin(t *testing.T, cfg bootConfig) {
	t.Helper()
	restarted, err := boot(context.Background(), cfg)
	if err != nil {
		t.Fatalf("boot on the restored data dir %s: %v", cfg.DataDir, err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	token := consoleLogin(t, restarted.api.Handler())
	p, err := restarted.authr.Authenticate(context.Background(), token)
	if err != nil || p.DisplayName != "Administrator" {
		t.Fatalf("restored admin: name=%q want Administrator err=%v", p.DisplayName, err)
	}
}

// An invalid key destination must refuse before promotion. Correcting it and
// retrying the same console restore then preserves the old state and recovers
// every source key, so a sealed provider credential opens after restart.
func TestConsoleRestoreInvalidKeyDestinationRefusesBeforePromotion(t *testing.T) {
	ctx := context.Background()
	const passphrase = "console restore passphrase"
	const secretName = "restore-test-provider"
	const secretValue = "synthetic restored provider credential"
	sourceCfg := bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Logger: quietLog(), Version: "26.1001"}
	source, err := boot(ctx, sourceCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if _, err := source.authr.BootstrapSuperadmin(ctx, demoEmail, demoPassword); err != nil {
		t.Fatal(err)
	}
	sourceToken := consoleLogin(t, source.api.Handler())
	principal, err := source.authr.Authenticate(ctx, sourceToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.secretStore.Put(ctx, principal, auth.GlobalSecretScope, secretName, secretValue, "restore regression"); err != nil {
		t.Fatal(err)
	}
	bundle := consoleBackupBundle(t, source.api.Handler(), sourceToken, passphrase)
	extracted := t.TempDir()
	manifest, _, err := dr.ExtractBundle(bytes.NewReader(bundle), extracted)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Keys) < 3 || manifest.Keys[0].Name != "audit-signing.key" {
		t.Fatal("fixture must fail the first of multiple custody keys")
	}

	targetCfg := sourceCfg
	targetCfg.DataDir = t.TempDir()
	target, err := boot(ctx, targetCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	if _, err := target.authr.BootstrapSuperadmin(ctx, demoEmail, demoPassword); err != nil {
		t.Fatal(err)
	}
	handler := target.api.Handler()
	token := consoleLogin(t, handler)
	// Prepare the target's backup directory through the product path (#495).
	consoleBackupBundle(t, handler, token, passphrase)
	oldKeys := map[string][]byte{}
	for _, key := range manifest.Keys {
		oldKeys[key.Name], err = os.ReadFile(filepath.Join(targetCfg.DataDir, key.Name))
		if err != nil {
			t.Fatal(err)
		}
		sourceKey, err := os.ReadFile(filepath.Join(sourceCfg.DataDir, key.Name))
		if err != nil || bytes.Equal(sourceKey, oldKeys[key.Name]) {
			t.Fatalf("fixture custody must differ for %s", key.Name)
		}
	}
	failedPath := filepath.Join(targetCfg.DataDir, manifest.Keys[0].Name)
	if err := os.Remove(failedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(failedPath, 0700); err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRequest(http.MethodPost, "/v1/console/dr/restore/upload", bytes.NewReader(bundle))
	upload.RemoteAddr = "10.0.0.1:1234"
	upload.Header.Set("Authorization", "Bearer "+token)
	upload.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, upload)
	var uploaded struct {
		UploadID string `json:"upload_id"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &uploaded) != nil || uploaded.UploadID == "" {
		t.Fatalf("upload=%d: %s", rec.Code, rec.Body.String())
	}
	code, apply, raw := doDemoViewJSON(t, handler, http.MethodPost, "/v1/console/dr/restore/"+uploaded.UploadID+"/apply", token, "", map[string]any{"passphrase": passphrase})
	jobID, _ := apply["job_id"].(string)
	if code != http.StatusAccepted || jobID == "" {
		t.Fatalf("apply=%d: %s", code, raw)
	}
	job := waitConsoleDRJobResult(t, handler, token, jobID, "failed")
	jobError, _ := job["error"].(string)
	if !strings.Contains(jobError, "key destination audit-signing.key is not a regular file") {
		t.Fatalf("wrong failure: %s", jobError)
	}
	code, _, raw = doDemoViewJSON(t, handler, http.MethodGet, "/readyz", token, "", nil)
	if code != http.StatusOK {
		t.Fatalf("a pre-promotion refusal stopped the engine: status=%d body=%s", code, raw)
	}
	if _, err := target.secretStore.Resolve(ctx, auth.GlobalSecretScope, secretName); err == nil {
		t.Fatal("key refusal imported the source database")
	}
	for _, key := range manifest.Keys[1:] {
		after, err := os.ReadFile(filepath.Join(targetCfg.DataDir, key.Name))
		if err != nil || !bytes.Equal(after, oldKeys[key.Name]) {
			t.Fatalf("later key %s was not left unattempted", key.Name)
		}
	}
	// The terminal job is visible before its deferred scratch cleanup releases
	// the operation lease. Acquire that same fence before offline recovery.
	lease := waitConsoleRestoreFence(t, targetCfg.DataDir)
	// Fix the destination while its source state is still intact, release the
	// test's fence, then retry the SAME uploaded bundle through the console.
	if err := os.Remove(failedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failedPath, oldKeys[manifest.Keys[0].Name], 0600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	code, apply, raw = doDemoViewJSON(t, handler, http.MethodPost, "/v1/console/dr/restore/"+uploaded.UploadID+"/apply", token, "", map[string]any{"passphrase": passphrase})
	jobID, _ = apply["job_id"].(string)
	if code != http.StatusAccepted || jobID == "" {
		t.Fatalf("retry apply=%d: %s", code, raw)
	}
	job = waitConsoleDRJob(t, handler, token, jobID)
	note, _ := job["notes"].(string)
	if !strings.Contains(note, "key custody intact") || !strings.Contains(note, "pre-restore-") {
		t.Fatalf("missing custody/preservation receipt: %s", note)
	}
	for _, key := range manifest.Keys {
		copies, err := filepath.Glob(filepath.Join(targetCfg.DataDir, key.Name+".pre-restore-*"))
		if err != nil || len(copies) != 1 {
			t.Fatalf("previous key copies for %s=%d err=%v", key.Name, len(copies), err)
		}
		saved, err := os.ReadFile(copies[0])
		if err != nil || !bytes.Equal(saved, oldKeys[key.Name]) {
			t.Fatalf("previous %s was not preserved", key.Name)
		}
	}
	if err := waitConsoleRestoreFence(t, targetCfg.DataDir).Release(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := boot(ctx, targetCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	consoleLogin(t, restarted.api.Handler())
	value, err := restarted.secretStore.Resolve(ctx, auth.GlobalSecretScope, secretName)
	if err != nil || string(value) != secretValue {
		t.Fatalf("restored sealed provider credential did not open: %v", err)
	}
}

func waitConsoleRestoreFence(t *testing.T, dataDir string) *opgate.Lease {
	t.Helper()
	anchor, present, err := opgate.AnchorForDataDir(dataDir)
	if err != nil || !present {
		t.Fatalf("recovery anchor: present=%v err=%v", present, err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var lease *opgate.Lease
	for {
		var acquired bool
		lease, acquired, err = opgate.TryAcquire(opgate.ModeExclusive, anchor)
		if err != nil && !errors.Is(err, opgate.ErrSelfHeld) {
			t.Fatal(err)
		}
		if acquired {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("restore did not release its recovery fence")
		case <-ticker.C:
		}
	}
	t.Cleanup(func() { _ = lease.Release() })
	return lease

}
