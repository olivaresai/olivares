// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeconfig

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// authposture.go is the host CREDENTIAL-MODE posture scanner: the one HOST-level
// signal the CLA-14 feeder emits (the capability surfaces are per-config-tree; this is
// per-host, emitted once per Gather).
//
// WHY it exists: for a Claude Code host authenticated by a personal/consumer
// SUBSCRIPTION (Free/Pro/Max), in-process OBSERVATION is the ONLY lawful posture a control
// plane may take — Anthropic forbids third parties from intermediating/proxying the
// subscription OAuth credential ("Anthropic does not permit third-party developers to offer
// Claude.ai login or to route requests through Free, Pro, or Max plan credentials on behalf
// of their users", code.claude.com/docs/en/legal-and-compliance, VERIFIED 2026-06-20). To
// ASSERT a fleet is governed lawfully, the plane must first make VISIBLE which credential
// mode each host runs in. No connector observed this before.
//
// WHAT it reads — PRESENCE/FORM only, never a value (minimal-data, docs/SECURITY-HARDENING.md):
//   - the host PROCESS ENVIRONMENT for the env-driven modes (set/unset of CLAUDE_CODE_USE_*,
//     ANTHROPIC_AUTH_TOKEN, ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_BASE_URL).
//     This is honest in the co-deployment model, where the feeder shares the host's
//     environment with Claude Code; the finding states the modes reflect this host's process
//     environment.
//   - the resolved CONFIG DIR (CLAUDE_CONFIG_DIR, else ~/.claude) for .credentials.json
//     (existence + file-mode 0600) and the apiKeyHelper settings key (presence only).
//
// PRECEDENCE (the EFFECTIVE mode when several coexist) is the documented Claude Code order
// (VERIFIED 2026-06-20, code.claude.com/docs/en/iam — "When multiple credentials are
// present, Claude Code chooses one in this order"):
//
//	1. cloud provider   CLAUDE_CODE_USE_BEDROCK / _VERTEX / _FOUNDRY /
//	   CLAUDE_CODE_USE_ANTHROPIC_AWS (and _MANTLE)
//	2. auth_token       ANTHROPIC_AUTH_TOKEN     (Authorization: Bearer — an LLM gateway)
//	3. api_key          ANTHROPIC_API_KEY        (X-Api-Key — direct Console key)
//	4. api_key_helper   apiKeyHelper settings    (a script returning a key)
//	5. oauth_token      CLAUDE_CODE_OAUTH_TOKEN  (setup-token: 1-year, inference-only, no Remote Control)
//	6. subscription     /login OAuth             (default for Pro/Max/Team/Enterprise; .credentials.json or macOS Keychain)
//
// The value of any credential is NEVER read into output: the finding carries the detected
// mode, the SET of coexisting source names, the .credentials.json mode bits and booleans —
// all non-sensitive — and a hashed DetailHash. The same bar as skillscan.go.

// findingAuthPosture is the CONTRACT kind of a Claude Code credential-mode posture finding
//. Consumers (console, compliance) key on this string. It is HOST-level (one finding
// family per host per Gather), distinct from the per-skill skill_posture findings.
const findingAuthPosture = "claude_auth_posture"

// subjectHost is the SubjectKind of an auth-posture finding: the host/workspace scope. The
// SubjectRef is the feeder's workspace label (the declaring origin), sanitized.
const subjectHost = "claude.host"

// authMode is a Claude Code credential mode (the value space of the auth_posture finding).
type authMode string

const (
	authCloudProvider authMode = "cloud_provider"
	authAuthToken     authMode = "auth_token"
	authAPIKey        authMode = "api_key"
	authAPIKeyHelper  authMode = "api_key_helper"
	authOAuthToken    authMode = "oauth_token"
	authSubscription  authMode = "subscription"
	// authNone = no inference credential is observable from this host (a /login subscription
	// or a macOS Keychain login that is not introspectable from disk, or a logged-out host).
	authNone authMode = "none"
)

// Credential-relevant environment variables (VERIFIED 2026-06-20, code.claude.com/docs/en/
// {iam,env-vars,server-managed-settings}; VERIFIED 2026-07-03 against
// code.claude.com/docs/en/claude-platform-on-aws and code.claude.com/docs/en/
// microsoft-foundry). Only PRESENCE/FORM is ever read. The verified env contract
// also includes per-surface keys ANTHROPIC_FOUNDRY_API_KEY and ANTHROPIC_AWS_API_KEY,
// plus CLAUDE_CODE_SKIP_ANTHROPIC_AWS_AUTH; this scanner does not change credential
// mode logic for those keys in this pass.
const (
	envUseBedrock      = "CLAUDE_CODE_USE_BEDROCK"
	envUseVertex       = "CLAUDE_CODE_USE_VERTEX"
	envUseFoundry      = "CLAUDE_CODE_USE_FOUNDRY"
	envUseAnthropicAWS = "CLAUDE_CODE_USE_ANTHROPIC_AWS"
	envUseMantle       = "CLAUDE_CODE_USE_MANTLE"
	envAuthToken       = "ANTHROPIC_AUTH_TOKEN"
	envAPIKey          = "ANTHROPIC_API_KEY"
	envOAuthToken      = "CLAUDE_CODE_OAUTH_TOKEN"
	envBaseURL         = "ANTHROPIC_BASE_URL"
	envConfigDir       = "CLAUDE_CONFIG_DIR"
)

// knownAuthMode reports whether s is one of the six documented credential modes (used to
// validate the operator's expected_auth_modes allowlist loudly at Open).
func knownAuthMode(s string) bool {
	switch authMode(s) {
	case authCloudProvider, authAuthToken, authAPIKey, authAPIKeyHelper, authOAuthToken, authSubscription:
		return true
	default:
		return false
	}
}

// env reads an environment variable from the injected source (os.LookupEnv by default).
func (f *Feeder) env(key string) (string, bool) {
	if f.lookupEnv != nil {
		return f.lookupEnv(key)
	}
	return os.LookupEnv(key)
}

// envSet reports whether a credential env var is set to a non-empty value. PRESENCE only —
// the value is never inspected beyond "is there a non-blank string".
func (f *Feeder) envSet(key string) bool {
	v, ok := f.env(key)
	return ok && strings.TrimSpace(v) != ""
}

// envEnabled reports whether a FLAG env var (CLAUDE_CODE_USE_*) is set to a truthy value;
// "0"/"false"/"no"/"off"/"" disable it (the cloud-provider switches are boolean toggles).
func (f *Feeder) envEnabled(key string) bool {
	v, ok := f.env(key)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// platform returns the runtime OS (injectable for tests so the macOS Keychain branch is
// exercisable off-darwin).
func (f *Feeder) platform() string {
	if f.goos != "" {
		return f.goos
	}
	return runtime.GOOS
}

// resolveConfigDir returns the Claude config directory whose .credentials.json / settings
// this host uses: CLAUDE_CONFIG_DIR (the documented override, honored on Linux/Windows),
// else ~/.claude. It returns "" when neither is resolvable (then the file-based subscription
// signal is simply unavailable — reported honestly, never guessed).
func (f *Feeder) resolveConfigDir() string {
	if v, ok := f.env(envConfigDir); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	if h, err := f.homeDirOrOS(); err == nil && strings.TrimSpace(h) != "" {
		return filepath.Join(h, ".claude")
	}
	return ""
}

// homeDirOrOS resolves the home directory from the injected source (os.UserHomeDir default).
func (f *Feeder) homeDirOrOS() (string, error) {
	if f.homeDir != nil {
		return f.homeDir()
	}
	return os.UserHomeDir()
}

// credsState is the minimal, non-sensitive view of the subscription credential FILE: its
// presence and whether its mode is broader than the expected owner-only 0600 (the CONTENTS
// are never read — existence + mode is the whole signal, per docs/SECURITY-HARDENING.md and the brief).
type credsState struct {
	present          bool
	mode             os.FileMode
	overPermissioned bool
}

// statCredentials inspects <configDir>/.credentials.json by Stat only.
func statCredentials(configDir string) credsState {
	if configDir == "" {
		return credsState{}
	}
	info, err := os.Stat(filepath.Join(configDir, ".credentials.json"))
	if err != nil || info.IsDir() {
		return credsState{}
	}
	perm := info.Mode().Perm()
	return credsState{present: true, mode: perm, overPermissioned: perm&0o077 != 0}
}

// apiKeyHelperSet reports whether a settings file in the config dir configures an
// apiKeyHelper (the credential-script setting). PRESENCE ONLY — the script path/command is
// never read into output (it can embed a secret path; minimal-data).
func apiKeyHelperSet(configDir string) bool {
	if configDir == "" {
		return false
	}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		var s struct {
			APIKeyHelper string `yaml:"apiKeyHelper"`
		}
		if unmarshalBounded(filepath.Join(configDir, name), &s) && strings.TrimSpace(s.APIKeyHelper) != "" {
			return true
		}
	}
	return false
}

// emitAuthPosture detects this host's EFFECTIVE credential mode by the documented
// precedence and emits the minimal-data posture findings. It never reads a credential value
// (env values, .credentials.json contents, the apiKeyHelper script) — only presence/form.
func (f *Feeder) emitAuthPosture(ctx context.Context, sink sdk.Sink, at time.Time) error {
	host := textscan.SanitizeDisplay(f.label)
	configDir := f.resolveConfigDir()
	creds := statCredentials(configDir)

	// Detect each source by PRESENCE (env) / form (file, settings key).
	cloud := f.envEnabled(envUseBedrock) || f.envEnabled(envUseVertex) ||
		f.envEnabled(envUseFoundry) || f.envEnabled(envUseAnthropicAWS) ||
		f.envEnabled(envUseMantle)
	authToken := f.envSet(envAuthToken)
	apiKey := f.envSet(envAPIKey)
	helper := apiKeyHelperSet(configDir)
	oauthToken := f.envSet(envOAuthToken)
	// Subscription: a .credentials.json on disk (Linux/Windows) OR the macOS Keychain
	// (encrypted, not introspectable from disk — presumed on darwin when no file is present).
	subKeychain := !creds.present && f.platform() == "darwin"
	subscription := creds.present || subKeychain
	// A custom inference endpoint (a gateway/proxy), tracked as a coexisting fact only — the
	// feeder reports its PRESENCE; the authorized-gateway POLICY judgment lives in the
	// managedsettings drift check (connectors/managedsettings/verify.go), not here.
	baseURL := f.envSet(envBaseURL)

	// EFFECTIVE mode = the first present source in precedence order; collect all present.
	var present []string
	mode := authNone
	pick := func(m authMode, on bool) {
		if on {
			present = append(present, string(m))
			if mode == authNone {
				mode = m
			}
		}
	}
	pick(authCloudProvider, cloud)
	pick(authAuthToken, authToken)
	pick(authAPIKey, apiKey)
	pick(authAPIKeyHelper, helper)
	pick(authOAuthToken, oauthToken)
	pick(authSubscription, subscription)

	coexist := "none"
	if len(present) > 0 {
		coexist = strings.Join(present, "+")
	}

	// --- inventory finding (always) -------------------------------------------------------
	// HONESTY (docs/SECURITY-HARDENING.md): the env-derived sources are read from THIS feeder's process
	// environment — a host fact only under co-deployment. The file-derived signals
	// (.credentials.json, apiKeyHelper) are genuine host facts. So the title states the
	// OBSERVATION SOURCE, never an unqualified absolute about the host's Claude Code.
	const observedFrom = " [observed from this host's process environment + config dir — host-accurate under co-deployment]"
	sev := model.SeverityInfo
	var title string
	switch mode {
	case authNone:
		title = "Claude Code credential mode: none — no inference credential observed in this host's process environment or config dir (a /login subscription or macOS Keychain login is not introspectable from disk)"
	default:
		title = "Claude Code credential mode: " + string(mode) + " (sources present: " + coexist + ")" + observedFrom
		if subKeychain {
			title += " [macOS Keychain — not introspectable, presumed from platform]"
		}
		// Severity bumps ONLY when the operator ASSERTS an allowlist and the effective mode is
		// outside it (an unset expectation is pure inventory — never invented drift).
		if f.expectedAuthModes != nil {
			if _, ok := f.expectedAuthModes[string(mode)]; !ok {
				sev = model.SeverityMedium
				title += " — CONTRADICTS the operator's expected credential mode(s)"
			}
		}
	}
	if baseURL {
		title += "; ANTHROPIC_BASE_URL override set on host"
	}
	detail := "auth-posture host=" + host + " mode=" + string(mode) + " present=" + coexist +
		" base_url=" + strconv.FormatBool(baseURL) + " keychain=" + strconv.FormatBool(subKeychain)
	if err := sink.Emit(ctx, authFinding(host, sev, title, detail, at)); err != nil {
		return err
	}

	// --- credential-file posture (Medium): a .credentials.json broader than 0600 leaks the
	// subscription token to other users on the box. Self-contained (policy-independent). ----
	if creds.present && creds.overPermissioned {
		m := octalMode(creds.mode)
		if err := sink.Emit(ctx, authFinding(host, model.SeverityMedium,
			"Claude Code .credentials.json mode "+m+" is broader than 0600 — the subscription token is readable beyond its owner (credential exposure)",
			"auth-posture-creds-mode host="+host+" mode="+m, at)); err != nil {
			return err
		}
	}

	// --- shadowing footgun (Low): an API key / auth token in this host's process environment
	// takes precedence over a present subscription. The vendor docs warn this silently fails
	// when the key belongs to a disabled/expired org (code.claude.com/docs/en/iam). Gated on
	// `subscription` (not just the on-disk file) so it ALSO fires for the presumed macOS
	// Keychain subscription — the platform where this footgun is the default posture and the
	// warning is most relevant. The wording stays honest about which subscription it saw. ----
	if mode == authAPIKey || mode == authAuthToken {
		if subscription {
			subWhich := "subscription credentials on disk"
			if subKeychain {
				subWhich = "presumed macOS Keychain subscription (not introspectable from disk)"
			}
			if err := sink.Emit(ctx, authFinding(host, model.SeverityLow,
				"a "+string(mode)+" in this host's process environment takes precedence over its "+subWhich+" — if the key/token belongs to a disabled or expired org, sessions fail (unset it to fall back to the subscription)",
				"auth-posture-shadow host="+host+" mode="+string(mode)+" keychain="+strconv.FormatBool(subKeychain), at)); err != nil {
				return err
			}
		}
	}
	return nil
}

// authFinding builds one minimal-data auth-posture finding. The Title is non-sensitive
// (mode/source names are constants; the host label is sanitized); the DetailHash hashes a
// stable, non-sensitive key — never a credential value.
func authFinding(host string, sev model.Severity, title, detailKey string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingAuthPosture,
		Severity:    sev,
		SubjectKind: subjectHost,
		SubjectRef:  host,
		Title:       title,
		DetailHash:  redact.Hash(detailKey),
		OccurredAt:  at,
	}
}

// octalMode renders a file permission as a 4-digit octal string (e.g. "0644"). Not
// sensitive — it is the posture signal, not a secret.
func octalMode(m os.FileMode) string {
	return "0" + strconv.FormatInt(int64(m.Perm()), 8)
}
