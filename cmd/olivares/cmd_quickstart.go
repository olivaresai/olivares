// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// newQuickstartCmd is the one-command first run: it starts the engine with the
// secure defaults (TLS-on, loopback-only, no default credentials) and prints a
// single, clear next step — open the embedded console and finish setup with a
// one-time token. It is `serve` with friendly defaults and a guided banner;
// everything it runs is the same secure path (runEngine).
func newQuickstartCmd() *cobra.Command {
	opts := serveOptions{
		engine:             "sqlite",
		checkpointInterval: time.Hour,
	}
	var quiet bool
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Start Olivares AI for the first time — secure by default, one command to the console",
		Long: "quickstart runs the engine with the secure defaults (TLS on, loopback-only, no\n" +
			"default credentials) and points you at the embedded console to create your first\n" +
			"administrator with a one-time token. It is the fastest safe way in; for production\n" +
			"options (systemd, Compose, Kubernetes, air-gapped) see INSTALL.md.",
		Example: `  # Start with defaults (loopback, self-signed TLS, SQLite)
  olivares quickstart

  # Start with a custom data directory
  olivares quickstart --data-dir /var/lib/olivares`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The first thing a first-time operator reads must be the product,
			// not its startup checks. The actionable panel below cannot move
			// before boot — it carries the one-time setup token, which only
			// exists once the engine is up — so a header goes first instead, and
			// says what the log about to appear is (E5).
			quickstartHeader(cmd.OutOrStdout(), opts.dataDir, quiet)
			if quiet {
				// Not a filter on what the engine checks — only on what this
				// first run prints. `serve`, the production path, is unchanged.
				slog.SetDefault(slog.New(slog.NewTextHandler(cmd.ErrOrStderr(),
					&slog.HandlerOptions{Level: slog.LevelError})))
			}
			announce := func(ctx context.Context, out io.Writer, eng *engine) error {
				return announceQuickstart(ctx, out, eng, consoleURL(opts.listen, false))
			}
			return runEngine(cmd.Context(), cmd.OutOrStdout(), opts, announce)
		},
	}
	cmd.Flags().StringVar(&opts.listen, "listen", "127.0.0.1:8443", "HTTP (REST + web console) listen address")
	cmd.Flags().StringVar(&opts.grpcListen, "grpc-listen", "127.0.0.1:8444", "gRPC listen address")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().BoolVar(&quiet, "quiet", false,
		"print only the guided panel, holding the engine's startup checks back to errors "+
			"(they are still evaluated, and `olivares status` reports the same posture)")
	cmd.AddCommand(newQuickstartGovernedRAGCmd())
	return cmd
}

// quickstartHeader is what the operator reads FIRST.
//
// Before the first sixty lines of a fresh `olivares quickstart` were engine
// startup checks — the welcome banner arrived at line 60 of the interleaved
// output, because runEngine boots before it can call announce (the panel carries
// the one-time setup token, which does not exist until the engine is up). The
// first impression of the product was a wall of WARN lines about postures the
// reader has no context for yet.
//
// The fix is ordering, not suppression: the checks still run and still print, so
// nothing an operator needs to know is lost. This header just says what they are
// and what comes after them. --quiet is there for anyone who wants the clean
// path; the log stays the default.
func quickstartHeader(out io.Writer, dataDir string, quiet bool) {
	if dataDir == "" {
		// Display only. If the default cannot be resolved, the boot right after this
		// banner fails with the real explanation; a banner must not crash, and must
		// not invent a path that is not the one about to be used.
		resolved, err := defaultDataDir()
		if err != nil {
			fmt.Fprintf(out, "\n=== OLIVARES AI — FIRST RUN ===\n"+
				"  Data dir:  not resolved yet — pass --data-dir or set OLIVARES_DATA_DIR\n")
			return
		}
		dataDir = resolved
	}
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	fmt.Fprintf(out, "\n=== OLIVARES AI — FIRST RUN ===\n"+
		"Starting the engine, secure by default: TLS on, loopback-only, no default\n"+
		"credentials. Data directory: %s\n", dataDir)
	if quiet {
		fmt.Fprint(out, "\nStartup checks are running (held back to errors by --quiet).\n"+
			"Your console URL and one-time setup token follow.\n\n")
		return
	}
	fmt.Fprint(out, "\nThe engine's startup checks print below — they report this deployment's\n"+
		"security posture, and several are WARN by design on a first run because\n"+
		"nothing is configured yet. Your console URL and one-time setup token come\n"+
		"AFTER them. Re-run with --quiet to see only the panel.\n\n")
}

// announceQuickstart prints the guided first-run panel: on a fresh install it
// mints the one-time setup token and points at the console wizard; once an
// administrator exists it simply points at the sign-in URL.
func announceQuickstart(ctx context.Context, out io.Writer, eng *engine, baseURL string) error {
	has, err := eng.authr.HasAnyUser(ctx)
	if err != nil {
		return err
	}
	if has {
		fmt.Fprintf(out, "\nOlivares AI is starting.\n"+
			"  Open the console and sign in:  %s\n"+
			"  (HTTPS with a self-signed certificate — your browser will warn once.)\n\n", baseURL)
		return nil
	}
	token, created, err := eng.setupTok.Ensure()
	if err != nil {
		return err
	}
	// Ensure returns EMPTY plaintext when a token already exists: it stores only a
	// hash, so the original cannot be recovered — it was shown once at mint time and
	// that is the design. Discarding `created` (this line used to read `token, _`)
	// meant that on the second quickstart of a data dir whose setup was never
	// completed, the welcome panel told the customer to "complete setup with this
	// one-time token" and then printed a BLANK LINE. There is no token to paste and
	// nothing on screen says so, which reads as the product being broken. Say what
	// is true and what to do instead.
	if !created && token == "" {
		fmt.Fprintf(out, "\n=== OLIVARES AI — SETUP STILL PENDING ===\n"+
			"The engine is starting and no administrator exists yet, but a setup token was\n"+
			"already issued for this data directory on an earlier run. It is stored as a hash\n"+
			"and CANNOT be shown again — that is deliberate.\n\n"+
			"  Console:  %s\n\n"+
			"If you still have that token, complete setup with it. If you do not, delete\n"+
			"%s and start again: a fresh token is minted on the next\n"+
			"boot. Removing it is safe while no administrator exists — the token gates only\n"+
			"first-boot setup.\n"+
			"=========================================\n\n",
			baseURL, filepath.Join(eng.dataDir, "setup.token"))
		return nil
	}
	fmt.Fprintf(out, "\n=== WELCOME TO OLIVARES AI ===\n"+
		"Starting the engine — secure by default (TLS on, loopback-only, no default\n"+
		"credentials). One step left: create your first administrator in the console.\n\n"+
		"  1. Open:   %s\n"+
		"     (HTTPS with a self-signed certificate on first boot — your browser will\n"+
		"      warn once; that is expected for a local install.)\n"+
		"  2. Complete setup with this one-time token (shown once, single-use):\n\n"+
		"         %s\n\n"+
		"Press Ctrl-C to stop. For production install paths, see INSTALL.md.\n"+
		"==============================\n\n", baseURL, token)
	return nil
}
