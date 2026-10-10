// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newQuickstartCmd is the one-command first run: it starts the engine with the
// secure defaults (TLS on, no default credentials, a single-use setup token) and
// prints a single, clear next step — open the embedded console and finish setup
// with that token. It is `serve` with friendly defaults and a guided banner;
// everything it runs is the same secure path (runEngine).
//
// It binds every interface, like `serve` (binddefaults.go). What makes this path
// safe is the three properties above, none of which depends on the bind; saying
// "loopback-only" here, as this comment and two banner lines used to, described a
// default the product no longer has and a safety it never came from.
func newQuickstartCmd() *cobra.Command {
	opts := serveOptions{
		engine:             "sqlite",
		checkpointInterval: time.Hour,
	}
	var quiet, verbose bool
	var postgresRef string
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Start the engine for the first time and open the console",
		Long: "quickstart runs the engine with the secure defaults (TLS on, no default\n" +
			"credentials, a single-use setup token) and points you at the embedded console to\n" +
			"create your first administrator with that token. The console accepts connections\n" +
			"from the network: pass --listen 127.0.0.1:8443 to restrict it to this machine.\n" +
			"The console guides setup, sign-in and your first session. Engine logs are saved\n" +
			"in the data directory; --verbose also prints them here. For\n" +
			"production options (systemd, Compose, Kubernetes, air-gapped) see INSTALL.md. The\n" +
			"three first-hour shapes (local, team, hybrid) are in the docs-site First hour\n" +
			"guide.",
		Example: `  # Start with defaults (every interface, self-signed TLS, SQLite)
  olivares quickstart

  # Restrict the console to this machine
  olivares quickstart --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444

  # Start with a custom data directory
  olivares quickstart --data-dir /var/lib/olivares

  # Use PostgreSQL: roles, passwords, TLS and keys are generated (the URL stays out of argv)
  olivares quickstart --postgres env:PG_MAINTENANCE_URL`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if postgresRef != "" {
				resolved, err := resolveDSNRef(cmd.Context(), "--postgres", postgresRef, osGetenv)
				if err != nil {
					return err
				}
				if _, err := parseQuickstartPostgresURL(resolved); err != nil {
					return err
				}
				opts.postgres = resolved
			}
			opts.quiet = quiet
			opts.quickstart = true
			opts.verbose = verbose
			opts.consoleLog = cmd.ErrOrStderr()
			opts.publicURLSet = cmd.Flags().Changed("public-url")
			opts.loginProxies.set = cmd.Flags().Changed("login-trusted-proxies")
			announce := func(ctx context.Context, out io.Writer, eng *engine, addr consoleAddress) error {
				if err := announceQuickstart(ctx, out, eng, addr); err != nil {
					return err
				}
				_, err := fmt.Fprintf(out, "Log file: %s (use --verbose to show engine logs here).\n", filepath.Join(eng.dataDir, "olivares.log"))
				return err
			}
			return runEngine(cmd.Context(), cmd.OutOrStdout(), opts, announce)
		},
	}
	cmd.Flags().StringVar(&postgresRef, "postgres", "", "initialize PostgreSQL from a maintenance URL (file:<path> or env:<VAR> keeps its password out of the command line); role credentials, TLS and keys are generated automatically")
	cmd.Flags().StringVar(&opts.listen, "listen", defaultHTTPListen, "HTTP (REST + web console) listen address. The default "+defaultHTTPListen+" is EVERY interface (0.0.0.0 and, where the kernel has IPv6, ::); bind 127.0.0.1:8443 to restrict it to this host")
	cmd.Flags().StringVar(&opts.publicURL, "public-url", "", publicURLFlagHelp)
	cmd.Flags().StringVar(&opts.loginProxies.value, "login-trusted-proxies", "", loginTrustedProxiesFlagHelp)
	cmd.Flags().StringVar(&opts.grpcListen, "grpc-listen", defaultGRPCListen, "gRPC listen address. The default "+defaultGRPCListen+" is EVERY interface, like --listen; bind 127.0.0.1:8444 to restrict it")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().BoolVar(&quiet, "quiet", false,
		"print the guided panel only (the default); engine logs remain in the log file")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "also print engine logs in the terminal; retain them in the log file")
	cmd.MarkFlagsMutuallyExclusive("quiet", "verbose")
	cmd.AddCommand(newQuickstartGovernedRAGCmd())
	return cmd
}

// announceQuickstart gives the next step for the actual setup state.
func announceQuickstart(ctx context.Context, out io.Writer, eng *engine, addr consoleAddress) error {
	has, err := eng.authr.HasAnyUser(ctx)
	if err != nil {
		return err
	}
	next := firstHourReturningNextSteps
	setup := ""
	if !has {
		token, created, err := eng.setupTok.Ensure()
		if err != nil {
			return err
		}
		if created {
			next = firstHourWelcomeNextSteps
			setup = fmt.Sprintf("  one-time token (shown once): %s\n", token)
		} else {
			next = firstHourPendingNextSteps
			setup = "Setup is still pending. The token issued earlier cannot be shown again.\n" +
				"If you lost it, get a replacement: olivares first-boot --data-dir '" + strings.ReplaceAll(eng.dataDir, "'", `'\''`) + "' --new-token\n"
		}
	}
	var addresses strings.Builder
	fmt.Fprintf(&addresses, "Console: %s\n", addr.URL())
	if addr.wildcard {
		if addr.Declared {
			// The declared URL may be a proxy; include the listener's local URL.
			for _, a := range addr.Reachable {
				if a.IsLoopback() {
					fmt.Fprintf(&addresses, "Console (local): %s\n", a.WithHost("localhost").Origin)
					break
				}
			}
		}
		for _, a := range addr.Reachable {
			if !a.IsLoopback() && a.Origin != addr.URL() {
				fmt.Fprintf(&addresses, "Console (LAN): %s\n", a.Origin)
			}
		}
		if notice := wildcardBindNotice(addr); notice != "" {
			addresses.WriteString(notice + "\n")
		}
	} else if addr.loopback {
		addresses.WriteString("This engine listens on loopback only (this computer).\n")
	}
	_, err = fmt.Fprintf(out, "%s%s%sPress Ctrl-C to stop.\n", &addresses, next, setup)
	return err
}

const firstHourWelcomeNextSteps = "Next: Open the console; it guides setup, sign-in and your first session.\n"
const firstHourReturningNextSteps = "Next: Open the console and sign in to continue your work.\n"
const firstHourPendingNextSteps = "Next: Open the console to finish setup with the one-time token issued earlier.\n"
