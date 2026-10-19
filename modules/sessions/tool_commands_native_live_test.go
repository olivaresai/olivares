// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Real vendor processes, fresh homes, local catalog exchanges only. No prompt,
// model turn or real credential. Gemini requires a synthetic key to open ACP.
func TestToolCommands_RealNativeCatalogs(t *testing.T) {
	for _, driver := range []ProviderDriver{codexDriver{}, geminiDriver{}, openCodeDriver{}} {
		t.Run(driver.Key(), func(t *testing.T) {
			program, err := exec.LookPath(driver.DefaultProgram())
			if err != nil {
				t.Skipf("native %s is not installed on PATH", driver.Key())
			}
			work, home := t.TempDir(), t.TempDir()
			write := func(rel, body string) {
				t.Helper()
				p := filepath.Join(work, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(".agents/skills/catalog-probe/SKILL.md", "---\nname: catalog-probe\ndescription: Project skill probe\n---\nBody.\n")
			write(".opencode/commands/catalog-probe.md", "---\ndescription: Project command probe\n---\nNever execute.\n")
			write(".gemini/commands/catalog-probe.toml", "description = \"Project command probe\"\nprompt = \"Never execute\"\n")
			if err := os.MkdirAll(filepath.Join(home, ".codex"), 0755); err != nil {
				t.Fatal(err)
			}
			env := []EnvVar{{Name: "HOME", Value: home}, {Name: "CODEX_HOME", Value: filepath.Join(home, ".codex")},
				{Name: "GEMINI_CLI_HOME", Value: home}, {Name: "GEMINI_CLI_NO_RELAUNCH", Value: "true"},
				{Name: "GEMINI_TELEMETRY_ENABLED", Value: "false"},
				{Name: "XDG_CONFIG_HOME", Value: filepath.Join(home, ".config")},
				{Name: "XDG_DATA_HOME", Value: filepath.Join(home, ".local/share")},
				{Name: "XDG_CACHE_HOME", Value: filepath.Join(home, ".cache")},
				{Name: envOpenCodeDisableModelsFetch, Value: "1"}, {Name: envOpenCodeDisableAutoUpdate, Value: "1"},
				{Name: envNPMConfigOffline, Value: "true"}, {Name: envOpenCodeDisableShare, Value: "1"}}
			if driver.Key() == providerDriverGemini {
				env = append(env, EnvVar{Name: "GEMINI_API_KEY", Value: "synthetic-catalog-probe"})
			}
			runCtx, stop := context.WithCancel(t.Context())
			defer stop()
			proc, err := NewProcRunner().Launch(runCtx, LaunchSpec{Program: program, Args: driver.LaunchArgs(DriverLaunch{WorkDir: work}), Dir: work, Env: env, Isolation: IsolationNative, WaitDelay: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := proc.Stop(ctx); err != nil {
					t.Errorf("stop native tool: %v", err)
				}
			}()
			session := driver.OpenSession(DriverSessionConfig{Send: proc.Send, WorkDir: work, ClientName: "olivares", ClientVersion: "test", CallTimeout: 20 * time.Second, Preset: PresetReadOnly})
			go func() {
				for frame := range proc.Output() {
					session.Deliver(frame)
				}
				session.Close(nil)
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			if codex, ok := session.(*codexSession); ok {
				if _, err := codex.conn.call(ctx, codexMethodInitialize, codexInitializeParams{ClientInfo: codexClientInfo{Name: "olivares", Version: "test"}}, 20*time.Second); err != nil {
					t.Fatal(err)
				}
				if err := codex.conn.notify(ctx, codexMethodInitialized, nil); err != nil {
					t.Fatal(err)
				}
			} else if _, err := session.Handshake(ctx); err != nil {
				t.Fatal(err)
			}
			reader := session.(commandCatalogReader)
			var items []toolCommand
			var reported bool
			for deadline := time.Now().Add(5 * time.Second); ; {
				items, reported, err = reader.commandCatalog(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if reported || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !reported {
				t.Fatal("native tool did not report its catalog")
			}
			got := toolCommandsDTO{Reported: reported, Commands: items}
			name := "catalog-probe"
			if driver.Key() == providerDriverGemini {
				name = "memory show"
			}
			c := commandByName(t, got, name)
			t.Logf("native %s: reported %d entries; command %s description=%q insert_text=%q", driver.Key(), len(items), c.Name, c.Description, c.InsertText)
			if driver.Key() == providerDriverCodex {
				write(".agents/skills/added-later/SKILL.md", "---\nname: added-later\ndescription: Added during the session\n---\nBody.\n")
				items, reported, err = reader.commandCatalog(ctx)
				if err != nil || !reported {
					t.Fatalf("refresh: %v reported=%v", err, reported)
				}
				commandByName(t, toolCommandsDTO{Commands: items}, "added-later")
				t.Log("native Codex: new project skill appeared without restarting the tool")
			}
		})
	}
}
