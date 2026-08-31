// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
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
)

// buildSessionRuntimeOptions assembles the operate-runtime options for module II
// from the environment. The native runner is always wired; the credential source
// is wired only when a token file is configured (otherwise launches stay
// deny-closed). The governance gates (LaunchGate/StopGate/Recorder) are left at
// their additive defaults here — Late-binds the real PEP/budget/kill-switch/
// recording adapters.
func buildSessionRuntimeOptions(getenv func(string) string, broker *wifCredentialBroker, log *slog.Logger) []sessions.Option {
	opts := []sessions.Option{sessions.WithRunner(sessions.NewProcRunner())}

	if bin := strings.TrimSpace(getenv(envSessionClaudeBin)); bin != "" {
		opts = append(opts, sessions.WithProgram(bin))
	}
	if base := strings.TrimSpace(getenv(envSessionBaseURL)); base != "" {
		opts = append(opts, sessions.WithInferenceBaseURL(base))
	}
	if src, kind := sessionCredentialSource(getenv, broker); src != nil {
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

// sessionCredentialSource selects the inference credential source for operated sessions, in
// order of precedence, and returns it with a non-secret label (or nil,"" leaving the module
// deny-closed):
//  1. the in-process WIF broker when OLIVARES_SESSION_RUNTIME_WIF is opted in — mints a
//     short-lived sk-ant-oat per launch under the tenant's federation rule; a mint failure
//     denies the launch, with NO downgrade to the static file (that would defeat the posture);
//  2. the rotated token FILE an external attester writes (the compat path);
//  3. none (deny-closed).
func sessionCredentialSource(getenv func(string) string, broker *wifCredentialBroker) (sessions.CredentialSource, string) {
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
			return sessions.Credential{}, err
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
