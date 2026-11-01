// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Only the persistence sealer is a test double. Resolution, signing, streaming,
// HTTP status handling and GFS deletion use the real product implementations.
type drTestSealer struct{}

func (drTestSealer) Seal(_ context.Context, _ model.TenantID, b []byte) (string, error) {
	return base64.StdEncoding.EncodeToString(b), nil
}
func (drTestSealer) Open(_ context.Context, _ model.TenantID, b string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(b)
}

func TestDROffsiteScheduleResolvesSecretStreamsAndPrunes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	secrets := auth.NewSecretStore(st, drTestSealer{})
	if _, err := secrets.Put(ctx, auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}, auth.GlobalSecretScope, "dr/offsite", `{"access_key_id":"test-access","secret_access_key":"test-secret"}`, ""); err != nil {
		t.Fatal(err)
	}
	read := make(chan []byte, 1)
	var deleted []string
	var deleteMu sync.Mutex
	var failUpload atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("offsite request was not signed")
		}
		switch r.Method {
		case http.MethodPut:
			if r.URL.Path != "/recovery/latest.drbundle" {
				t.Error("upload did not use the bundle's final name")
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			read <- b
			if failUpload.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case http.MethodGet:
			fmt.Fprint(w, `<ListBucketResult><Contents><Key>latest.drbundle</Key><LastModified>2026-10-28T02:00:00Z</LastModified><Size>13</Size></Contents><Contents><Key>old.drbundle</Key><LastModified>2026-01-01T02:00:00Z</LastModified><Size>13</Size></Contents><Contents><Key>company/report.pdf</Key><LastModified>2026-10-28T01:00:00Z</LastModified></Contents><Contents><Key>company/invoice.pdf</Key><LastModified>2026-10-28T00:00:00Z</LastModified></Contents><Contents><Key>notes.txt</Key><LastModified>2026-10-28T01:00:00Z</LastModified></Contents><Contents><Key>notes-old.txt</Key><LastModified>2026-10-28T00:00:00Z</LastModified></Contents><Contents><Key>other/nested.drbundle</Key><LastModified>2026-01-01T02:00:00Z</LastModified></Contents><Contents><Key>unknown.drbundle</Key><LastModified>invalid</LastModified></Contents></ListBucketResult>`)
		case http.MethodDelete:
			deleteMu.Lock()
			deleted = append(deleted, r.URL.Path)
			deleteMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer remote.Close()
	path := filepath.Join(dir, "latest.drbundle")
	if err := os.WriteFile(path, []byte("bundle bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{secretStore: secrets}
	// HTTP is confined to this in-process transport test. The console and stored
	// policy validation independently require HTTPS before any execution.
	o := &drOffsite{Endpoint: remote.URL, Bucket: "recovery", CredentialRef: "store:dr/offsite", GFS: drGFS{KeepLast: 1}}
	if _, err := s.replicateScheduledBackup(ctx, o, path, time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := string(<-read); got != "bundle bytes" {
		t.Fatalf("uploaded %q", got)
	}
	deleteMu.Lock()
	if len(deleted) != 1 || deleted[0] != "/recovery/old.drbundle" {
		t.Errorf("offsite retention deleted foreign or undated objects: %v", deleted)
	}
	deleteMu.Unlock()
	failUpload.Store(true)
	if _, err := s.replicateScheduledBackup(ctx, o, path, time.Now()); err == nil {
		t.Fatal("upload failure was hidden")
	}
	o.CredentialRef = "store:missing"
	if _, err := s.replicateScheduledBackup(ctx, o, path, time.Now()); err == nil {
		t.Fatal("missing credentials were accepted")
	}
}
