//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The read interface pins the authorized snapshot capability; its authority
// comes from the constructor-owned dependency record.
type workspaceSnapshotPort interface {
	ReadWorkspaceSnapshot(context.Context, model.TenantID, auth.Principal, string, string, func(string, fs.FileMode, []byte) error) (int64, error)
}

func snapshotFixture(t *testing.T, params CreateWorkspaceParams, opts ...Option) (*Module, model.TenantID, auth.Principal, workspaceDTO, string) {
	t.Helper()
	m, _, tenant, _ := newRuntimeHarness(t, opts...)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "packs", "research"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "packs", "research", "SKILL.md"), []byte("research instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	params.RootPath = root
	ws := mkWorkspace(t, m, tenant, params)
	principal := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()}
	m.WorkspaceSnapshotAuthority = func(context.Context, model.TenantID, auth.Principal, string, model.ID) (string, error) {
		return "fixture-authority", nil
	}
	return m, tenant, principal, ws, root
}

func TestWorkspaceSnapshotUnsafeSources(t *testing.T) {
	for _, name := range []string{"symlink", "directory-link", "fifo", "hardlink", "protected-file", "protected-tree", "protected-root", "system-config-root", "replaced-root", "excluded-subpath", "disabled", "no-authority", "foreign-tenant"} {
		t.Run(name, func(t *testing.T) {
			m, tenant, principal, ws, root := snapshotFixture(t, CreateWorkspaceParams{})
			dir := filepath.Join(root, "packs", "research")
			var err error
			switch name {
			case "symlink":
				err = os.Symlink(filepath.Join(dir, "SKILL.md"), filepath.Join(dir, "linked.md"))
			case "directory-link":
				err = os.Symlink(t.TempDir(), filepath.Join(dir, "linked"))
			case "fifo":
				err = unix.Mkfifo(filepath.Join(dir, "pipe"), 0o600)
			case "hardlink":
				err = os.Link(filepath.Join(dir, "SKILL.md"), filepath.Join(root, "outside.md"))
			case "protected-file":
				err = os.WriteFile(filepath.Join(dir, ".env"), []byte("fixture only"), 0o600)
			case "protected-tree":
				err = os.Mkdir(filepath.Join(dir, "credentials"), 0o700)
			case "protected-root":
				setSnapshotRegistration(t, m, tenant, ws.WorkspaceRef, colWsRootPath, filepath.Join(root, "accounts"))
			case "system-config-root":
				setSnapshotRegistration(t, m, tenant, ws.WorkspaceRef, colWsRootPath, "/etc")
			case "replaced-root":
				moved := root + "-moved"
				err = os.Rename(root, moved)
				t.Cleanup(func() { _ = os.RemoveAll(moved) })
				if err == nil {
					err = os.Symlink(moved, root)
				}
			case "excluded-subpath":
				setSnapshotRegistration(t, m, tenant, ws.WorkspaceRef, colWsAllowSubpaths, `["other"]`)
			case "disabled":
				setSnapshotRegistration(t, m, tenant, ws.WorkspaceRef, colWsState, wsDisabled)
			case "no-authority":
				m.WorkspaceSnapshotAuthority = nil
			case "foreign-tenant":
				tenant = model.TenantID(model.NewID())
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			version, err := m.ReadWorkspaceSnapshot(t.Context(), tenant, principal, ws.WorkspaceRef, "packs/research", func(string, fs.FileMode, []byte) error { calls++; return nil })
			if err == nil || version != 0 || calls != 0 {
				t.Fatalf("unsafe source released a snapshot: version=%d calls=%d err=%v", version, calls, err)
			}
			if strings.Contains(err.Error(), root) {
				t.Fatal("snapshot refusal exposed a host path")
			}
		})
	}
}

// Configured account homes can have ordinary directory names. A folder
// registration must not make those engine-owned files an import source.
func TestWorkspaceSnapshotConfiguredProtectedTrees(t *testing.T) {
	for _, name := range []string{"account-root", "profile-root", "login-root", "engine-storage", "account-alias", "profile-alias", "login-alias", "engine-alias"} {
		t.Run(name, func(t *testing.T) {
			m, tenant, principal, ws, root := snapshotFixture(t, CreateWorkspaceParams{})
			selected := filepath.Join(root, "packs", "research")
			protected := selected
			if strings.HasSuffix(name, "-alias") {
				protected = filepath.Join(t.TempDir(), "state-link")
				if err := os.Symlink(selected, protected); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "account-root", "account-alias":
				m.UseAccountsRoot(protected)
			case "profile-root", "profile-alias":
				m.UseProfileHomesRoot(protected)
			case "login-root", "login-alias":
				m.UseToolLoginsRoot(protected)
			case "engine-storage", "engine-alias":
				WithConfinement([]string{protected}, false)(m)
			}
			calls := 0
			version, err := m.ReadWorkspaceSnapshot(t.Context(), tenant, principal, ws.WorkspaceRef, "packs/research", func(string, fs.FileMode, []byte) error { calls++; return nil })
			if err == nil || version != 0 || calls != 0 {
				t.Fatalf("configured protected tree released bytes: version=%d calls=%d err=%v", version, calls, err)
			}
			if strings.Contains(err.Error(), selected) {
				t.Fatal("protected-tree refusal exposed a host path")
			}
		})
	}
}

func TestWorkspaceSnapshotInputAndCancellation(t *testing.T) {
	m, tenant, principal, ws, _ := snapshotFixture(t, CreateWorkspaceParams{})
	for _, dir := range []string{"", ".", "../packs", "/packs", "packs//research", "packs\\research", "packs/.ssh", "packs/config", "packs/account", "packs/research\x00", "packs/research\n", "C:/packs", strings.Repeat("x/", 16) + "x"} {
		calls := 0
		version, err := m.ReadWorkspaceSnapshot(t.Context(), tenant, principal, ws.WorkspaceRef, dir, func(string, fs.FileMode, []byte) error { calls++; return nil })
		if !isStatus(err, 400) || version != 0 || calls != 0 {
			t.Fatalf("unsafe directory %q was admitted: version=%d calls=%d err=%v", dir, version, calls, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if version, err := m.ReadWorkspaceSnapshot(ctx, tenant, principal, ws.WorkspaceRef, "packs/research", func(string, fs.FileMode, []byte) error { t.Fatal("cancelled snapshot released bytes"); return nil }); !errors.Is(err, context.Canceled) || version != 0 {
		t.Fatalf("cancelled snapshot = %d, %v", version, err)
	}
}

func TestWorkspaceSnapshotLimitsAndDLP(t *testing.T) {
	for _, name := range []string{"registered-file-limit", "snapshot-file-limit", "total-limit", "entry-limit", "depth-limit", "dlp-hit", "dlp-unavailable"} {
		t.Run(name, func(t *testing.T) {
			params := CreateWorkspaceParams{}
			var opts []Option
			if name == "registered-file-limit" {
				params.MaxReadBytes = 8
			}
			if strings.HasPrefix(name, "dlp-") {
				params.DLPMode = dlpDeny
				if name == "dlp-hit" {
					opts = append(opts, WithClassifier(fakeClassifier{trigger: "research"}))
				}
			}
			m, tenant, principal, ws, root := snapshotFixture(t, params, opts...)
			dir := filepath.Join(root, "packs", "research")
			switch name {
			case "snapshot-file-limit":
				if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("x", 1<<20+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "total-limit":
				for i := 0; i < 17; i++ {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.txt", i)), []byte(strings.Repeat("x", 1<<20)), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "entry-limit":
				for i := 0; i < 1024; i++ {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.txt", i)), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "depth-limit":
				if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(strings.Repeat("nested/", 16)+"leaf")), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			version, err := m.ReadWorkspaceSnapshot(t.Context(), tenant, principal, ws.WorkspaceRef, "packs/research", func(string, fs.FileMode, []byte) error { calls++; return nil })
			if err == nil || version != 0 || calls != 0 {
				t.Fatalf("limit or DLP refusal released bytes: version=%d calls=%d err=%v", version, calls, err)
			}
		})
	}
}

func TestWorkspaceSnapshotChangesRefused(t *testing.T) {
	for _, stage := range []string{"classifier", "authority", "receiver"} {
		for _, change := range []string{"bytes", "empty-directory", "registration", "source-authority", "cancel"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				var mutate func()
				params := CreateWorkspaceParams{}
				var opts []Option
				if stage == "classifier" {
					params.DLPMode = dlpLabel
					opts = append(opts, WithClassifier(ClassifierFunc(func(string) ([]SensitivityHit, error) { mutate(); return nil, nil })))
				}
				m, tenant, principal, ws, root := snapshotFixture(t, params, opts...)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				stamp, changed := "authority-one", false
				mutate = func() {
					if changed {
						return
					}
					changed = true
					var err error
					switch change {
					case "bytes":
						err = os.WriteFile(filepath.Join(root, "packs", "research", "SKILL.md"), []byte("changed instructions\n"), 0o600)
					case "empty-directory":
						err = os.Mkdir(filepath.Join(root, "packs", "research", "added"), 0o700)
					case "registration":
						setSnapshotRegistration(t, m, tenant, ws.WorkspaceRef, colWsState, wsDisabled)
					case "source-authority":
						stamp = "authority-two"
					case "cancel":
						cancel()
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				checks := 0
				m.WorkspaceSnapshotAuthority = func(context.Context, model.TenantID, auth.Principal, string, model.ID) (string, error) {
					checks++
					if stage == "authority" && checks == 2 {
						mutate()
					}
					return stamp, nil
				}
				calls := 0
				version, err := m.ReadWorkspaceSnapshot(ctx, tenant, principal, ws.WorkspaceRef, "packs/research", func(string, fs.FileMode, []byte) error {
					calls++
					if stage == "receiver" {
						mutate()
					}
					return nil
				})
				if err == nil || version != 0 || (stage != "receiver" && calls != 0) {
					t.Fatalf("changed snapshot reported success: version=%d callbacks=%d err=%v", version, calls, err)
				}
			})
		}
	}
}

func TestWorkspaceSnapshotLateAuthorityChange(t *testing.T) {
	m, tenant, principal, ws, _ := snapshotFixture(t, CreateWorkspaceParams{})
	checks := 0
	authority := "original-source-authority"
	m.WorkspaceSnapshotAuthority = func(context.Context, model.TenantID, auth.Principal, string, model.ID) (string, error) {
		observed := authority
		checks++
		if checks == 3 {
			// The decision just completed; a concurrent source writer now
			// changes authority while the reader observes the filesystem.
			authority = "revoked-source-authority"
		}
		return observed, nil
	}
	version, err := m.ReadWorkspaceSnapshot(t.Context(), tenant, principal, ws.WorkspaceRef, "packs/research", func(string, fs.FileMode, []byte) error { return nil })
	if err == nil || version != 0 {
		t.Fatal("source authority changed after a decision but the snapshot committed")
	}
}

func TestWorkspaceSnapshotReceiverOwnsItsBytes(t *testing.T) {
	m, tenant, principal, ws, _ := snapshotFixture(t, CreateWorkspaceParams{})
	if version, err := m.ReadWorkspaceSnapshot(t.Context(), tenant, principal, ws.WorkspaceRef, "packs/research", func(_ string, _ fs.FileMode, data []byte) error { data[0] = 'X'; return nil }); err != nil || version != 1 {
		t.Fatalf("consumer mutation corrupted the verification buffer: %d, %v", version, err)
	}
	want := errors.New("consumer refused")
	if version, err := m.ReadWorkspaceSnapshot(t.Context(), tenant, principal, ws.WorkspaceRef, "packs/research", func(string, fs.FileMode, []byte) error { return want }); !errors.Is(err, want) || version != 0 {
		t.Fatalf("receiver error was not preserved: %d, %v", version, err)
	}
}

func setSnapshotRegistration(t *testing.T, m *Module, tenant model.TenantID, ref, column string, value any) {
	t.Helper()
	ctx := context.Background()
	if err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		rec, err := findWorkspaceRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		rec[column] = value
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceSnapshotRegisteredRead(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	reader, ok := any(m).(workspaceSnapshotPort)
	if !ok {
		t.Fatal("registered workspace snapshot reader is not connected")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "packs", "research", "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		name, data string
		mode       fs.FileMode
	}{
		{"SKILL.md", "---\nname: research\ndescription: Read references\n---\nRead the selected references.\n", 0o600},
		{"scripts/check.sh", "#!/bin/sh\nexit 0\n", 0o700},
	} {
		if err := os.WriteFile(filepath.Join(root, "packs", "research", filepath.FromSlash(file.name)), []byte(file.data), file.mode); err != nil {
			t.Fatal(err)
		}
	}
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: root, AllowSubpaths: []string{"packs"}})
	principal := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()}
	authorityChecks := 0
	m.WorkspaceSnapshotAuthority = func(ctx context.Context, gotTenant model.TenantID, gotPrincipal auth.Principal, ref string, id model.ID) (string, error) {
		if ctx.Err() != nil || gotTenant != tenant || gotPrincipal.UserID != principal.UserID || ref != ws.WorkspaceRef || id.IsZero() {
			t.Fatal("snapshot authorization lost the authenticated request or stored registration")
		}
		authorityChecks++
		return "fixture-authority-version-one", nil
	}
	files := map[string]string{}
	modes := map[string]fs.FileMode{}
	version, err := reader.ReadWorkspaceSnapshot(context.Background(), tenant, principal, ws.WorkspaceRef, "packs/research", func(relative string, mode fs.FileMode, data []byte) error {
		if authorityChecks < 2 {
			t.Fatal("snapshot released bytes before final authority verification")
		}
		files[relative], modes[relative] = string(data), mode
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 || len(files) != 2 || files["scripts/check.sh"] != "#!/bin/sh\nexit 0\n" || files["SKILL.md"] == "" {
		t.Fatalf("snapshot did not preserve the selected relative files and registration version: version=%d files=%v", version, files)
	}
	if modes["SKILL.md"] != 0o644 || modes["scripts/check.sh"] != 0o755 {
		t.Fatalf("snapshot did not normalize regular-file modes: %v", modes)
	}
}
