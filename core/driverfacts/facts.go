// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package driverfacts declares the tool metadata shared by installation, host
// observation, sign-in, profile selection and session launch. It performs no I/O.
// Protocol implementations and credential values do not belong in this table.
package driverfacts

import (
	"slices"
	"strings"
)

const (
	ClaudeConfigHomeEnv   = "CLAUDE_CONFIG_DIR"
	CodexConfigHomeEnv    = "CODEX_HOME"
	GrokConfigHomeEnv     = "GROK_HOME"
	OpenCodeConfigHomeEnv = "OPENCODE_CONFIG_DIR"
	GeminiConfigHomeEnv   = "GEMINI_CLI_HOME"
	ClaudeRuntimeBinEnv   = "OLIVARES_SESSION_RUNTIME_CLAUDE_BIN"
	CodexRuntimeBinEnv    = "OLIVARES_SESSION_RUNTIME_CODEX_BIN"
	GrokRuntimeBinEnv     = "OLIVARES_SESSION_RUNTIME_GROK_BIN"
	OpenCodeRuntimeBinEnv = "OLIVARES_SESSION_RUNTIME_OPENCODE_BIN"
	GeminiRuntimeBinEnv   = "OLIVARES_SESSION_RUNTIME_GEMINI_BIN"

	InstallManifest = "signed-manifest"
	InstallPackage  = "origin-package"
	InstallRelease  = "release-archive"
	SignInPaste     = "paste-code"
	SignInDevice    = "device-code"

	// Egress describes the endpoint a binding can configure, not an OS network
	// boundary. Native confinement and protocol enforcement remain launch work.
	EgressConfigured = "configured-endpoint"
	EgressVendor     = "vendor-default-only"
)

// ProxyEnv and TrustEnv are the standard variables the tools read, in either
// case, to reach their vendor from this host: the HTTP(S) proxy, and the CA
// bundles of the TLS stacks they use. The engine's own environment is their one
// source. A tool on the host network (its sign-in) inherits them; the session
// egress bridge replaces them with its own.
var (
	ProxyEnv = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}
	TrustEnv = []string{"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE"}
)

// NetworkEnv reports which of the two families a variable name is in.
func NetworkEnv(name string) (proxy, trust bool) {
	name = strings.ToUpper(name)
	return slices.Contains(ProxyEnv, name), slices.Contains(TrustEnv, name)
}

// LoginMethod is one official way to sign a tool in: the arguments of the tool's
// own login command, with the id a client names it by and a label for a person.
type LoginMethod struct {
	ID, Label string
	Args      []string
}

// LoginMethodInfo is what a client sees of a method. Default marks the one
// LoginArgs runs, which is what a start without a method id does.
type LoginMethodInfo struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
}

type Binding struct {
	Kind   string
	Egress string
}

// Facts describes built-in capabilities, not installation or login readiness.
// Empty Installer and SignIn mean those operations are unsupported; Session
// means a session adapter exists, not that it is registered on this node.
type Facts struct {
	Key, Name, Program string
	// Alias is the word generated account names start from (`gemini`,
	// `gemini-b`). Empty means the Key, as for every tool whose key is its name.
	Alias                                   string
	ConfigDir, ConfigHomeEnv, RuntimeBinEnv string
	Installer, SignIn                       string
	Session                                 bool
	LoginArgs, StatusArgs, AuthFailures     []string
	// LoginMethods are the official login methods the engine can relay, when a tool
	// offers more than one. LoginArgs stays the default: a start without a method id
	// runs it, so a tool that lists none is signed in exactly as before.
	LoginMethods                           []LoginMethod
	OwnKind, KeyPhrase, BindingDescription string
	Bindings                               []Binding
	// EnvPrefixes are the tool's own credential and routing variable families. A
	// session child gets them only from that tool's governed launch values: never
	// inherited from the engine, never from a launch gate.
	EnvPrefixes []string
	// EgressMeasured: the tool completes record-bound turns with network access
	// to its record's endpoint only, measured behind the session proxy. Only then
	// is that boundary enforced; other tools keep the host network.
	EgressMeasured bool
}

// openCodeHeadlessLogin is OpenCode's default login: its built-in ChatGPT device
// method, picked by the label the tool prints.
var openCodeHeadlessLogin = []string{"auth", "login", "--provider", "openai", "--method", "ChatGPT Pro/Plus (headless)"}

var rows = []Facts{
	{
		Key: "claude", Name: "Claude Code", Program: "claude", Session: true,
		EnvPrefixes: []string{"ANTHROPIC_", "CLAUDE_CODE_"},
		ConfigDir:   ".claude", ConfigHomeEnv: ClaudeConfigHomeEnv, RuntimeBinEnv: ClaudeRuntimeBinEnv,
		Installer: InstallManifest, SignIn: SignInPaste,
		LoginArgs: []string{"auth", "login", "--claudeai"}, StatusArgs: []string{"auth", "status", "--json"},
		AuthFailures: []string{"not logged in", "please run /login", "invalid api key", "oauth token has expired", "authentication_error"},
		OwnKind:      "anthropic", KeyPhrase: "an Anthropic key",
		BindingDescription: "Claude Code runs only on an Anthropic key",
		Bindings:           []Binding{{"anthropic", EgressConfigured}},
		EgressMeasured:     true,
	},
	{
		Key: "codex", Name: "Codex", Program: "codex", Session: true,
		EnvPrefixes: []string{"CODEX_", "OPENAI_"},
		ConfigDir:   ".codex", ConfigHomeEnv: CodexConfigHomeEnv, RuntimeBinEnv: CodexRuntimeBinEnv,
		Installer: InstallRelease, SignIn: SignInDevice,
		LoginArgs: []string{"login", "--device-auth"}, StatusArgs: []string{"login", "status"},
		AuthFailures: []string{"not logged in", "codex login", "please log in", "unauthorized"},
		OwnKind:      "openai", KeyPhrase: "an OpenAI key",
		BindingDescription: "Codex runs only on an OpenAI key, an OpenAI-compatible endpoint or a local model (Ollama)",
		Bindings:           []Binding{{"openai", EgressConfigured}, {"openai_compatible", EgressConfigured}, {"ollama", EgressConfigured}},
		EgressMeasured:     true,
	},
	{
		Key: "grok", Name: "Grok Build", Program: "grok", Session: true,
		EnvPrefixes: []string{"GROK_", "XAI_"},
		ConfigDir:   ".grok", ConfigHomeEnv: GrokConfigHomeEnv, RuntimeBinEnv: GrokRuntimeBinEnv,
		Installer: InstallPackage, SignIn: SignInDevice,
		LoginArgs:    []string{"login", "--device-auth"},
		AuthFailures: []string{"not logged in", "grok login", "unauthenticated", "unauthorized"},
		OwnKind:      "xai", KeyPhrase: "an xAI key",
		BindingDescription: "Grok Build runs only on an xAI key",
		Bindings:           []Binding{{"xai", EgressConfigured}},
	},
	{
		Key: "opencode", Name: "OpenCode", Program: "opencode", Session: true,
		EnvPrefixes: []string{"OPENCODE_"},
		ConfigDir:   ".config/opencode", ConfigHomeEnv: OpenCodeConfigHomeEnv, RuntimeBinEnv: OpenCodeRuntimeBinEnv,
		Installer: InstallRelease, SignIn: SignInDevice,
		LoginArgs: openCodeHeadlessLogin,
		// Measured on the pinned binary (v1.18.30): `opencode auth login --provider
		// openai` offers "ChatGPT Pro/Plus (browser)", "ChatGPT Pro/Plus (headless)"
		// and "Manually enter API Key". Only the headless one is relayed: the browser
		// method waits for a callback on the engine host, and the key method reads a
		// secret from the tool's own prompt.
		LoginMethods:       []LoginMethod{{ID: "chatgpt-headless", Label: "ChatGPT Pro/Plus (headless)", Args: openCodeHeadlessLogin}},
		StatusArgs:         []string{"auth", "list"},
		AuthFailures:       []string{"no provider", "api key", "unauthorized", "not authenticated"},
		KeyPhrase:          "a key or a local model (Ollama)",
		BindingDescription: "OpenCode runs only on an Anthropic, OpenAI or xAI key at the provider's own address, or on a local model (Ollama)",
		Bindings:           []Binding{{"anthropic", EgressVendor}, {"openai", EgressVendor}, {"xai", EgressVendor}, {"ollama", EgressConfigured}},
	},
	// GEMINI_CLI_HOME names the home that holds .gemini, not .gemini itself.
	{
		Key: "gemini-cli", Alias: "gemini", Name: "Gemini CLI", Program: "gemini", Session: true,
		EnvPrefixes: []string{"GEMINI_CLI_"},
		ConfigDir:   ".gemini", ConfigHomeEnv: GeminiConfigHomeEnv, RuntimeBinEnv: GeminiRuntimeBinEnv,
		Installer: InstallRelease, SignIn: SignInPaste, LoginArgs: []string{"--acp"},
		AuthFailures: []string{"api key is missing", "authentication required", "invalid api key", "unauthenticated"},
		OwnKind:      "gemini", KeyPhrase: "a Gemini API key",
		BindingDescription: "Gemini CLI runs on a Gemini API key at Google's own address",
		Bindings:           []Binding{{"gemini", EgressVendor}},
		EgressMeasured:     true,
	},
	// Ollama is the local model service used by session drivers.
	{Key: "ollama", Name: "Ollama", Program: "ollama", Installer: InstallRelease},
}

// CanBind preserves the closed set of provider kinds and endpoint restrictions.
// Unknown drivers, kinds and endpoint policies cannot authorize a binding.
func (f Facts) CanBind(kind, baseURL string) bool {
	for _, b := range f.Bindings {
		if b.Kind == kind {
			return b.Egress == EgressConfigured || b.Egress == EgressVendor && strings.TrimSpace(baseURL) == ""
		}
	}
	return false
}

// NameStem is the word a generated account name starts from: the Alias when the
// tool has one, else its Key.
func (f Facts) NameStem() string {
	if f.Alias != "" {
		return f.Alias
	}
	return f.Key
}

// LoginArgsFor returns the arguments of the login method id. The empty id is the
// default (LoginArgs); any other id must be one of LoginMethods.
func (f Facts) LoginArgsFor(id string) ([]string, bool) {
	if id == "" {
		return f.LoginArgs, true
	}
	for _, m := range f.LoginMethods {
		if m.ID == id {
			return m.Args, true
		}
	}
	return nil, false
}

// LoginMethodIDs are the ids LoginArgsFor accepts besides the empty one.
func (f Facts) LoginMethodIDs() []string {
	ids := make([]string, 0, len(f.LoginMethods))
	for _, m := range f.LoginMethods {
		ids = append(ids, m.ID)
	}
	return ids
}

// LoginMethodList describes the methods to a client; empty when the tool has one.
func (f Facts) LoginMethodList() []LoginMethodInfo {
	var out []LoginMethodInfo
	for _, m := range f.LoginMethods {
		out = append(out, LoginMethodInfo{ID: m.ID, Label: m.Label, Default: slices.Equal(m.Args, f.LoginArgs)})
	}
	return out
}

// SignInKeys are the keys of the tools the engine can sign in (rows with a
// SignIn), in declaration order. It is the one place that set is read from.
func SignInKeys() []string {
	var keys []string
	for _, row := range rows {
		if row.SignIn != "" {
			keys = append(keys, row.Key)
		}
	}
	return keys
}

// SignInNames are the display names of those tools, in the same order.
func SignInNames() []string {
	var names []string
	for _, row := range rows {
		if row.SignIn != "" {
			names = append(names, row.Name)
		}
	}
	return names
}

// SessionKeys are the keys of the tools a session can run (rows with a Session),
// in declaration order.
func SessionKeys() []string {
	var keys []string
	for _, row := range rows {
		if row.Session {
			keys = append(keys, row.Key)
		}
	}
	return keys
}

// InstallerKeys are the keys of the tools the engine can install (rows with an
// Installer), in declaration order.
func InstallerKeys() []string {
	var keys []string
	for _, row := range rows {
		if row.Installer != "" {
			keys = append(keys, row.Key)
		}
	}
	return keys
}

// JoinList writes items as a sentence list: "a", "a or b", "a, b or c".
func JoinList(items []string, conjunction string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conjunction + " " + items[len(items)-1]
}

// OlivaresLoginInstance names the AI tools instance of an organization's own login
// of a tool, the one made through Olivares under the data directory: "<key>/olivares".
// The tool's default instance, the engine user's own login, is the bare key. The
// providers view lists instances by this name and a session's run names the one it
// runs on.
func OlivaresLoginInstance(key string) string { return key + "/olivares" }

// Lookup returns independent data: callers cannot change another reader's facts.
func Lookup(key string) (Facts, bool) {
	for _, row := range rows {
		if row.Key == key {
			return clone(row), true
		}
	}
	return Facts{}, false
}

// All returns the declaration order, with no writable aliases to the table.
func All() []Facts {
	out := make([]Facts, len(rows))
	for i, row := range rows {
		out[i] = clone(row)
	}
	return out
}

func clone(f Facts) Facts {
	f.LoginArgs = slices.Clone(f.LoginArgs)
	f.LoginMethods = slices.Clone(f.LoginMethods)
	for i := range f.LoginMethods {
		f.LoginMethods[i].Args = slices.Clone(f.LoginMethods[i].Args)
	}
	f.StatusArgs = slices.Clone(f.StatusArgs)
	f.AuthFailures = slices.Clone(f.AuthFailures)
	f.Bindings = slices.Clone(f.Bindings)
	f.EnvPrefixes = slices.Clone(f.EnvPrefixes)
	return f
}
