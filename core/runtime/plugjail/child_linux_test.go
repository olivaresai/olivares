// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package plugjail

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	coreconfine "github.com/olivaresai/olivares/core/runtime/confine"
	"golang.org/x/sys/unix"
)

type childResult struct {
	ReadAllowed, WroteScratch, DeniedSecret, DeniedPluginWrite, NoNewPrivs, ReadExecutable bool
	UID                                                                                    int
	Groups                                                                                 []int
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == coreconfine.HelperArg {
		os.Exit(coreconfine.RunHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "__owned_plugin_engine" {
		cmd := exec.Command(os.Args[2], append([]string{"__owned_plugin_child"}, os.Args[4:]...)...)
		c := Default("owned-child")
		c.WritableScratch = os.Args[3]
		_, cleanup, err := Apply(cmd, c)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		out, err := cmd.CombinedOutput()
		CloseSpawnFD(cmd)
		cleanup()
		cleanup() // teardown remains idempotent after the child is dead
		_, _ = os.Stdout.Write(out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "__owned_plugin_child" {
		_, read := os.ReadFile(os.Args[2])
		write := os.WriteFile(os.Args[3], []byte("scratch"), 0o600)
		_, secret := os.ReadFile(os.Args[4])
		pluginWrite := os.WriteFile(os.Args[5], []byte("forbidden"), 0o600)
		nnp, _ := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
		_, execRead := os.ReadFile(os.Args[0])
		groups, _ := os.Getgroups()
		_ = json.NewEncoder(os.Stdout).Encode(childResult{
			UID: os.Geteuid(), Groups: groups, NoNewPrivs: nnp == 1, ReadExecutable: execRead == nil,
			ReadAllowed: read == nil, WroteScratch: write == nil,
			DeniedSecret:      errors.Is(secret, os.ErrPermission),
			DeniedPluginWrite: errors.Is(pluginWrite, os.ErrPermission),
		})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOwnedPluginChildEnforcesFilesystemPolicy(t *testing.T) {
	if coreconfine.Probe().Mode != coreconfine.ModeLandlock {
		t.Skip(coreconfine.Probe().Reason)
	}
	root, err := os.MkdirTemp("/tmp", "olivares-plugjail-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	pluginDir, scratch, private := filepath.Join(root, "plugin"), filepath.Join(root, "scratch"), filepath.Join(root, "private")
	for _, dir := range []string{pluginDir, scratch, private} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(pluginDir, "probe")
	if err := os.WriteFile(program, bytes, 0o711); err != nil {
		t.Fatal(err)
	}
	allowed, secret := filepath.Join(pluginDir, "resource"), filepath.Join(private, "synthetic-secret")
	for _, path := range []string{allowed, secret} {
		if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Copy the parent/helper into the owned traversable fixture too: a root CI
	// engine must re-exec after UID drop without traversing Go's private build dir.
	engine := filepath.Join(root, "engine")
	if err := os.WriteFile(engine, bytes, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), engine, "__owned_plugin_engine", program, scratch, allowed, filepath.Join(scratch, "written"), secret, filepath.Join(pluginDir, "planted"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("owned child: %v: %s", err, out)
	}
	var got childResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("child output %q: %v", out, err)
	}
	if !got.ReadAllowed || !got.WroteScratch || !got.DeniedSecret || !got.DeniedPluginWrite || !got.NoNewPrivs {
		t.Fatalf("owned child policy: %+v; want allowed read/scratch write and denied secret/plugin write", got)
	}
	t.Logf("owned SDK child binary_sha256=%x proof=%+v", sha256.Sum256(bytes), got)
	if os.Geteuid() == 0 {
		if got.UID == 0 || got.ReadExecutable || len(got.Groups) != 0 {
			t.Fatalf("privileged engine lost execute-only/UID/group compatibility: %+v", got)
		}
	} else if got.UID != os.Geteuid() {
		t.Fatalf("unprivileged engine changed child UID: %+v", got)
	}
}

// The mount layer that hides the engine user's user manager from a plugin sits in front of
// the helper's arguments. The jail must still be recognized as the jail: the loader checks a
// pinned digest against the plugin's own path only for a wrapped command (go-plugin would
// hash the helper), and ConfirmLaunch attests Landlock and no_new_privs from it.
func TestPluginJailWithTheUserBusHiddenIsStillAJail(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a root engine's plugin runs as another uid, which root's runtime directory refuses; no layer is added")
	}
	if coreconfine.Probe().Mode != coreconfine.ModeLandlock {
		t.Skip(coreconfine.Probe().Reason)
	}
	runtimeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtimeDir, "bus"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	att, cleanup, err := Apply(cmd, Default("bus-hidden"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer CloseSpawnFD(cmd)
	if coreconfine.UserBusExposed(cmd) {
		t.Skipf("this host gives the jail no user and mount namespace to hide the bus in: %q", att.Degraded)
	}
	if !coreconfine.IsWrapped(cmd) {
		t.Fatalf("the jail's command %q is not recognized as wrapped once the user bus is hidden", cmd.Args)
	}
	if got := ConfirmLaunch(cmd, att); !got.Landlock || !got.NoNewPrivs {
		t.Fatalf("attestation after launch: %+v; want Landlock and no_new_privs", got)
	}
}
