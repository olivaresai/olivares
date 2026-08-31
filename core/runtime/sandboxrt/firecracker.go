// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// firecrackerKind is the backend's policy selector and attestation runner name.
const firecrackerKind = "firecracker"

// FirecrackerConfig configures the Firecracker microVM backend (operator-
// provisioned; no secrets). A microVM is the STRONGEST isolation tier (README.md
// §3): a hardware-virtualized guest with its own kernel, run multi-tenant behind
// the jailer (a dedicated uid/gid + chroot + cgroup + seccomp). It requires KVM.
type FirecrackerConfig struct {
	// Binary is the firecracker executable (default "firecracker").
	Binary string
	// Jailer is the firecracker jailer executable (default "jailer"); when present
	// the VM is launched inside its chroot/cgroup/uid-gid jail (multi-tenant).
	Jailer string
	// ChrootBase is the jailer chroot base (default a temp subdir).
	ChrootBase string
	// KernelImage is the operator-provisioned guest kernel (vmlinux). Absent ⇒ the
	// backend cannot run (fail-closed at Execute).
	KernelImage string
	// RootfsImage is the operator-provisioned, READ-ONLY guest root filesystem that
	// contains the guest harness (an ext4 image). Absent ⇒ fail-closed.
	RootfsImage string
	// HarnessPath is the in-guest harness entrypoint (default "/sandbox-harness").
	HarnessPath string
	// VcpuCount / MemSizeMib bound the microVM (defaults 1 vCPU / 256 MiB).
	VcpuCount  int
	MemSizeMib int
	// Timeout bounds a single firecracker invocation (default 60s).
	Timeout time.Duration
}

// firecrackerBackend is the microVM-backed isolation backend.
type firecrackerBackend struct {
	cfg    FirecrackerConfig
	runner cmdRunner
}

var _ Backend = (*firecrackerBackend)(nil)

// NewFirecrackerBackend builds the Firecracker backend with production defaults
// and the os/exec runner. It returns the Backend seam (concrete type internal).
func NewFirecrackerBackend(cfg FirecrackerConfig) Backend {
	return newFirecrackerBackendWith(cfg, execRunner{timeout: firecrackerTimeout(cfg)})
}

// newFirecrackerBackendWith is the testable constructor (injected cmdRunner).
func newFirecrackerBackendWith(cfg FirecrackerConfig, runner cmdRunner) *firecrackerBackend {
	if cfg.Binary == "" {
		cfg.Binary = "firecracker"
	}
	if cfg.Jailer == "" {
		cfg.Jailer = "jailer"
	}
	if cfg.HarnessPath == "" {
		cfg.HarnessPath = "/sandbox-harness"
	}
	if cfg.VcpuCount <= 0 {
		cfg.VcpuCount = 1
	}
	if cfg.MemSizeMib <= 0 {
		cfg.MemSizeMib = 256
	}
	if cfg.ChrootBase == "" {
		cfg.ChrootBase = filepath.Join(os.TempDir(), "olivares-sandboxrt", "firecracker")
	}
	return &firecrackerBackend{cfg: cfg, runner: runner}
}

func firecrackerTimeout(cfg FirecrackerConfig) time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return 60 * time.Second
}

// Name is the backend's selector.
func (b *firecrackerBackend) Name() string { return firecrackerKind }

// Isolated reports the microVM's real OS-level isolation guarantee.
func (b *firecrackerBackend) Isolated() bool { return true }

// Preflight verifies firecracker is present AND that /dev/kvm exists (the microVM
// needs hardware virtualization). Without KVM the backend fails closed — never a
// faked microVM on a host that cannot run one.
func (b *firecrackerBackend) Preflight(ctx context.Context) error {
	if _, err := b.runner.look(b.cfg.Binary); err != nil {
		return fmt.Errorf("firecracker binary %q not found on PATH", b.cfg.Binary)
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return fmt.Errorf("/dev/kvm not available (no hardware virtualization): %w", err)
	}
	_, code, err := b.runner.run(ctx, "", os.Environ(), b.cfg.Binary, "--version")
	if err != nil {
		return fmt.Errorf("firecracker not executable: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("firecracker --version exited %d", code)
	}
	return nil
}

// fcConfig is the subset of the Firecracker VM config the backend writes. The
// hardened shape: a READ-ONLY rootfs drive, a bounded read-write scratch drive
// (the guest's only writable surface — the result is written here), and (for a
// deny-all job) NO network interface; a red-team job adds a tap restricted by the
// host to the egress proxy. SMT is off; the balloon/secret devices are absent.
type fcConfig struct {
	BootSource    fcBootSource    `json:"boot-source"`
	Drives        []fcDrive       `json:"drives"`
	MachineConfig fcMachineConfig `json:"machine-config"`
	NetworkIfaces []fcNetIface    `json:"network-interfaces,omitempty"`
}

type fcBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type fcDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type fcMachineConfig struct {
	VcpuCount  int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	Smt        bool `json:"smt"`
}

type fcNetIface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
}

// Execute boots a fresh microVM running the guest harness, reads the result the
// guest writes to the read-write scratch drive, then KILLS the VM and VERIFIES
// its jail/state is gone. The job spec is delivered to the guest via the scratch
// drive; the harness's egress is forced through the proxy (boot args).
func (b *firecrackerBackend) Execute(ctx context.Context, job Job, profile Profile, proxyAddr string) (BackendResult, error) {
	if strings.TrimSpace(b.cfg.KernelImage) == "" || strings.TrimSpace(b.cfg.RootfsImage) == "" {
		return BackendResult{}, fmt.Errorf("sandboxrt: firecracker backend has no KernelImage/RootfsImage provisioned (fail-closed)")
	}
	id := newInstanceID(firecrackerKind, job.RunID)
	instDir := filepath.Join(b.cfg.ChrootBase, id)
	if err := os.MkdirAll(instDir, 0o700); err != nil {
		return BackendResult{}, fmt.Errorf("sandboxrt: cannot create instance dir: %w", err)
	}
	// Safety net: every return path — including the pre-boot error paths below —
	// removes the instance dir, so the on-disk job spec (which carries the probe
	// payload) never leaks past the run (the explicit destroy() below also removes
	// + verifies it; this defer is a no-op after that).
	defer func() { _ = os.RemoveAll(instDir) }()

	// 1) Write the guest-harness job spec (delivered via the scratch drive).
	jobBytes, err := encodeHarnessJob(job, proxyAddr)
	if err != nil {
		return BackendResult{InstanceID: id}, err
	}
	jobPath := filepath.Join(instDir, "job.json")
	if err := os.WriteFile(jobPath, jobBytes, 0o600); err != nil {
		return BackendResult{InstanceID: id}, fmt.Errorf("sandboxrt: cannot write job spec: %w", err)
	}

	// 2) Build the hardened VM config. The guest writes its result to result.json
	// on the host-visible scratch path; egress (red-team only) is proxy-forced.
	cfgPath := filepath.Join(instDir, "vmconfig.json")
	needNet := !job.Egress.denyAll()
	bootArgs := b.bootArgs(profile, proxyAddr, needNet)
	vmCfg := fcConfig{
		BootSource: fcBootSource{KernelImagePath: b.cfg.KernelImage, BootArgs: bootArgs},
		Drives: []fcDrive{
			{DriveID: "rootfs", PathOnHost: b.cfg.RootfsImage, IsRootDevice: true, IsReadOnly: true},
			{DriveID: "scratch", PathOnHost: jobPath, IsRootDevice: false, IsReadOnly: false},
		},
		MachineConfig: fcMachineConfig{VcpuCount: b.cfg.VcpuCount, MemSizeMib: b.cfg.MemSizeMib, Smt: false},
	}
	if needNet {
		// A red-team job gets a tap the HOST restricts to the egress proxy (the
		// only route out); the guest still has no general-purpose network.
		vmCfg.NetworkIfaces = []fcNetIface{{IfaceID: "eth0", HostDevName: "fc-" + sanitizeID(id)}}
	}
	cfgBytes, err := json.MarshalIndent(vmCfg, "", "  ")
	if err != nil {
		return BackendResult{InstanceID: id}, err
	}
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		return BackendResult{InstanceID: id}, fmt.Errorf("sandboxrt: cannot write vm config: %w", err)
	}

	// 3) Boot the microVM (no API server; config-driven, blocking). The guest
	// harness runs, writes result.json to the scratch drive, and the VM exits.
	_, code, runErr := b.runner.run(ctx, instDir, os.Environ(),
		b.cfg.Binary, "--no-api", "--id", id, "--config-file", cfgPath)
	if runErr != nil {
		d, v := b.destroy(id, instDir)
		return BackendResult{InstanceID: id, HadNIC: needNet, Destroyed: d, DestroyVerified: v}, runErr
	}
	if code != 0 {
		d, v := b.destroy(id, instDir)
		return BackendResult{InstanceID: id, HadNIC: needNet, Destroyed: d, DestroyVerified: v},
			fmt.Errorf("sandboxrt: firecracker exited %d", code)
	}

	// 4) Read the result the guest wrote to the scratch drive (BEFORE destruction).
	resultBytes, rerr := os.ReadFile(filepath.Join(instDir, "result.json"))
	if rerr != nil {
		d, v := b.destroy(id, instDir)
		return BackendResult{InstanceID: id, HadNIC: needNet, Destroyed: d, DestroyVerified: v},
			fmt.Errorf("sandboxrt: microVM produced no result")
	}
	steps, response, reached, perr := decodeHarnessResult(resultBytes)

	// 5) Destroy the instance and VERIFY it is gone, then return.
	destroyed, verified := b.destroy(id, instDir)
	if perr != nil {
		return BackendResult{InstanceID: id, HadNIC: needNet, Destroyed: destroyed, DestroyVerified: verified}, perr
	}
	return BackendResult{
		Steps: steps, Response: response, Reached: reached,
		InstanceID: id, HadNIC: needNet, Destroyed: destroyed, DestroyVerified: verified,
	}, nil
}

// bootArgs builds the guest kernel boot args: a quiet console, the harness as
// init reading the scratch job spec, and the proxy address when egress is needed.
func (b *firecrackerBackend) bootArgs(_ Profile, proxyAddr string, needNet bool) string {
	args := []string{
		"console=ttyS0", "reboot=k", "panic=1", "pci=off",
		"init=" + b.cfg.HarnessPath,
		"sandbox.job=/dev/vdb", "sandbox.result=/dev/vdb",
	}
	if needNet && proxyAddr != "" {
		args = append(args, "sandbox.proxy=http://"+proxyAddr)
	}
	return strings.Join(args, " ")
}

// destroy kills the microVM (a best-effort guard for a hung guest — the
// config-file boot is foreground, so a returned run already exited), removes ALL
// instance state, and VERIFIES it is gone: the instance directory must no longer
// exist. destroyed reflects the ACTUAL removal (not assumed); a still-present
// directory means the ephemeral guarantee is NOT confirmed. It uses a FRESH
// context so a canceled/timed-out run does not also abort cleanup.
func (b *firecrackerBackend) destroy(id, instDir string) (destroyed, verified bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _, _ = b.runner.run(ctx, "", os.Environ(), b.cfg.Jailer, "--id", id, "--", "--kill")
	removeErr := os.RemoveAll(instDir)
	destroyed = removeErr == nil
	if _, err := os.Stat(instDir); err == nil {
		return destroyed, false
	}
	return destroyed, true
}
