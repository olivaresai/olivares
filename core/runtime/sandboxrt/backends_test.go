// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeCmd is a deterministic cmdRunner for the OS-level backends: it captures the
// generated spec/config (so a test asserts the hardened shape without the real
// runsc/firecracker binary), simulates the guest result, and records every
// invocation (so a test can assert no secret reaches argv) — mirroring executor's
// fakeRunner pattern.
type fakeCmd struct {
	lookErr     error
	versionExit int
	runStdout   string // gVisor: the guest harness stdout
	runExit     int
	stateExit   int    // gVisor: `runsc state` exit (non-zero ⇒ gone ⇒ verified)
	fcResult    string // firecracker: result.json the guest "writes" to the scratch

	capturedConfig []byte
	calls          [][]string
}

func (f *fakeCmd) look(name string) (string, error) {
	if f.lookErr != nil {
		return "", f.lookErr
	}
	return "/usr/bin/" + name, nil
}

func (f *fakeCmd) run(_ context.Context, dir string, _ []string, name string, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch {
	case hasArg(args, "--version"):
		return []byte("version 1.0"), f.versionExit, nil
	case hasArg(args, "delete"):
		return nil, 0, nil
	case hasArg(args, "state"):
		return nil, f.stateExit, nil
	case hasArg(args, "run"): // gVisor run
		if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
			f.capturedConfig = b
		}
		return []byte(f.runStdout), f.runExit, nil
	case hasArg(args, "--config-file"): // firecracker boot
		if b, err := os.ReadFile(filepath.Join(dir, "vmconfig.json")); err == nil {
			f.capturedConfig = b
		}
		if f.fcResult != "" {
			_ = os.WriteFile(filepath.Join(dir, "result.json"), []byte(f.fcResult), 0o600)
		}
		return nil, f.runExit, nil
	default: // jailer kill, etc.
		return nil, 0, nil
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// anyCallHasArg reports whether any recorded invocation carried the exact arg.
func anyCallHasArg(f *fakeCmd, want string) bool {
	for _, call := range f.calls {
		if hasArg(call, want) {
			return true
		}
	}
	return false
}

const harnessOK = `{"steps":[{"key":"s1","output":"ROWS","mock_hit":true},{"key":"s2","output":"[[mock-miss:absent]]","mock_hit":false}]}`

// TestGVisorPreflightFailsClosed proves preflight fails when runsc is absent or
// non-functional (the deny-closed capability gate).
func TestGVisorPreflightFailsClosed(t *testing.T) {
	missing := newGVisorBackendWith(GVisorConfig{}, &fakeCmd{lookErr: errors.New("not found")})
	if err := missing.Preflight(context.Background()); err == nil {
		t.Fatal("preflight passed with runsc absent")
	}
	broken := newGVisorBackendWith(GVisorConfig{}, &fakeCmd{versionExit: 1})
	if err := broken.Preflight(context.Background()); err == nil {
		t.Fatal("preflight passed with runsc --version failing")
	}
	ok := newGVisorBackendWith(GVisorConfig{}, &fakeCmd{versionExit: 0})
	if err := ok.Preflight(context.Background()); err != nil {
		t.Fatalf("preflight failed with a working runsc: %v", err)
	}
}

// TestGVisorExecuteBuildsHardenedBundleAndVerifiesDestroy proves Execute builds a
// hardened OCI bundle, parses the guest result, and verifies destruction.
func TestGVisorExecuteBuildsHardenedBundleAndVerifiesDestroy(t *testing.T) {
	rootfs := t.TempDir()
	fc := &fakeCmd{runStdout: harnessOK, stateExit: 1 /* gone */}
	b := newGVisorBackendWith(GVisorConfig{RootfsDir: rootfs, BundleRoot: t.TempDir()}, fc)

	res, err := b.Execute(context.Background(), Job{RunID: "scn-1",
		Steps: []Step{{Key: "s1", Input: "db"}, {Key: "s2", Input: "absent"}},
		Mocks: []Mock{{Resource: "db", Response: "ROWS"}},
	}, HardenedProfile(), "127.0.0.1:9")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Steps) != 2 || res.Steps[0].Output != "ROWS" || !res.Destroyed || !res.DestroyVerified {
		t.Fatalf("unexpected result: %+v", res)
	}
	// The captured config.json is the hardened OCI spec.
	var spec ociSpec
	if err := json.Unmarshal(fc.capturedConfig, &spec); err != nil {
		t.Fatalf("captured config not OCI json: %v (raw=%s)", err, fc.capturedConfig)
	}
	if spec.Root.Path != rootfs || !spec.Root.Readonly {
		t.Fatalf("root not read-only/operator-rootfs: %+v", spec.Root)
	}
	if !spec.Process.NoNewPrivileges || spec.Process.User.UID == 0 {
		t.Fatalf("process not hardened: %+v", spec.Process)
	}
	if !hasNamespace(spec.Linux.Namespaces, "network") {
		t.Fatal("instance has no network namespace")
	}
	var sawJobMount bool
	for _, m := range spec.Mounts {
		if m.Destination == guestJobPath {
			sawJobMount = true
			if !hasOpt(m.Options, "ro") {
				t.Fatalf("job mount not read-only: %+v", m)
			}
		}
	}
	if !sawJobMount {
		t.Fatal("job spec not bind-mounted into the instance")
	}
}

// TestGVisorNetworkModeAndNIC proves a synthetic (deny-all) run uses --network=none
// with no NIC, a red-team run with a configured Network uses that mode and reports
// a NIC, and a red-team run WITHOUT a configured Network stays --network=none
// (honestly failing to reach the target rather than opening an un-routed network).
func TestGVisorNetworkModeAndNIC(t *testing.T) {
	rootfs := t.TempDir()
	rt := Job{RunID: "rt", Target: "http://10.1.2.3/", Probe: &Probe{ID: "p"},
		Egress: EgressPolicy{Allow: []EgressRule{{Host: "10.1.2.3"}}}}

	// Synthetic deny-all ⇒ --network=none, no NIC.
	syn := &fakeCmd{runStdout: harnessOK, stateExit: 1}
	bs := newGVisorBackendWith(GVisorConfig{RootfsDir: rootfs, BundleRoot: t.TempDir()}, syn)
	rsyn, _ := bs.Execute(context.Background(), Job{RunID: "s"}, HardenedProfile(), "127.0.0.1:9")
	if rsyn.HadNIC || !anyCallHasArg(syn, "--network=none") {
		t.Fatalf("synthetic run not --network=none/no-NIC (HadNIC=%v)", rsyn.HadNIC)
	}

	// Red-team WITH a configured network mode ⇒ that mode + a NIC.
	cfgd := &fakeCmd{runStdout: harnessOK, stateExit: 1}
	bc := newGVisorBackendWith(GVisorConfig{RootfsDir: rootfs, BundleRoot: t.TempDir(), Network: "sandbox"}, cfgd)
	rc, _ := bc.Execute(context.Background(), rt, HardenedProfile(), "127.0.0.1:9")
	if !rc.HadNIC || !anyCallHasArg(cfgd, "--network=sandbox") {
		t.Fatalf("configured red-team run not --network=sandbox/NIC (HadNIC=%v)", rc.HadNIC)
	}

	// Red-team WITHOUT a configured network mode ⇒ stays none (fails to reach).
	unc := &fakeCmd{runStdout: harnessOK, stateExit: 1}
	bu := newGVisorBackendWith(GVisorConfig{RootfsDir: rootfs, BundleRoot: t.TempDir()}, unc)
	ru, _ := bu.Execute(context.Background(), rt, HardenedProfile(), "127.0.0.1:9")
	if ru.HadNIC || !anyCallHasArg(unc, "--network=none") {
		t.Fatalf("unconfigured red-team run opened a network (HadNIC=%v)", ru.HadNIC)
	}
}

// TestFirecrackerCleansInstanceDir proves every run leaves no instance state on
// disk (the on-disk job spec carrying the probe payload never leaks).
func TestFirecrackerCleansInstanceDir(t *testing.T) {
	base := t.TempDir()
	kernel := writeTemp(t, "vmlinux")
	rootfs := writeTemp(t, "rootfs.ext4")
	b := newFirecrackerBackendWith(FirecrackerConfig{KernelImage: kernel, RootfsImage: rootfs, ChrootBase: base}, &fakeCmd{fcResult: harnessOK})
	if _, err := b.Execute(context.Background(), Job{RunID: "x"}, HardenedProfile(), ""); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("instance dir leaked under ChrootBase: %v", entries)
	}
}

// TestGVisorFailsClosedWithoutRootfs proves the backend refuses to run with no
// operator-provisioned rootfs.
func TestGVisorFailsClosedWithoutRootfs(t *testing.T) {
	b := newGVisorBackendWith(GVisorConfig{BundleRoot: t.TempDir()}, &fakeCmd{runStdout: harnessOK})
	if _, err := b.Execute(context.Background(), Job{RunID: "x"}, HardenedProfile(), ""); err == nil {
		t.Fatal("execute passed with no RootfsDir")
	}
}

// TestGVisorUnverifiedDestroyWhenStillResolvable proves destruction is NOT marked
// verified when `runsc state` still resolves the instance.
func TestGVisorUnverifiedDestroyWhenStillResolvable(t *testing.T) {
	fc := &fakeCmd{runStdout: harnessOK, stateExit: 0 /* still present */}
	b := newGVisorBackendWith(GVisorConfig{RootfsDir: t.TempDir(), BundleRoot: t.TempDir()}, fc)
	res, err := b.Execute(context.Background(), Job{RunID: "y"}, HardenedProfile(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.DestroyVerified {
		t.Fatal("destroy marked verified although state still resolves the instance")
	}
}

// TestFirecrackerExecuteBuildsHardenedVMAndScopesNIC proves the microVM config has
// a read-only rootfs drive, no NIC for a deny-all job, a tap only for a scoped
// red-team job, and that destruction is verified.
func TestFirecrackerExecuteBuildsHardenedVMAndScopesNIC(t *testing.T) {
	kernel := writeTemp(t, "vmlinux")
	rootfs := writeTemp(t, "rootfs.ext4")
	base := t.TempDir()

	// Deny-all synthetic job ⇒ no network interface.
	fc := &fakeCmd{fcResult: harnessOK}
	b := newFirecrackerBackendWith(FirecrackerConfig{KernelImage: kernel, RootfsImage: rootfs, ChrootBase: base}, fc)
	res, err := b.Execute(context.Background(), Job{RunID: "scn"}, HardenedProfile(), "")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.Destroyed || !res.DestroyVerified || len(res.Steps) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	var cfg fcConfig
	if err := json.Unmarshal(fc.capturedConfig, &cfg); err != nil {
		t.Fatalf("captured vm config not json: %v", err)
	}
	if len(cfg.Drives) == 0 || !cfg.Drives[0].IsReadOnly || !cfg.Drives[0].IsRootDevice {
		t.Fatalf("rootfs drive not read-only root: %+v", cfg.Drives)
	}
	if len(cfg.NetworkIfaces) != 0 {
		t.Fatalf("deny-all microVM has a NIC: %+v", cfg.NetworkIfaces)
	}

	// Scoped red-team job ⇒ exactly one tap (host-restricted to the proxy).
	fc2 := &fakeCmd{fcResult: harnessOK}
	b2 := newFirecrackerBackendWith(FirecrackerConfig{KernelImage: kernel, RootfsImage: rootfs, ChrootBase: t.TempDir()}, fc2)
	_, err = b2.Execute(context.Background(), Job{
		RunID: "rt", Target: "http://10.1.2.3:8080/",
		Probe:  &Probe{ID: "inj-01", Payload: "x"},
		Egress: EgressPolicy{Allow: []EgressRule{{Host: "10.1.2.3", Ports: []int{8080}}}},
	}, HardenedProfile(), "127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	var cfg2 fcConfig
	_ = json.Unmarshal(fc2.capturedConfig, &cfg2)
	if len(cfg2.NetworkIfaces) != 1 {
		t.Fatalf("scoped red-team microVM NICs = %d, want 1", len(cfg2.NetworkIfaces))
	}
}

// TestFirecrackerPreflightFailsClosed proves preflight fails when firecracker is
// absent. (KVM presence is host-dependent; when /dev/kvm is absent it also fails,
// which is asserted opportunistically.)
func TestFirecrackerPreflightFailsClosed(t *testing.T) {
	missing := newFirecrackerBackendWith(FirecrackerConfig{}, &fakeCmd{lookErr: errors.New("not found")})
	if err := missing.Preflight(context.Background()); err == nil {
		t.Fatal("preflight passed with firecracker absent")
	}
	if _, statErr := os.Stat("/dev/kvm"); statErr != nil {
		// No KVM on this host: preflight must fail even with the binary present.
		present := newFirecrackerBackendWith(FirecrackerConfig{}, &fakeCmd{versionExit: 0})
		if err := present.Preflight(context.Background()); err == nil {
			t.Fatal("preflight passed without /dev/kvm")
		}
	}
}

// TestFirecrackerFailsClosedWithoutImages proves the backend refuses to run with
// no operator-provisioned kernel/rootfs.
func TestFirecrackerFailsClosedWithoutImages(t *testing.T) {
	b := newFirecrackerBackendWith(FirecrackerConfig{ChrootBase: t.TempDir()}, &fakeCmd{fcResult: harnessOK})
	if _, err := b.Execute(context.Background(), Job{RunID: "x"}, HardenedProfile(), ""); err == nil {
		t.Fatal("execute passed with no kernel/rootfs images")
	}
}

func writeTemp(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
