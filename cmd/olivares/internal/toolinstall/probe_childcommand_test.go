// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

// probeChild is one child the engine's ChildCommand was asked to build.
type probeChild struct {
	rw, ro  []string
	program string
}

// recordChildren wires a ChildCommand that records each child and starts it
// unconfined, as the composition's builder would after confining it.
func recordChildren(e *Engine) *[]probeChild {
	var got []probeChild
	e.SetChildCommand(func(ctx context.Context, rw, ro []string, program string, args ...string) (*exec.Cmd, error) {
		got = append(got, probeChild{rw: rw, ro: ro, program: program})
		return exec.CommandContext(ctx, program, args...), nil
	})
	return &got
}

// An install's version probe is built by the engine's ChildCommand (#1430): it
// may write only its scratch home and names no readable path of its own: the
// release's lib/<program> comes from the shared confinement policy (#2900).
func TestInstallProbeIsBuiltByTheEngineChildCommand(t *testing.T) {
	t.Run("release archive (ollama)", func(t *testing.T) {
		engine, req, _ := archiveEngine(t, DriverOllama, archiveFixture(t, DriverOllama, nil), false)
		got := recordChildren(engine)
		plan, err := engine.PlanV2(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := engine.InstallV2(context.Background(), req, plan, io.Discard); err != nil {
			t.Fatal(err)
		}
		if len(*got) != 1 {
			t.Fatalf("children built through the ChildCommand: %+v, want the one probe", *got)
		}
		c := (*got)[0]
		staging := filepath.Dir(filepath.Dir(c.program))
		if !strings.HasSuffix(c.program, "/bin/ollama") || !strings.HasPrefix(c.program, req.DestRoot+string(filepath.Separator)) {
			t.Errorf("probe program %s, want the staged bin/ollama under %s", c.program, req.DestRoot)
		}
		if !slices.Equal(c.rw, []string{filepath.Join(staging, ".probe")}) {
			t.Errorf("probe writable %v, want only its scratch %s", c.rw, filepath.Join(staging, ".probe"))
		}
		if len(c.ro) != 0 {
			t.Errorf("probe readable %v, want nothing beyond its program", c.ro)
		}
	})
	t.Run("single binary (claude)", func(t *testing.T) {
		f := newFixture(t)
		f.publish(fxVersion)
		got := recordChildren(f.engine)
		if _, _, err := f.install(fxVersion); err != nil {
			t.Fatal(err)
		}
		if len(*got) != 1 {
			t.Fatalf("children built through the ChildCommand: %+v, want the one probe", *got)
		}
		c := (*got)[0]
		if !strings.HasPrefix(c.program, f.root+string(filepath.Separator)) || filepath.Base(c.program) != "claude" {
			t.Errorf("probe program %s, want the staged claude under %s", c.program, f.root)
		}
		if len(c.rw) != 1 || filepath.Base(c.rw[0]) != ".probe" || !strings.HasPrefix(c.rw[0], f.root+string(filepath.Separator)) {
			t.Errorf("probe writable %v, want only its scratch under %s", c.rw, f.root)
		}
		if len(c.ro) != 0 {
			t.Errorf("probe readable %v, want nothing beyond its program", c.ro)
		}

		// A detection's probe of the installed release is the engine's child too.
		*got = nil
		cands, err := f.engine.Detect(context.Background(), DetectOptions{Driver: "claude", Root: f.root, Home: t.TempDir(), Probe: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(*got) != 1 || len(cands) == 0 || cands[0].Probe == nil {
			t.Fatalf("detect probes through the ChildCommand: %+v, candidates %+v", *got, cands)
		}
	})
}

// Every provider's probe names its scratch writable: a confined child with no
// writable path is refused (confine.Policy), which would fail every install.
func TestEveryProviderProbeNamesItsScratch(t *testing.T) {
	dir := toolinstalltest.ExecCapableDir(t)
	exe := filepath.Join(dir, "bin", "tool")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	// One line every provider accepts as its own --version.
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 'claude codex grok ollama opencode gemini 1.2.3 (Claude Code)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, p := range installProbes() {
		t.Run(name, func(t *testing.T) {
			scratch := t.TempDir()
			var rw []string
			ctx := withChildCommand(context.Background(), func(ctx context.Context, w, _ []string, program string, args ...string) (*exec.Cmd, error) {
				if rw = w; len(w) == 0 {
					return nil, errors.New("a confined child needs at least one writable path")
				}
				return exec.CommandContext(ctx, program, args...), nil
			})
			if _, err := p.Probe(ctx, exe, scratch, ""); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(rw, []string{scratch}) {
				t.Errorf("probe writable %v, want only its scratch %s", rw, scratch)
			}
		})
	}
}

// installProbes is every provider's install probe, by name: one row per driver an
// install can stage. A new driver adds its row here and to the tests that range over it.
func installProbes() map[string]interface {
	Probe(context.Context, string, string, string) (ProbeReport, error)
} {
	return map[string]interface {
		Probe(context.Context, string, string, string) (ProbeReport, error)
	}{
		"claude": NewClaude(ClaudeOptions{}), "codex": NewCodex(CodexOptions{}), "grok": NewGrok(GrokOptions{}),
		"codex release": NewCodexRelease(ReleaseArchiveOptions{}), "ollama": NewOllama(ReleaseArchiveOptions{}),
		"opencode": NewOpenCode(ReleaseArchiveOptions{}), "gemini": newReleaseArchive(DriverGemini, ReleaseArchiveOptions{}),
	}
}

// An install probe is a read like any other: it runs the vendor's --version and
// nothing else, from an empty scratch home, in an environment of its own. Nothing of
// the operator's home or environment reaches it, so it runs none of their hooks or
// plugins, starts none of their MCP servers and reaches none of their logins.
func TestEveryInstallProbeRunsOnlyVersionFromItsScratchHome(t *testing.T) {
	operator := t.TempDir()
	t.Setenv("HOME", operator)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(operator, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(operator, ".codex"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(operator, ".config"))
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GH_TOKEN"} {
		t.Setenv(name, "operator-secret")
	}
	exe := filepath.Join(toolinstalltest.ExecCapableDir(t), "bin", "tool")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	// The stub reports how it was started, in the scratch home it was given (every
	// start appends), and prints one line every provider accepts as its own --version.
	stub := "#!/bin/sh\n{ echo \"args=$*\"; echo \"pwd=$(pwd)\"; env; } >> \"$HOME/ran\"\n" +
		"echo 'claude codex grok ollama opencode gemini 1.2.3 (Claude Code)'\n"
	if err := os.WriteFile(exe, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, p := range installProbes() {
		t.Run(name, func(t *testing.T) {
			scratch := t.TempDir()
			if _, err := p.Probe(context.Background(), exe, scratch, ""); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(scratch, "home", "ran"))
			if err != nil {
				t.Fatalf("the probe never ran the tool from its scratch home: %v", err)
			}
			ran := string(raw)
			var starts []string
			for line := range strings.SplitSeq(ran, "\n") {
				if strings.HasPrefix(line, "args=") {
					starts = append(starts, line)
				}
			}
			if !slices.Equal(starts, []string{"args=--version"}) {
				t.Errorf("the probe must start the tool once, with --version; it started %q:\n%s", starts, ran)
			}
			if strings.Contains(ran, operator) || strings.Contains(ran, "operator-secret") {
				t.Errorf("the operator's home or environment reached the probe:\n%s", ran)
			}
			for _, line := range strings.Split(ran, "\n") {
				key, value, ok := strings.Cut(line, "=")
				if !ok || key == "args" || key == "PATH" || key == "_" || !strings.HasPrefix(value, "/") {
					continue
				}
				if !strings.HasPrefix(value, scratch) {
					t.Errorf("%s=%s is outside the probe's scratch home %s", key, value, scratch)
				}
			}
		})
	}
}

// A ChildCommand that cannot build the probe stops the install; the staged file
// never runs unconfined in its place.
func TestInstallProbeThatCannotBeConfinedDoesNotRun(t *testing.T) {
	f := newFixture(t)
	f.publish(fxVersion)
	f.engine.SetChildCommand(func(context.Context, []string, []string, string, ...string) (*exec.Cmd, error) {
		return nil, errors.New("this host cannot confine")
	})
	_, _, err := f.install(fxVersion)
	requireKind(t, err, KindProbeFailed)
	if !strings.Contains(err.Error(), "this host cannot confine") {
		t.Errorf("refusal %q does not name the builder's error", err)
	}
	if n := f.ran(); n != 0 {
		t.Errorf("the staged executable ran %d times without its ChildCommand", n)
	}
}

// A probe that fails must show the tool's whole message: Node prints the
// error, its stack and its code on separate lines, and the first line alone
// ("node:internal/modules/run_main:76") named no cause (#2900).
func TestProbeRefusalKeepsTheToolsWholeMessage(t *testing.T) {
	script := "echo 'node:fs:441'; echo \"Error: EACCES: permission denied, open '/lib/gemini/gemini.js'\"; " +
		"echo '    at Object.readFileSync (node:fs:441:20)'; echo \"  code: 'EACCES',\"; echo 'Node.js v24.15.0'; exit 1"
	_, err := runProbe(t.Context(), probeSpec{Exe: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin"}, Budget: 10 * time.Second, Grace: time.Second})
	requireKind(t, err, KindProbeFailed)
	for _, want := range []string{"Error: EACCES: permission denied", "code: 'EACCES'", "Node.js v24.15.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	long := "head-marker\n" + strings.Repeat("noise line\n", 2000) + "code: 'ELAST'\n"
	_, err = runProbe(t.Context(), probeSpec{Exe: "/bin/sh", Args: []string{"-c", "printf '%s' \"$0\"; exit 1", long}, Env: []string{"PATH=/usr/bin:/bin"}, Budget: 10 * time.Second, Grace: time.Second})
	if err == nil || len(err.Error()) > 4096 || !strings.Contains(err.Error(), "head-marker") || !strings.Contains(err.Error(), "code: 'ELAST'") {
		t.Errorf("a long output must keep its head and its tail within 4 KiB: %d bytes %q", len(fmt.Sprint(err)), err)
	}
}

func TestProbeOutputExcerptBounds(t *testing.T) {
	whole := strings.Repeat("a", probeRefusalBytes)
	over := whole + "b"
	// A multi-byte rune straddling the head cut must not leave a broken byte.
	split := strings.Repeat("a", probeRefusalHead-1) + "\u00e9" + strings.Repeat("z", probeRefusalBytes)
	for name, tc := range map[string]struct {
		in      string
		want    string
		wantCut bool
	}{
		"empty":                 {in: " \n", want: ""},
		"trimmed":               {in: "\n  Node.js v24\n", want: "Node.js v24"},
		"exactly the bound":     {in: whole, want: whole},
		"one byte over":         {in: over, wantCut: true},
		"rune across the split": {in: split, wantCut: true},
	} {
		t.Run(name, func(t *testing.T) {
			got := probeOutputExcerpt(tc.in)
			if !utf8.ValidString(got) {
				t.Fatalf("excerpt is not valid UTF-8: %q", got)
			}
			if !tc.wantCut {
				if got != tc.want {
					t.Fatalf("excerpt %q, want %q", got, tc.want)
				}
				return
			}
			if !strings.Contains(got, "\n…\n") || len(got) > probeRefusalBytes+len("\n…\n") {
				t.Fatalf("a long output must be cut to its head and tail: %d bytes", len(got))
			}
			if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, tc.in[len(tc.in)-8:]) {
				t.Fatalf("the excerpt lost its head or its tail: %.20q … %.20q", got, got[len(got)-20:])
			}
		})
	}
}
