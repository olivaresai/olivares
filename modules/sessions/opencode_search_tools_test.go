// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// #3173: OpenCode's glob and grep run ripgrep: the rg on its PATH, else one it
// downloads from github.com, which a confined session cannot reach (measured on
// 1.19.4: "ripgrep execution failed"). The rg that `olivares tool install opencode`
// placed goes first on an OpenCode child's PATH, and a confined child may run it.
func TestOpenCodeLaunchPutsTheInstalledRipgrepFirstOnPATH(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	program := []byte("#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(dir, "rg"), program, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(program)
	m := New(WithOpenCodeSearchDir(dir, hex.EncodeToString(sum[:])))
	launch := func(driver string) LaunchSpec {
		spec := LaunchSpec{Confinement: &confine.Policy{}, Env: []EnvVar{{Name: "PATH", Value: "/profile/bin:/usr/bin"}}}
		_ = m.prepareOpenCodeSearch(CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: driver}}, &spec)
		return spec
	}
	spec := launch(providerDriverOpenCode)
	if path, _ := specEnvValue(spec, "PATH"); path != dir+string(os.PathListSeparator)+"/profile/bin:/usr/bin" {
		t.Fatalf("OpenCode PATH = %q, want the installed ripgrep's folder first", path)
	}
	if !slices.Contains(spec.Confinement.ReadOnly, dir) {
		t.Fatalf("a confined OpenCode child cannot run the installed ripgrep: %+v", spec.Confinement)
	}
	if path, _ := specEnvValue(launch(providerDriverClaude), "PATH"); path != "/profile/bin:/usr/bin" {
		t.Fatalf("another driver's PATH = %q, want it unchanged", path)
	}
	// An rg something wrote over is not the pinned program: it is not run.
	if err := os.WriteFile(filepath.Join(dir, "rg"), []byte("#!/bin/sh\necho planted\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if spec := launch(providerDriverOpenCode); len(spec.Confinement.ReadOnly) != 0 {
		t.Fatalf("a ripgrep that is not the pinned program was put on PATH: %+v", spec)
	}
	if err := os.Remove(filepath.Join(dir, "rg")); err != nil {
		t.Fatal(err)
	}
	if spec := launch(providerDriverOpenCode); len(spec.Confinement.ReadOnly) != 0 {
		t.Fatalf("with no ripgrep installed the launch changed: %+v", spec)
	}
}

// #3202: a damaged pinned ripgrep refuses the OpenCode launch, naming the fix that
// works, instead of launching a session whose glob and grep fail.
func TestOpenCodeLaunchRefusesADamagedRipgrep(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rg"), []byte("#!/bin/sh\necho planted\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("#!/bin/sh\n"))
	m := New(WithOpenCodeSearchDir(dir, hex.EncodeToString(sum[:])))
	spec := LaunchSpec{Confinement: &confine.Policy{}}
	err := m.prepareOpenCodeSearch(CreateRunParams{ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverOpenCode}}, &spec)
	if err == nil || !strings.Contains(err.Error(), "olivares tool install opencode") || strings.Contains(err.Error(), dir) || len(spec.Confinement.ReadOnly) != 0 {
		t.Fatalf("damaged ripgrep launch = %v, grants %v; want a refusal naming `olivares tool install opencode`, not the server's path, and no grant", err, spec.Confinement.ReadOnly)
	}
}
