// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/audit"
)

func TestCompletionBashOutput(t *testing.T) {
	t.Parallel()
	root := newRootCmd()
	var buf bytes.Buffer
	if err := root.GenBashCompletionV2(&buf, true); err != nil {
		t.Fatalf("GenBashCompletionV2: %v", err)
	}
	out := buf.String()
	if len(out) == 0 {
		t.Fatal("bash completion output is empty")
	}
	if !strings.Contains(out, "_olivares") {
		t.Errorf("bash completion output does not contain _olivares:\n%s", out[:min(len(out), 200)])
	}
}

func TestCompletionZshOutput(t *testing.T) {
	t.Parallel()
	root := newRootCmd()
	var buf bytes.Buffer
	if err := root.GenZshCompletion(&buf); err != nil {
		t.Fatalf("GenZshCompletion: %v", err)
	}
	out := buf.String()
	if len(out) == 0 {
		t.Fatal("zsh completion output is empty")
	}
	if !strings.Contains(out, "#compdef") {
		t.Errorf("zsh completion output does not contain #compdef:\n%s", out[:min(len(out), 200)])
	}
}

func TestCompletionFishOutput(t *testing.T) {
	t.Parallel()
	root := newRootCmd()
	var buf bytes.Buffer
	if err := root.GenFishCompletion(&buf, true); err != nil {
		t.Fatalf("GenFishCompletion: %v", err)
	}
	out := buf.String()
	if len(out) == 0 {
		t.Fatal("fish completion output is empty")
	}
	if !strings.Contains(out, "complete") {
		t.Errorf("fish completion output does not contain 'complete':\n%s", out[:min(len(out), 200)])
	}
}

func TestFlagCompletions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		buildCmd func() *cobra.Command
		flag     string
		want     []string
	}{
		{"global-output", newRootCmd, "output", []string{"text", "json"}},
		{"agent-create-transport", newAgentSessionCreateCmd, "transport", []string{"stream-json", "remote-control"}},
		{"agent-create-permission-mode", newAgentSessionCreateCmd, "permission-mode", []string{"default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"}},
		{"agent-create-effort", newAgentSessionCreateCmd, "effort", []string{"low", "medium", "high", "xhigh", "max"}},
		{"agent-create-isolation", newAgentSessionCreateCmd, "isolation", []string{"native", "container", "sandbox"}},
		{"agent-ls-state", newAgentSessionListCmd, "state", []string{"pending", "running", "idle", "stopped", "failed", "cleaned"}},
		{"workspace-add-mode", newWorkspaceAddCmd, "mode", []string{"rw", "ro"}},
		{"workspace-add-dlp", newWorkspaceAddCmd, "dlp", []string{"label", "deny", "off"}},
		{"audit-verify-pubkey-alg", auditVerifyCmd, "pubkey-alg", []string{"ed25519", "ecdsa-p256-sha256", "ecdsa-p384-sha384", "rsa-pkcs1-sha256", "rsa-pss-sha256"}},
		{"config-generate-profile", configGenerateCmd, "profile", []string{"eval", "single-node-prod", "postgres-prod", "k8s"}},
		{"config-generate-engine", configGenerateCmd, "engine", []string{"sqlite", "postgres"}},
		{"serve-engine", newServeCmd, "engine", []string{"sqlite", "postgres"}},
		{"migrate-status-engine", migrateStatusCmd, "engine", []string{"sqlite", "postgres"}},
		{"eventing-create-role", eventingSubCreateCmd, "role", []string{"viewer", "editor", "admin", "owner"}},
		{"eventing-create-auth-type", eventingSubCreateCmd, "auth-type", []string{"none", "bearer", "basic", "header"}},
		{"eventing-deliveries-status", eventingDeliveriesListCmd, "status", []string{"queued", "delivering", "delivered", "dead", "denied"}},
		{"keys-wrap-purpose", keysWrapCmd, "purpose", []string{"audit", "catalog", "policy"}},
		{"db-check-engine", dbCheckCmd, "engine", []string{"sqlite", "postgres"}},
		{"db-init-sslmode", dbInitCmd, "sslmode", []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}},
		{"eventing-format", newEventingCmd, "format", []string{"text", "json"}},
		{"superadmin-format", newSuperadminCmd, "format", []string{"text", "json"}},
		{"db-format", newDBCmd, "format", []string{"text", "json"}},
		{"migrate-format", newMigrateCmd, "format", []string{"text", "json"}},
		{"secrets-format", newSecretsCmd, "format", []string{"text", "json"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cmd := c.buildCmd()

			// Look up the registered completion function for the flag.
			fn, ok := cmd.GetFlagCompletionFunc(c.flag)
			if !ok {
				t.Fatalf("no completion registered for --%s", c.flag)
			}
			got, dir := fn(cmd, nil, "")
			if dir != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("--%s: directive = %v, want ShellCompDirectiveNoFileComp", c.flag, dir)
			}
			if len(got) != len(c.want) {
				t.Fatalf("--%s: got %d completions %v, want %d %v", c.flag, len(got), got, len(c.want), c.want)
			}
			for i, w := range c.want {
				// cobra's completion protocol is "value\tdescription": the shell
				// shows the description beside the value. Compare the VALUE, so a
				// completion may explain itself without failing here (E6 gave
				// --isolation descriptions saying which value the launcher wires).
				if value, _, _ := strings.Cut(got[i], "\t"); value != w {
					t.Errorf("--%s[%d] = %q, want value %q", c.flag, i, got[i], w)
				}
			}
		})
	}
}

func TestWorkspaceRefCommandsUseWorkspaceCompletion(t *testing.T) {
	t.Parallel()
	want := reflect.ValueOf(completeWorkspaces).Pointer()
	for _, build := range []func() *cobra.Command{
		newWorkspaceRemoveCmd,
		newWorkspaceFilesCmd,
		newWorkspaceStatCmd,
		newWorkspaceGetCmd,
		newWorkspacePutCmd,
		newWorkspaceMkdirCmd,
		newWorkspaceMoveCmd,
		newWorkspaceRmCmd,
	} {
		cmd := build()
		if cmd.ValidArgsFunction == nil {
			t.Errorf("%s has no ValidArgsFunction", cmd.Use)
			continue
		}
		if got := reflect.ValueOf(cmd.ValidArgsFunction).Pointer(); got != want {
			t.Errorf("%s ValidArgsFunction is not completeWorkspaces", cmd.Use)
		}
	}

	for _, cmd := range []*cobra.Command{newWorkspaceAddCmd(), newWorkspaceListCmd()} {
		if cmd.ValidArgsFunction != nil {
			t.Errorf("%s must not complete workspace refs", cmd.Use)
		}
	}
}

func TestTextJSONFormatValidation(t *testing.T) {
	t.Parallel()
	tenant := "11111111-1111-1111-1111-111111111111"
	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"eventing", newEventingCmd(), []string{"subscriptions", "ls", "--tenant", tenant, "--format", "yaml"}},
		{"superadmin", newSuperadminCmd(), []string{"status", "--format", "yaml"}},
		{"db", newDBCmd(), []string{"check", "--format", "yaml"}},
		{"migrate", newMigrateCmd(), []string{"status", "--format", "yaml"}},
		{"secrets", newSecretsCmd(), []string{"ls", "--format", "yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cmd.SetArgs(tc.args)
			if err := tc.cmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid --format") {
				t.Fatalf("Execute() error = %v, want invalid --format", err)
			}
		})
	}
}

func TestDynamicCompletionsNoServer(t *testing.T) {
	// Cannot use t.Parallel with t.Setenv, but we need to guarantee the env
	// vars are cleared for the completion helpers.
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")

	names, dir := completeSessions(nil, nil, "")
	if len(names) != 0 {
		t.Errorf("completeSessions with no env: got %v, want empty", names)
	}
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("completeSessions directive = %v, want ShellCompDirectiveNoFileComp", dir)
	}

	names, dir = completeWorkspaces(nil, nil, "")
	if len(names) != 0 {
		t.Errorf("completeWorkspaces with no env: got %v, want empty", names)
	}
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("completeWorkspaces directive = %v, want ShellCompDirectiveNoFileComp", dir)
	}
}

// TestAuditExportFormatCompletionCoversEveryValidFormat is anchored to the
// engine's own accepted set rather than to a literal list, because a literal is
// exactly how this drifted: the CLI advertised, completed and error-messaged
// three formats while audit.ValidFormat accepted five, leaving the LEEF ledger
// export — the one a QRadar shop needs to ingest the tamper-evident chain — and
// the OCSF projection reachable but undiscoverable. A new format added to the
// engine now fails here until the CLI surfaces it.
func TestAuditExportFormatSurfacesDeriveFromTheEngineRegistry(t *testing.T) {
	t.Parallel()

	cmd := auditExportCmd()
	fn, ok := cmd.GetFlagCompletionFunc("format")
	if !ok {
		t.Fatal("audit export has no completion for --format")
	}
	got, _ := fn(cmd, nil, "")

	// Iterated from audit.Formats(), never from a copy of it: the literal list is
	// exactly how the CLI came to advertise three formats while the engine accepted
	// five, hiding the LEEF ledger export — the one a QRadar shop needs to ingest the
	// tamper-evident chain — behind an error message that said it did not exist.
	var want []string
	for _, f := range audit.Formats() {
		want = append(want, string(f))
	}
	if !slices.Equal(got, want) {
		t.Errorf("--format completion = %v, want the registry's %v", got, want)
	}

	// The operator-facing strings are built from the registry too, so they cannot
	// disagree with the completion.
	list := audit.FormatList()
	for _, text := range []string{cmd.Short, cmd.Flags().Lookup("format").Usage} {
		if !strings.Contains(text, list) {
			t.Errorf("operator-facing text %q does not carry the registry list %q", text, list)
		}
	}

	// And the error path an operator actually hits is EXECUTED, not inferred from
	// the help text: a wrong --format must name every accepted value.
	bad := auditExportCmd()
	bad.SetOut(io.Discard)
	bad.SetErr(io.Discard)
	bad.SetArgs([]string{"--tenant", "019f95b6-bb13-72a6-bf9e-702d8c4eab18", "--format", "not-a-format"})
	err := bad.Execute()
	if err == nil {
		t.Fatal("an unknown --format must fail")
	}
	if !strings.Contains(err.Error(), list) {
		t.Errorf("error %q does not name the accepted formats %q", err, list)
	}
	for _, f := range want {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error %q omits accepted format %q", err, f)
		}
	}
}
