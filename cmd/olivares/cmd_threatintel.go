// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/threatfeed"
)

// cmd_threatintel.go is the operator CLI for the AI threat-intel feed add-on:
// verify / apply / pull / sign / status. The verb LOGIC lives in the commercial
// enterprise/threatintel module reached through the build-neutral threatIntelSource
// seam (threatintelgate.go). The command is always registered (main.go), but in the
// default AGPL build the seam is nil, so each verb fails HONESTLY: the add-on needs
// an enterprise build + a valid OLIVARES_THREATINTEL_CONFIG (it does not pretend to
// work). Minimal data: the verbs print a FeedStatus summary; a private signing key
// is read from a file/env and NEVER logged.

// envThreatIntelSigningKey is the fallback source of the publisher private key for
// `threatintel sign` when --key is not given (a base64-std Ed25519 private key).
const envThreatIntelSigningKey = "OLIVARES_THREATINTEL_SIGNING_KEY"

var errThreatIntelNotActive = errors.New(
	"threat-intel feed not active: " + enterpriseEditionHint +
		", and it also needs a valid OLIVARES_THREATINTEL_CONFIG (see server logs for any config error)")

// resolveThreatIntelSource builds the source from the operator environment, or
// returns an honest error when the add-on is not available in this build/config.
func resolveThreatIntelSource() (threatIntelSource, error) {
	src := newThreatIntelSource(os.Getenv, slog.Default())
	if src == nil {
		return nil, errThreatIntelNotActive
	}
	return src, nil
}

func newThreatIntelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "threatintel",
		Short: "Manage the AI threat-intel catalog and its signed catalog releases (enterprise add-on)",
		Example: "  olivares threatintel status --crosswalk\n" +
			"  olivares threatintel verify ./threat-feed.json\n" +
			"  olivares threatintel apply ./threat-feed.json",
		Long: "threatintel manages the AI threat-intel add-on (enterprise): a base catalog compiled into\n" +
			"the build, plus optional signed feed artifacts you pin a publisher key for. Olivares operates\n" +
			"no curated feed distribution — the endpoint and the trusted keys are yours. Subcommands:\n" +
			"verify and apply signed feeds (fail-closed), pull from the configured endpoint, sign a feed\n" +
			"(publisher), and show the active feed + Claude/Anthropic governance crosswalk. Requires an\n" +
			"enterprise build and OLIVARES_THREATINTEL_CONFIG; the default AGPL build reports it is unavailable.",
	}
	cmd.AddCommand(
		threatIntelVerifyCmd(),
		threatIntelApplyCmd(),
		threatIntelPullCmd(),
		threatIntelSignCmd(),
		threatIntelStatusCmd(),
	)
	return cmd
}

func threatIntelVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "verify <catalog-file>",
		Short:   "Verify a signed catalog release (signature + expiry + schema); does not apply it",
		Long:    "verify checks a threat-intelligence feed's signature, schema and expiry without changing the active feed.",
		Example: "  olivares threatintel verify ./threat-feed.json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := resolveThreatIntelSource()
			if err != nil {
				return err
			}
			blob, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read feed file: %w", err)
			}
			st, err := src.Verify(blob, time.Now().UTC())
			if err != nil {
				return fmt.Errorf("feed verification failed (fail-closed, not applied): %w", err)
			}
			return printFeedStatus(cmd, st)
		},
	}
}

func threatIntelApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "apply <catalog-file>",
		Short:   "Verify and apply a signed catalog release (fail-closed, anti-rollback); persists it for the engine",
		Long:    "apply verifies a signed threat-intelligence feed, refuses rollback or invalid content, and persists it as the engine's last-known-good feed.",
		Example: "  olivares threatintel apply ./threat-feed.json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := resolveThreatIntelSource()
			if err != nil {
				return err
			}
			blob, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read feed file: %w", err)
			}
			st, err := src.Apply(blob, time.Now().UTC())
			if err != nil {
				return fmt.Errorf("feed apply failed (fail-closed; last-known-good retained): %w", err)
			}
			return printFeedStatus(cmd, st)
		},
	}
}

func threatIntelPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "pull",
		Short:   "Pull the catalog release from the configured endpoint, then verify and apply it (fail-closed)",
		Long:    "pull fetches the configured threat-intelligence feed, verifies it, and applies it without replacing the last-known-good feed on any failure.",
		Example: "  olivares threatintel pull",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			src, err := resolveThreatIntelSource()
			if err != nil {
				return err
			}
			st, err := src.Pull(cmd.Context(), time.Now().UTC())
			if err != nil {
				return fmt.Errorf("feed pull failed (fail-closed; last-known-good retained): %w", err)
			}
			return printFeedStatus(cmd, st)
		},
	}
}

func threatIntelSignCmd() *cobra.Command {
	var inPath, keyPath, outPath string
	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Sign an unsigned catalog envelope (publisher side; key minted with `olivares license keygen`)",
		Long: "sign wraps an unsigned feed envelope JSON into a signed feed blob, using a base64-std Ed25519\n" +
			"private key (mint it with `olivares license keygen`). The key is read from --key (a file) or the\n" +
			envThreatIntelSigningKey + " env var, and is never logged. Reads the envelope from --in (or stdin),\n" +
			"writes the signed blob to --out (or stdout).",
		Example: "  olivares threatintel sign --in feed-envelope.json --key publisher.key --out threat-feed.json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			src, err := resolveThreatIntelSource()
			if err != nil {
				return err
			}
			payload, err := readInput(cmd, inPath)
			if err != nil {
				return fmt.Errorf("read feed envelope: %w", err)
			}
			keyB64, err := readSigningKey(keyPath)
			if err != nil {
				return err
			}
			blob, err := src.Sign(payload, keyB64)
			if err != nil {
				return fmt.Errorf("sign feed: %w", err)
			}
			return writeOutput(cmd, outPath, blob)
		},
	}
	cmd.Flags().StringVar(&inPath, "in", "-", "unsigned feed envelope JSON file (\"-\" = stdin)")
	cmd.Flags().StringVar(&keyPath, "key", "", "base64-std Ed25519 private key file (else $"+envThreatIntelSigningKey+")")
	cmd.Flags().StringVar(&outPath, "out", "-", "signed feed output file (\"-\" = stdout)")
	return cmd
}

func threatIntelStatusCmd() *cobra.Command {
	var crosswalk bool
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Show the active catalog release (versions, expiry, channels) and the governance crosswalk summary",
		Long:    "status prints the active threat-intelligence feed's version, expiry and channels, or the Claude/Anthropic governance crosswalk with --crosswalk.",
		Example: "  olivares threatintel status --crosswalk",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			src, err := resolveThreatIntelSource()
			if err != nil {
				return err
			}
			if crosswalk {
				return printJSON(cmd, src.Crosswalk())
			}
			return printFeedStatus(cmd, src.Status(time.Now().UTC()))
		},
	}
	cmd.Flags().BoolVar(&crosswalk, "crosswalk", false, "print the Claude/Anthropic governance crosswalk instead of the feed status")
	return cmd
}

// --- small IO helpers (output to cmd streams; errors returned, never printed) ---

func printFeedStatus(cmd *cobra.Command, st threatfeed.FeedStatus) error { return printJSON(cmd, st) }

// printJSON is now format-aware despite the name it kept: it renders through
// renderStatusOut (E2), so `-o text` produces a readable report instead of
// the JSON these commands used to print regardless.
func printJSON(cmd *cobra.Command, v any) error {
	return renderReportOut(cmd, v)
}

func readInput(cmd *cobra.Command, path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" || path == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	return os.ReadFile(path)
}

func writeOutput(cmd *cobra.Command, path string, b []byte) error {
	if strings.TrimSpace(path) == "" || path == "-" {
		_, err := cmd.OutOrStdout().Write(append(b, '\n'))
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// readSigningKey reads the base64 private key from a file (--key) or the env
// fallback. It is never echoed/logged.
func readSigningKey(keyPath string) (string, error) {
	if p := strings.TrimSpace(keyPath); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("read signing key: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if env := strings.TrimSpace(os.Getenv(envThreatIntelSigningKey)); env != "" {
		return env, nil
	}
	return "", fmt.Errorf("no signing key: pass --key <file> or set $%s", envThreatIntelSigningKey)
}
