// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/secure"
)

// THE COMMAND THAT ANSWERS "WHAT NOW?" WITHOUT A SHELL.
//
// 2026-09-17, on a server: after `docker compose … up --wait` the container
// is healthy, `docker exec -it compose-olivares-1 bash` fails because the image is
// distroless, and nothing tells the operator how to continue.
//
// `olivares first-boot` is that answer, and it is shaped by two facts that are not
// negotiable:
//
//  1. THE IMAGE HAS NO SHELL. So the answer must be ONE argv the operator can pass
//     to `docker exec`, not a pipeline, not a `sed` range over a log.
//  2. THE SETUP TOKEN CANNOT BE SHOWN AGAIN. Only its SHA-256 is stored; the
//     plaintext exists for the length of one Fprintf (core/secure/setup.go). A
//     command that promised to print it would either be lying or would require
//     storing the secret in recoverable form, which is a real security regression
//     traded for a nicer transcript.
//
// So this command shows the addresses, says exactly which state setup is in, and —
// while setup is still pending — offers `--new-token`, which MINTS A FRESH ONE.
// That is the banner's own documented remedy ("delete setup.token and start again:
// a fresh token is minted on the next boot"), minus the restart: SetupToken.Verify
// reads the file on every call, so a running engine honours a token reissued by a
// second process at once.
//
// WHY --new-token GRANTS NOTHING. It requires write access to the data directory.
// Anything with that access already holds the audit signing key, the TLS private
// key and the store itself — reissuing a first-boot token is not a step up from
// there. And it is refused once an administrator exists, which is the property the
// token was ever protecting.

type firstBootOptions struct {
	dataDir  string
	newToken bool
}

// newFirstBootCmd reports where this installation's console answers and what the
// operator's next step is.
func newFirstBootCmd() *cobra.Command {
	o := firstBootOptions{}
	cmd := &cobra.Command{
		Use:           "first-boot",
		Short:         "Show this installation's console address and the state of first setup",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: "first-boot reads this installation's data directory and prints the address or\n" +
			"addresses the console answers at, plus whether first setup is still pending. It\n" +
			"needs no shell, no credential and no network: it is the command to run against a\n" +
			"distroless container whose startup banner has scrolled away or rotated out of the\n" +
			"log.\n\n" +
			"It does NOT print the one-time setup token. The engine stores only a SHA-256 of\n" +
			"that token, so the original cannot be recovered — it is shown once, at mint time.\n" +
			"While no administrator exists, --new-token mints a replacement and prints it once;\n" +
			"the running engine accepts it immediately and the previous token stops working.",
		Example: "  # In a Compose or Docker deployment, with no shell in the image\n" +
			"  docker compose -f deploy/compose/docker-compose.yml exec olivares olivares first-boot\n\n" +
			"  # Lost the token before setup was completed? Mint a replacement\n" +
			"  docker compose -f deploy/compose/docker-compose.yml exec olivares \\\n" +
			"    olivares first-boot --new-token\n\n" +
			"  # A local or systemd install\n" +
			"  olivares first-boot --data-dir /var/lib/olivares",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir := o.dataDir
			if dataDir == "" {
				resolved, err := defaultDataDir()
				if err != nil {
					return err
				}
				dataDir = resolved
			}
			return runFirstBoot(cmd.OutOrStdout(), dataDir, o.newToken)
		},
	}
	cmd.Flags().StringVar(&o.dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().BoolVar(&o.newToken, "new-token", false, "mint a replacement one-time setup token and print it once; refused after an administrator exists")
	return cmd
}

// reportWriter records the first write failure and stops forwarding bytes.
//
// It exists for ONE line: the replacement token. --new-token retires the previous
// token before minting the new one, so a write that silently failed would leave an
// operator with a token they never saw and an exit code that said it worked. The
// whole report is written through it so that line cannot be the only one checked
// and then drift apart from the rest.
type reportWriter struct {
	w   io.Writer
	err error
}

func (r *reportWriter) Write(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.w.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	r.err = err
	return n, err
}

// runFirstBoot prints the report. It is a plain function so a test can drive it
// over a data directory without a cobra command.
func runFirstBoot(dest io.Writer, dataDir string, newToken bool) error {
	out := &reportWriter{w: dest}
	state, stateErr := readConsoleState(dataDir)
	if os.IsNotExist(stateErr) {
		return exitcode.New(exitcode.Usage, fmt.Errorf(
			"no engine has recorded a console address in %s. Either no engine has started with this data directory, or it is not the directory the engine uses: start it (`olivares serve --data-dir %s`), or pass the right --data-dir",
			dataDir, dataDir))
	}
	// An unreadable record is reported and the rest of the report still runs: the
	// setup state below comes from a different file and is the half that matters
	// most to somebody who cannot get in.
	if stateErr != nil {
		fmt.Fprintf(out, "The recorded console address could not be read: %v\n\n", stateErr)
	}

	tok := secure.NewSetupToken(filepath.Join(dataDir, "setup.token"))
	pending := tok.Exists()

	fmt.Fprint(out, "=== OLIVARES AI — THIS INSTALLATION ===\n")
	fmt.Fprintf(out, "Data directory: %s\n", dataDir)
	if stateErr == nil {
		// The timestamp is the LAST boot that announced, not proof that anything
		// is running now. Say which of the two it is: an operator reading a stale
		// record as a live one is exactly the confusion this command exists to end.
		fmt.Fprintf(out, "Last started:   %s (recorded at boot; this command does not check that the engine is running)\n",
			state.BootedAt.Format(time.RFC3339))
		writeConsoleAddresses(out, state)
	}
	fmt.Fprintln(out)

	if !pending {
		fmt.Fprint(out, "Setup is COMPLETE: an administrator exists and the one-time setup token has\n"+
			"been consumed. Open the console at the address above and sign in. If nobody\n"+
			"knows the credentials, recover through `olivares superadmin --help` rather than\n"+
			"by deleting anything in the data directory.\n")
		if newToken {
			// Refusing is the whole guarantee. The token gates the creation of the
			// FIRST administrator; minting one after that would be a way to make a
			// second, and the refusal says so.
			fmt.Fprint(out, "\n--new-token was refused: a setup token exists only before the first\n"+
				"administrator is created, and one already exists here.\n")
			return withReportFailure(out, exitcode.New(exitcode.Err, nil))
		}
		fmt.Fprint(out, "=======================================\n")
		return withReportFailure(out, nil)
	}

	// The CONSOLE and the CLI, in that order, because both are real routes and the
	// walk measured that only one of them was ever named. `olivares auth bootstrap`
	// completes setup against the running engine without a browser, which is the
	// only route on a headless host — and it is the one an operator reading THIS
	// output is already positioned to take.
	fmt.Fprintf(out, "Setup is PENDING: no administrator exists yet. Finish it in the console with\n"+
		"the one-time token, or from this terminal by pasting that token in:\n\n"+
		"  olivares auth bootstrap --server %s \\\n"+
		"    --ca-cert %s \\\n"+
		"    --setup-token-file - \\\n"+
		"    --email you@example.com --password-file <file> --save-context\n",
		firstBootConsoleOrPlaceholder(state, stateErr), filepath.Join(dataDir, "tls.crt"))
	if !newToken {
		fmt.Fprintf(out, "\nThe token cannot be shown again: this engine stores only its SHA-256, so the\n"+
			"original exists nowhere. It was printed ONCE, when the engine first started —\n"+
			"in a container, read it back from the log:\n\n"+
			"  docker logs <container> | grep -A 6 'FIRST-BOOT SETUP'\n\n"+
			"If it is gone, mint a replacement — safe while no administrator exists, and the\n"+
			"running engine accepts it at once:\n\n"+
			"  olivares first-boot --data-dir %s --new-token\n"+
			"=======================================\n", dataDir)
		return withReportFailure(out, nil)
	}

	// The reissue itself. Consume-then-Ensure, in that order, so the window in
	// which two tokens are valid does not exist: the old hash is gone before the
	// new one is written.
	if err := tok.Consume(); err != nil {
		return fmt.Errorf("could not retire the previous setup token: %w", err)
	}
	plaintext, created, err := tok.Ensure()
	if err != nil {
		return fmt.Errorf("could not mint a replacement setup token: %w", err)
	}
	if !created || plaintext == "" {
		return fmt.Errorf("a replacement setup token was not minted; the previous one has been retired, so restart the engine to mint a fresh one")
	}
	fmt.Fprintf(out, "\nA replacement one-time token has been minted. The previous one no longer\n"+
		"works. This is shown ONCE and is single-use:\n\n         %s\n\n"+
		"=======================================\n", plaintext)
	// The token was minted and the previous one retired. If the bytes were not
	// accepted, the operator has no token on screen: say so loudly rather than
	// exit 0. Minting again is safe while no administrator exists, and the
	// message says which command does it.
	return withReportFailure(out, nil)
}

// withReportFailure turns a rejected or short write into the command's verdict,
// keeping any verdict the report had already reached.
func withReportFailure(out *reportWriter, verdict error) error {
	if out.err == nil {
		return verdict
	}
	failure := fmt.Errorf("the report was not written in full (%w); nothing on screen can be trusted to be complete. If --new-token was used, the previous token is already retired — run it again", out.err)
	if verdict == nil {
		return exitcode.New(exitcode.Err, failure)
	}
	return exitcode.New(exitcode.From(verdict), failure)
}

// writeConsoleAddresses prints the address block, preferring the operator's own
// declared address and otherwise listing everything the bind answers at. It
// repeats the engine's own recorded paragraph rather than composing a second
// description of the same thing.
func writeConsoleAddresses(out io.Writer, state consoleState) {
	if state.Declared {
		fmt.Fprintf(out, "Console:        %s\n", state.Browse)
		fmt.Fprint(out, "                (the address declared with --public-url / OLIVARES_PUBLIC_URL)\n")
		return
	}
	if len(state.Addresses) == 0 {
		fmt.Fprintf(out, "Console:        %s\n", state.Browse)
		return
	}
	fmt.Fprintf(out, "Console:        %s\n", state.Addresses[0])
	for _, addr := range state.Addresses[1:] {
		fmt.Fprintf(out, "                %s\n", addr)
	}
	if state.Advice != "" {
		fmt.Fprintf(out, "\n%s\n", state.Advice)
	}
}

// firstBootConsoleOrPlaceholder is the address to put in the printed command.
//
// When the console record could not be read, the report says so above and this
// returns a placeholder rather than an empty string: a command with a blank
// --server is one an operator would run and not understand, while <console-url>
// is visibly theirs to fill in.
func firstBootConsoleOrPlaceholder(state consoleState, stateErr error) string {
	if stateErr == nil && strings.TrimSpace(state.Browse) != "" {
		return state.Browse
	}
	return "<console-url>"
}
