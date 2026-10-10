// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func geminiTestHome(t *testing.T, files map[string]string) *ProviderHomeSnapshot {
	t.Helper()
	config := filepath.Join(t.TempDir(), ".gemini")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(config, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &ProviderHomeSnapshot{Driver: providerDriverGemini, ConfigHome: config, UserHome: filepath.Dir(config)}
}

// A rule in the home that approves a tool skips session/request_permission, so
// the session's live policy never sees the tool. Measured on 0.62.0.
func TestGeminiHomeThatPreApprovesToolsRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	for name, files := range map[string]map[string]string{
		"tools.allowed":             {"settings.json": `{"tools":{"allowed":["run_shell_command"]}}`},
		"tools.allowed with limits": {"settings.json": `{"tools":{"allowed":["run_shell_command(git)"]},"ui":{"theme":"x"}}`},
		"settings with comments":    {"settings.json": "{\n // trusted\n \"tools\": {\"allowed\": [\"run_shell_command\"]}\n}"},
		"a policy file":             {"policies/allow.toml": "[[rule]]\n"},
		// The pinned CLI (0.62.0, createPolicyEngineConfig) turns each of these settings
		// into an allow rule, or loads allow rules from a path the home names.
		"tools.core":                       {"settings.json": `{"tools":{"core":["run_shell_command"]}}`},
		"policyPaths":                      {"settings.json": `{"policyPaths":["/tmp/anywhere"]}`},
		"adminPolicyPaths":                 {"settings.json": `{"adminPolicyPaths":["/tmp/anywhere"]}`},
		"mcp.allowed":                      {"settings.json": `{"mcp":{"allowed":["srv"]}}`},
		"a trusted MCP server":             {"settings.json": `{"mcpServers":{"a":{"command":"x"},"b":{"command":"y","trust":true}}}`},
		"tools.core with comments":         {"settings.json": "{\n // all\n \"tools\": {\"core\": [\"run_shell_command\"]}\n}"},
		"policyPaths with comments":        {"settings.json": "{\n // all\n \"policyPaths\": [\"/tmp/anywhere\"]\n}"},
		"adminPolicyPaths with comments":   {"settings.json": "{\n // all\n \"adminPolicyPaths\": [\"/tmp/anywhere\"]\n}"},
		"a trusted MCP server w/ comments": {"settings.json": "{\n // all\n \"mcpServers\": {\"b\": {\"trust\": true}}\n}"},
		// The CLI reads JSON.parse's way: keys are case-sensitive and the last of a repeated
		// key wins, so a differently cased or repeated key must not hide the real one.
		"tools.allowed beside ALLOWED":       {"settings.json": `{"tools":{"allowed":["run_shell_command"],"ALLOWED":[]}}`},
		"tools.allowed beside Tools":         {"settings.json": `{"tools":{"allowed":["run_shell_command"]},"Tools":{"allowed":[]}}`},
		"trust beside TRUST":                 {"settings.json": `{"mcpServers":{"a":{"trust":true,"TRUST":false}}}`},
		"a repeated key, last is set":        {"settings.json": `{"tools":{"core":[]},"tools":{"core":["x"]},"policyPaths":[],"policyPaths":["/p"]}`},
		"trust as a string":                  {"settings.json": `{"mcpServers":{"a":{"trust":"true"}}}`},
		"allowed as a string":                {"settings.json": `{"tools":{"allowed":"run_shell_command"}}`},
		"an escaped key in a commented file": {"settings.json": "{\n // c\n \"tools\": {\"\\u0061llowed\": [\"run_shell_command\"]}\n}"},
	} {
		err := geminiHomeRefusal(geminiTestHome(t, files))
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(err.Error(), "pre-approve") {
			t.Errorf("%s: err = %v, want a 409 refusal naming the pre-approval", name, err)
		}
	}
}

func TestGeminiHomeWithoutPreApprovalStarts(t *testing.T) {
	t.Parallel()
	for name, files := range map[string]map[string]string{
		"an empty home":         nil,
		"empty allow list":      {"settings.json": `{"tools":{"allowed":[]}}`},
		"unrelated settings":    {"settings.json": `{"ui":{"theme":"dark"},"mcp":{"excluded":["x"]}}`},
		"empty rule lists":      {"settings.json": `{"tools":{"core":[]},"policyPaths":[],"adminPolicyPaths":[],"mcp":{"allowed":[]}}`},
		"an untrusted server":   {"settings.json": `{"mcpServers":{"a":{"command":"x","trust":false},"b":{"command":"y"}}}`},
		"a commented file":      {"settings.json": "{\n // a theme\n \"ui\": {\"theme\": \"dark\"} /* kept */\n}"},
		"the last key is empty": {"settings.json": `{"tools":{"allowed":["x"]},"tools":{"allowed":[]}}`},
		"a login and a project": {"oauth_creds.json": `{}`, "projects.json": `{}`},
	} {
		if err := geminiHomeRefusal(geminiTestHome(t, files)); err != nil {
			t.Errorf("%s: err = %v, want the launch to start", name, err)
		}
	}
	// An empty policies directory holds no rule.
	home := geminiTestHome(t, nil)
	if err := os.MkdirAll(filepath.Join(home.ConfigHome, "policies"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := geminiHomeRefusal(home); err != nil {
		t.Errorf("empty policies directory: %v", err)
	}
}

func TestGeminiHomeThatCannotBeCheckedRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	home := geminiTestHome(t, nil)
	// settings.json is a directory: it exists and cannot be read as a file.
	if err := os.Mkdir(filepath.Join(home.ConfigHome, "settings.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := geminiHomeRefusal(home)
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(err.Error(), "could not be checked") {
		t.Fatalf("err = %v, want a refusal rather than a home assumed empty", err)
	}
}

// A settings.json the child made into a FIFO would block the launch's read for ever,
// and one of unbounded size would be read whole.
func TestGeminiHomeWithAnUnreadableKindOfSettingsFileRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func(path string) error{
		"a FIFO":           func(path string) error { return syscall.Mkfifo(path, 0o600) },
		"an oversize file": func(path string) error { return os.WriteFile(path, make([]byte, geminiSettingsMaxBytes+1), 0o600) },
	} {
		home := geminiTestHome(t, nil)
		if err := build(filepath.Join(home.ConfigHome, "settings.json")); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- geminiHomeRefusal(home) }()
		select {
		case err := <-done:
			var re *runErr
			if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(err.Error(), "could not be checked") {
				t.Errorf("%s: err = %v, want a refusal rather than a home assumed empty", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the check did not return", name)
		}
	}
}

func TestGeminiConfigHomeMustBeNamedDotGemini(t *testing.T) {
	t.Parallel()
	for _, home := range []*ProviderHomeSnapshot{
		nil,
		{Driver: providerDriverGemini},
		{Driver: providerDriverGemini, ConfigHome: "/homes/probe/config"},
		{Driver: providerDriverGemini, ConfigHome: "/homes/probe/.gemini-other"},
	} {
		err := geminiHomeRefusal(home)
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusBadRequest {
			t.Errorf("home %+v: err = %v, want a 400 refusal", home, err)
		}
	}
}

// The child reads <GEMINI_CLI_HOME>/.gemini, so the variable is the parent of the
// configuration home the profile validated, even when the user home is elsewhere.
func TestGeminiCLIHomeIsTheParentOfTheValidatedConfigHome(t *testing.T) {
	t.Parallel()
	m := New(WithProviderDriver(NewGeminiDriver()))
	spec := m.childSpec(CreateRunParams{
		Transport:    TransportStreamJSON,
		WorkspaceDir: "/workspace/probe",
		ProviderHome: &ProviderHomeSnapshot{
			ProfileID: "ppf_probe", Driver: providerDriverGemini,
			ConfigHome: "/logins/tenant/gemini-cli/.gemini", UserHome: "/profile-homes/ppf_probe",
			AuthSource: AuthSourceAccountHome,
		},
	}, childDecision{})
	got := map[string]string{}
	for _, item := range spec.Env {
		got[item.Name] = item.Value
	}
	if got["GEMINI_CLI_HOME"] != "/logins/tenant/gemini-cli" || got["HOME"] != "/profile-homes/ppf_probe" {
		t.Fatalf("GEMINI_CLI_HOME=%q HOME=%q, want the config home's parent and the profile's user home", got["GEMINI_CLI_HOME"], got["HOME"])
	}
}

func TestGeminiAndOpenCodeRefuseTemplateControlsTheyCannotApply(t *testing.T) {
	t.Parallel()
	if got := nativeACPToolName(providerDriverGemini); got != "Gemini CLI" {
		t.Fatalf("name = %q", got)
	}
	if got := nativeACPToolName(providerDriverOpenCode); got != "OpenCode" {
		t.Fatalf("name = %q", got)
	}
	for _, driver := range []string{providerDriverClaude, providerDriverCodex, providerDriverGrok, ""} {
		if nativeACPToolName(driver) != "" {
			t.Errorf("%q must keep its own handling of template controls", driver)
		}
	}
	for _, tool := range []string{"Gemini CLI", "OpenCode"} {
		for what, p := range map[string]CreateRunParams{
			"instructions":      {Instructions: "be brief"},
			"tool restrictions": {AllowedTools: []string{"Read"}, PermissionMode: "default"},
		} {
			err := refuseACPUnsupportedControls(p, tool)
			if err == nil || !strings.Contains(err.Error(), "this "+tool+" driver has no mapping") || !strings.Contains(err.Error(), "refusing the launch rather than discarding them") {
				t.Errorf("%s with %s: err = %v, want the launch refused", tool, what, err)
			}
		}
		if err := refuseACPUnsupportedControls(CreateRunParams{}, tool); err != nil {
			t.Errorf("%s with no template controls: %v", tool, err)
		}
	}
}

func TestGeminiCLIEnvironmentIsNeverInheritedFromTheHost(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"GEMINI_CLI_HOME", "GEMINI_CLI_SYSTEM_SETTINGS_PATH", "GEMINI_CLI_TRUSTED_FOLDERS_PATH"} {
		if !forbiddenInheritedEnvName(name) {
			t.Errorf("%s must not reach a child from the host: it moves the CLI's home or its settings", name)
		}
	}
	// Explicit credentials on unbound legacy launches keep working.
	for _, name := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if forbiddenInheritedEnvName(name) {
			t.Errorf("%s was allowed to reach a child before and must stay allowed", name)
		}
	}
}

// Create and resume both mint the launch's authority, so a home the child could
// have written a pre-approval into is refused there, before any process starts.
func TestGeminiLaunchAuthorityRefusesAPreApprovingHomeAndAcceptsAClean(t *testing.T) {
	t.Parallel()
	m := New(WithProviderDriver(NewGeminiDriver()))
	launch := func(home *ProviderHomeSnapshot) error {
		home.Driver, home.AuthSource = providerDriverGemini, AuthSourceAccountHome
		_, env, err := m.mintLaunchAuthority(context.Background(), model.TenantID("tenant-test"), "", CreateRunParams{ProviderHome: home})
		if err == nil && len(env) != 0 {
			t.Fatalf("an account-home launch injects nothing: %v", env)
		}
		return err
	}
	err := launch(geminiTestHome(t, map[string]string{"settings.json": `{"tools":{"allowed":["run_shell_command"]}}`}))
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(err.Error(), "pre-approve") {
		t.Fatalf("pre-approving home: err = %v, want a 409 refusal", err)
	}
	keyHome := geminiTestHome(t, map[string]string{"settings.json": `{"tools":{"allowed":["run_shell_command"]}}`})
	keyHome.Driver, keyHome.AuthSource = providerDriverGemini, AuthSourceManagedInjection
	_, _, err = m.mintLaunchAuthority(context.Background(), model.TenantID("tenant-test"), "", CreateRunParams{ProviderHome: keyHome})
	if !errors.As(err, &re) || re.status != http.StatusConflict || !strings.Contains(err.Error(), "pre-approve") {
		t.Fatalf("pre-approving bound-key home: err = %v, want a 409 refusal", err)
	}
	if err := launch(geminiTestHome(t, nil)); err != nil {
		t.Fatalf("clean home: %v", err)
	}
}

// Readiness names both native Google sign-in and a bound Gemini API key.
func TestGeminiNothingToRunOnNamesTheNativeLoginAndGeminiKey(t *testing.T) {
	t.Parallel()
	tool, ok := resolveToolFor(providerDriverGemini)
	if !ok {
		t.Fatal("gemini-cli is a session tool")
	}
	err := tool.nothingToRunOn()
	var coded *codedRunErr
	if !errors.As(err, &coded) || coded.code != resolveCodeNothingToRunOn || !strings.Contains(err.Error(), "Sign it in under AI tools") ||
		!strings.Contains(err.Error(), "add a Gemini API key in Providers") {
		t.Fatalf("err = %v, want the nothing_to_run_on code and native login/key next steps", err)
	}
	// A tool a provider can be bound to keeps its own sentence.
	opencode, _ := resolveToolFor(providerDriverOpenCode)
	if err := opencode.nothingToRunOn(); !strings.Contains(err.Error(), "Providers has nothing it can use. Sign it in under AI tools, or add a key or a local model (Ollama) in Providers.") {
		t.Fatalf("opencode sentence changed: %v", err)
	}
}
