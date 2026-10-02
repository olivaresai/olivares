// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// Probe the actual executable under this launch's OS policy. No session bearer,
// injected secret, MCP configuration or inherited account home reaches the probe.
// Other runners keep the native sandbox: absence of a probe never disables it.
func (m *Module) prepareCodexSandbox(ctx context.Context, p *CreateRunParams, spec *LaunchSpec) error {
	if launchDriverKey(*p) != providerDriverCodex || runtime.GOOS != "linux" || launchPreset(*p) == PresetFull {
		return nil
	}
	runner, native := m.rt.runner.(*procRunner)
	if !native {
		return nil
	}
	probe := *spec
	probe.Args = []string{"-c", "check_for_update_on_startup=false", "-c", `sandbox_mode="read-only"`, "sandbox", "linux", "--", "/bin/true"}
	probe.Env, probe.EnvAllow = nil, nil
	for _, item := range spec.Env {
		if item.Name == envUserHome || item.Name == envCodexHome {
			probe.Env = append(probe.Env, item)
		}
	}
	probe.WaitDelay = time.Second
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	proc, err := runner.Launch(probeCtx, probe)
	if proc == nil {
		return launchFailedErr("Codex sandbox check failed", err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range proc.Output() {
			// Probe diagnostics are lower-trust data, never logs or public output.
		}
	}()
	code, waitErr := proc.Wait()
	stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	stopErr := proc.Stop(stopCtx)
	stopCancel()
	if !childWasReaped(waitErr) || !childWasReaped(stopErr) {
		return &runErr{http.StatusBadGateway, "Codex sandbox check could not stop its process; the session was not started"}
	}
	<-drained
	if err != nil || waitErr != nil || stopErr != nil || probeCtx.Err() != nil {
		return &runErr{http.StatusBadGateway, "Codex sandbox check did not finish reliably; the session was not started"}
	}
	if code == 0 {
		return nil
	}
	if code < 0 {
		return &runErr{http.StatusBadGateway, "Codex sandbox check was interrupted; the session was not started"}
	}
	state, known := confinementOf(proc)
	if !known || state.Mode != confine.ModeLandlock || spec.Confinement == nil {
		return &runErr{http.StatusBadGateway, "Codex's native sandbox is unavailable and this server cannot enforce session confinement"}
	}
	if launchPreset(*p) == PresetReadOnly {
		return &runErr{http.StatusBadGateway, "Codex's native read-only sandbox is unavailable; the session was not started"}
	}
	// A positive native failure may fall back only while OS confinement is
	// required. Preserve the preset and live approval policy at the driver seam.
	p.codexSandboxFallback = true
	spec.ConfinementRequired = true
	spec.Args = append([]string{"-c", `sandbox_mode="danger-full-access"`}, spec.Args...)
	return nil
}

func codexSandboxDetail(p CreateRunParams) string {
	if p.codexSandboxFallback {
		return "Codex native sandbox unavailable; OS confinement enforced; network not confined"
	}
	return ""
}
