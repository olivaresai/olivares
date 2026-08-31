// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/secadvisory"
)

// productModule is the OSV package name (ecosystem "Go") the self-check matches
// advisories against — this binary's own module path.
const productModule = "github.com/olivaresai/olivares/cmd/olivares"

// newSecurityCmd builds `olivares security`, whose `check` subcommand self-checks the
// running binary against a signed advisories feed: it verifies the feed OFFLINE
// against the release key, then reports every advisory that affects the running version,
// with the fix version to upgrade to. This is the product side of the advisory pipeline
// — the "are we affected?" answer an operator (or the console, via the same engine) needs
// without trusting an unauthenticated feed and without any network at check time.
func newSecurityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Security self-checks (advisory feed verification and affected-version reporting)",
		Long: "security is the product's own PSIRT surface. check asks a signed advisory feed\n" +
			"whether THIS version is affected and exits 7 when it is, so a fleet can be\n" +
			"swept without a human reading release notes. advisories and rulepack are the\n" +
			"publisher side: they build and sign the artifacts the fleet then verifies.\n\n" +
			"drill exercises the whole pipeline end to end and times it, because an\n" +
			"advisory path that was never rehearsed is not a response capability.",
		Example: "  olivares security check --feed advisories.json --product-version 26.7.0\n" +
			"  olivares security rulepack verify --in rulepack.json --pubkey \"$RELEASE_PUBLIC_KEY\"\n" +
			"  olivares security drill",
	}
	cmd.AddCommand(newSecurityCheckCmd(), securityDrillCmd())
	// (integration): the PSIRT advisory PRODUCER + the hot-reload rule-pack
	// channel (connectors/threatfeed), grafted alongside the check consumer.
	registerSecurityResponse(cmd)
	return cmd
}

func newSecurityCheckCmd() *cobra.Command {
	var (
		feedPath       string
		sigPath        string
		pubFlag        string
		productVersion string
		quiet          bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check a product version against a signed advisories feed",
		Long: strings.TrimSpace(`
Verify a signed security-advisories feed (OSV format) against the release key and report
which advisories affect a selected product version (the running version by default). The
feed and its detached signature are read from disk, so the check works fully offline
(air-gap: point --feed at the advisories file carried in an update or DDIL bundle).

Exit status is 0 when no advisory affects the selected version and 7 when at least one
does (so it composes in a health probe, fleet check, or CI gate). A build that declares
no version cannot be checked at all: that exits 8 and names the way out, because a clean
answer there would be an artifact of comparing against version zero, not a measurement.`),
		Example: "  olivares security check --feed advisories.json --product-version 26.7.0",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(feedPath) == "" {
				return fmt.Errorf("olivares security check: --feed <advisories.json> is required")
			}
			if strings.TrimSpace(sigPath) == "" {
				sigPath = feedPath + ".sig"
			}
			feedBytes, err := os.ReadFile(feedPath)
			if err != nil {
				return fmt.Errorf("read advisories feed: %w", err)
			}
			sig, err := os.ReadFile(sigPath)
			if err != nil {
				return fmt.Errorf("read advisories signature %s: %w", sigPath, err)
			}
			pub, keySrc, err := resolveReleaseKey(pubFlag)
			if err != nil {
				return fmt.Errorf("resolve release key: %w", err)
			}

			// Verify BEFORE parse: a tampered or wrong-key feed never reaches the check.
			feed, err := secadvisory.VerifyFeed(feedBytes, sig, pub)
			if err != nil {
				return fmt.Errorf("advisories feed did not verify against the %s release key: %w", keySrc, err)
			}

			checkVersion := strings.TrimSpace(productVersion)
			versionFromFlag := checkVersion != ""
			if !versionFromFlag {
				checkVersion = version
			}
			report, err := feed.Check(productModule, checkVersion)
			if err != nil {
				if !errors.Is(err, secadvisory.ErrVersionNotCheckable) {
					return err
				}
				// A non-version the OPERATOR typed is a malformed QUESTION, not an
				// unanswerable one — say so as a usage error, and keep exit 8 meaning
				// "this BUILD cannot be checked" so a fleet sweep can act on that alone.
				if versionFromFlag && !release.IsUnstamped(checkVersion) {
					return exitcode.New(exitcode.Usage, fmt.Errorf(
						"olivares security check: --product-version %q is not a semantic version (MAJOR.MINOR.PATCH)", checkVersion))
				}
				// Deliberately NOT gated on --quiet, and deliberately on the same stream
				// as the two verdicts. --quiet means "say nothing when UNAFFECTED", and
				// this is not that; and an operator who redirects stdout into a ticket
				// must not receive an empty file, because an empty file reads as "clean"
				// — which is the exact failure this command was fabricating.
				//
				// The -o json pane is added around these EXACT bytes, not in place of
				// them: renderOut runs this closure verbatim when no format was asked
				// for, so the stream, the wording and the --quiet exemption above are
				// the same decision they were, and the exit code below is unchanged.
				if rerr := renderOut(cmd, func(out io.Writer) error {
					fmt.Fprintf(out, "olivares %s: CANNOT DETERMINE whether any advisory affects this build.\n", displayVersion(checkVersion))
					fmt.Fprintf(out, "  cause:   %s.\n", unstampedCause(checkVersion))
					fmt.Fprintf(out, "           With no version it has no position in the release ordering, so no\n")
					fmt.Fprintf(out, "           advisory range can be evaluated against it. Reporting \"not affected\"\n")
					fmt.Fprintf(out, "           here would be an artifact of comparing against version zero, not a\n")
					fmt.Fprintf(out, "           measurement.\n")
					fmt.Fprintf(out, "  way out: name the version to check —\n")
					fmt.Fprintf(out, "             olivares security check --feed %s --product-version <MAJOR.MINOR.PATCH>\n", feedPath)
					fmt.Fprintf(out, "           A released binary carries its own stamp; only a build from source does not.\n")
					_, werr := fmt.Fprintf(out, "  the feed itself verified fine (%d advisories) — it is the VERSION that is unknown.\n", len(feed.Advisories))
					return werr
				}, securityCheckNotCheckable(checkVersion, len(feed.Advisories))); rerr != nil {
					return rerr
				}
				return exitcode.New(exitcode.Indeterminate, nil)
			}
			// An advisory about this product that nobody could evaluate leaves the
			// question open, so "no known advisory affects this version" is not
			// available: with nothing else matching, that is an abstention (exit 8),
			// not a clean bill. Same reason as the unstamped build above — the command
			// may only report what it actually measured. Not gated on --quiet.
			if len(report.Findings) == 0 && !report.Determined() {
				if rerr := renderOut(cmd, func(out io.Writer) error {
					fmt.Fprintf(out, "olivares %s: CANNOT DETERMINE whether any advisory affects this version.\n", checkVersion)
					fmt.Fprintf(out, "  cause:   %d of the %d advisory(ies) in this feed could not be evaluated, so\n", len(report.Unevaluable), len(feed.Advisories))
					fmt.Fprintf(out, "           \"not affected\" would be a claim about advisories this build never\n")
					fmt.Fprintf(out, "           read:\n")
					for _, u := range report.Unevaluable {
						fmt.Fprintf(out, "             - %s: %s\n", u.ID, u.Reason)
					}
					fmt.Fprintf(out, "  way out: this is a FEED problem, not a key problem — the signature verified.\n")
					fmt.Fprintf(out, "           Take it to the advisory publisher, or upgrade to a build that\n")
					_, werr := fmt.Fprintf(out, "           understands these ranges.\n")
					return werr
				}, securityCheckReport(checkVersion, len(feed.Advisories), report)); rerr != nil {
					return rerr
				}
				return exitcode.New(exitcode.Indeterminate, nil)
			}
			if len(report.Findings) == 0 {
				// --quiet gates the JSON pane too, and that is the reading that keeps the
				// two panes reporting the SAME facts: --quiet says "do not report the
				// unaffected result", not "report it in prose only". An operator who
				// asked for silence and got a document would have a flag that is honored
				// on one pane and ignored on the other, which is worse than either rule.
				// Exit 0 with no output is the answer, exactly as it is for text.
				if quiet {
					return nil
				}
				return renderOut(cmd, func(out io.Writer) error {
					_, werr := fmt.Fprintf(out, "olivares %s: no known advisory affects this version (feed verified, %d advisories).\n", checkVersion, len(feed.Advisories))
					return werr
				}, securityCheckReport(checkVersion, len(feed.Advisories), report))
			}
			if rerr := renderOut(cmd, func(out io.Writer) error {
				fmt.Fprintf(out, "olivares %s is AFFECTED by %d advisory(ies):\n", checkVersion, len(report.Findings))
				for _, f := range report.Findings {
					fix := f.FixedIn
					if fix == "" {
						fix = "no fixed release yet"
					} else {
						fix = "fixed in " + fix
					}
					sev := ""
					if f.Severity != "" {
						sev = " [" + f.Severity + "]"
					}
					fmt.Fprintf(out, "  - %s%s: %s (%s)\n", f.ID, sev, f.Summary, fix)
					if f.Reference != "" {
						fmt.Fprintf(out, "      %s\n", f.Reference)
					}
				}
				// Affected AND incomplete: the findings above are real, but they are not the
				// whole answer. Saying so keeps the count from being read as exhaustive.
				if !report.Determined() {
					fmt.Fprintf(out, "\n%d further advisory(ies) could not be evaluated, so this list may be incomplete:\n", len(report.Unevaluable))
					for _, u := range report.Unevaluable {
						fmt.Fprintf(out, "  - %s: %s\n", u.ID, u.Reason)
					}
				}
				_, werr := fmt.Fprintln(out, "\nRun `olivares upgrade` to move to a patched, signed release.")
				return werr
			}, securityCheckReport(checkVersion, len(feed.Advisories), report)); rerr != nil {
				return rerr
			}
			// Non-zero exit so a probe/gate can act on it, without printing a Go error.
			return errAffected
		},
	}
	cmd.Flags().StringVar(&feedPath, "feed", "", "path to the signed advisories feed (OSV JSON)")
	cmd.Flags().StringVar(&sigPath, "sig", "", "path to the detached signature (default: <feed>.sig)")
	cmd.Flags().StringVar(&pubFlag, "pubkey", "", "release public key (base64 or @file); default: the embedded key")
	cmd.Flags().StringVar(&productVersion, "product-version", "", "product version to check (default: the running binary version)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "print nothing when unaffected")
	// SilenceUsage/Errors on the affected sentinel so exit 1 is clean, not a usage dump.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}

// securityCheckResult is the -o json pane of `security check`: the SAME four
// desenlaces the prose reports, in the one shape all four share.
//
// THE KEY SET NEVER CHANGES WITH THE OUTCOME. `findings` and `unevaluable` are
// `[]` rather than absent when empty, and `cause` is `""` rather than absent when
// there is nothing to explain, so a consumer never has to branch on the shape
// before it can read the verdict. A key that appears only on some runs forces
// every script to guess, which is the cost this whole unit exists to remove.
//
// `affected` IS A THREE-STATE ON PURPOSE, and it is the one field in this lot
// that is not a plain mirror of a printed line. It is `null` — never `false` —
// whenever the check could not reach a verdict, because a JSON `false` there is
// exactly the sentence removed from the prose: "not affected" manufactured
// out of a version that has no position in the release ordering, or out of
// advisories this build never read. A machine that treats null as false is
// making that claim itself; one that reads `determined` first cannot.
//
// The three fields are therefore read in this order: `determined` says whether
// there is an answer, `affected` says what it is, and the two lists say what it
// rests on. The exit code carries the same three states (0 / 7 / 8).
type securityCheckResult struct {
	// Version is the version actually checked, RAW — the stamp or the
	// --product-version value, never the "(no version)" the headline prints,
	// which is a rendering choice for a human and not a version.
	Version string `json:"version"`
	// Determined is Report.Determined(): every advisory about this product was
	// decided one way or the other. False is an ABSTENTION, not a clean bill.
	Determined bool `json:"determined"`
	// Affected is nil when Determined is false. See the type comment.
	Affected *bool `json:"affected"`
	// Cause names why no verdict was possible when the VERSION is the problem
	// (an unstamped build, or a stamp that is not a semantic version). When the
	// FEED is the problem, `unevaluable` is the cause and this stays "".
	Cause string `json:"cause"`
	// FeedAdvisories is the number of advisories in the verified feed — the same
	// count the prose reports, and the denominator of `unevaluable`.
	FeedAdvisories int                        `json:"feed_advisories"`
	Findings       []securityCheckFinding     `json:"findings"`
	Unevaluable    []securityCheckUnevaluable `json:"unevaluable"`
}

// securityCheckFinding mirrors secadvisory.Finding with the CLI's snake_case
// keys. It is a local DTO rather than the engine type because that type carries
// no json tags at all (core/secadvisory/advisory.go:181), so marshaling it
// directly would publish Go field names — `FixedIn` — as the wire contract.
//
// Every field is emitted even when empty: `fixed_in: ""` is the machine form of
// the prose's "no fixed release yet", and dropping it would make "no patch
// exists" indistinguishable from "this tool forgot to say".
type securityCheckFinding struct {
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	Severity  string `json:"severity"`
	FixedIn   string `json:"fixed_in"`
	Reference string `json:"reference"`
}

// securityCheckUnevaluable mirrors secadvisory.Unevaluable, same reason.
type securityCheckUnevaluable struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// securityCheckNotCheckable builds the document for the desenlace where the
// VERSION has no position in the release ordering: no verdict, no findings, and
// `affected` explicitly null.
func securityCheckNotCheckable(version string, feedAdvisories int) securityCheckResult {
	return securityCheckResult{
		Version:        version,
		Determined:     false,
		Affected:       nil,
		Cause:          unstampedCause(version),
		FeedAdvisories: feedAdvisories,
		Findings:       []securityCheckFinding{},
		Unevaluable:    []securityCheckUnevaluable{},
	}
}

// securityCheckReport builds the document for the three desenlaces that reached
// the engine: affected, clean, and "the feed carries advisories nobody could
// evaluate". It derives `affected` from the SAME two expressions the prose
// branches on, so the pane cannot disagree with the sentence beside it.
func securityCheckReport(version string, feedAdvisories int, report secadvisory.Report) securityCheckResult {
	res := securityCheckResult{
		Version:        version,
		Determined:     report.Determined(),
		FeedAdvisories: feedAdvisories,
		Findings:       make([]securityCheckFinding, 0, len(report.Findings)),
		Unevaluable:    make([]securityCheckUnevaluable, 0, len(report.Unevaluable)),
	}
	for _, f := range report.Findings {
		res.Findings = append(res.Findings, securityCheckFinding{
			ID: f.ID, Summary: f.Summary, Severity: f.Severity,
			FixedIn: f.FixedIn, Reference: f.Reference,
		})
	}
	for _, u := range report.Unevaluable {
		res.Unevaluable = append(res.Unevaluable, securityCheckUnevaluable{ID: u.ID, Reason: u.Reason})
	}
	// AFFECTED is decidable in two of the three cases, and the third is the one
	// that matters: findings present is a verdict even when the list may be
	// incomplete (the prose says so and still exits 7), while NO findings and an
	// incomplete catalog is not a verdict at all.
	switch {
	case len(report.Findings) > 0:
		affected := true
		res.Affected = &affected
	case report.Determined():
		affected := false
		res.Affected = &affected
	}
	return res
}

// displayVersion renders the version in the abstention headline. An EMPTY stamp would
// otherwise print "olivares : CANNOT DETERMINE…", which reads like a formatting bug
// rather than the fact it is reporting.
func displayVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(no version)"
	}
	return v
}

// unstampedCause names WHY the version cannot be ordered, in the operator's terms. The
// two shapes are one defect but not one sentence: a build with no stamp at all, and a
// stamp that is not a version (`task build:bin` falls back to `git describe --always`,
// which yields a bare commit SHA when no tag is reachable).
func unstampedCause(v string) string {
	if release.IsUnstamped(v) {
		return "this binary declares no version (built from source without -X main.version)"
	}
	return fmt.Sprintf("this binary's version stamp %q is not a semantic version", v)
}

// errAffected is the sentinel that makes `security check` exit non-zero when the running
// version is affected, without rendering as a Go error message.
var errAffected = &affectedError{}

type affectedError struct{}

func (*affectedError) Error() string { return "" }
