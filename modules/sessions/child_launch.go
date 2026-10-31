// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// THE CHILD LAUNCH: what a session's child process receives.
//
// Create and resume resolve a launch decision (childDecision) and hand it here.
// childSpec turns it into the exact child: program and argv, folder, explicit
// environment, filesystem policy and network policy. launchChild then lets the
// launch-time contributions add theirs, in one order — the repository read
// credential (session_git_read.go), the profile's programs and package registries
// (provider_profile_packages.go), the Codex sandbox probe
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
	// networkModel is the model an own-login OpenCode launch is held to, read once
	// by launchChild (launchNetworkModel) for the refusal, the policy and the driver.
	networkModel    string
	networkModelSet bool
	// gateEnv is the launch gate's grant, already checked (validateGateEnv).
	gateEnv []EnvVar
	// providerEnv is a registered driver's governed managed credential; empty
	// for every other source.
	providerEnv []EnvVar
	// setupRun asks a start to run the approved setup command of its new worktree
	// once, before the child starts (runWorktreeSetup): the first start of a worktree run,
	// whether it is a create, an approved queued start or a resume of a run whose first
	// start failed. A resume of a run that has started never asks.
	// setupCleanupFailed means the setup guard already exited without proving its
	// detached descendants were gone. No later start may overlap them.
	setupCleanupFailed bool
	setupRun           bool
}

// setupInBackground says whether this start has a setup command that its session may have
// to own once the request has waited for it (runtime_launch_background.go). A folder the
// launch holds open read-only handles for is never started from a goroutine: the handles
// close when the request's own function returns. Worktree launches refuse such folders,
// so the check is a guard, not a path.
func (d childDecision) setupInBackground() bool {
	return d.setupRun && d.ws != nil && d.ws.setupCommand != "" && len(d.ws.readOnlyHandles) == 0
}

// launchChild builds the child, lets the launch-time contributions add theirs and
// starts it. stage names the step that failed, for the refusal the caller writes;
// the spec is returned whatever happened, as the runner may hand back a process
// together with an error.
func (m *Module) launchChild(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, d childDecision) (LaunchSpec, Process, string, error) {
	spec, proc, stage, err := m.prepareChild(ctx, runCtx, tenant, runRef, p, d)
	if err != nil {
		return spec, proc, stage, err
	}
	// The setup runs after the session's spec is final, so it gets the same confinement, and
	// before the child exists, so the first turn never sees a half-prepared worktree. It is
	// also before the check in startChildProcess, because what it writes is read by the session.
	if d.setupRun {
		if proc, err := m.runWorktreeSetup(ctx, tenant, runRef, *p, spec, d.ws, syncSetupLimit()); err != nil {
			// An unreaped setup is a live launch error: the create/resume caller keeps
			// its custody and cannot start a second setup beside it.
			return spec, proc, worktreeSetupStage, err
		}
	}
	proc, stage, err = m.startChildProcess(ctx, runCtx, tenant, runRef, p, d.ws, spec, ctx.Err)
	return spec, proc, stage, err
}

// prepareChild is the launch up to the setup: the refusals that need no process, the
// child's final spec and every launch-time contribution to it. A start that runs its
// setup in the background (runtime_launch_background.go) stops here, registers the
// session and continues with the setup and startChildProcess on its own.
func (m *Module) prepareChild(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, d childDecision) (LaunchSpec, Process, string, error) {
	if d.setupCleanupFailed {
		return LaunchSpec{}, nil, worktreeSetupStage, worktreeRefusal("the previous setup's process cleanup was not confirmed; resume is refused, so stop its leftover processes and start a new worktree")
	}
	if err := d.ws.noDeleteIsolationRefusal(p.Isolation); err != nil {
		return LaunchSpec{}, nil, "folder access mode", err
	}
	if p.Isolation == IsolationContainer {
		if err := m.containerRefusal(*p, d); err != nil {
			return LaunchSpec{}, nil, "container isolation", err
		}
	}
	program, err := m.resolveRunProgram(ctx, tenant, *p)
	if err != nil {
		return LaunchSpec{}, nil, "tool version selection", err
	}
	p.resolvedProgram, p.resolvedIdentity = toolProgramIdentity(program)
	d.networkModel, d.networkModelSet = launchNetworkModel(launchDriverKey(*p), *p, d), true
	spec := m.childSpec(*p, d)
	spec.RunOn, spec.RunRef, spec.WorkspaceDirOwned = p.RunOn, runRef, p.WorkspaceDirOwned
	spec.TenantID = string(tenant)
	if err := openCodeUnknownHosts(launchDriverKey(*p), d.cred.bound, d.networkModel, spec.NetworkPolicy); err != nil {
		return spec, nil, "provider hosts", err
	}
	m.watchEgressRefusals(tenant, runRef, spec.NetworkPolicy)
	if spec.NetworkPolicy != nil && keyHeaderKinds[d.cred.bound.Kind] && spec.NetworkPolicy.CredentialSHA256 == ([sha256.Size]byte{}) {
		m.warnf("sessions: a launch on a provider key has no key to pin; the session proxy does not check which key it forwards",
			"run_ref", runRef, "kind", d.cred.bound.Kind)
	}
	if err := remoteLaunchSpecRefusal(spec); err != nil {
		return spec, nil, "remote launch preparation", err
	}
	if stage, err := m.prepareSessionLaunch(ctx, runCtx, tenant, runRef, p, &spec); err != nil {
		return spec, launchPreparationProcess(err), stage, err
	}
	return spec, nil, "", nil
}

// startChildProcess is the launch after the setup: the project configuration check, the
// last look at whether the start was cancelled (cancelled says so: the request's context
// for a start that waits, the start's own for one the session owns) and the process.
func (m *Module) startChildProcess(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, ws *resolvedWorkspace, spec LaunchSpec, cancelled func() error) (Process, string, error) {
	// The files may have changed since the check that let this start through.
	if verdict := m.checkProjectConfig(ctx, tenant, *p, ws); verdict.refusal != nil && !m.claudeHooksApply(*p, spec.Env) {
		return nil, "project agent configuration check", verdict.refusal
	}
	if err := cancelled(); err != nil {
		return nil, "create request cancellation", worktreeRefusal("the create request was cancelled before the session could start: %v", err)
	}
	if err := remoteLaunchSpecRefusal(spec); err != nil {
		return nil, "remote launch preparation", err
	}
	if spec.RunOn != "" {
		proc, err := m.rt.RunPlacement.Launch(runCtx, spec)
		return proc, "remote tool process launch", err
	}
	proc, err := m.rt.Runner.Launch(runCtx, spec)
	return proc, "tool process launch", err
}

func launchPreparationProcess(err error) Process {
	var probeErr *codexSandboxLaunchError
	if errors.As(err, &probeErr) {
		return probeErr.proc
	}
	return nil
}

// launchNetworkModel is the model whose provider an own-login OpenCode launch is held
// to: the launch's own, or else the one the account's OpenCode configuration sets,
// which OpenCode uses when the launch names none (#3094).
func launchNetworkModel(driver string, p CreateRunParams, d childDecision) string {
	if p.Model != "" || driver != providerDriverOpenCode || d.cred.bound.Kind != "" || p.ProviderHome == nil {
		return p.Model
	}
	return openCodeConfiguredModel(p.ProviderHome.ConfigHome)
}

// openCodeUnknownHosts refuses an own-login OpenCode launch whose hosts are not known
// (#3094): its model's provider has no entry in openCodeOwnLoginHosts, or it names no
// model and the account's configuration sets none. Such a session would keep the host
// network, and a session reaches only its provider or does not start.
func openCodeUnknownHosts(driver string, bound BoundProvider, model string, network *egress.Policy) error {
	if driver != providerDriverOpenCode || bound.Kind != "" || network != nil {
		return nil
	}
	if facts, ok := driverfacts.Lookup(driver); !ok || !facts.EgressMeasured {
		return nil
	}
	if model == "" {
		return &runErr{503, "OpenCode's own sign-in names no model here, and its configuration sets none that Olivares can read (model in opencode.jsonc or opencode.json, plain JSON), so the hosts it would reach are not known; choose a model in the console's launch form, or olivares session start <folder> --model opencode/<model>"}
	}
	provider := openCodeModelProvider(model)
	if provider == "" {
		return &runErr{503, fmt.Sprintf("OpenCode's own sign-in is held to its login's hosts, known for the opencode, opencode-go and openai providers; model %q names no provider (provider/model): choose one of theirs, or a provider key", model)}
	}
	return &runErr{503, fmt.Sprintf("OpenCode's own sign-in is held to its login's hosts, known for the opencode, opencode-go and openai providers; model %q names %q: choose one of those, or a provider key", model, provider)}
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

	netModel := d.networkModel
	if !d.networkModelSet {
		netModel = launchNetworkModel(driverKey, p, d)
	}
	network := sessionNetworkPolicy(driverKey, d.cred.bound, netModel, d.gateEnv, d.providerEnv)
	var args []string
	var driverLaunch DriverLaunch
	program := p.resolvedProgram
	if driven {
		// The driver owns its own official argv. The resume ID is deliberately NOT a flag
		// here: an app-server/ACP child resumes through a METHOD on the owned
		// protocol, and the correlated root response is the only thing allowed to
		// nominate the conversation.
		if program == "" {
			program = m.driverProgram(drv)
		}
		driverLaunch = DriverLaunch{WorkDir: dir, Model: netModel, Effort: p.Effort, Preset: launchPreset(p), CodexSandboxFallback: p.codexSandboxFallback, LocalModelEndpoint: d.cred.localModelEndpoint, LocalModels: d.cred.localModels, BoundProvider: d.cred.bound, OwnNetwork: network != nil}
		if p.ProviderHome != nil {
			driverLaunch.ConfigHome = p.ProviderHome.ConfigHome
			driverLaunch.UserHome = p.ProviderHome.UserHome
		}
		args = drv.LaunchArgs(driverLaunch)
	} else {
		if program == "" {
			program = m.claudeProgram()
		}
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
		if p.Transport != TransportRemoteControl && d.cred.bound.Kind != "" {
			claude.Settings = claudeBoundSettings
			// Repeat only non-secret model policy at the same settings priority as
			// the quiet switches, so saved settings cannot replace a preset's roles.
			settingsEnv := map[string]string{}
			for _, item := range d.providerEnv {
				if item.Name == "ANTHROPIC_MODEL" || strings.HasPrefix(item.Name, "ANTHROPIC_DEFAULT_") && strings.HasSuffix(item.Name, "_MODEL") || item.Name == "CLAUDE_CODE_SUBAGENT_MODEL" {
					settingsEnv[item.Name] = item.Value
				}
			}
			if len(settingsEnv) > 0 {
				for _, item := range claudeBoundProviderEnv {
					if item.Name != "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST" {
						settingsEnv[item.Name] = item.Value
					}
				}
				// Claude also needs native modelOverrides to recognize a provider ID;
				// the role env alone routes it correctly but still emits an SDK warning.
				overrides := map[string]string{}
				for alias, name := range map[string]string{
					"opus": "ANTHROPIC_DEFAULT_OPUS_MODEL", "sonnet": "ANTHROPIC_DEFAULT_SONNET_MODEL",
					"haiku": "ANTHROPIC_DEFAULT_HAIKU_MODEL", "fable": "ANTHROPIC_DEFAULT_FABLE_MODEL",
				} {
					if id := settingsEnv[name]; id != "" {
						overrides[claudeModelAliases[alias]] = id
					}
				}
				settings := map[string]any{"env": settingsEnv}
				if len(overrides) > 0 {
					primary := settingsEnv["ANTHROPIC_MODEL"]
					alias := strings.TrimSuffix(strings.ToLower(p.Model), "[1m]")
					if _, known := claudeModelAliases[alias]; p.Model != "" && !known && alias != "best" && alias != "opusplan" {
						primary = p.Model
					}
					// A separate, recognized version slot registers a saved/explicit main
					// that differs from the vendor's roles. The launch still uses its exact ID.
					if !strings.HasPrefix(strings.ToLower(primary), "claude-") {
						overrides["claude-sonnet-4-6"] = primary
					}
					// Never redirect an explicitly selected Claude version.
					delete(overrides, alias)
					settings["modelOverrides"] = overrides
				}
				// All values are strings or maps of strings, so marshaling cannot fail.
				raw, _ := json.Marshal(settings)
				claude.Settings = string(raw)
			}
		}
		if p.FastMode != nil {
			fast := `{"fastMode":` + strconv.FormatBool(*p.FastMode)
			if claude.Settings == "" {
				claude.Settings = fast + `}`
			} else {
				claude.Settings = fast + `,` + claude.Settings[1:]
			}
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
	var policy *confine.Policy
	var confinementFiles []*os.File
	var noDelete []string
	var mounts confine.Mounts
	var mountsNote string
	var binds []ContainerBind
	if p.Isolation == IsolationContainer {
		// A container is held by what it is given, not by a Landlock policy: dir is a
		// path inside it, so nothing here may be computed from it on the host.
		binds = m.containerFolderBinds(p, d, preset)
		if driverKey == providerDriverClaude {
			// Claude Code reads its managed (policy-tier) settings from the system directory
			// of the image it runs in; the host's policy is what applies.
			if bind, ok := claudeManagedSettingsBind(); ok {
				binds = append(binds, bind)
			}
		}
		if mount != nil && preset == PresetReadOnly {
			mount.ReadOnly = true
		}
	} else {
		policy = m.sessionConfinement(dir, p.ProviderHome, preset)
		if policy != nil && d.ws != nil {
			confinementFiles = d.ws.readOnlyHandles
			if d.ws.mountMode == mountNoDelete && preset != PresetReadOnly {
				noDelete = withoutDelete(policy, dir)
			}
		}
		if policy != nil && preset != PresetReadOnly && dir != "" {
			// A read-only session cannot write its folder at all.
			mounts, mountsNote = m.GitMounts(dir)
		}
	}
	return LaunchSpec{
		Program:       program,
		Args:          args,
		Dir:           dir,
		Env:           env,
		BoundProvider: d.cred.bound,
		NetworkPolicy: network,
		EnvAllow:      p.EnvAllow,
		Isolation:     p.Isolation,
		WaitDelay:     m.rt.waitDelay,
		Workspace:     mount,
		Binds:         binds,
		// The child may write its folder and its account homes, and nothing of
		// the engine (runtime_confinement.go).
		Confinement: policy, ConfinementFiles: confinementFiles, ConfinementNoDelete: noDelete,
		ConfinementMounts: mounts, ConfinementMountsNote: mountsNote,
		files:                                newFileScope(dir, p.Isolation != IsolationNative && p.Isolation != "", preset, d.ws, policy, mounts),
		ConfinementRequireTruncateProtection: p.requireTruncateProtection,
		// A read-only session's promise, like a no-delete folder's, is the operating
		// system's: it does not start where it cannot be confined, whatever the node's
		// own setting.
		ConfinementRequired: m.rt.confineRequired || preset == PresetReadOnly || (d.ws != nil && (len(d.ws.readOnlyFolders) > 0 || d.ws.mountMode == mountNoDelete)),
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

// sessionLaunchSetupTimeout bounds a session MCP setup that never answers. Its only
// slow step is the tenant roster read, which queues on the engine's one store
// connection behind every other launch (the file it writes takes under 5 ms), so on
// slow storage a burst of starts waits there: 7.7 s at most with 40 processes
// syncing the same disk, against a 5 s bound that refused a start every other
// step of the same launch waited out. The API server cuts a response at 60 s
// (core/api/server.go WriteTimeout), so the refusal must come well before that.
const sessionLaunchSetupTimeout = 30 * time.Second

// Setup diagnostics name a fixed stage, never a lower-trust error that has seen
// injected credentials. A registry outage cannot hold the create request open
// longer than sessionLaunchSetupTimeout.
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
	if err := m.configureCredentials(ctx, runCtx, tenant, runRef, p, spec); err != nil {
		return "managed credentials", err
	}
	if err := m.configureProfilePackages(ctx, runCtx, tenant, runRef, p, spec); err != nil {
		return "profile programs, package registries and egress hosts", err
	}
	if err := m.prepareOpenCodeSearch(*p, spec); err != nil {
		return "OpenCode's ripgrep", err
	}
	if err := m.prepareCodexSandbox(ctx, p, spec); err != nil {
		return "Codex sandbox check", err
	}
	if err := m.prepareClaudeHooks(spec, *p, runRef); err != nil {
		return "session hook setup", err
	}
	if err := m.prepareSessionDecoys(ctx, runCtx, tenant, runRef, spec); err != nil {
		return "decoy credentials", err
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

// claudeHooksApply says whether a launch gets the engine's tool hooks, and with
// them --setting-sources "" (ConfigureClaudeHookPEP): env is the launch's
// environment once the launch gate has granted its PEP endpoint. One predicate
// serves the hook setup and the project configuration check, so the check
// never assumes an exclusion the launch does not carry.
func (m *Module) claudeHooksApply(p CreateRunParams, env []EnvVar) bool {
	if launchDriverKey(p) != providerDriverClaude || p.Transport != TransportStreamJSON || m.rt.hookDataDir == "" {
		return false
	}
	for _, v := range env {
		if v.Name == envHookPEPURL && v.Value != "" {
			return true
		}
	}
	return false
}

// prepareClaudeHooks installs Olivares' tool-call hooks into a Claude Code
// stream-json launch, create and resume alike, once the launch gate has put the
// PEP endpoint and credential in its environment (ConfigureClaudeHookPEP). A
// Claude launch that carries them and cannot have its hooks written is refused:
// it never runs ungoverned. A launch without them (no PEP mounted on this
// engine) is launched as before.
// containerHookBinary is the container hook's own entrypoint label, not the host hook's
// olivares_exec_t: the kernel allows only one transition on olivares_exec_t.
var containerHookBinary = "/usr/libexec/olivares-container-hook"

func (m *Module) prepareClaudeHooks(spec *LaunchSpec, p CreateRunParams, runRef string) error {
	if !m.claudeHooksApply(p, spec.Env) {
		return nil
	}
	binary := m.rt.hookBinary
	if spec.Isolation == IsolationContainer && selinuxEnforcingAt(selinuxEnforcePath) {
		info, err := os.Stat(containerHookBinary)
		switch {
		case err == nil && info.Mode().IsRegular():
			binary = containerHookBinary
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("sessions: cannot use the container hook entrypoint %s: %w", containerHookBinary, err)
		}
	}
	if err := ConfigureClaudeHookPEP(spec, m.rt.hookDataDir, runRef, binary); err != nil {
		return err
	}
	if spec.Isolation == IsolationContainer {
		// The hook command is the engine's own program, wherever it lives on the host:
		// a container that lacked it would run its tool calls with no hook to ask.
		spec.Binds = append(spec.Binds, ContainerBind{HostPath: binary, Target: binary, ReadOnly: true, NoRelabel: true})
	} else {
		// A native child reads this launch's settings directory; a container takes it inline.
		spec.AllowRead(filepath.Join(m.rt.hookDataDir, "run", runRef))
	}
	return nil
}

// The common create/resume/work spec resolves network authority once. Only a
// record-bound launch of a tool whose hosts were measured behind the proxy is
// confined: to its record's endpoint and the engine's own hook control endpoints.
// Every other launch keeps today's network.
//
// The provider's endpoint accepts any key, so the policy also carries the SHA-256 of
// the record's key (never the key) and the proxy forwards only that one. Only the
// kinds whose tool sends its key in a header the proxy compares are pinned: Claude
// Code (X-Api-Key) and Codex (bearer). Gemini CLI and xAI keys are not checked.
//
// OpenCode's own login has no record: its model's provider names the hosts it
// needs (openCodeOwnLoginHosts), and a model naming none keeps today's network.
// Cursor's account has no model-dependent route: its vendor-declared production
// apiUrl, metricsUrl and websiteUrl are the whole boundary (cursorOwnLoginHosts).
func sessionNetworkPolicy(driver string, bound BoundProvider, model string, injected, credentials []EnvVar) *egress.Policy {
	if facts, ok := driverfacts.Lookup(driver); !ok || !facts.EgressMeasured {
		return nil
	}
	var p *egress.Policy
	switch {
	case bound.Kind != "":
		p = &egress.Policy{Providers: []string{bound.Endpoint}}
	case driver == providerDriverOpenCode:
		hosts, ok := openCodeOwnLoginHosts[openCodeModelProvider(model)]
		if !ok {
			return nil
		}
		// The own login's hosts are public names: a reserved address refuses.
		// Copies: a profile's registries are appended to this launch's policy
		// (applyProfileRegistries), never to the table every launch reads.
		p = &egress.Policy{Providers: slices.Clone(hosts[:1]), PublicProviders: slices.Clone(hosts[1:]), PublicOnly: true}
	case driver == providerDriverCursor:
		p = &egress.Policy{Providers: slices.Clone(cursorOwnLoginHosts[:1]), PublicProviders: slices.Clone(cursorOwnLoginHosts[1:]), PublicOnly: true}
	default:
		return nil
	}
	if driver == providerDriverOpenCode {
		p.LoopbackPorts = []int{openCodeACPPort}
	}
	if keyHeaderKinds[bound.Kind] {
		keyEnv := providerRecordKeyEnv(bound.Kind)
		for _, item := range credentials {
			if key := strings.TrimSpace(item.Value); (item.Name == keyEnv || bound.Kind == ProviderKindAnthropic && item.Name == "ANTHROPIC_AUTH_TOKEN") && key != "" {
				p.CredentialSHA256 = sha256.Sum256([]byte(key))
			}
		}
	}
	for _, item := range injected {
		if slices.Contains(hookControlEnv, item.Name) {
			p.Controls = append(p.Controls, item.Value)
		}
	}
	return p
}

// keyHeaderKinds are the record kinds whose launched tool sends the record's key
// in a header the egress proxy compares (egress.credentialHeaders).
var keyHeaderKinds = map[string]bool{ProviderKindAnthropic: true, ProviderKindOpenAI: true, ProviderKindOpenAICompatible: true}
