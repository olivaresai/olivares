// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// sessionruntime.go is the OPERATE seam adapter: it wires the concrete
// session RUNNER (the native streaming process launcher) and the secret-less
// inference CREDENTIAL source into module II's operate runtime. Like
// deployexec.go (deploy↔executor) and sandboxrt.go, it is a composition-root
// adapter — the ONLY layer that bridges the AGPL module to the AGPL core runtime.
// The module never imports the runner/credential implementations; it depends only
// on its own deny-closed seams, so can later swap the credential source for a
// full per-tenant claude-wif exchange behind the SAME seam.
//
// DENY-CLOSED: no runner ⇒ no launch. The Community host runner bridges stdio
// PIPES, because the transport this module launches is a stdio protocol.
//
// ⛔ IT IS NOT A PSEUDO-TERMINAL, AND THAT IS MEASURED, NOT PREFERRED. This line
// wired sessions.NewPTYRunner in an earlier revision. Claude Code 2.1.275
// with the stream-json argv this module builds (`--print`) REFUSES a terminal on
// stdin: it writes `Error: Input must be provided either through stdin or as a
// prompt argument when using --print` on stderr and exits 1 without a single
// protocol frame, so every managed Community run would have failed against the
// real binary. sessions.NewPTYRunner stays implemented and covered for the
// interactive form that will need it; the transport a launch form requires is
// declared once, in cliruntime.LaunchTransport. The overlay Identity & Scale
// listener is neither of these runners.
//
// ⛔ AND THE RUNNER IS NO LONGER NAMED HERE: IT IS ASKED FOR. Naming the right
// runner in this file made the declaration decorative — sessionRunnerOption
// resolves it through sessions.NewOfficialRunner, so changing
// cliruntime.LaunchTransport for a kind changes what production launches.
//
// The inference credential is the load-bearing deny-closed default — without a
// configured short-lived token source, a stream-json launch fails closed (no
// static key). Two credential sources, both opt-in and deny-closed (the module is
// unchanged either way — it only sees a Credential whose Token it injects as
// ANTHROPIC_AUTH_TOKEN):
//   - WIF (preferred): the in-process claude-wif/SPIFFE exchange — wifbroker.go
//     mints a short-lived sk-ant-oat per launch under the tenant's federation rule,
//     removing the operator's external attester sidecar. Opt-in via
//     OLIVARES_SESSION_RUNTIME_WIF; a mint failure denies the launch (no static fallback).
//   - FILE (compat): the executor's rotated FILE source — an EXTERNAL WIF/SPIFFE
//     refresher the operator already runs writes a short-lived token to the path; we read
//     it per launch and discard it. The compatibility path for deployments not yet on WIF.

const (
	// envSessionTokenFile is the path template of the rotated, short-lived
	// inference token an external WIF/SPIFFE refresher writes (deny-closed when unset).
	envSessionTokenFile = "OLIVARES_SESSION_RUNTIME_TOKEN_FILE"
	// envSessionTokenTTL is the asserted lifetime of a freshly-read token
	// (match the refresher's rotation cadence; default 15m).
	envSessionTokenTTL = "OLIVARES_SESSION_RUNTIME_TOKEN_TTL"
	// envSessionBaseURL routes operated sessions' inference through Olivares' own
	// gateway (ANTHROPIC_BASE_URL) so it is PEP/budget/model-governed.
	envSessionBaseURL = "OLIVARES_SESSION_RUNTIME_BASE_URL"
	// envSessionClaudeBin overrides the launched executable (default "claude").
	envSessionClaudeBin = "OLIVARES_SESSION_RUNTIME_CLAUDE_BIN"
	// envSessionCodexBin names the OFFICIAL Codex CLI this node may operate, and
	// naming it is what REGISTERS the driver. Readiness is per driver on purpose
	// (RATIFIED §3): there is no shared switch that turns several providers on, and
	// an operator who has not pinned an official binary gets a profile that is
	// honestly observable and honestly not launchable rather than a launch that
	// resolves some `codex` off the PATH.
	envSessionCodexBin = "OLIVARES_SESSION_RUNTIME_CODEX_BIN"
	// envSessionGrokBin names the OFFICIAL Grok CLI this node may operate. It is a
	// SEPARATE registration from the Codex one, deliberately: readiness is per driver
	// (RATIFIED §3), so an operator who has pinned one official binary and not the
	// other gets exactly one operable driver, and the Claude path is unaffected by
	// both. There is no switch that turns several providers on at once.
	envSessionGrokBin = "OLIVARES_SESSION_RUNTIME_GROK_BIN"
	// envSessionOpenCodeBin names the OFFICIAL OpenCode CLI this node may operate.
	// Readiness is per driver (RATIFIED §3): pinning this binary registers the
	// OpenCode ACP driver and does not turn Codex, Grok or Claude on.
	envSessionOpenCodeBin = "OLIVARES_SESSION_RUNTIME_OPENCODE_BIN"
)

// buildSessionRuntimeOptions assembles the operate-runtime options for module II
// from the environment. The runner is wired from the launch-form declaration
// (sessionRunnerOption) and is absent only when that declaration cannot be
// served by one runner; the credential source is wired only when a token file
// is configured (otherwise launches stay deny-closed). The governance gates (LaunchGate/StopGate/Recorder) are left at
// their additive defaults here — Late-binds the real PEP/budget/kill-switch/
// recording adapters.
func buildSessionRuntimeOptions(getenv func(string) string, broker *wifCredentialBroker, dataDir string, log *slog.Logger) []sessions.Option {
	opts := sessionRunnerOption(log)
	// HC1: the host-tools read's adapter. It captures its locations here and
	// detects nothing until a request asks (hosttools.go). It is given the ENGINE's
	// own data directory, never the environment default: an official CLI installed
	// into `<data-dir>/tools` is what registers its driver at the next boot, and
	// re-deriving the root here made that fail for every engine started with
	// --data-dir (measured 2026-09-18).
	obs := newHostToolObserverForDataDir(dataDir, getenv)
	opts = append(opts, sessions.WithHostToolObserver(obs))
	// the session-workspace root is NOT wired here. It is bound after
	// construction by useSessionWorkspaceRoot, through the door that REPORTS a
	// refusal — an Option cannot return one, and a refused root that nobody hears
	// about is a node whose unworkspaced launches all fail with a reason only the
	// caller of the first one ever reads.

	if bin := strings.TrimSpace(getenv(envSessionClaudeBin)); bin != "" {
		opts = append(opts, sessions.WithProgram(bin))
	}
	// The version Olivares presents to an official CLI's handshake is the BUILD's,
	// never a constant the module invented.
	opts = append(opts, sessions.WithProductVersion(version))
	pinOfficialSessionDriver(&opts, getenv, log, obs, envSessionCodexBin, "codex", sessions.NewCodexDriver,
		"authentication", "per profile: "+sessions.AuthSourceAccountHome+" or "+sessions.AuthSourceManagedInjection+" (managed needs its own governed adapter)")
	pinOfficialSessionDriver(&opts, getenv, log, obs, envSessionGrokBin, "grok", sessions.NewGrokDriver,
		"transport", "agent --no-leader stdio (owned child, ACP over stdio; never a leader, server or relay)")
	if bin := strings.TrimSpace(getenv(envSessionOpenCodeBin)); bin != "" {
		opts = append(opts,
			sessions.WithProviderDriver(sessions.NewOpenCodeDriver()),
			sessions.WithDriverProgram("opencode", bin),
		)
		if log != nil {
			log.Info("session runtime: the official OpenCode driver is operable on this node",
				"program", bin,
				"transport", "acp --hostname 127.0.0.1 (owned child, ACP over stdio; never serve, web, attach or an external listener)",
				"authentication", "per profile: "+sessions.AuthSourceAccountHome+" or "+sessions.AuthSourceManagedInjection+" (managed needs its own governed adapter)")
		}
	} else if log != nil {
		log.Info("session runtime: no OpenCode driver registered; opencode profiles are observable and not launchable",
			"set", envSessionOpenCodeBin+" to the pinned official opencode binary")
	}
	if base := strings.TrimSpace(getenv(envSessionBaseURL)); base != "" {
		opts = append(opts, sessions.WithInferenceBaseURL(base))
	}
	if src, kind := sessionCredentialSourceWithDiagnostics(getenv, broker, log); src != nil {
		opts = append(opts, sessions.WithCredentialSource(src))
		if log != nil {
			log.Info("session runtime: inference credential source wired", "source", kind)
		}
	} else if log != nil {
		log.Info("session runtime: no inference credential source configured; stream-json launches are deny-closed",
			"set", envSessionRuntimeWIF+" (in-process WIF mint) or "+envSessionTokenFile+" (rotated token file)")
	}
	// wire the DLP classifier for governed file reads (the deterministic
	// catalog — zero-egress, reproducible). Default-on: a `label`-mode workspace gets
	// sensitivity labels, and a `deny`-mode workspace can refuse a sensitive read
	// (without it, deny-mode fails closed — see the module seam).
	opts = append(opts, sessions.WithClassifier(securityWorkspaceClassifier{}))
	return opts
}

// sessionWorkspaceDirName is the subdirectory of the data directory that holds
// one directory per session that named no registered workspace. It sits beside
// `tools` and the store, under the data directory's own 0700 and its .gitignore.
const sessionWorkspaceDirName = "session-workspaces"

// sessionWorkspaceRootFor derives that root from the engine's resolved data
// directory. An empty or relative data directory yields NO root, which the module
// treats as deny-closed: a guessed absolute path would be worse than a refusal,
// because it would put a session's files somewhere nobody configured.
func sessionWorkspaceRootFor(dataDir string) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return ""
	}
	return filepath.Join(filepath.Clean(dataDir), sessionWorkspaceDirName)
}

// useSessionWorkspaceRoot binds that root at BOOT, through the module's
// error-returning door, and reports what the node ended up able to do.
//
// ⛔ WHY NOT THE OPTION, MEASURED 2026-09-18. `WithSessionWorkspaceRoot` cannot
// return anything — an Option is `func(*Module)` — so a refused root left this
// engine silently unable to launch any session without a registered workspace,
// and the only place that said so was the 503 the FIRST such launch returned to
// whoever happened to make it. The validation existed and had no production
// caller: the refusal is a configuration fact, and configuration facts belong in
// the boot log, once, with their remedy.
//
// The state is READ BACK from the module rather than inferred from the return,
// so the line describes what the node is actually doing. A refusal is not fatal
// to the engine: every other launch, and every launch that names a registered
// workspace, is unaffected — so this reports and continues.
func useSessionWorkspaceRoot(m *sessions.Module, dataDir string, log *slog.Logger) error {
	if m == nil {
		return nil
	}
	root := sessionWorkspaceRootFor(dataDir)
	if root == "" {
		if log != nil {
			log.Warn("session runtime: no data directory is known here, so a session without a workspace cannot be given a directory of its own and is refused",
				"effect", "launches that name a registered workspace are unaffected",
				"why", "the alternative would be the engine's own working directory, which a governed session must never inherit")
		}
		return nil
	}
	err := m.UseSessionWorkspaceRoot(root)
	if err == nil && m.SessionWorkspaceRootConfigured() {
		if log != nil {
			log.Info("session runtime: a session with no registered workspace gets a directory of its own",
				"root", root,
				"removed", "when the operator releases the session, unless a registered workspace claims that path")
		}
		return nil
	}
	if err == nil {
		err = errors.New("the module did not take the root and reported no reason")
	}
	if log != nil {
		log.Error("session runtime: the session-workspace root derived from this engine's data directory was REFUSED, so a session with no registered workspace is deny-closed on this node",
			"root", root,
			"remedy", err.Error(),
			"unaffected", "launches that name a registered workspace")
	}
	return err
}

func pinOfficialSessionDriver(
	opts *[]sessions.Option,
	getenv func(string) string,
	log *slog.Logger,
	obs *hostToolObserver,
	envName, driver string,
	newDriver func() sessions.ProviderDriver,
	extraKey, extraVal string,
) {
	bin := strings.TrimSpace(getenv(envName))
	source := "environment"
	if bin == "" && obs != nil {
		if managed := obs.latestProgram(driver); managed != "" {
			bin, source = managed, "managed-install"
		}
	}
	if bin == "" {
		if log != nil {
			// The hint names what ACHIEVES the registration, and both halves are
			// measured. Until 2026-09-18 it offered `agent tool install` while only the
			// variable worked: the observer re-derived the tools root from
			// the environment, so an install into the engine's own `<data-dir>/tools`
			// was never seen. It is seen now — at the NEXT BOOT, because registration
			// happens at construction, and a hint that omitted the restart would be
			// half true in the same way the old one was.
			log.Info("session runtime: no "+driver+" driver registered; profiles are observable and not launchable",
				"set", envName+" to a pinned official binary",
				"or", "olivares agent tool install --driver "+driver+", then restart the engine")
		}
		return
	}
	*opts = append(*opts,
		sessions.WithProviderDriver(newDriver()),
		sessions.WithDriverProgram(driver, bin),
	)
	if log != nil {
		args := []any{"program", bin, "pin", source}
		if extraKey != "" {
			args = append(args, extraKey, extraVal)
		}
		log.Info("session runtime: the official "+driver+" driver is operable on this node", args...)
	}
}

// sessionRunnerOption wires the session RUNNER the launch forms DECLARE they
// need, and wires none when the declaration cannot be resolved.
//
// ⛔ IT DOES NOT NAME A RUNNER, AND THAT IS THE POINT. This line used to read
// `sessions.WithRunner(sessions.NewProcRunner())`: the right runner, chosen for
// the right reason, by a line that did not read the reason. An independent
// review measured the consequence on 2026-09-18 — the transport declaration
// (cliruntime.LaunchTransport) governed only the cliruntime driver, which has no
// production caller, so this line and the declaration could disagree for ever
// and every managed session would die on stderr with nothing red. Asking the
// factory is what makes the declaration decide what production launches.
//
// A refusal leaves the module DENY-CLOSED (no runner ⇒ every launch fails
// closed) and says so at ERROR. That is the honest answer to "the declared
// transports of the official CLIs disagree, and I hold one runner": launching
// some of them on a transport their own CLI refuses would be worse than
// launching none, because the refusal is visible and the wrong transport is a
// child that exits 1 before its first protocol frame.
func sessionRunnerOption(log *slog.Logger) []sessions.Option {
	runner, err := sessions.NewOfficialRunner(cliruntime.Kinds()...)
	if err != nil {
		if log != nil {
			log.Error("session runtime: the declared launch transports of the official CLIs cannot be served by one runner; NO runner is wired and every managed launch is denied",
				"error", err.Error(),
				"declared_by", "modules/sessions/cliruntime.LaunchTransport",
				"remedy", "one runner serves one transport: split the runner per kind before declaring two")
		}
		return nil
	}
	if log != nil {
		// The transport is read back from the runner rather than restated, so this
		// line cannot claim one thing while the factory returned another. A runner
		// that does not implement the optional read says nothing, and nothing is
		// logged as "not reported" instead of being assumed.
		transport := "not reported by this runner"
		if reporter, ok := runner.(sessions.RunnerTransportReporter); ok {
			transport = string(reporter.ProvidesTransport())
		}
		log.Info("session runtime: the runner wired is the one the official launch forms declare",
			"transport", transport,
			"declared_for", strings.Join(cliruntime.Kinds(), ","),
			"why", "the owned operate forms are stdio protocols; Claude Code's --print form refuses a terminal on stdin",
			"limit", "the OpenCode driver registered below is outside that declaration: its ACP child speaks the same standard streams, but nothing declares that here")
	}
	return []sessions.Option{sessions.WithRunner(runner)}
}

// logSessionLaunchInspection records, once at boot, whether the wired Runner can
// be inspected WITHOUT launching. It is not decoration: the inspection seam is
// optional by design, so a Runner that lacks it makes the console's launch
// requirements panel report the runner and the program as "could not check" on
// every profile — correctly, and with no other trace anywhere. Naming it here is
// what turns that into something an operator can look up instead of deduce.
func logSessionLaunchInspection(m *sessions.Module, log *slog.Logger) {
	if log == nil || m == nil {
		return
	}
	if m.LaunchInspectionAvailable() {
		log.Info("session runtime: the wired runner reports launch requirements without executing anything",
			"observes", "the resolved program's metadata and the requested isolation posture",
			"never", "the provider program, a login file or a credential mint")
		return
	}
	log.Warn("session runtime: the wired runner cannot be inspected without launching; the console will report the runner and the program as not checked",
		"effect", "launch readiness answers unknown for those two requirements and never promotes them to ready",
		"unchanged", "launching itself, which keeps all of its own validations")
}

// sessionCredentialSource selects the inference credential source for operated sessions, in
// order of precedence, and returns it with a non-secret label (or nil,"" leaving the module
// deny-closed):
//  1. the in-process WIF broker when OLIVARES_SESSION_RUNTIME_WIF is opted in — mints a
//     short-lived sk-ant-oat per launch under the tenant's federation rule; a mint failure
//     denies the launch, with NO downgrade to the static file (that would defeat the posture);
//  2. the rotated token FILE an external attester writes (the compat path);
//  3. none (deny-closed).
func sessionCredentialSource(getenv func(string) string, broker *wifCredentialBroker) (sessions.CredentialSource, string) {
	return sessionCredentialSourceWithDiagnostics(getenv, broker, nil)
}

// sessionCredentialSourceWithDiagnostics is the same selection with the boot logger
// available, so a WIRED token file that cannot be read says so once in the operator's
// log at the moment it denies a launch. A nil logger is the silent form the inference
// proxy composes with; it changes no decision, only whether the refusal is visible.
func sessionCredentialSourceWithDiagnostics(
	getenv func(string) string,
	broker *wifCredentialBroker,
	log *slog.Logger,
) (sessions.CredentialSource, string) {
	if broker != nil && sessionWIFEnabled(getenv(envSessionRuntimeWIF)) {
		return broker.sessionSource(strings.TrimSpace(getenv(envSessionWIFRule))), "wif (in-process ephemeral mint)"
	}
	path := strings.TrimSpace(getenv(envSessionTokenFile))
	if path == "" {
		return nil, ""
	}
	ttl := 15 * time.Minute
	if raw := strings.TrimSpace(getenv(envSessionTokenTTL)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			ttl = d
		}
	}
	src := executor.NewFileTokenSource(executor.FileTokenConfig{
		PathTemplate: path, TTL: ttl, Scheme: "session-token-file",
	})
	return sessions.CredentialSourceFunc(func(ctx context.Context, req sessions.CredentialRequest) (sessions.Credential, error) {
		c, err := src.Mint(ctx, executor.MintRequest{
			Environment: "operate",
			Runtime:     "claude-code",
			Target:      req.RunRef,
			Mode:        executor.ModeRead,
		})
		if err != nil {
			return sessions.Credential{}, classifyTokenFileMintErr(ctx, err, log)
		}
		// Carry only the non-sensitive id/scheme/expiry onto the module credential;
		// the Token is used at launch and never persisted.
		return sessions.Credential{ID: c.ID, Token: c.Token, Scheme: c.Scheme, NotAfter: c.NotAfter}, nil
	}), "rotated token file (external attester)"
}

// sessionWIFEnabled reports the operator's opt-in to the in-process WIF broker for the
// sessions plane (the codebase truthy convention).
func sessionWIFEnabled(v string) bool {
	v = strings.TrimSpace(v)
	return v == "1" || strings.EqualFold(v, "true")
}

// classifyTokenFileMintErr turns a WIRED token file that could not be read into the
// module's deny-closed credential classification, and nothing else into it.
//
// Measured before this existed: with OLIVARES_SESSION_RUNTIME_TOKEN_FILE set to a
// path the process cannot read, `POST /runs` with transport stream-json answered
// 500 {"error":{"message":"internal error"}}. The executor's mint error carries
// executor.ErrNoCredentialSource, which is NOT the module's errNoCredential, so
// denyClosedErr left it unclassified and writeRunErr called it internal. The engine
// knew the deployment fault and reported a bug in itself.
//
// The two non-firing directions are the point of the function:
//   - a canceled or expired request is the caller going away, not the credential
//     source refusing, so it travels untouched;
//   - anything that is not the source's own deny-closed cause (an unreachable
//     backend, an unexpected failure) stays unclassified, because "I could not reach
//     it" is still not "I refuse" (P1-R6-01).
//
// The cause is WRAPPED, not replaced: executor.ErrNoCredentialSource stays reachable
// for internal inspection while the module answers a fixed public sentence.
func classifyTokenFileMintErr(ctx context.Context, err error, log *slog.Logger) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if cause := ctx.Err(); cause != nil {
		// The file read itself does not observe the context, so a mint that failed
		// under a canceled request cannot be told apart from one that would have
		// failed anyway. Both identities are kept and the CANCELLATION is the one
		// that leads, so the module's cancellation taxonomy answers instead of the
		// credential one — the deployment is not reported as broken because a client
		// hung up.
		return fmt.Errorf("%w: %w", cause, err)
	}
	if !errors.Is(err, executor.ErrNoCredentialSource) {
		return err
	}
	if log != nil {
		// The operator diagnostic boundary, and only it. The variable NAME is the
		// locator: the expanded path, the configured template and the token itself
		// stay out of this line exactly as they stay out of the API body.
		log.Warn("session runtime: the configured inference token file could not be read; this launch is denied",
			"source", "rotated token file (external attester)",
			"configured_by", envSessionTokenFile,
			"remedy", "the external refresher must keep a non-empty, readable short-lived token at the configured path")
	}
	return fmt.Errorf("%w: %w", sessions.ErrCredentialUnavailable, err)
}
