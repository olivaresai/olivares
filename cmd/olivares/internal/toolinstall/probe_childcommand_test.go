// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall/toolinstalltest"
)

// probeChild is one child the engine's ChildCommand was asked to build.
type probeChild struct {
	rw, ro  []string
	program string
	// libs is whether the read-only grants existed when the probe was built.
	libs bool
}

// recordChildren wires a ChildCommand that records each child and starts it
// unconfined, as the composition's builder would after confining it.
func recordChildren(e *Engine) *[]probeChild {
	var got []probeChild
	e.SetChildCommand(func(ctx context.Context, rw, ro []string, program string, args ...string) (*exec.Cmd, error) {
		c := probeChild{rw: rw, ro: ro, program: program, libs: true}
		for _, path := range ro {
			if _, err := os.Stat(path); err != nil {
				c.libs = false
			}
		}
		got = append(got, c)
		return exec.CommandContext(ctx, program, args...), nil
	})
	return &got
}

// An install's version probe is built by the engine's ChildCommand (#1430): it
// may write only its scratch home, and a release archive's probe reads the
// runtime its program loads from lib/<driver>.
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
		if !slices.Equal(c.ro, []string{filepath.Join(staging, "lib", "ollama")}) || !c.libs {
			t.Errorf("probe readable %v (present %v), want the staged lib/ollama", c.ro, c.libs)
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
	providers := map[string]interface {
		Probe(context.Context, string, string, string) (ProbeReport, error)
	}{
		"claude": NewClaude(ClaudeOptions{}), "codex": NewCodex(CodexOptions{}), "grok": NewGrok(GrokOptions{}),
		"codex release": NewCodexRelease(ReleaseArchiveOptions{}), "ollama": NewOllama(ReleaseArchiveOptions{}),
		"opencode": NewOpenCode(ReleaseArchiveOptions{}), "gemini": newReleaseArchive(DriverGemini, ReleaseArchiveOptions{}),
	}
	for name, p := range providers {
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
