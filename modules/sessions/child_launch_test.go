// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// What a session's child receives, with no process: the program, its argv, its
// folder, the exact environment in order, and the network and filesystem policy,
// for each driver, a profile, a gate grant, the runtime's grants and a vault
// secret. One row per launch shape; the values are probes no driver invents.
// Every row runs under a profile, as every create and resume does: an unprofiled
// launch is refused (resolveLaunchProfileInto, revalidateStoredProfile).
func TestChildLaunchPerDriver(t *testing.T) {
	gate := []EnvVar{
		{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:7/pep"},
		{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "pep-token"},
	}
	grants := func(p CreateRunParams) CreateRunParams {
		p.WorkspaceDir = "/srv/work"
		p.Model = "probe-model"
		p.EnvAllow = []string{"MY_FLAG"}
		p.secretEnvValues = []EnvVar{{Name: "MY_SECRET", Value: "secret-value"}}
		if p.ProviderHome.Driver == providerDriverClaude {
			// A profile gives every Claude launch its tool surface (applySessionPolicy);
			// the other tools negotiate theirs in their own protocol.
			p.ToolSurface, p.ToolSurfaceDeclared = []string{defaultToolSurface}, true
		}
		return p
	}
	// childSpec does not read the auth source: it decides which credential the mint
	// hands over (mintLaunchAuthority), and each row's cred is that credential.
	home := func(driver, auth string) *ProviderHomeSnapshot {
		return &ProviderHomeSnapshot{Driver: driver, AuthSource: auth, ConfigHome: "/homes/p/config", UserHome: "/homes/p/user"}
	}
	work := WorkSessionCredential{Token: "work-token", SessionRef: "ses_probe", RunRef: "run_probe"}
	comm := CommunicationSessionCredential{Token: "comm-token"}
	rows := []struct {
		name        string
		p           CreateRunParams
		cred        Credential
		resumeID    string
		providerEnv []EnvVar
		// noGateway launches on a node without an inference gateway: the engine
		// refuses a record-bound Claude launch on one that has it.
		noGateway bool
		want      string
	}{
		{name: "claude own login", p: grants(CreateRunParams{ProviderHome: home(providerDriverClaude, AuthSourceAccountHome)}), want: `
program /opt/probe/claude
arg "--input-format"
arg "stream-json"
arg "--output-format"
arg "stream-json"
arg "--verbose"
arg "--print"
arg "--replay-user-messages"
arg "--permission-mode"
arg "default"
arg "--model"
arg "probe-model"
arg "--tools"
arg "default"
dir /srv/work
env ANTHROPIC_BASE_URL=https://gateway.probe/v1
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env CLAUDE_CONFIG_DIR=/homes/p/config
env DISABLE_AUTOUPDATER=1
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net host
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [] []
`},
		{name: "claude resume, historical bearer", p: grants(CreateRunParams{ProviderHome: home(providerDriverClaude, "")}), cred: Credential{Token: "cred-token"}, resumeID: "claude-session-1", want: `
program /opt/probe/claude
arg "--input-format"
arg "stream-json"
arg "--output-format"
arg "stream-json"
arg "--verbose"
arg "--print"
arg "--replay-user-messages"
arg "--permission-mode"
arg "default"
arg "--model"
arg "probe-model"
arg "--tools"
arg "default"
arg "--resume"
arg "claude-session-1"
dir /srv/work
env ANTHROPIC_AUTH_TOKEN=cred-token
env ANTHROPIC_BASE_URL=https://gateway.probe/v1
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env CLAUDE_CONFIG_DIR=/homes/p/config
env DISABLE_AUTOUPDATER=1
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net host
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [] []
`},
		{name: "claude remote-control", p: grants(CreateRunParams{Transport: TransportRemoteControl, ProviderHome: home(providerDriverClaude, AuthSourceAccountHome)}), want: `
program /opt/probe/claude
arg "--remote-control"
arg "--permission-mode"
arg "default"
arg "--model"
arg "probe-model"
arg "--tools"
arg "default"
dir /srv/work
env ANTHROPIC_BASE_URL=https://gateway.probe/v1
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env CLAUDE_CONFIG_DIR=/homes/p/config
env DISABLE_AUTOUPDATER=1
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net host
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [] []
`},
		{
			name: "claude record-bound",
			p:    grants(CreateRunParams{ProviderHome: home(providerDriverClaude, AuthSourceManagedInjection)}),
			cred: Credential{Token: "cred-token", bound: BoundProvider{Kind: ProviderKindAnthropic, Endpoint: "https://api.anthropic.com"}},
			want: `
program /opt/probe/claude
arg "--input-format"
arg "stream-json"
arg "--output-format"
arg "stream-json"
arg "--verbose"
arg "--print"
arg "--replay-user-messages"
arg "--permission-mode"
arg "default"
arg "--model"
arg "probe-model"
arg "--tools"
arg "default"
arg "--settings"
arg "{\"env\":{\"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC\":\"1\",\"CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL\":\"1\"}}"
dir /srv/work
env ANTHROPIC_AUTH_TOKEN=cred-token
env ANTHROPIC_BASE_URL=https://gateway.probe/v1
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env CLAUDE_CONFIG_DIR=/homes/p/config
env DISABLE_AUTOUPDATER=1
env CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1
env CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
env CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net providers=[https://api.anthropic.com] controls=[http://127.0.0.1:7/pep]
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [anthropic] [https://api.anthropic.com]
`},
		{
			name:        "claude record-bound key",
			p:           grants(CreateRunParams{ProviderHome: home(providerDriverClaude, AuthSourceManagedInjection)}),
			cred:        Credential{bound: BoundProvider{Kind: ProviderKindAnthropic, Endpoint: "https://api.anthropic.com"}},
			providerEnv: []EnvVar{{Name: "ANTHROPIC_API_KEY", Value: "record-key"}, {Name: "ANTHROPIC_BASE_URL", Value: "https://api.anthropic.com"}},
			noGateway:   true,
			want: `
program /opt/probe/claude
arg "--input-format"
arg "stream-json"
arg "--output-format"
arg "stream-json"
arg "--verbose"
arg "--print"
arg "--replay-user-messages"
arg "--permission-mode"
arg "default"
arg "--model"
arg "probe-model"
arg "--tools"
arg "default"
arg "--settings"
arg "{\"env\":{\"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC\":\"1\",\"CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL\":\"1\"}}"
dir /srv/work
env ANTHROPIC_API_KEY=record-key
env ANTHROPIC_BASE_URL=https://api.anthropic.com
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env CLAUDE_CONFIG_DIR=/homes/p/config
env DISABLE_AUTOUPDATER=1
env CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1
env CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
env CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net providers=[https://api.anthropic.com] controls=[http://127.0.0.1:7/pep]
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [anthropic] [https://api.anthropic.com]
`},
		{name: "codex resume", p: grants(CreateRunParams{ProviderHome: home(providerDriverCodex, AuthSourceAccountHome)}), resumeID: "codex-thread-1", want: `
program codex
arg "-c"
arg "analytics.enabled=false"
arg "-c"
arg "features.plugins=false"
arg "-c"
arg "feedback.enabled=false"
arg "-c"
arg "otel.exporter=\"none\""
arg "-c"
arg "otel.trace_exporter=\"none\""
arg "-c"
arg "check_for_update_on_startup=false"
arg "app-server"
arg "--listen"
arg "stdio://"
dir /srv/work
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env CODEX_HOME=/homes/p/config
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net host
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [] []
`},
		{
			name:        "codex record-bound",
			p:           grants(CreateRunParams{ProviderHome: home(providerDriverCodex, AuthSourceManagedInjection)}),
			cred:        Credential{bound: BoundProvider{Kind: ProviderKindOpenAI, Endpoint: "https://api.openai.com/v1"}},
			providerEnv: []EnvVar{{Name: "OPENAI_API_KEY", Value: "provider-key"}},
			want: `
program codex
arg "-c"
arg "model_provider=\"olivares_record\""
arg "-c"
arg "model_providers.olivares_record={name=\"Olivares record\",base_url=\"https://api.openai.com/v1\",wire_api=\"responses\",requires_openai_auth=false,model_catalog_url=\"https://api.openai.com/v1/models\",env_key=\"OPENAI_API_KEY\"}"
arg "-c"
arg "features.api_key_model_discovery=false"
arg "-c"
arg "cli_auth_credentials_store=\"ephemeral\""
arg "-c"
arg "analytics.enabled=false"
arg "-c"
arg "features.plugins=false"
arg "-c"
arg "feedback.enabled=false"
arg "-c"
arg "otel.exporter=\"none\""
arg "-c"
arg "otel.trace_exporter=\"none\""
arg "-c"
arg "check_for_update_on_startup=false"
arg "app-server"
arg "--listen"
arg "stdio://"
dir /srv/work
env OPENAI_API_KEY=provider-key
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env CODEX_HOME=/homes/p/config
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net providers=[https://api.openai.com/v1] controls=[http://127.0.0.1:7/pep]
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [openai] [https://api.openai.com/v1]
`},
		{name: "grok", p: grants(CreateRunParams{ProviderHome: home(providerDriverGrok, AuthSourceAccountHome)}), want: `
program grok
arg "agent"
arg "--no-leader"
arg "--model"
arg "probe-model"
arg "stdio"
dir /srv/work
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env GROK_HOME=/homes/p/config
env GROK_DISABLE_AUTOUPDATER=1
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net host
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [] []
`},
		{name: "opencode", p: grants(CreateRunParams{ProviderHome: home(providerDriverOpenCode, AuthSourceAccountHome)}), want: `
program opencode
arg "acp"
arg "--hostname"
arg "127.0.0.1"
arg "--cwd"
arg "/srv/work"
dir /srv/work
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env OPENCODE_CONFIG_DIR=/homes/p/config
env XDG_CONFIG_HOME=/homes/p/user/.config
env XDG_DATA_HOME=/homes/p/user/.local/share
env XDG_STATE_HOME=/homes/p/user/.local/state
env XDG_CACHE_HOME=/homes/p/user/.cache
env OPENCODE_DISABLE_AUTOUPDATE=1
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net host
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [] []
`},
		{name: "gemini", p: grants(CreateRunParams{ProviderHome: home(providerDriverGemini, AuthSourceAccountHome)}), want: `
program gemini
arg "--acp"
dir /srv/work
env MY_SECRET=secret-value
env OLIVARES_WORK_TOKEN=work-token
env OLIVARES_WORK_SESSION_ID=ses_probe
env OLIVARES_WORK_RUN_REF=run_probe
env OLIVARES_COMMUNICATION_TOKEN=comm-token
env HOME=/homes/p/user
env GEMINI_CLI_HOME=/homes/p
env GEMINI_CLI_NO_RELAUNCH=true
env OLIVARES_HOOK_PEP_URL=http://127.0.0.1:7/pep
env OLIVARES_HOOK_PEP_TOKEN=pep-token
allow [MY_FLAG]
net host
fs rw=[/srv/work,/homes/p/config,/homes/p/user] ro=[] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation  waitdelay 7s
bound [] []
`},
	}
	opts := []Option{
		WithStopWaitDelay(7 * time.Second),
		WithProgram("/opt/probe/claude"),
		WithConfinement([]string{"/srv/olivares"}, false),
		WithProviderDriver(NewCodexDriver()), WithProviderDriver(NewGrokDriver()),
		WithProviderDriver(NewOpenCodeDriver()), WithProviderDriver(NewGeminiDriver()),
	}
	withGateway, noGateway := New(append(opts, WithInferenceBaseURL("https://gateway.probe/v1"))...), New(opts...)
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			m := withGateway
			if row.noGateway {
				m = noGateway
			}
			spec := m.childSpec(row.p, childDecision{cred: row.cred, work: work, communication: comm, resumeID: row.resumeID, gateEnv: gate, providerEnv: row.providerEnv})
			if got := describeChild(spec); got != strings.TrimPrefix(row.want, "\n") {
				t.Errorf("%s child:\n%s", row.name, got)
			}
		})
	}
}

// describeChild is one line per fact of the child, in a stable order.
func describeChild(spec LaunchSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "program %s\n", spec.Program)
	for _, arg := range spec.Args {
		fmt.Fprintf(&b, "arg %q\n", arg)
	}
	fmt.Fprintf(&b, "dir %s\n", spec.Dir)
	for _, e := range spec.Env {
		fmt.Fprintf(&b, "env %s=%s\n", e.Name, e.Value)
	}
	fmt.Fprintf(&b, "allow [%s]\n", strings.Join(spec.EnvAllow, ","))
	if p := spec.NetworkPolicy; p != nil {
		fmt.Fprintf(&b, "net providers=[%s] controls=[%s]\n", strings.Join(p.Providers, ","), strings.Join(p.Controls, ","))
	} else {
		b.WriteString("net host\n")
	}
	if p := spec.Confinement; p != nil {
		fmt.Fprintf(&b, "fs rw=[%s] ro=[%s] protect=[%s]\n", strings.Join(p.ReadWrite, ","), strings.Join(p.ReadOnly, ","), strings.Join(p.Protect, ","))
	} else {
		b.WriteString("fs none\n")
	}
	fmt.Fprintf(&b, "fs required=%v handles=%d truncate=%v\n", spec.ConfinementRequired, len(spec.ConfinementFiles), spec.ConfinementRequireTruncateProtection)
	fmt.Fprintf(&b, "isolation %s waitdelay %s\n", spec.Isolation, spec.WaitDelay)
	fmt.Fprintf(&b, "bound [%s] [%s]\n", spec.BoundProvider.Kind, spec.BoundProvider.Endpoint)
	return b.String()
}

// childMCPFixture configures the session MCP endpoint the way the engine's source does.
type childMCPFixture struct{ dir string }

func (f childMCPFixture) ConfigureSessionMCP(_ context.Context, _ model.TenantID, runRef, driver string, spec *LaunchSpec) (func(), error) {
	return ConfigureSessionMCP(spec, driver, f.dir, runRef, "https://127.0.0.1:7/session/mcp", "OLIVARES_HOOK_PEP_TOKEN")
}

// The contributions after the spec (git read, Claude hooks, session MCP) reach the
// child in one order, on create and on resume alike: what the runner is handed.
func TestChildLaunchContributionsOnCreateAndResume(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	useClaudeManagedSettingsDir(t, t.TempDir())
	fr := &fakeRunner{}
	gate := &spyGate{inner: LaunchDecision{Allowed: true, InjectEnv: []EnvVar{
		{Name: "OLIVARES_HOOK_PEP_URL", Value: "https://127.0.0.1:7/v1/hooks"},
		{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "pep-token"},
	}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(gate),
		WithClaudeHookPEP(dataDir, "/usr/local/bin/olivares"), WithConfinement([]string{"/srv/olivares"}, false))
	m.GitRead, m.GitReadDataDir = newFakeGitRead("https://github.com/acme/widgets.git"), dataDir
	m.SessionMCP = childMCPFixture{dataDir}

	profile, err := m.GetProfile(ctx, tenant, ensureRuntimeTestProfileRef(t, m, tenant))
	if err != nil {
		t.Fatal(err)
	}
	dto, err := createProfiledTestRun(t, m, ctx, tenant, gitReadParams(true))
	if err != nil {
		t.Fatal(err)
	}
	created := fr.lastSpec()
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumed := fr.lastSpec()
	if created.Dir == "" || resumed.Dir != created.Dir {
		t.Fatalf("resume started the child in %q, create in %q", resumed.Dir, created.Dir)
	}
	tempName := regexp.MustCompile(`(git-credential-|mcp-)[0-9]+`)
	normal := func(spec LaunchSpec) string {
		s := describeChild(spec)
		s = strings.ReplaceAll(s, dataDir, "<data>")
		s = strings.ReplaceAll(s, spec.Dir, "<folder>")
		s = strings.ReplaceAll(s, profile.ConfigHome, "<config>")
		s = strings.ReplaceAll(s, profile.UserHome, "<user>")
		s = strings.ReplaceAll(s, dto.RunRef, "<run>")
		return tempName.ReplaceAllString(s, "${1}*")
	}
	for name, got := range map[string]string{"create": normal(created), "resume": normal(resumed)} {
		want := map[string]string{"create": `
program claude
arg "--input-format"
arg "stream-json"
arg "--output-format"
arg "stream-json"
arg "--verbose"
arg "--print"
arg "--replay-user-messages"
arg "--permission-mode"
arg "default"
arg "--tools"
arg "default"
arg "--settings"
arg "<data>/run/<run>/pep-settings.json"
arg "--setting-sources"
arg ""
arg "--mcp-config"
arg "<data>/run/<run>/mcp-*.json"
arg "--strict-mcp-config"
dir <folder>
env ANTHROPIC_AUTH_TOKEN=tok-secret
env HOME=<user>
env CLAUDE_CONFIG_DIR=<config>
env DISABLE_AUTOUPDATER=1
env OLIVARES_HOOK_PEP_URL=https://127.0.0.1:7/v1/hooks
env OLIVARES_HOOK_PEP_TOKEN=pep-token
env GIT_CONFIG_COUNT=2
env GIT_CONFIG_KEY_0=credential.https://github.com/acme/widgets.helper
env GIT_CONFIG_VALUE_0=!f() { test "$1" = get && cat '<data>/run/<run>/git-credential-*'; }; f
env GIT_CONFIG_KEY_1=credential.https://github.com/acme/widgets.git.helper
env GIT_CONFIG_VALUE_1=!f() { test "$1" = get && cat '<data>/run/<run>/git-credential-*'; }; f
allow []
net host
fs rw=[<folder>,<config>,<user>] ro=[<data>/run/<run>/git-credential-*,<data>/run/<run>,<data>/run/<run>/mcp-*.json] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation native waitdelay 5s
bound [] []
`, "resume": `
program claude
arg "--input-format"
arg "stream-json"
arg "--output-format"
arg "stream-json"
arg "--verbose"
arg "--print"
arg "--replay-user-messages"
arg "--permission-mode"
arg "default"
arg "--tools"
arg "default"
arg "--settings"
arg "<data>/run/<run>/pep-settings.json"
arg "--setting-sources"
arg ""
arg "--mcp-config"
arg "<data>/run/<run>/mcp-*.json"
arg "--strict-mcp-config"
dir <folder>
env ANTHROPIC_AUTH_TOKEN=tok-secret
env HOME=<user>
env CLAUDE_CONFIG_DIR=<config>
env DISABLE_AUTOUPDATER=1
env OLIVARES_HOOK_PEP_URL=https://127.0.0.1:7/v1/hooks
env OLIVARES_HOOK_PEP_TOKEN=pep-token
env GIT_CONFIG_COUNT=2
env GIT_CONFIG_KEY_0=credential.https://github.com/acme/widgets.helper
env GIT_CONFIG_VALUE_0=!f() { test "$1" = get && cat '<data>/run/<run>/git-credential-*'; }; f
env GIT_CONFIG_KEY_1=credential.https://github.com/acme/widgets.git.helper
env GIT_CONFIG_VALUE_1=!f() { test "$1" = get && cat '<data>/run/<run>/git-credential-*'; }; f
allow []
net host
fs rw=[<folder>,<config>,<user>] ro=[<data>/run/<run>/git-credential-*,<data>/run/<run>,<data>/run/<run>/mcp-*.json] protect=[/srv/olivares]
fs required=false handles=0 truncate=false
isolation native waitdelay 5s
bound [] []
`}[name]
		if got != strings.TrimPrefix(want, "\n") {
			t.Errorf("%s child:\n%s", name, got)
		}
	}
}

// A resume gives the child fresh runtime grants, each exactly once, as create does.
func TestChildLaunchResumeCarriesTheRuntimeGrants(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{}
	m, _, tenant, clk := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	wireDualCredentialProbe(m, &dualCredentialProbe{now: clk.get})
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := fr.lastSpec()
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, actorU, actorKindU, ""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumed := fr.lastSpec()
	for _, name := range []string{"OLIVARES_WORK_TOKEN", "OLIVARES_WORK_SESSION_ID", "OLIVARES_WORK_RUN_REF", "OLIVARES_COMMUNICATION_TOKEN"} {
		before, after := envValues(created.Env, name), envValues(resumed.Env, name)
		if len(before) != 1 || len(after) != 1 {
			t.Fatalf("%s: create gave %d, resume %d; want exactly one each", name, len(before), len(after))
		}
		if strings.HasSuffix(name, "_TOKEN") && before[0] == after[0] {
			t.Fatalf("%s: resume reused the create-time grant", name)
		}
	}
	for name, spec := range map[string]LaunchSpec{"create": created, "resume": resumed} {
		if ref := envValues(spec.Env, "OLIVARES_WORK_RUN_REF")[0]; ref != dto.RunRef {
			t.Fatalf("%s: the work grant names run %q, want %q", name, ref, dto.RunRef)
		}
	}
}

// A resume keeps the workspace's read-only folders: the child still holds them
// read-only and does not start unconfined.
func TestChildLaunchResumeKeepsTheWorkspaceFolders(t *testing.T) {
	runner := &fakeRunner{}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, false))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "resume-folders")
	folder, tools := t.TempDir(), t.TempDir()
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": folder, "read_only_folders": []string{tools},
	}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	run := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"workspace_ref":        created.body["workspace_ref"].(string),
		"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant),
	}, tenantHdr(tenant))
	if run.code != http.StatusCreated {
		t.Fatalf("launch=%d %s", run.code, run.raw)
	}
	first := runner.lastSpec()
	runRef := run.body["run_ref"].(string)
	if stopped := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/stop", admin, nil, tenantHdr(tenant)); stopped.code != http.StatusOK {
		t.Fatalf("stop=%d %s", stopped.code, stopped.raw)
	}
	if resumed := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/resume", admin, nil, tenantHdr(tenant)); resumed.code != http.StatusOK {
		t.Fatalf("resume=%d %s", resumed.code, resumed.raw)
	}
	again := runner.lastSpec()
	if first.Dir == "" {
		t.Fatal("the session started with no folder")
	}
	for name, spec := range map[string]LaunchSpec{"create": first, "resume": again} {
		if !workspaceFolderHandleNamed(spec, tools) || !spec.ConfinementRequired || spec.Dir != first.Dir {
			t.Fatalf("%s: folder handle=%v required=%v dir=%q (create dir %q)", name,
				workspaceFolderHandleNamed(spec, tools), spec.ConfinementRequired, spec.Dir, first.Dir)
		}
	}
}

// launchChild names the step that refused the child, starts nothing after it and
// returns the spec it built; create and resume write the refusal from that stage.
// A step that fails first keeps the later ones from running: no hook settings are
// written for a launch the repository read credential refused.
func TestLaunchChildNamesTheStageThatFailed(t *testing.T) {
	useClaudeManagedSettingsDir(t, t.TempDir())
	pepWithoutToken := []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "https://127.0.0.1:7/v1/hooks"}}
	// With a token the hook step writes its settings when it runs: the first refusal
	// case proves it never ran after git_read refused.
	pep := append(slices.Clone(pepWithoutToken), EnvVar{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "pep-token"})
	gitRead := func(t *testing.T, m *Module, p *CreateRunParams) {
		m.GitRead, m.GitReadDataDir = newFakeGitRead("https://github.com/acme/widgets.git"), t.TempDir()
		p.GitRead, p.MayUseSecretEnv = gitReadBinding, true
	}
	// A record-bound Codex child whose CODEX_HOME is empty: the Codex sandbox check
	// cannot read its saved configuration and refuses it on any host.
	codex := []Option{WithProviderDriver(NewCodexDriver())}
	codexBound := Credential{bound: BoundProvider{Kind: ProviderKindOpenAI, Endpoint: "https://api.openai.com/v1"}}
	codexHome := func(p *CreateRunParams) { p.ProviderHome = &ProviderHomeSnapshot{Driver: providerDriverCodex} }
	for _, tc := range []struct {
		name   string
		opts   []Option
		setup  func(*testing.T, *Module, *CreateRunParams)
		cred   Credential
		gate   []EnvVar
		runner error
		stage  string
	}{
		{name: "repository read credential", setup: gitRead, stage: "repository read credential"},
		// After git_read and before session MCP: a session MCP source that would
		// refuse too is never reached.
		{name: "Codex sandbox check", opts: codex, cred: codexBound, setup: func(t *testing.T, m *Module, p *CreateRunParams) {
			codexHome(p)
			m.SessionMCP = presetMCPFailure{t}
		}, stage: "Codex sandbox check"},
		{name: "git_read before the Codex sandbox check", opts: codex, cred: codexBound, setup: func(t *testing.T, m *Module, p *CreateRunParams) {
			codexHome(p)
			gitRead(t, m, p)
		}, stage: "repository read credential"},
		{name: "hooks", gate: pepWithoutToken, stage: "session hook setup"},
		// Before session MCP: a session MCP source that would refuse too is never reached.
		{name: "hooks before session MCP", gate: pepWithoutToken, setup: func(t *testing.T, m *Module, _ *CreateRunParams) {
			m.SessionMCP = presetMCPFailure{t}
		}, stage: "session hook setup"},
		{name: "session MCP", setup: func(t *testing.T, m *Module, _ *CreateRunParams) {
			m.SessionMCP = presetMCPFailure{t}
		}, stage: "session MCP setup"},
		{name: "runner", runner: errors.New("synthetic spawn failure"), stage: "tool process launch"},
		{name: "first refusal wins", setup: gitRead, gate: pep, stage: "repository read credential"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			fr := &fakeRunner{launchErr: tc.runner}
			m, _, tenant, _ := newRuntimeHarness(t, append([]Option{WithRunner(fr), WithClaudeHookPEP(dataDir, "/usr/local/bin/olivares")}, tc.opts...)...)
			p := CreateRunParams{Transport: TransportStreamJSON, WorkspaceDir: t.TempDir(), ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
			if tc.setup != nil {
				tc.setup(t, m, &p)
			}
			spec, proc, stage, err := m.launchChild(t.Context(), t.Context(), tenant, "run-refused", &p, childDecision{cred: tc.cred, gateEnv: tc.gate})
			if err == nil || proc != nil || stage != tc.stage {
				t.Fatalf("stage=%q proc=%v err=%v, want the refusal of %q", stage, proc, err, tc.stage)
			}
			if spec.Program == "" || spec.Dir != p.WorkspaceDir {
				t.Fatalf("the refused child's spec was not returned: %+v", spec)
			}
			wantLaunched := 0
			if tc.runner != nil {
				wantLaunched = 1
			}
			if launched := len(fr.specs); launched != wantLaunched {
				t.Fatalf("the runner was asked %d times, want %d", launched, wantLaunched)
			}
			if tc.stage != "session hook setup" {
				if _, err := os.Stat(filepath.Join(dataDir, "run", "run-refused")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("hook settings were prepared for a launch refused at %q: %v", tc.stage, err)
				}
			}
		})
	}
}
