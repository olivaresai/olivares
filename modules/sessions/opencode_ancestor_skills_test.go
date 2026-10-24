// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/skills"
)

// #2997: in a session folder that is not a git repository, OpenCode 1.19.3 walks
// every ancestor for .claude/skills and .agents/skills and grants each found
// folder external_directory: allow (measured: 138 skills from one ancestor). The
// launch turns those scans off and lists the session folder's own two folders
// again, relative to it, so nothing above the folder is read.
func TestOpenCodeLaunchScansNoAncestorSkillFolders(t *testing.T) {
	for name, launch := range map[string]DriverLaunch{
		"plain":  {},
		"preset": {Preset: PresetAsk},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{}
			for _, e := range (openCodeDriver{}).LaunchEnv(launch) {
				env[e.Name] = e.Value
			}
			if env["OPENCODE_DISABLE_EXTERNAL_SKILLS"] != "1" {
				t.Fatalf("external skill scans stay on: %v", env)
			}
			var cfg struct {
				Skills struct {
					Paths []string `json:"paths"`
				} `json:"skills"`
			}
			if err := json.Unmarshal([]byte(env[envOpenCodeConfigContent]), &cfg); err != nil {
				t.Fatalf("inline configuration %q: %v", env[envOpenCodeConfigContent], err)
			}
			if want := []string{".claude/skills", ".agents/skills"}; !reflect.DeepEqual(cfg.Skills.Paths, want) {
				t.Fatalf("skills.paths = %v, want the session folder's own %v", cfg.Skills.Paths, want)
			}
		})
	}
}

// With the external scans off, OpenCode still reads skills from its own config
// folder $HOME/.opencode (measured), so assigned skills are delivered there.
func TestOpenCodeAssignedSkillsUseTheConfigFolderOpenCodeStillScans(t *testing.T) {
	runner := &fakeRunner{initSID: "opencode-skills"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, true))
	run, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	if err := mutateRunForWorkTest(m, tenant, run.RunRef, func(r model.Record) { r[colRunProfileDriver] = providerDriverOpenCode }); err != nil {
		t.Fatal(err)
	}
	src := sessionSkillsFixture{selection: skills.Selection{Digest: "fixture", Members: []skills.SelectedMember{{Name: "research"}}},
		files: []skills.DeliveryFile{{Path: "research/SKILL.md", Bytes: []byte("verified"), Mode: 0444}}}
	if err := m.UseSessionSkills(src, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	spec := LaunchSpec{Dir: t.TempDir(), Isolation: IsolationNative, Confinement: &confine.Policy{}, Env: []EnvVar{{Name: envUserHome, Value: t.TempDir()}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := m.prepareSessionSkills(t.Context(), ctx, tenant, run.RunRef, CreateRunParams{Actor: "user:u1", ActorKind: model.ActorUser}, &spec); err != nil {
		t.Fatal(err)
	}
	home := spec.Env[slices.IndexFunc(spec.Env, func(e EnvVar) bool { return e.Name == envUserHome })].Value
	if got, err := os.ReadFile(filepath.Join(home, ".opencode", "skills", "research", "SKILL.md")); err != nil || string(got) != "verified" {
		t.Fatalf("delivered skill = %q %v", got, err)
	}
	if !slices.Contains(spec.Confinement.Sealed, filepath.Join(home, ".opencode")) {
		t.Fatalf("delivery folder is not sealed: %v", spec.Confinement.Sealed)
	}
}
