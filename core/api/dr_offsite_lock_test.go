// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// offsiteLockFixture is a Server with a real store and secret store, a bundle on
// disk and an S3 stand-in whose ?object-lock and DELETE answers the test picks.
func offsiteLockFixture(t *testing.T, lock func(http.ResponseWriter), del int) (*Server, store.Store, *drOffsite, string) {
	t.Helper()
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
	if _, err := secrets.Put(ctx, auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}, auth.GlobalSecretScope, "dr/offsite", `{"access_key_id":"write-only","secret_access_key":"test-secret"}`, ""); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Has("object-lock"):
			lock(w)
		case r.Method == http.MethodGet:
			fmt.Fprint(w, `<ListBucketResult><Contents><Key>latest.drbundle</Key><LastModified>2026-10-28T02:00:00Z</LastModified></Contents><Contents><Key>old.drbundle</Key><LastModified>2026-01-01T02:00:00Z</LastModified></Contents></ListBucketResult>`)
		case r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
		case r.Method == http.MethodDelete:
			w.WriteHeader(del)
			if del == http.StatusForbidden {
				fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`)
			}
		}
	}))
	t.Cleanup(remote.Close)
	path := filepath.Join(dir, "latest.drbundle")
	if err := os.WriteFile(path, []byte("bundle bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := &drOffsite{Endpoint: remote.URL, Bucket: "recovery", CredentialRef: "store:dr/offsite", GFS: drGFS{KeepLast: 1}}
	return &Server{secretStore: secrets, st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, st, o, path
}

func lockConfig(mode string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		rule := ""
		if mode != "" {
			rule = "<Rule><DefaultRetention><Mode>" + mode + "</Mode><Days>30</Days></DefaultRetention></Rule>"
		}
		fmt.Fprint(w, "<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled>"+rule+"</ObjectLockConfiguration>")
	}
}

// The console warns from this state: only a default COMPLIANCE retention locks
// what the engine writes, since its puts carry no lock headers of their own.
func TestDROffsiteScheduleReportsBucketLock(t *testing.T) {
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		lock func(http.ResponseWriter)
		want string
	}{
		{"compliance default", lockConfig("COMPLIANCE"), "compliance"},
		{"governance default", lockConfig("GOVERNANCE"), "governance"},
		{"enabled without default retention", lockConfig(""), "no_default"},
		{"no object lock", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<Error><Code>ObjectLockConfigurationNotFoundError</Code></Error>`)
		}, "none"},
		{"store without the API", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotImplemented)
			fmt.Fprint(w, `<Error><Code>NotImplemented</Code></Error>`)
		}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, o, path := offsiteLockFixture(t, tc.lock, http.StatusNoContent)
			lock, err := s.replicateScheduledBackup(context.Background(), o, path, now)
			if err != nil {
				t.Fatal(err)
			}
			if lock != tc.want {
				t.Fatalf("offsite lock = %q, want %q", lock, tc.want)
			}
		})
	}
}

// A write credential without s3:DeleteObject makes the retention DELETE fail.
// The run fails, and the refusal is in the system audit ledger.
func TestDROffsiteScheduleAuditsRefusedDelete(t *testing.T) {
	ctx := context.Background()
	s, st, o, path := offsiteLockFixture(t, lockConfig("COMPLIANCE"), http.StatusForbidden)
	lock, err := s.replicateScheduledBackup(ctx, o, path, time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("a refused offsite delete was hidden")
	}
	if lock != "compliance" {
		t.Fatalf("a failed run lost the lock state: %q", lock)
	}
	audited := 0
	if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(e model.AuditEvent) error {
			if e.Action == "dr.offsite.delete_failed" {
				audited++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Fatalf("refused offsite delete audit entries = %d, want 1", audited)
	}
}

// offsite_lock describes the bucket last checked, so only a new bucket clears it.
func TestDRScheduleSetOffsiteClearsLockOnNewBucket(t *testing.T) {
	target := drOffsite{Bucket: "recovery", CredentialRef: "store:dr-offsite", GFS: drGFS{Daily: 7}}
	d := drSchedule{}
	d.setOffsite(&target)
	d.OffsiteLock = "none"
	retuned := target
	retuned.GFS.Daily = 14
	d.setOffsite(&retuned)
	if d.OffsiteLock != "none" || d.Offsite.GFS.Daily != 14 {
		t.Fatalf("same bucket: %+v, want the checked state kept and the new GFS saved", d)
	}
	moved := target
	moved.Bucket = "recovery-locked"
	d.setOffsite(&moved)
	if d.OffsiteLock != "" || d.Offsite.Bucket != "recovery-locked" {
		t.Fatalf("new bucket kept the old bucket's lock state: %+v", d)
	}
	d.OffsiteLock = "compliance"
	d.setOffsite(&drOffsite{})
	if d.Offsite != nil || d.OffsiteLock != "" {
		t.Fatalf("an empty bucket must disable the copy and its lock state: %+v", d)
	}
}

// The warning must outlive a restart: the lock state is persisted with the schedule.
func TestDRScheduleOffsiteLockSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := offsiteLockFixture(t, lockConfig(""), http.StatusNoContent)
	s.drSvc = newDRService(DRConfig{DataDir: t.TempDir()})
	// The stored policy is validated on load, so the target is HTTPS; no request is sent.
	o := &drOffsite{Endpoint: "https://s3.example.net", Bucket: "recovery", CredentialRef: "store:dr/offsite"}
	if err := s.saveDRSchedule(ctx, drSchedule{drProtection: drProtection{Offsite: o}, OffsiteLock: "no_default"}); err != nil {
		t.Fatal(err)
	}
	restarted := &Server{st: st, log: s.log, drSvc: newDRService(DRConfig{DataDir: t.TempDir()})}
	if err := restarted.loadDRSchedule(ctx); err != nil {
		t.Fatal(err)
	}
	if got := restarted.drSvc.scheduleSnapshot().OffsiteLock; got != "no_default" {
		t.Fatalf("offsite_lock after restart = %q, want no_default", got)
	}
}
