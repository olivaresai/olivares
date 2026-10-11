// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupWritePlanOwnership pins the ownership setup gives what it writes on a
// packaged host. The env file holds the flags the unit passes to the engine:
// systemd reads it as root, so it is root:olivares 0640, the policy of the
// package postinstall, install-service.sh and `doctor --mode system`. The 0600
// secret files are read by the engine itself, so they go to the service account.
// Their directory stays root-owned, with service-group traversal (0750).
// Even when secrets share the env directory, the service cannot replace entries.
// A --force rerun applies the same owners: the overwrite keeps the old inode, so
// only the explicit chown repairs a file an earlier run gave away.
func TestSetupWritePlanOwnership(t *testing.T) {
	const svcUID, svcGID = 4242, 4343
	origUser, origChown := setupServiceUser, setupChown
	t.Cleanup(func() { setupServiceUser, setupChown = origUser, origChown })
	setupServiceUser = func() (*user.User, error) {
		return &user.User{Username: "olivares", Uid: "4242", Gid: "4343"}, nil
	}
	type owner struct{ uid, gid int }
	got := map[string]owner{}
	setupChown = func(path string, uid, gid int) error {
		got[path] = owner{uid, gid}
		return nil
	}

	for _, sharedDir := range []bool{false, true} {
		dir := t.TempDir()
		envPath := filepath.Join(dir, "olivares.env")
		secretPath := filepath.Join(dir, "secrets", "app.dsn")
		if sharedDir {
			secretPath = filepath.Join(dir, "app.dsn")
		}
		plan := installPlan{
			Profile: profilePostgresPro, Engine: "postgres", Listen: "127.0.0.1:8443", GRPCListen: "127.0.0.1:9443",
			DSNArg:  "file:" + secretPath,
			Secrets: []envSecretFile{{Path: secretPath, Content: "postgres://app:pw@db/olivares"}},
		}
		for _, force := range []bool{false, true} {
			clear(got)
			if force {
				// Reproduce an older setup's root-only directory before the rerun.
				if err := os.Chmod(filepath.Dir(secretPath), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := writePlan(io.Discard, plan, envPath, force); err != nil {
				t.Fatalf("writePlan(force=%v): %v", force, err)
			}
			if want := (owner{0, svcGID}); got[envPath] != want {
				t.Errorf("force=%v: env file owner = %d:%d, want root:olivares %d:%d",
					force, got[envPath].uid, got[envPath].gid, want.uid, want.gid)
			}
			if want := (owner{svcUID, svcGID}); got[secretPath] != want {
				t.Errorf("force=%v: secret file owner = %d:%d, want the service account %d:%d",
					force, got[secretPath].uid, got[secretPath].gid, want.uid, want.gid)
			}
			secretDir := filepath.Dir(secretPath)
			if want := (owner{0, svcGID}); got[secretDir] != want {
				t.Errorf("force=%v: secrets directory owner = %d:%d, want root:olivares %d:%d for service traversal without directory writes",
					force, got[secretDir].uid, got[secretDir].gid, want.uid, want.gid)
			}
			for path, mode := range map[string]os.FileMode{envPath: 0o640, secretPath: 0o600, secretDir: 0o750} {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat %s: %v", path, err)
				}
				if info.Mode().Perm() != mode {
					t.Errorf("force=%v: %s mode = %o, want %o", force, filepath.Base(path), info.Mode().Perm(), mode)
				}
			}
		}
	}
}

// TestSetupWritePlanWithoutServiceUser: on a host with no `olivares` account
// setup chowns nothing (no fallback to uid/gid 0) and says so.
func TestSetupWritePlanWithoutServiceUser(t *testing.T) {
	origUser, origChown := setupServiceUser, setupChown
	t.Cleanup(func() { setupServiceUser, setupChown = origUser, origChown })
	setupServiceUser = func() (*user.User, error) { return nil, user.UnknownUserError("olivares") }
	setupChown = func(path string, uid, gid int) error {
		t.Errorf("chown %s to %d:%d without a service account", path, uid, gid)
		return nil
	}

	envPath := filepath.Join(t.TempDir(), "olivares.env")
	var out strings.Builder
	plan := installPlan{Profile: profileEval, Engine: "sqlite", Listen: "127.0.0.1:8443", GRPCListen: "127.0.0.1:9443"}
	if err := writePlan(&out, plan, envPath, false); err != nil {
		t.Fatalf("writePlan: %v", err)
	}
	if !strings.Contains(out.String(), "note: no `olivares` service user on this host") {
		t.Errorf("output lacks the no-service-user note:\n%s", out.String())
	}
}
