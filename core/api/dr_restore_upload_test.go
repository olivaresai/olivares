// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr"
)

// restoreUploadBundle returns the bytes of a small authenticated DR bundle, as a
// console client would read them from a file the operator chose.
func restoreUploadBundle(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.drbundle")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = writeDRMetadataBundle(t, f, &dr.Manifest{
		Format:     dr.ManifestFormat,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		EngineKind: "sqlite",
		Store:      dr.StoreSnapshot{Method: dr.MethodPITR, File: "external"},
	})
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("write fixture bundle: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// uploadRestoreBundle posts raw bytes the way the console does (an octet-stream
// body, not JSON), which harness.do cannot send.
func uploadRestoreBundle(h *harness, token string, body []byte) (int, string) {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/console/dr/restore/upload", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// Restoring onto a freshly installed host is the disaster-recovery case, and a
// fresh install has never made a backup, so <data-dir>/backups does not exist
// yet. The upload must create it instead of failing with a 500 (#495).
func TestDRRestoreUploadCreatesMissingBackupDir(t *testing.T) {
	bundle := restoreUploadBundle(t)

	t.Run("valid bundle is accepted and staged", func(t *testing.T) {
		h, backupDir := drHarness(t)
		admin := h.adminLogin()
		if _, err := os.Stat(backupDir); !os.IsNotExist(err) {
			t.Fatalf("precondition: backup dir must be absent, stat err=%v", err)
		}

		code, raw := uploadRestoreBundle(h, admin, bundle)
		if code != http.StatusOK {
			t.Fatalf("upload with no backup dir = %d %s, want 200", code, raw)
		}
		var out struct {
			UploadID string         `json:"upload_id"`
			Manifest map[string]any `json:"manifest"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if out.Manifest["format"] != dr.ManifestFormat {
			t.Fatalf("upload response carries no manifest: %s", raw)
		}
		staged := filepath.Join(backupDir, out.UploadID)
		if _, err := os.Stat(staged); err != nil {
			t.Fatalf("staged upload %s is not where the apply step reads it: %v", staged, err)
		}
		info, err := os.Stat(backupDir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("backup dir mode = %o, want 700 (it holds key material)", perm)
		}
	})

	t.Run("existing backup dir still works", func(t *testing.T) {
		h, backupDir := drHarness(t)
		admin := h.adminLogin()
		if err := os.Mkdir(backupDir, 0o700); err != nil {
			t.Fatal(err)
		}

		if code, raw := uploadRestoreBundle(h, admin, bundle); code != http.StatusOK {
			t.Fatalf("upload with an existing backup dir = %d %s, want 200", code, raw)
		}
	})

	t.Run("corrupt bundle is a 400 and leaves no staged file", func(t *testing.T) {
		h, backupDir := drHarness(t)
		admin := h.adminLogin()

		code, raw := uploadRestoreBundle(h, admin, []byte("not a bundle"))
		if code != http.StatusBadRequest || !strings.Contains(raw, "invalid or corrupt DR bundle") {
			t.Fatalf("corrupt upload with no backup dir = %d %s, want 400 invalid or corrupt DR bundle", code, raw)
		}
		entries, err := os.ReadDir(backupDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("rejected upload left %d file(s) in the backup dir", len(entries))
		}
	})
}
