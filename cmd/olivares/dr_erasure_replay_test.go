// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// bootedHarness drives a boot() engine on a real data dir through its own HTTP
// handler, with the e2e helpers: the setup token, the first admin and a tenant.
func bootedHarness(t *testing.T, eng *engine, dir string) *harness {
	t.Helper()
	h := &harness{t: t, h: eng.api.Handler(), st: eng.store, authr: eng.authr}
	tok, _, err := secure.NewSetupToken(filepath.Join(dir, "setup.token")).Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	if code, raw := h.req("POST", "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "admin@replay.test", "password": "supersecret-replay",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	h.adminToken = h.login(t, "admin@replay.test", "supersecret-replay")
	h.tenantA = h.createOrg("Acme", "acme")
	return h
}

func (h *harness) login(t *testing.T, email, password string) string {
	t.Helper()
	var login struct {
		Token string `json:"token"`
	}
	if code := h.reqInto("POST", "/v1/auth/login", "", "", map[string]any{
		"email": email, "password": password,
	}, &login); code != http.StatusOK || login.Token == "" {
		t.Fatalf("login %s = %d", email, code)
	}
	h.stepUp(login.Token)
	return login.Token
}

// eraseUser runs the governed erasure of one tenant member: the DSR, the
// execute that opens the approval, two distinct approvers, the execute that
// shreds.
func (h *harness) eraseUser(t *testing.T, email string, approvers ...string) {
	t.Helper()
	var req struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/compliance/erasure", h.adminToken, h.tenantA, map[string]any{
		"subject_kind": "user", "subject_ref": email, "case_ref": "DSR-RESTORE",
	}, &req); code != http.StatusCreated || req.ID == "" {
		t.Fatalf("create erasure = %d", code)
	}
	path := "/v1/m/compliance/erasure/" + req.ID + "/execute"
	var pending struct {
		Status      string `json:"status"`
		ApprovalRef string `json:"approval_ref"`
	}
	if code := h.reqInto("POST", path, h.adminToken, h.tenantA, map[string]any{}, &pending); code != http.StatusAccepted || pending.ApprovalRef == "" {
		t.Fatalf("first execute = %d %+v, want 202 with an approval ref", code, pending)
	}
	for _, tok := range approvers {
		if code, raw := h.decide(t, tok, pending.ApprovalRef, "approve"); code != http.StatusOK {
			t.Fatalf("approve = %d: %s", code, raw)
		}
	}
	var receipt map[string]any
	if code := h.reqInto("POST", path, h.adminToken, h.tenantA, map[string]any{}, &receipt); code != http.StatusOK || receipt["key_shredded"] != true {
		t.Fatalf("execute after two approvals = %d %v, want a shredded-key receipt", code, receipt)
	}
}

func excludedIn(t *testing.T, dir string, tenant model.TenantID, user model.ID) bool {
	t.Helper()
	prepareCompositionTestBoot(t)
	defer prepareCompositionTestBoot(t)
	ctx := context.Background()
	eng, err := drBoot(ctx, drFlags{dataDir: dir, engineKind: "sqlite"})
	if err != nil {
		t.Fatalf("boot %s: %v", dir, err)
	}
	defer func() { _ = eng.Close() }()
	excluded, err := eng.authr.SubjectExcluded(ctx, tenant, user)
	if err != nil {
		t.Fatalf("subject excluded: %v", err)
	}
	for _, id := range []model.TenantID{tenant, model.SystemTenantID} {
		if err := eng.store.View(ctx, id, func(sc store.Scope) error {
			v, err := sc.Audit().Verify(ctx, 0)
			if err == nil && !v.OK {
				t.Errorf("audit chain of %s does not verify: %s", id, v.Reason)
			}
			return err
		}); err != nil {
			t.Fatalf("verify chain of %s: %v", id, err)
		}
	}
	return excluded
}

// erasedEstate is a data dir whose tenant member was erased after bundle was taken.
type erasedEstate struct {
	dir, bundle, passFile string
	// beforeErasure is a copy of the store taken with the bundle.
	beforeErasure string
	tenant        model.TenantID
	subject       model.ID
}

func newErasedEstate(t *testing.T) erasedEstate {
	t.Helper()
	ctx := context.Background()
	e := erasedEstate{dir: t.TempDir()}

	prepareCompositionTestBoot(t)
	eng, err := boot(ctx, bootConfig{DataDir: e.dir, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	h := bootedHarness(t, eng, e.dir)
	var subject struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/users", h.adminToken, "", map[string]any{
		"email": "maria@replay.test", "password": strings.Repeat("s", 16), "tenant": h.tenantA, "role": "viewer",
	}, &subject); code != http.StatusCreated || subject.ID == "" {
		t.Fatalf("create subject = %d", code)
	}
	e.subject = model.ID(subject.ID)
	_, approverB := h.createApprover(t, "approver-b@replay.test")
	_, approverC := h.createApprover(t, "approver-c@replay.test")
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	e.tenant = tenantAID(t, h)
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	e.passFile = filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(e.passFile, []byte("a strong DR passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.bundle = filepath.Join(t.TempDir(), "before-erasure.drbundle")
	if out, err := runDR(t, "backup", "--data-dir", e.dir, "--engine", "sqlite", "--out", e.bundle, "--passphrase-file", e.passFile); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	e.beforeErasure = filepath.Join(t.TempDir(), "olivares.db")
	if err := dr.CopyFile(filepath.Join(e.dir, "olivares.db"), e.beforeErasure); err != nil {
		t.Fatal(err)
	}

	prepareCompositionTestBoot(t)
	eng, err = boot(ctx, bootConfig{DataDir: e.dir, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("reboot: %v", err)
	}
	h.h, h.st, h.authr = eng.api.Handler(), eng.store, eng.authr
	h.eraseUser(t, "maria@replay.test", approverB, approverC)
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !excludedIn(t, e.dir, e.tenant, e.subject) {
		t.Fatal("the erasure did not remove the subject from the tenant")
	}
	tombs, err := os.ReadFile(filepath.Join(e.dir, erasureTombstoneFile))
	if err != nil || strings.Count(string(tombs), "\n") != 1 {
		t.Fatalf("tombstone log = %q err=%v, want one line", tombs, err)
	}
	if strings.Contains(string(tombs), "maria") {
		t.Fatalf("the tombstone log holds the subject in clear: %s", tombs)
	}
	return e
}

// A person erased after the backup was taken must not come back when that
// backup is restored: the restore reapplies the erasure, says so, and the
// chain still verifies.
func TestDRRestoreReappliesAnErasureTheBackupPredates(t *testing.T) {
	prevVersion := version
	version = "1.900" // the DR manifest refuses an unstamped producer (TestDRBackupVerifyRestoreCLI)
	t.Cleanup(func() { version = prevVersion })
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = "enterprise" // erasure with key shred is Business

	for _, mode := range []string{"--force", "--in-place"} {
		t.Run(mode, func(t *testing.T) {
			e := newErasedEstate(t)
			out, err := runDR(t, "restore", "--in", e.bundle, "--data-dir", e.dir, "--engine", "sqlite",
				"--passphrase-file", e.passFile, mode, "--operator", "dpo@replay.test", "--reason", "INC-RESTORE")
			if err != nil {
				t.Fatalf("restore: %v\n%s", err, out)
			}
			if !strings.Contains(out, "erasure replay: 1 reapplied, 0 already in the backup") {
				t.Errorf("the restore report does not say the erasure was reapplied:\n%s", out)
			}
			if !excludedIn(t, e.dir, e.tenant, e.subject) {
				t.Fatal("the erased subject came back with the restore")
			}
			if _, err := os.Stat(filepath.Join(e.dir, dr.ErasureReplayMarker)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the replay marker is still there after a complete replay: %v", err)
			}
		})
	}
}

// The console restore promotes the store and leaves only the marker: the serve
// boot that loads the estate applies the erasure before it serves.
func TestServeBootReappliesAnErasureAfterAConsoleRestore(t *testing.T) {
	prevVersion := version
	version = "1.900"
	t.Cleanup(func() { version = prevVersion })
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = "enterprise"

	e := newErasedEstate(t)
	// What runRestore leaves (core/api/dr_handler.go): the marker, then the store.
	if err := dr.MarkErasureReplay(e.dir); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(filepath.Join(e.dir, "olivares.db"+suffix)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if err := dr.CopyFile(e.beforeErasure, filepath.Join(e.dir, "olivares.db")); err != nil {
		t.Fatal(err)
	}

	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: e.dir, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("serve boot after the restore: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !excludedIn(t, e.dir, e.tenant, e.subject) {
		t.Fatal("the erased subject came back after the console restore and the restart")
	}
	if _, err := os.Stat(filepath.Join(e.dir, dr.ErasureReplayMarker)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the replay marker is still there after the boot replayed: %v", err)
	}
}
