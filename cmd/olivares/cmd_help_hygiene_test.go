// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestDeprecatedJSONFlagIsHiddenButKept: the CLI audit of 09b found "--json  deprecated
// alias for -o json" in the help of seven commands. It still works for old scripts; the
// help shows -o json only.
func TestDeprecatedJSONFlagIsHiddenButKept(t *testing.T) {
	seen := 0
	walkCommands(newRootCmd(), func(c *cobra.Command) {
		f := c.Flags().Lookup("json")
		if f == nil || f.Usage != "deprecated alias for -o json" {
			return
		}
		seen++
		if !f.Hidden {
			t.Errorf("%s shows the deprecated --json in its help", c.CommandPath())
		}
	})
	if seen == 0 {
		t.Fatal("no command carries the deprecated --json; this test reads nothing")
	}
}

// TestHelpExamplesUseNoAccountNames: an example showed `--name claude-b`, the name of one
// of our own accounts. Examples use neutral names. (The help may still describe the
// engine's own naming rule for extra accounts, "claude, then claude-b".)
func TestHelpExamplesUseNoAccountNames(t *testing.T) {
	internal := regexp.MustCompile(`\b(claude|codex|grok)-[a-z]\b`)
	walkCommands(newRootCmd(), func(c *cobra.Command) {
		if m := internal.FindString(c.Example); m != "" {
			t.Errorf("%s: an example uses the account name %q", c.CommandPath(), m)
		}
	})
}

// TestHelpSaysEngineNotControlPlane: the CLI audit of 09b found "control-plane base URL",
// "verify the control plane", "kubeconfig-style client contexts for remote control planes"
// and the example host "plane.example.com" in the help. rule: no "control" framing in
// product copy. The help says "engine".
func TestHelpSaysEngineNotControlPlane(t *testing.T) {
	framing := regexp.MustCompile(`(?i)control[ -]planes?|kubeconfig|plane\.example\.com`)
	walkCommands(newRootCmd(), func(c *cobra.Command) {
		texts := []string{c.Short, c.Long, c.Example}
		c.Flags().VisitAll(func(f *pflag.Flag) { texts = append(texts, f.Usage) })
		for _, text := range texts {
			if m := framing.FindString(text); m != "" {
				t.Errorf("%s: help says %q", c.CommandPath(), m)
			}
		}
	})
}

func TestConfigHelpEnvironmentDescriptionsSayEngine(t *testing.T) {
	help := helpFor(t, "config")
	for _, name := range []string{
		"OLIVARES_BASE_URL", "OLIVARES_OIDC_CLIENT_ID", "OLIVARES_OIDC_CLIENT_SECRET",
		"OLIVARES_SAML_SP_ENTITY_ID", "OLIVARES_SERVER_URL",
	} {
		t.Run(name, func(t *testing.T) {
			_, description, found := strings.Cut(help, "  "+name+"\n    ")
			if !found {
				t.Fatalf("config help omits %s", name)
			}
			description, _, _ = strings.Cut(description, "\n")
			if !strings.Contains(description, "engine") {
				t.Errorf("%s description = %q; want engine wording", name, description)
			}
		})
	}
}

// TestConnectionFlagsAreListedOnlyInLogin: the CLI audit of 09b counted the connection
// flags as 9 of the 12-18 flags in the help of every basic command, though a person signs
// in once. Every command that carries the engine connection set hides them; `login` (and
// `auth login`) lists them. They keep working everywhere: the session tests pass --server,
// --token and --tenant to commands that now hide them.
func TestConnectionFlagsAreListedOnlyInLogin(t *testing.T) {
	logins := 0
	walkCommands(newRootCmd(), func(c *cobra.Command) {
		// A command carries the engine connection set when it has --pin-sha256 from it.
		if c.Flags().Lookup("pin-sha256") == nil || c.Flags().Lookup("url") != nil {
			return
		}
		// login and auth login; tool login signs a vendor tool in and is a client command.
		isLogin := c.CommandPath() == "olivares login" || c.CommandPath() == "olivares auth login"
		if isLogin {
			logins++
		}
		for _, name := range connectionFlags {
			f := c.Flags().Lookup(name)
			if f == nil {
				continue
			}
			// --token is deprecated for --token-file: login hides it too.
			listed := isLogin && name != "token"
			if listed && f.Hidden {
				t.Errorf("%s hides --%s; login is where they are listed", c.CommandPath(), name)
			}
			if !listed && !f.Hidden {
				t.Errorf("%s shows --%s in its help", c.CommandPath(), name)
			}
		}
	})
	if logins != 2 {
		t.Fatalf("found %d login commands with the connection flags, want 2 (login, auth login)", logins)
	}
}

// TestDeprecatedTokenStillWorksAndPointsToTokenFile: the CLI audit of 09b deprecates
// --token (a secret in argv) for --token-file. An old script keeps working: the bearer
// is sent, stdout is unchanged and stderr carries one line that says --token is
// deprecated and names --token-file, never the value. No help lists --token, login's
// included, and the "no token" sentence names --token-file.
func TestDeprecatedTokenStillWorksAndPointsToTokenFile(t *testing.T) {
	p := newSessionControlProbe(t, http.StatusAccepted, acceptedJSON)
	out, errb, err := execSessionCLI(t, nil,
		append([]string{"agent", "session", "input", "run-123", "--text", "go on"}, sessionCreds(p.URL)...)...)
	if err != nil {
		t.Fatalf("--token must keep working: %v stderr=%s", err, errb)
	}
	if got := p.lastAuth(); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want the --token bearer", got)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("stdout = %q, want nothing (the warning belongs on stderr)", out)
	}
	if errb != cliTokenArgvWarning+"\n" {
		t.Fatalf("stderr = %q, want exactly the deprecation line", errb)
	}
	for _, want := range []string{"--token is deprecated", "use --token-file"} {
		if !strings.Contains(errb, want) {
			t.Fatalf("stderr %q does not say %q", errb, want)
		}
	}
	if strings.Contains(errb, "test-token") {
		t.Fatalf("the warning repeats the bearer: %q", errb)
	}

	for _, path := range [][]string{{"login"}, {"auth", "login"}, {"agent", "session", "input"}} {
		help := helpFor(t, path...)
		if strings.Contains(help, "--token ") {
			t.Errorf("olivares %s --help lists the deprecated --token:\n%s", strings.Join(path, " "), help)
		}
	}
	if help := helpFor(t, "login"); !strings.Contains(help, "--token-file") {
		t.Errorf("olivares login --help does not list --token-file:\n%s", help)
	}

	msg := missingCLIValueError("token", "--token", "OLIVARES_TOKEN",
		cliResolvedConfig{ContextName: "prod", ConfigPath: "/home/ana/.config/olivares/config.yaml"}).Error()
	if !strings.Contains(msg, "pass --token-file,") {
		t.Fatalf("no-token sentence = %q, want it to name --token-file", msg)
	}
}
