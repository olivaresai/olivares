// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/sessions"
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
// DENY-CLOSED: the native runner is always wired (so the seam is satisfied), but
// the inference credential is the load-bearing deny-closed default — without a
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
// from the environment. The native runner is always wired; the credential source
// is wired only when a token file is configured (otherwise launches stay
// deny-closed). The governance gates (LaunchGate/StopGate/Recorder) are left at
// their additive defaults here — Late-binds the real PEP/budget/kill-switch/
// recording adapters.
func buildSessionRuntimeOptions(getenv func(string) string, broker *wifCredentialBroker, log *slog.Logger) []sessions.Option {
	opts := []sessions.Option{sessions.WithRunner(sessions.NewProcRunner())}
	// HC1: the host-tools read's adapter. It captures its locations here and
	// detects nothing until a request asks (hosttools.go).
	opts = append(opts, sessions.WithHostToolObserver(newHostToolObserver(getenv)))

	if bin := strings.TrimSpace(getenv(envSessionClaudeBin)); bin != "" {
		opts = append(opts, sessions.WithProgram(bin))
	}
	// The version Olivares presents to an official CLI's handshake is the BUILD's,
	// never a constant the module invented.
	opts = append(opts, sessions.WithProductVersion(version))
	if bin := strings.TrimSpace(getenv(envSessionCodexBin)); bin != "" {
		opts = append(opts,
			sessions.WithProviderDriver(sessions.NewCodexDriver()),
			sessions.WithDriverProgram("codex", bin),
		)
		if log != nil {
			log.Info("session runtime: the official Codex driver is operable on this node",
				"program", bin,
				"authentication", "per profile: "+sessions.AuthSourceAccountHome+" or "+sessions.AuthSourceManagedInjection+" (managed needs its own governed adapter)")
		}
	} else if log != nil {
		log.Info("session runtime: no Codex driver registered; codex profiles are observable and not launchable",
			"set", envSessionCodexBin+" to the pinned official codex binary")
	}
	if bin := strings.TrimSpace(getenv(envSessionGrokBin)); bin != "" {
		opts = append(opts,
			sessions.WithProviderDriver(sessions.NewGrokDriver()),
			sessions.WithDriverProgram("grok", bin),
		)
		if log != nil {
			log.Info("session runtime: the official Grok driver is operable on this node",
				"program", bin,
				"transport", "agent --no-leader stdio (owned child, ACP over stdio; never a leader, server or relay)",
				"authentication", "per profile: "+sessions.AuthSourceAccountHome+" or "+sessions.AuthSourceManagedInjection+" (managed needs its own governed adapter)")
		}
	} else if log != nil {
		log.Info("session runtime: no Grok driver registered; grok profiles are observable and not launchable",
			"set", envSessionGrokBin+" to the pinned official grok binary")
	}
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
