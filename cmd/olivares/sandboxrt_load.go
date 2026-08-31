// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/olivaresai/olivares/core/runtime/sandboxrt"
)

// This file loads the isolated-execution runtime from the operator-provisioned
// config (OLIVARES_SANDBOX_RUNTIME_CONFIG) and assembles the real sandboxrt
// engine. It mirrors loadDeployExecutorConfig: an absent path yields NO engine
// (the modules keep their honest defaults — the in-proc-mock runner / the offline
// sandbox), while a supplied unreadable/invalid file fails startup. When backends ARE
// configured, the engine is wired even if the host primitive turns out absent: it then
// fails closed per run (ErrNoIsolation), exactly as the deploy executor errors per
// operation when its backend is unreachable — an explicit operator request for
// OS-level isolation is honored or fails LOUDLY, never silently downgraded to the
// synthetic in-proc runner. Operator paths (binaries, rootfs/kernel images) live
// ONLY here, never in the module store.

// sandboxRuntimeConfig is the operator's isolated-runtime provisioning. Each
// backend block is OPTIONAL; only the configured backends are wired (selection by
// policy, never hardcoded). Default names the policy-primary backend.
type sandboxRuntimeConfig struct {
	Default     string         `json:"default,omitempty"`
	GVisor      *gvisorCfgJSON `json:"gvisor,omitempty"`
	Firecracker *fcCfgJSON     `json:"firecracker,omitempty"`
}

type gvisorCfgJSON struct {
	Binary      string `json:"binary"`
	StateRoot   string `json:"state_root"`
	BundleRoot  string `json:"bundle_root"`
	RootfsDir   string `json:"rootfs_dir"`
	HarnessPath string `json:"harness_path"`
	Platform    string `json:"platform"`
	Network     string `json:"network"`
	TimeoutSecs int    `json:"timeout_seconds"`
}

type fcCfgJSON struct {
	Binary      string `json:"binary"`
	Jailer      string `json:"jailer"`
	ChrootBase  string `json:"chroot_base"`
	KernelImage string `json:"kernel_image"`
	RootfsImage string `json:"rootfs_image"`
	HarnessPath string `json:"harness_path"`
	VcpuCount   int    `json:"vcpu_count"`
	MemSizeMib  int    `json:"mem_size_mib"`
	TimeoutSecs int    `json:"timeout_seconds"`
}

// loadSandboxRuntimeConfig reads OLIVARES_SANDBOX_RUNTIME_CONFIG. A missing path
// is an empty config (no runtime wired; the modules keep safe defaults). A supplied
// path must be readable and contain valid JSON or startup fails closed.
func loadSandboxRuntimeConfig(_ *slog.Logger) (sandboxRuntimeConfig, error) {
	path := os.Getenv("OLIVARES_SANDBOX_RUNTIME_CONFIG")
	if path == "" {
		return sandboxRuntimeConfig{}, nil
	}
	var cfg sandboxRuntimeConfig
	if err := loadOperatorJSONConfig("OLIVARES_SANDBOX_RUNTIME_CONFIG", path, &cfg); err != nil {
		return sandboxRuntimeConfig{}, err
	}
	return cfg, nil
}

// newSandboxRuntime builds the sandboxrt engine from config, or nil when NO
// backend is configured (the modules then keep the in-proc-mock runner / offline
// sandbox). Backends are registered in POLICY order: Default first, then the rest
// in declaration order (gVisor, Firecracker).
func newSandboxRuntime(cfg sandboxRuntimeConfig, log *slog.Logger) *sandboxrt.Engine {
	type back struct {
		name string
		b    sandboxrt.Backend
	}
	var backs []back
	if cfg.GVisor != nil {
		backs = append(backs, back{name: "gvisor", b: sandboxrt.NewGVisorBackend(cfg.GVisor.to())})
	}
	if cfg.Firecracker != nil {
		backs = append(backs, back{name: "firecracker", b: sandboxrt.NewFirecrackerBackend(cfg.Firecracker.to())})
	}
	if len(backs) == 0 {
		return nil
	}
	// Order by policy: the configured Default goes first; the rest keep order.
	def := strings.ToLower(strings.TrimSpace(cfg.Default))
	opts := []sandboxrt.Option{sandboxrt.WithLogger(log)}
	if def != "" {
		for _, bk := range backs {
			if bk.name == def {
				opts = append(opts, sandboxrt.WithBackend(bk.b))
			}
		}
	}
	for _, bk := range backs {
		if def != "" && bk.name == def {
			continue
		}
		opts = append(opts, sandboxrt.WithBackend(bk.b))
	}
	names := make([]string, 0, len(backs))
	for _, bk := range backs {
		names = append(names, bk.name)
	}
	log.Info("sandbox-runtime: isolated execution runtime wired (XVII/XVIII now ACT)", "backends", strings.Join(names, ","), "default", def)
	return sandboxrt.New(opts...)
}

func (c gvisorCfgJSON) to() sandboxrt.GVisorConfig {
	return sandboxrt.GVisorConfig{
		Binary: c.Binary, StateRoot: c.StateRoot, BundleRoot: c.BundleRoot, RootfsDir: c.RootfsDir,
		HarnessPath: c.HarnessPath, Platform: c.Platform, Network: c.Network, Timeout: secs(c.TimeoutSecs),
	}
}

func (c fcCfgJSON) to() sandboxrt.FirecrackerConfig {
	return sandboxrt.FirecrackerConfig{
		Binary: c.Binary, Jailer: c.Jailer, ChrootBase: c.ChrootBase, KernelImage: c.KernelImage,
		RootfsImage: c.RootfsImage, HarnessPath: c.HarnessPath, VcpuCount: c.VcpuCount,
		MemSizeMib: c.MemSizeMib, Timeout: secs(c.TimeoutSecs),
	}
}
