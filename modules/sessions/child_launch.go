// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// THE CHILD LAUNCH: what a session's child process receives.
//
// Create and resume resolve a launch decision (childDecision) and hand it here.
// childSpec turns it into the exact child: program and argv, folder, explicit
// environment, filesystem policy and network policy. launchChild then lets the
// launch-time contributions add theirs, in one order — the repository read
// credential (session_git_read.go), the Codex sandbox probe
// (driver_codex_sandbox_probe.go), Claude's tool hooks (claude_hook_launch.go) and
// the session MCP endpoint (session_mcp_launch.go) — and starts the child on the
// runner. The runner adds only what exists once the process does: the session's
// TMPDIR, a HOME for a child that names none, and the inherited safe base
// (sanitizedEnv). Which source may name which variable is child_env.go.

// childDecision is what a launch resolved before the spawn, and nothing else.
type childDecision struct {
	// cred is the Claude path's inference credential and, for a record-bound
	// launch, the provider it is bound to; a registered driver takes its local
	// model endpoint and models from it.
	cred Credential
	// work and communication are the runtime's own grants to the child.
	work          WorkSessionCredential
	communication CommunicationSessionCredential
	// resumeID is the Claude conversation a resume continues; empty on create.
	resumeID string
	// ws is the resolved workspace; nil when the run has none.
	ws *resolvedWorkspace
	// gateEnv is the launch gate's grant, already checked (validateGateEnv).
	gateEnv []EnvVar
	// providerEnv is a registered driver's governed managed credential; empty
	// for every other source.
	providerEnv []EnvVar
}

// launchChild builds the child, lets the launch-time contributions add theirs and
// starts it. stage names the step that failed, for the refusal the caller writes;
// the spec is returned whatever happened, as the runner may hand back a process
// together with an error.
func (m *Module) launchChild(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, d childDecision) (LaunchSpec, Process, string, error) {
	spec := m.childSpec(*p, d)
	if stage, err := m.prepareSessionLaunch(ctx, runCtx, tenant, runRef, p, &spec); err != nil {
		return spec, nil, stage, err
	}
	proc, err := m.rt.Runner.Launch(runCtx, spec)
	return spec, proc, "tool process launch", err
}

// claudeBoundProviderEnv holds a Claude Code session on a key from Providers to that key's
// endpoint and keeps it quiet, with
// Claude Code's own documented switches (code.claude.com/docs/en/env-vars):
//   - CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST: the endpoint and key the launch sets win over any
//     settings file, and managed model pins cannot re-route it;
//   - CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: no telemetry (real use saw Claude Code's
//     log intake), error reporting, release notes or feature-flag fetches;
//   - CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL: the plugin marketplace install
//     the previous switch does not cover.
//
// The endpoint itself is ANTHROPIC_BASE_URL from the record mint. Only a record-bound launch
// gets these: a session on the person's own sign-in keeps its own settings, and Remote
// Control (never record-bound) needs the feature flags.
var claudeBoundProviderEnv = []EnvVar{
	{Name: "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", Value: "1"},
	{Name: "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", Value: "1"},
	{Name: "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL", Value: "1"},
}

// claudeSettingsFlag is Claude Code's flag for settings that outrank the user's and the
// project's (a file path or inline JSON).
const claudeSettingsFlag = "--settings"

// claudeBoundSettings repeats the two quiet switches as flag settings. Claude Code applies
// a saved user or project settings env over the launch environment, so a saved
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="" would turn the traffic back on
// (5 requests to api.anthropic.com came back on Claude Code 2.1.288). Flag
// settings outrank both, and with them those requests stay at 0. The host pin needs no
// repeat: Claude Code already ignores settings files for routing under it.
var claudeBoundSettings = `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1","CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL":"1"}}`

// childSpec constructs the child from a launch decision: the argv (a proper
// []string — never a shell-split string), the EXPLICIT env (the minted inference
// token by value, used and discarded; optional gateway base URL; the governance PEP
// env), the RESOLVED workspace and the filesystem and network policy. It starts no
// process and writes nothing.
//
// It has TWO shapes, chosen by the run's driver, and the split is deliberate:
//
//   - the historical Claude path, unchanged to the byte. Its argv, its
//     ANTHROPIC_* bearer, its gateway base URL and its updater pin all belong to
//     that CLI and travel with it;
//   - a REGISTERED driver, whose argv comes from the driver itself and which
//     receives NO Anthropic variable at all. Reusing the Claude bearer for another
//     provider would confuse two providers' credentials, and the cheapest way to
//     make it impossible is for this function to never build it.
func (m *Module) childSpec(p CreateRunParams, d childDecision) LaunchSpec {
	driverKey := launchDriverKey(p)
	drv, driven := m.driverFor(driverKey)
	dir, mount := launchWorkspaceTarget(p, d.ws)

	var args []string
	var driverLaunch DriverLaunch
	program := m.claudeProgram()
	if driven {
		// The driver owns its own official argv. The resume ID is deliberately NOT a flag
		// here: an app-server/ACP child resumes through a METHOD on the owned
		// protocol, and the correlated root response is the only thing allowed to
		// nominate the conversation.
		program = m.driverProgram(drv)
		driverLaunch = DriverLaunch{WorkDir: dir, Model: p.Model, Effort: p.Effort, Preset: launchPreset(p), CodexSandboxFallback: p.codexSandboxFallback, LocalModelEndpoint: d.cred.localModelEndpoint, LocalModels: d.cred.localModels, BoundProvider: d.cred.bound}
		if p.ProviderHome != nil {
			driverLaunch.ConfigHome = p.ProviderHome.ConfigHome
			driverLaunch.UserHome = p.ProviderHome.UserHome
		}
		args = drv.LaunchArgs(driverLaunch)
	} else {
		// The Claude argv is not built here: cliruntime declares the `--print`
		// stream-json form and its transport, and this launch path consults that one
		// table, so changing the declaration changes what the engine launches.
		//
		// The template's terms travel as argv the operator never chose. They
		// are built from the SERVER's merge (templateapply.go), so a caller who
		// skips the console and posts straight to /runs gets the same confinement;
		// cliruntime is where the flag shapes of those terms live.
		claude := cliruntime.LaunchRequest{
			WorkDir:        dir,
			Model:          p.Model,
			Effort:         p.Effort,
			PermissionMode: p.PermissionMode,
			ResumeID:       d.resumeID,
			AllowedTools:   p.AllowedTools,
			// The profile's declared tool surface (provider_profile_policy.go),
			// resolved for every launch before the child is built.
			ToolSurface:         p.ToolSurface,
			ToolSurfaceDeclared: p.ToolSurfaceDeclared,
			Instructions:        p.Instructions,
			Name:                p.Name,
		}
		if p.Transport == TransportRemoteControl {
			// Lifecycle-only: I/O is relayed to Anthropic's cloud, not bridged.
			args = cliruntime.ClaudeRemoteControlArgs(claude)
		} else {
			// The governed stream-json form, and the DEFAULT for anything else:
			// validateCreate already normalizes an empty transport to it, and a
			// transport this function does not recognize must not fall through to an
			// argv with no form flag at all — that would launch the vendor CLI
			// INTERACTIVELY under a row that claims a bridged session.
			args = cliruntime.ClaudeArgs(claude)
			if d.cred.bound.Kind != "" {
				args = append(args, claudeSettingsFlag, claudeBoundSettings)
			}
		}
	}

	var env []EnvVar
	if !driven {
		if d.cred.Token != "" {
			// Bearer precedence over a (now-stripped) ANTHROPIC_API_KEY; the WIF token is
			// short-lived and never persisted (only its id reaches the row/ledger).
			env = append(env, EnvVar{Name: "ANTHROPIC_AUTH_TOKEN", Value: d.cred.Token})
		}
		if m.rt.baseURL != "" {
			// Route the operated session's inference through Olivares' own gateway so it
			// is PEP/budget/model-governed (here it is an env-ref).
			env = append(env, EnvVar{Name: "ANTHROPIC_BASE_URL", Value: m.rt.baseURL})
		}
	}
	// The governed managed credential of THIS driver's own adapter, and
	// nothing else. Empty under provider_account_home, where the authorized home is
	// the credential and Olivares injects nothing at all.
	env = append(env, d.providerEnv...)
	// The vault secrets this launch was given, by the names validated against every
	// reserved variable (session_secret_env.go); opened for this spawn only.
	env = append(env, p.secretEnvValues...)
	if d.work.Token != "" {
		// Exact-session kernel authority. These are explicit launch values, not
		// inherited host environment, and the token is never persisted/logged.
		env = append(env,
			EnvVar{Name: envWorkToken, Value: d.work.Token},
			EnvVar{Name: envWorkSessionID, Value: d.work.SessionRef},
			EnvVar{Name: envWorkRunRef, Value: d.work.RunRef},
		)
	}
	if d.communication.Token != "" {
		// The communication bearer is deliberately separate from work authority and injected
		// exactly once. Its tuple is carried inside the authenticated principal; no
		// caller-controlled binding env is needed or accepted.
		env = append(env, EnvVar{
			Name: envCommunicationToken, Value: d.communication.Token,
		})
	}
	// A profiled launch runs under the profile's homes. Both are explicit launch
	// values — they override whatever the host process inherited — and neither is a
	// credential: the configuration home is where the provider keeps its own settings
	// and transcripts, HOME is the child's user home. LaunchSpec.Dir (the workspace) is
	// a third, unrelated path. Which VARIABLE carries the configuration home is the
	// driver's own (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, …): naming another provider's
	// variable would point a child at a home nobody selected. A caller or a gate that
	// names either variable for a profiled launch is refused before this point rather
	// than resolved by order.
	if p.ProviderHome != nil {
		configHome := p.ProviderHome.ConfigHome
		if p.ProviderHome.Driver == providerDriverGemini {
			// Gemini CLI's variable names the home that holds .gemini, not .gemini
			// itself: the parent of the configuration home the profile validated.
			configHome = filepath.Dir(p.ProviderHome.ConfigHome)
		}
		env = append(env,
			EnvVar{Name: envUserHome, Value: p.ProviderHome.UserHome},
			EnvVar{Name: m.configHomeEnvForDriver(p.ProviderHome.Driver), Value: configHome},
		)
		if p.ProviderHome.Driver == providerDriverOpenCode {
			// Runtime-owned XDG mapping beside HOME/ConfigHome. Do not set
			// XDG_RUNTIME_DIR: OC1 refuses an unqualified runtime directory.
			env = append(env, openCodeXDGMapping(p.ProviderHome.UserHome)...)
		}
	}
	if driven {
		// A registered driver may need an explicit, non-secret variable of its OWN
		// official CLI — today, the one that pins the child's version by disabling its
		// background updater, which is the same guarantee the Claude branch below gets
		// from Claude's own variable. It is optional: a driver that declares none
		// builds exactly the environment it built before. A name the profile owns is
		// dropped (driverMaySetEnv).
		if withEnv, ok := drv.(ProviderDriverLaunchEnv); ok {
			for _, item := range withEnv.LaunchEnv(driverLaunch) {
				if !driverMaySetEnv(driverKey, item.Name) {
					m.warnf("sessions: a provider driver named a variable the profile owns; it was dropped",
						"driver", driverKey, "name", item.Name)
					continue
				}
				env = append(env, item)
			}
		}
	} else {
		// A conducted session MUST NOT self-mutate under governance: disable Claude Code's
		// background auto-updater for the child so the binary stays pinned for the session's
		// lifetime (reproducibility + the co-deployment's pinned-artifact guarantee).
		// The deploy artifacts (image/compose/systemd) also set this on the engine, but the
		// procRunner env is a strict ALLOWLIST that would otherwise strip it from the child —
		// so inject it explicitly here, where it actually reaches `claude`. It is Claude's
		// own variable and is not invented for another provider's CLI.
		env = append(env, EnvVar{Name: envDisableAutoupdater, Value: "1"})
		if d.cred.bound.Kind != "" {
			env = append(env, claudeBoundProviderEnv...)
		}
	}

	// The governance env the LaunchGate wants on the child — the OLIVARES_HOOK_PEP_*
	// the managed PreToolUse hook reads to reach the governed PEP, so every tool-call the
	// operated session makes is policy-checked in line. Appended last (authoritative over
	// any host value); a per-session PEP bearer is held in memory and never persisted.
	env = append(env, d.gateEnv...)

	preset := launchPreset(p)
	policy := m.sessionConfinement(dir, p.ProviderHome, preset)
	var confinementFiles []*os.File
	if policy != nil && d.ws != nil {
		confinementFiles = d.ws.readOnlyHandles
	}
	return LaunchSpec{
		Program:       program,
		Args:          args,
		Dir:           dir,
		Env:           env,
		BoundProvider: d.cred.bound,
		NetworkPolicy: sessionNetworkPolicy(driverKey, d.cred.bound, d.gateEnv),
		EnvAllow:      p.EnvAllow,
		Isolation:     p.Isolation,
		WaitDelay:     m.rt.waitDelay,
		Workspace:     mount,
		// The child may write its folder and its account homes, and nothing of
		// the engine (runtime_confinement.go).
		Confinement: policy, ConfinementFiles: confinementFiles,
		ConfinementRequireTruncateProtection: p.requireTruncateProtection,
		// A read-only session's promise is the operating system's: it does not start
		// where it cannot be confined, whatever the node's own setting.
		ConfinementRequired: m.rt.confineRequired || preset == PresetReadOnly || (d.ws != nil && len(d.ws.readOnlyFolders) > 0),
	}
}

// launchWorkspaceTarget resolves the child's working directory and, for a
// containerized launch, its bind mount.
//
// ref→path is GOVERNED. A resolved workspace sets the working directory; for
// native that is the canonical host root, for a container it is the in-container
// target plus the bind mount the container runner consumes.
//
// A run with no workspace never gets an empty Dir: the native runner would fall
// back to the ENGINE's process cwd. Such a run is given a directory of its own
// (runtime_workspace_dir.go) before it is persisted, so this function returns it. An empty value here means the caller resolved neither, which
// createRunInternal refuses before reaching this point.
func launchWorkspaceTarget(p CreateRunParams, ws *resolvedWorkspace) (string, *WorkspaceMount) {
	if ws == nil {
		return p.WorkspaceDir, nil
	}
	switch p.Isolation {
	case IsolationContainer, IsolationSandbox:
		return ws.containerTgt, &WorkspaceMount{
			HostPath:        ws.rootReal,
			ContainerTarget: ws.containerTgt,
			ReadOnly:        ws.mountMode == mountRO,
		}
	default: // native
		if p.WorktreeBranch != "" {
			// The session's own worktree (runtime_worktree.go), not the workspace folder.
			return p.WorkspaceDir, nil
		}
		return ws.rootReal, nil
	}
}

const sessionLaunchSetupTimeout = 5 * time.Second

// Setup diagnostics name a fixed stage, never a lower-trust error that has seen
// injected credentials. A registry outage cannot hold the create request open.
func (m *Module) prepareSessionLaunch(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, spec *LaunchSpec) (string, error) {
	if err := m.prepareSessionSkills(ctx, runCtx, tenant, runRef, *p, spec); err != nil {
		return "assigned skills delivery", err
	}
	if err := checkGeminiBoundProviderConfig(spec); err != nil {
		return "Gemini authentication settings", err
	}
	if err := m.configureGitRead(ctx, runCtx, tenant, runRef, p, spec); err != nil {
		return "repository read credential", err
	}
	if err := m.prepareCodexSandbox(ctx, p, spec); err != nil {
		return "Codex sandbox check", err
	}
	if err := m.prepareClaudeHooks(spec, *p, runRef); err != nil {
		return "session hook setup", err
	}
	if m.rt.SessionMCP == nil {
		return "", nil
	}
	setupCtx, cancel := context.WithTimeout(ctx, sessionLaunchSetupTimeout)
	defer cancel()
	cleanup, err := m.rt.SessionMCP.ConfigureSessionMCP(setupCtx, tenant, runRef, launchDriverKey(*p), spec)
	if cleanup != nil {
		context.AfterFunc(runCtx, cleanup)
	}
	return "session MCP setup", err
}

// prepareClaudeHooks installs Olivares' tool-call hooks into a Claude Code
// stream-json launch, create and resume alike, once the launch gate has put the
// PEP endpoint and credential in its environment (ConfigureClaudeHookPEP). A
// Claude launch that carries them and cannot have its hooks written is refused:
// it never runs ungoverned. A launch without them (no PEP mounted on this
// engine) is launched as before.
func (m *Module) prepareClaudeHooks(spec *LaunchSpec, p CreateRunParams, runRef string) error {
	if launchDriverKey(p) != providerDriverClaude || p.Transport != TransportStreamJSON || m.rt.hookDataDir == "" {
		return nil
	}
	pep := false
	for _, v := range spec.Env {
		if v.Name == envHookPEPURL && v.Value != "" {
			pep = true
		}
	}
	if !pep {
		return nil
	}
	if err := ConfigureClaudeHookPEP(spec, m.rt.hookDataDir, runRef, m.rt.hookBinary); err != nil {
		return err
	}
	// Grant only this launch's settings directory to the confined child.
	spec.AllowRead(filepath.Join(m.rt.hookDataDir, "run", runRef))
	return nil
}

// The common create/resume/work spec resolves network authority once. Only a
// record-bound launch of a tool whose hosts were measured behind the proxy is
// confined: to its record's endpoint and the engine's own hook control endpoints.
// Every other launch keeps today's network.
func sessionNetworkPolicy(driver string, bound BoundProvider, injected []EnvVar) *egress.Policy {
	if facts, ok := driverfacts.Lookup(driver); bound.Kind == "" || !ok || !facts.EgressMeasured {
		return nil
	}
	p := &egress.Policy{Providers: []string{bound.Endpoint}}
	for _, item := range injected {
		if slices.Contains(hookControlEnv, item.Name) {
			p.Controls = append(p.Controls, item.Value)
		}
	}
	return p
}
