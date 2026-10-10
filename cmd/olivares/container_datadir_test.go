// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// `docker exec olivares olivares first-boot` carries no --data-dir, so it resolves the
// default. Inside a container that default must be the directory the engine serves, or
// the documented recovery command, `--help` and the serve banner all fail with exit 2.

// servedDataDir returns the --data-dir an argv serves.
func servedDataDir(t *testing.T, args []string) string {
	t.Helper()
	for i, arg := range args {
		if v, ok := strings.CutPrefix(arg, "--data-dir="); ok {
			return v
		}
		if arg == "--data-dir" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("no --data-dir in %q", args)
	return ""
}

// resolveDefaultDataDir runs the real defaultDataDir the way the image would: the given
// HOME and OLIVARES_DATA_DIR, no XDG_DATA_HOME, and a working directory with no ./olivares-data.
func resolveDefaultDataDir(t *testing.T, home, dataDirEnv string) string {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("OLIVARES_DATA_DIR", dataDirEnv)
	t.Setenv("XDG_DATA_HOME", "")
	t.Chdir(t.TempDir())
	got, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestContainerImageDefaultDataDirIsTheServedOne(t *testing.T) {
	for _, name := range []string{"Dockerfile", "Dockerfile.release", "Dockerfile.fips", "Dockerfile.stig", "Dockerfile.agentops"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatal(err)
			}
			// Only the final stage's ENV and CMD reach the running image.
			env := map[string]string{}
			var cmd []string
			for _, line := range strings.Split(string(data), "\n") {
				keyword, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
				switch strings.ToUpper(keyword) { // Dockerfile keywords are case-insensitive
				case "FROM":
					env, cmd = map[string]string{}, nil
				case "ENV":
					for _, pair := range strings.Fields(rest) {
						if k, v, ok := strings.Cut(pair, "="); ok {
							env[k] = v
						}
					}
				case "CMD":
					if err := json.Unmarshal([]byte(rest), &cmd); err != nil {
						t.Fatal(err)
					}
				}
			}
			served := servedDataDir(t, cmd)
			if got := resolveDefaultDataDir(t, env["HOME"], env["OLIVARES_DATA_DIR"]); got != served {
				t.Errorf("image CMD serves %s but a bare `olivares first-boot` resolves %s; "+
					"set ENV OLIVARES_DATA_DIR=%s in %s", served, got, served, name)
			}
		})
	}
}

type composeOlivares struct {
	Command     []string          `yaml:"command"`
	Environment map[string]string `yaml:"environment"`
}

func readComposeOlivares(t *testing.T, name string) composeOlivares {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "compose", name))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Services struct {
			Olivares composeOlivares `yaml:"olivares"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return file.Services.Olivares
}

func TestComposeDefaultDataDirIsTheServedOne(t *testing.T) {
	// Read every file first: resolveDefaultDataDir changes the working directory.
	base := readComposeOlivares(t, "docker-compose.yml")
	overlayPaths, err := filepath.Glob(filepath.Join("..", "..", "deploy", "compose", "docker-compose.*.yml"))
	if err != nil || len(overlayPaths) == 0 {
		t.Fatalf("no compose overlays found: %v", err)
	}
	overlays := map[string]composeOlivares{}
	for _, path := range overlayPaths {
		overlays[filepath.Base(path)] = readComposeOlivares(t, filepath.Base(path))
	}

	served := servedDataDir(t, base.Command)
	if got := resolveDefaultDataDir(t, base.Environment["HOME"], base.Environment["OLIVARES_DATA_DIR"]); got != served {
		t.Errorf("compose serves %s but a bare `olivares first-boot` resolves %s; "+
			"set OLIVARES_DATA_DIR: %s in the olivares service environment", served, got, served)
	}
	// An overlay that replaces the command layers over the base environment, so
	// the directory it serves must still be the one the base environment names.
	for name, overlay := range overlays {
		if !strings.Contains(strings.Join(overlay.Command, " "), "--data-dir") {
			continue
		}
		if got := servedDataDir(t, overlay.Command); got != base.Environment["OLIVARES_DATA_DIR"] {
			t.Errorf("%s serves %s but the base compose environment names OLIVARES_DATA_DIR=%s",
				name, got, base.Environment["OLIVARES_DATA_DIR"])
		}
	}
}

// The command an operator types, run through the real cobra command: no --data-dir,
// the image's environment. With the image's OLIVARES_DATA_DIR it finds the engine's
// record; without it (the defect) it looks under $HOME and exits 2.
func TestFirstBootWithoutDataDirFlagFollowsTheEnvironment(t *testing.T) {
	served := seedConsoleState(t, true)
	run := func(t *testing.T, dataDirEnv string) (string, error) {
		t.Helper()
		resolveDefaultDataDir(t, t.TempDir(), dataDirEnv) // HOME, OLIVARES_DATA_DIR, no XDG, empty cwd
		var out bytes.Buffer
		cmd := newFirstBootCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{}) // nil would make cobra read the test binary's own flags
		err := cmd.Execute()
		return out.String(), err
	}
	t.Run("with the image environment", func(t *testing.T) {
		out, err := run(t, served)
		if err != nil {
			t.Fatalf("first-boot: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Setup is PENDING") || !strings.Contains(out, served) {
			t.Errorf("the report does not name the served directory and its setup state:\n%s", out)
		}
	})
	t.Run("without it", func(t *testing.T) {
		_, err := run(t, "")
		if code := exitcode.From(err); code != exitcode.Usage {
			t.Errorf("exit code = %d (err %v), want %d: the directory under $HOME holds no engine record", code, err, exitcode.Usage)
		}
	})
}
