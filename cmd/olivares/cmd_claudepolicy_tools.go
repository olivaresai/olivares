// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/spf13/cobra"
)

// checkinTool is one AI tool a workstation reports on the check-in: the contract's
// driver, version and state. The account, method and config directory never leave
// this host, so they are not fields here.
type checkinTool struct {
	Driver  string `json:"driver"`
	Version string `json:"version,omitempty"`
	State   string `json:"state"`
}

// checkinToolsSnapshot reads this workstation directly. The fleet connection is
// used only for the POST; its inventory is never a source for this report.
func checkinToolsSnapshot(cmd *cobra.Command, flags *authClientFlags) ([]checkinTool, error) {
	// Retain destination validation before any local observation, without reading
	// the destination's tool inventory.
	if _, err := mcpClient(cmd, flags); err != nil {
		return nil, err
	}
	ctx := cmd.Context()
	root, err := resolveAgentToolRoot("")
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	engine := toolInstallEngine(ctx)
	tools := make([]checkinTool, 0)
	for _, driver := range engine.DriverKeys() {
		facts, ok := driverfacts.Lookup(driver)
		if !ok {
			continue
		}
		selected := ""
		if !facts.NoPATHFallback {
			selected, _ = exec.LookPath(facts.Program)
		}
		if installed, found, latestErr := engine.LatestInstalled(ctx, root, driver); latestErr != nil {
			return nil, latestErr
		} else if found {
			selected = installed.Executable
		}
		// Select without executing a candidate. In particular, a damaged managed
		// release must not reach either the version probe or the native login reader.
		opts := toolinstall.DetectOptions{Driver: driver, Root: root, Home: home, PathEnv: os.Getenv("PATH"), NoAuthObservation: true, NoDigest: true}
		candidates, detectErr := engine.Detect(ctx, opts)
		if detectErr != nil {
			return nil, fmt.Errorf("read local %s: %w", driver, detectErr)
		}
		row := toolRow{Driver: driver}
		program := ""
		for _, candidate := range candidates {
			// A generic name such as agent is not evidence of this driver.
			if facts.NoPATHFallback && candidate.Origin != "managed" {
				continue
			}
			if selected != "" && candidate.Path != selected && candidate.Resolved != selected && !slices.Contains(candidate.Aliases, selected) {
				continue
			}
			if candidate.Match == toolinstall.MatchDamaged || candidate.Match == toolinstall.MatchUnverified {
				row.probeFailed = true
				continue
			}
			if !candidate.Executable {
				continue
			}
			row.Installed, row.Version, program = true, candidate.Version, candidate.Path
			opts.NoDigest = false
			opts.ProbePaths = []string{program}
			observed, probeErr := engine.Detect(ctx, opts)
			if probeErr != nil && toolinstall.KindOf(probeErr) != toolinstall.KindProbeFailed {
				return nil, fmt.Errorf("probe local %s: %w", driver, probeErr)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			row.probeFailed = probeErr != nil
			for _, result := range observed {
				if result.Path == program || result.Resolved == program || slices.Contains(result.Aliases, program) {
					row.Version = result.Version
					break
				}
			}
			break
		}
		if row.Installed && !row.probeFailed && facts.SignIn != "" {
			configDir, env := checkinLoginEnvironment(facts, home)
			signedIn, statusErr := agenttoolsapi.LocalLoginStatus(ctx, engine, confinedToolCommand(filepath.Dir(root)), driver, program, configDir, env)
			if statusErr != nil {
				row.probeFailed = true
			} else {
				row.SignedIn = &signedIn
			}
		}
		version := row.Version
		if !checkinVersionPattern.MatchString(version) {
			version = ""
		}
		tools = append(tools, checkinTool{Driver: driver, Version: version, State: checkinToolState(row)})
	}
	return tools, nil
}

// checkinLoginEnvironment follows native home overrides without forwarding keys,
// tokens or the workstation's unrelated environment to a child process.
func checkinLoginEnvironment(facts driverfacts.Facts, home string) (string, []string) {
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "TERM=dumb", "NO_COLOR=1", "BROWSER=true"}
	xdgConfig := ""
	for _, item := range []struct{ name, fallback string }{
		{"XDG_CONFIG_HOME", filepath.Join(home, ".config")},
		{"XDG_DATA_HOME", filepath.Join(home, ".local", "share")},
		{"XDG_STATE_HOME", filepath.Join(home, ".local", "state")},
		{"XDG_CACHE_HOME", filepath.Join(home, ".cache")},
	} {
		value := os.Getenv(item.name)
		if value == "" {
			value = item.fallback
		}
		env = append(env, item.name+"="+value)
		if item.name == "XDG_CONFIG_HOME" {
			xdgConfig = value
		}
	}
	config := os.Getenv(facts.ConfigHomeEnv)
	if config == "" {
		config = filepath.Join(home, facts.ConfigDir)
		if facts.Key == "opencode" {
			config = filepath.Join(xdgConfig, "opencode")
		}
	}
	value := config
	if facts.Key == "gemini-cli" {
		value = os.Getenv(facts.ConfigHomeEnv)
		if value == "" {
			value = home
		}
		config = filepath.Join(value, ".gemini")
		env = append(env, "NO_BROWSER=true", "GEMINI_CLI_NO_RELAUNCH=true")
	}
	env = append(env, facts.ConfigHomeEnv+"="+value)
	if facts.Key == "opencode" {
		env = append(env, "OPENCODE_DISABLE_AUTOUPDATE=1")
	}
	if facts.Key == "cursor" {
		env = append(env, "NO_OPEN_BROWSER=1")
	}
	return config, env
}

var checkinVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_~-]{0,63}$`)

// checkinToolState maps a tool row to the contract's state vocabulary. A tool whose
// sign-in could not be read is "error", not not_installed: the probe is what finds a
// tool on PATH. A tool with no sign-in of its own (SignedIn nil) is "unknown", not
// signed out: nothing asked it.
func checkinToolState(r toolRow) string {
	switch {
	case r.probeFailed:
		return "error"
	case !r.Installed:
		return "not_installed"
	case r.SignedIn == nil:
		return "unknown"
	case *r.SignedIn:
		return "ready"
	default:
		return "not_signed_in"
	}
}
