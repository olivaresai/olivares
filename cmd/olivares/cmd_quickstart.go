// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
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
	var quiet bool
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Start Olivares AI for the first time — secure by default, one command to the console",
		Long: "quickstart runs the engine with the secure defaults (TLS on, no default\n" +
			"credentials, a single-use setup token) and points you at the embedded console to\n" +
			"create your first administrator with that token. The console accepts connections\n" +
			"from the network: pass --listen 127.0.0.1:8443 to restrict it to this machine.\n" +
			"After that token it names the rest of the first hour: enroll a passkey, connect\n" +
			"one coding agent, and run olivares doctor. It is the fastest safe way in; for\n" +
			"production options (systemd, Compose, Kubernetes, air-gapped) see INSTALL.md. The\n" +
			"three first-hour shapes (local, team, hybrid) are in the docs-site First hour\n" +
			"guide.",
		Example: `  # Start with defaults (every interface, self-signed TLS, SQLite)
  olivares quickstart

  # Restrict the console to this machine
  olivares quickstart --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444

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
			// Not a filter on what the engine checks — only on what this first run
			// prints, and only its LEVEL. runEngine installs the one handler
			// (enginelog.go); this line used to install a DIFFERENT one, which is how
			// --quiet came to change the format of every line as well as its level.
			opts.quiet = quiet
			opts.publicURLSet = cmd.Flags().Changed("public-url")
			announce := func(ctx context.Context, out io.Writer, eng *engine, addr consoleAddress) error {
				return announceQuickstart(ctx, out, eng, addr)
			}
			return runEngine(cmd.Context(), cmd.OutOrStdout(), opts, announce)
		},
	}
	cmd.Flags().StringVar(&opts.listen, "listen", defaultHTTPListen, "HTTP (REST + web console) listen address. The default "+defaultHTTPListen+" is EVERY interface (0.0.0.0 and, where the kernel has IPv6, ::); bind 127.0.0.1:8443 to restrict it to this host")
	cmd.Flags().StringVar(&opts.publicURL, "public-url", "", publicURLFlagHelp)
	cmd.Flags().StringVar(&opts.grpcListen, "grpc-listen", defaultGRPCListen, "gRPC listen address. The default "+defaultGRPCListen+" is EVERY interface, like --listen; bind 127.0.0.1:8444 to restrict it")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().BoolVar(&quiet, "quiet", false,
		"print only the guided panel, holding the engine's startup checks back to errors "+
			"(they are still evaluated, and 'olivares status' reports the same posture)")
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
		"Starting the engine, secure by default: TLS on, no default credentials, a\n"+
		"single-use setup token. Data directory: %s\n", dataDir)
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
func announceQuickstart(ctx context.Context, out io.Writer, eng *engine, addr consoleAddress) error {
	baseURL := addr.URL()
	has, err := eng.authr.HasAnyUser(ctx)
	if err != nil {
		return err
	}
	if has {
		fmt.Fprintf(out, "\nOlivares AI is starting.\n"+
			"  Open the console and sign in:  %s\n"+
			"  (HTTPS with a self-signed certificate — your browser will warn once.)\n\n%s%s",
			baseURL, adviceBlock(addr.Advice), firstHourReturningNextSteps)
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
			"%s"+
			"%s"+
			"=========================================\n\n",
			baseURL, filepath.Join(eng.dataDir, "setup.token"), adviceBlock(addr.Advice), firstHourPendingNextSteps)
		return nil
	}
	fmt.Fprintf(out, "\n=== WELCOME TO OLIVARES AI ===\n"+
		"Starting the engine — secure by default (TLS on, no default credentials, a\n"+
		"single-use setup token). One step left: create your first administrator.\n\n"+
		"  1. Open:   %s\n"+
		"     (HTTPS with a self-signed certificate on first boot — your browser will\n"+
		"      warn once; that is expected for a local install.)\n"+
		"  2. Complete setup with this one-time token (shown once, single-use):\n\n"+
		"         %s\n\n"+
		// The CLI route goes DIRECTLY under the token it consumes: it is the
		// second half of step 2, and the address advice below is about step 1.
		// Printed after the advice, its "Or" pointed back across an unrelated
		// paragraph about passkeys.
		"%s"+
		"%s"+
		"%s"+
		"Press Ctrl-C to stop. For production install paths, see INSTALL.md.\n"+
		"==============================\n\n", baseURL, token,
		cliSetupRoute(baseURL, eng.dataDir), adviceBlock(addr.Advice), firstHourWelcomeNextSteps)
	return nil
}

// cliSetupRoute is step 2 WITHOUT a browser.
//
// MEASURED 2026-09-18 walking the first hour: every panel and every page sent the
// operator to the console to create the first administrator, and `olivares auth
// bootstrap` — which does exactly that, against the running engine, in 0.41 s —
// was named by nothing the product prints. An operator on a headless host, or one
// already in the terminal that just printed the token, had no way to learn the
// command exists.
//
// The flags are the ones this engine actually requires, not a shortened form: the
// certificate is self-signed on a first boot, so --ca-cert is not optional.
//
// The token is read from STDIN, and that is not a stylistic choice. An earlier
// version of this function pointed --setup-token-file at
// <data-dir>/setup.token, which is where the engine keeps the token's SHA-256 —
// the plaintext is stored NOWHERE by design (core/secure/setup.go), which is the
// same reason first-boot says it cannot be shown again. Running the printed
// command verbatim against a live engine answered HTTP 403, so the product would
// have printed a command that cannot work. Measured 2026-09-18; the stdin form
// completes in 277 ms.
//
// Stdin also keeps the token out of the process table, which --setup-token would
// not.
func cliSetupRoute(baseURL, dataDir string) string {
	return "     Or, without a browser, from this terminal — paste the token above:\n\n" +
		"         olivares auth bootstrap --server " + baseURL + " \\\n" +
		"           --ca-cert " + filepath.Join(dataDir, "tls.crt") + " \\\n" +
		"           --setup-token-file - \\\n" +
		"           --email you@example.com --password-file <file> --save-context\n\n"
}

// firstHourWelcomeNextSteps is the rest of the first hour, printed after the
// setup token. Measured 2026-09-17: the welcome panel stopped at step 2 and the
// operator had no product-owned next action (docs named passkey enrollment,
// agent connect and evidence; the binary did not). These lines are the product
// telling the operator what to do next. They must stay in the binary, not only
// in the First hour guide.
const firstHourWelcomeNextSteps = "  After setup, the product names the rest of the first hour:\n" +
	"  3. Sign in. Enroll a passkey on Identity → Privileged login before you add\n" +
	"     connectors or sources (AAL3). Open the console at the localhost URL, not\n" +
	"     the IP, so the passkey ceremony can complete.\n" +
	"  4. Connect one coding agent. Detect it on this host with:\n" +
	"         olivares agent tool detect\n" +
	"     Register it in the inventory (POST /v1/agents). Wire the Claude Code hook\n" +
	"     with `olivares agent managed-settings` and OLIVARES_HOOK_PEP_CONFIG. The\n" +
	"     First hour guide has local, team and hybrid shapes.\n" +
	"  5. Confirm this host with `olivares doctor`. It names the next first-hour\n" +
	"     step when a coding agent or the hook PEP is not yet wired.\n\n"

// firstHourReturningNextSteps is for a data directory that already has an
// administrator. The welcome token is gone; the operator still needs the next
// first-hour action.
const firstHourReturningNextSteps = "  Next: run `olivares doctor`. It names the next first-hour step when a coding\n" +
	"  agent or the hook PEP is not yet wired. See the First hour guide.\n\n"

// firstHourPendingNextSteps is for a restart before setup completed. The token
// cannot be shown again; the rest of the first hour still applies after setup.
const firstHourPendingNextSteps = "  After you complete setup, run `olivares doctor` for the next first-hour step.\n"

// adviceBlock renders the address paragraphs as their own block, or nothing at
// all. A panel with nothing to say about its address prints exactly what it
// printed before.
func adviceBlock(advice string) string {
	if advice == "" {
		return ""
	}
	return advice + "\n\n"
}
