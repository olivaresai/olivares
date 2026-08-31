// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_claudepolicy.go drives the managed-* authoring console
// (/v1/m/claude-policy, modules/governance/claudepolicy.go): validate, dry-run
// and publish a versioned policy document for one of the four Claude Code
// managed surfaces, then see where it actually landed.
//
// IT IS NOT `agent managed-settings`, AND THE DIFFERENCE MATTERS. That command
// renders a managed-settings file LOCALLY, on this host. This one authors the
// tenant's versioned policy on the control plane and signs an artifact
// distribution agents pull. Same subject, opposite direction.
//
// TWO EXIT-CODE DECISIONS THIS FILE MAKES, both about not calling an unknown a
// pass:
//
//	`validate` answers 200 whether or not the document is good — the verdict is in
//	the body (validateResult, claudepolicy.go:179). So a document with validation
//	ERRORS exits 7, not 0. A CI step that ran `validate` and branched on the exit
//	code would otherwise have shipped every broken policy it was given.
//
//	`dry-run` IS NOT THAT SHAPE, and this line says so because it was measured
//	rather than inferred from its neighbor: an invalid document is refused with
//	HTTP 400 (claudepolicy.go:300) and the 200 body it does return carries neither
//	`ok` nor `diagnostics` (dryRunResult, claudepolicy.go:197). It still goes
//	through the same report reader, so an engine that ever moved to a
//	200-with-verdict could not slip past — but this file must not advertise an
//	exit 7 the engine cannot produce.
//
//	`checkin` answers 200 with verified=false when the artifact hash a
//	distribution agent echoes does NOT match what was signed — a tampered or
//	stale artifact. That is exit 7 as well, and the command says which.
//
// PAGINATION: this family declares none and honors none. `versions` returns
// every revision in one answer (claudepolicy.go:465 lists them all), so no
// --cursor is offered here. The census expected these two governance-hosted
// families to inherit cursor+limit; measured against the handlers, they do not.

const claudePolicyModule = "claude-policy"

// claudePolicySurfaces are the four managed-* surfaces the console authors. The
// engine is authoritative (surfaceOf, claudepolicy.go:256); this list only turns
// a typo into a usage error before a request is sent, and it is stated in the
// same words the engine uses so the two cannot disagree quietly.
var claudePolicySurfaces = []string{"managed-settings", "hooks", "managed-mcp", "sandbox"}

func newClaudePolicyCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "claude-policy",
		Short: "Author, publish and track the Claude Code managed-* policy surfaces",
		Long: "claude-policy is the authoring console for the four managed surfaces —\n" +
			"managed-settings, hooks, managed-mcp and sandbox — over the same API the web\n" +
			"console calls: validate a document, dry-run it against what hosts actually\n" +
			"report, publish a revision, and see where the signed artifact landed.\n\n" +
			"It is NOT `olivares agent managed-settings`, which renders a managed-settings\n" +
			"file on THIS host. This authors the tenant's versioned policy on the control\n" +
			"plane.\n\n" +
			"Exit codes: a document with validation errors exits 7, not 0, so a CI step can\n" +
			"branch on `validate` without parsing the report.",
		Example: "  olivares claude-policy validate managed-settings --content-file settings.json\n" +
			"  olivares claude-policy publish hooks --content-file hooks.json --note \"add bash guard\"\n" +
			"  olivares claude-policy distribution managed-settings\n" +
			"  olivares claude-policy versions ls managed-settings",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newClaudePolicyValidateCmd(&flags),
		newClaudePolicyDryRunCmd(&flags),
		newClaudePolicyPublishCmd(&flags),
		newClaudePolicyVersionsCmd(&flags),
		newClaudePolicyArtifactCmd(&flags),
		newClaudePolicyCheckinCmd(&flags),
		newClaudePolicyDistributionCmd(&flags),
	)
	return root
}

// claudePolicySurface validates the positional surface argument locally so a typo
// costs exit 2 and no request, and names the four the engine accepts.
func claudePolicySurface(name string) (string, error) {
	for _, s := range claudePolicySurfaces {
		if name == s {
			return s, nil
		}
	}
	return "", exitcode.New(exitcode.Usage, fmt.Errorf(
		"unknown surface %q (want managed-settings, hooks, managed-mcp or sandbox)", name))
}

// claudePolicyReportIssues renders a validate/dry-run report and decides the exit
// code from the VERDICT, never from the HTTP status.
//
// The rule is stated once here because both commands need it and because getting
// it wrong is silent: the route answers 200 for a document full of errors, so a
// client that trusted the status would report every broken policy as valid.
// Warnings do NOT fail — they are advice, and failing on them would make the
// distinction meaningless.
//
// `ok` IS THE ENGINE'S OWN VERDICT AND IT OUTRANKS THE DIAGNOSTIC COUNT. Reading
// only the diagnostics re-derives the answer from a severity STRING: today
// validateResult sets ok = len(issues) == 0 and stamps every issue "error"
// (claudepolicy.go:279), so the two agree — but they agree by arithmetic, not by
// construction. An engine that ever answered ok=false with an empty list, or
// spelled a severity "fatal", would be reported as a clean document by a client
// that counted instead of asking. So both are consulted and either one fails.
//
// It is a *bool ON PURPOSE. `dry-run` shares this reader and its 200 body carries
// no `ok` at all (dryRunResult, claudepolicy.go:197); a plain bool would decode
// that ABSENCE as false and fail every dry-run of a perfectly good document.
// nil means "the engine did not answer that question", which is not a verdict.
func claudePolicyReportIssues(cmd *cobra.Command, flags *authClientFlags, res agentExecResult, what string) error {
	var report struct {
		OK          *bool `json:"ok"`
		Diagnostics []struct {
			Message  string `json:"message"`
			Severity string `json:"severity"`
		} `json:"diagnostics"`
	}
	decodeErr := res.decode(&report)
	if rerr := renderAgentExecObject(cmd, flags, res, nil); rerr != nil {
		return rerr
	}
	if decodeErr != nil {
		return exitcode.New(exitcode.Degraded, fmt.Errorf(
			"the %s report could not be parsed (%w); this is NOT a passing document", what, decodeErr))
	}
	errors := 0
	for _, d := range report.Diagnostics {
		if d.Severity == "error" {
			errors++
		}
	}
	if report.OK != nil && !*report.OK {
		return exitcode.New(exitcode.Degraded, fmt.Errorf(
			"%s answered ok=false (%d error diagnostic(s) listed): the document is NOT publishable as it stands",
			what, errors))
	}
	if errors > 0 {
		return exitcode.New(exitcode.Degraded, fmt.Errorf(
			"%s reported %d error diagnostic(s): the document is NOT publishable as it stands", what, errors))
	}
	return nil
}

func newClaudePolicyValidateCmd(flags *authClientFlags) *cobra.Command {
	var contentFile string
	cmd := &cobra.Command{
		Use:   "validate <surface>",
		Short: "Validate a policy document server-side (exit 7 when it has errors)",
		Long: "validate runs the authoritative server-side validation for one surface. It has\n" +
			"no effect and persists nothing.\n\n" +
			"THE EXIT CODE IS THE POINT. The route answers 200 even for a document full of\n" +
			"errors — the verdict is in the report — so this command reads the report: clean\n" +
			"exits 0, one or more ERROR diagnostics exits 7. Warnings do not fail it.",
		Example: "  olivares claude-policy validate managed-settings --content-file settings.json\n" +
			"  olivares claude-policy validate hooks --content-file -",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			content, err := readAgentExecDocument(cmd, contentFile)
			if err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodPost, path: "/" + agentExecPathID(surface) + "/validate",
				body: map[string]any{"content": string(content)},
			}.do(cmd)
			if err != nil {
				return err
			}
			return claudePolicyReportIssues(cmd, flags, res, "validation")
		},
	}
	cmd.Flags().StringVar(&contentFile, "content-file", "", "the policy document, '-' for stdin (required)")
	_ = cmd.MarkFlagRequired("content-file")
	return cmd
}

func newClaudePolicyDryRunCmd(flags *authClientFlags) *cobra.Command {
	var contentFile string
	cmd := &cobra.Command{
		Use:   "dry-run <surface>",
		Short: "Resolve a document against observed hosts without writing anything",
		Long: "dry-run validates the document and resolves it against what distribution agents\n" +
			"have actually reported, showing the changes a publish would make.\n\n" +
			"NO HOST IS WRITTEN by a dry-run, and nothing is persisted.\n\n" +
			"It does NOT report like `validate`: an invalid document is refused outright\n" +
			"with the engine's HTTP 400 and its reason, not returned as a 200 carrying a\n" +
			"diagnostic list. Run `validate` first when you want the full list.",
		Example: "  olivares claude-policy dry-run managed-settings --content-file settings.json\n" +
			"  olivares claude-policy dry-run managed-mcp --content-file - -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			content, err := readAgentExecDocument(cmd, contentFile)
			if err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodPost, path: "/" + agentExecPathID(surface) + "/dry-run",
				body: map[string]any{"content": string(content)},
			}.do(cmd)
			if err != nil {
				return err
			}
			return claudePolicyReportIssues(cmd, flags, res, "dry-run")
		},
	}
	cmd.Flags().StringVar(&contentFile, "content-file", "", "the policy document, '-' for stdin (required)")
	_ = cmd.MarkFlagRequired("content-file")
	return cmd
}

func newClaudePolicyPublishCmd(flags *authClientFlags) *cobra.Command {
	var (
		contentFile string
		note        string
	)
	cmd := &cobra.Command{
		Use:   "publish <surface>",
		Short: "Publish a new revision and, when a distributor is wired, sign it",
		Long: "publish appends a revision and — when a distributor is wired — mints a SIGNED\n" +
			"artifact for the pull agents. It prints the artifact's sha256 and key\n" +
			"fingerprint: pin the fingerprint and hand it to the agents out of band.\n\n" +
			"THE DISTRIBUTION FIELD IS THREE-VALUED AND THIS COMMAND SHOWS IT VERBATIM:\n" +
			"`distributed` (signed and enqueued), `seam-pending` (published, but no\n" +
			"distributor is wired — nothing will pull it) and `enqueue-failed`. Only the\n" +
			"first means hosts will see it, and a publish that reports the other two still\n" +
			"succeeded as an authoring act, so it exits 0 and says so loudly on stderr.\n\n" +
			"drift_computed distinguishes a REAL empty drift list from an honest unknown:\n" +
			"an empty `drift` with drift_computed=false means nothing was observed, NOT\n" +
			"that every host matches.",
		Example: "  olivares claude-policy publish managed-settings --content-file settings.json\n" +
			"  olivares claude-policy publish hooks --content-file hooks.json --note \"add bash guard\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			content, err := readAgentExecDocument(cmd, contentFile)
			if err != nil {
				return err
			}
			body := map[string]any{"content": string(content)}
			if note != "" {
				body["note"] = note
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodPost, path: "/" + agentExecPathID(surface) + "/publish", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			var out struct {
				Revision      int64  `json:"revision"`
				Distribution  string `json:"distribution"`
				DriftComputed bool   `json:"drift_computed"`
			}
			decodeErr := res.decode(&out)
			if rerr := renderAgentExecObject(cmd, flags, res, nil); rerr != nil {
				return rerr
			}
			// A BODY THIS CANNOT READ IS NOT A CLEAN PUBLISH. Dropping the decode
			// error left Distribution == "" and skipped the "NOT distributed"
			// warning entirely, so an unreadable answer exited 0 having printed
			// nothing — the one outcome an operator must never get from the verb
			// that ships a policy to every host.
			//
			// It is Degraded, not Server: the engine answered 2xx, so the revision
			// may well exist, and telling a pipeline "server failure" invites the
			// retry that publishes it twice. This is the same call `checkin` and
			// `recording sessions verify` make on an unparseable verdict.
			if decodeErr != nil {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"the publish answer could not be parsed (%w): the revision may have been published, "+
						"but whether it was DISTRIBUTED is unknown — check `claude-policy distribution %s`",
					decodeErr, safeCLIValue(surface, "")))
			}
			if out.Distribution != "" && out.Distribution != "distributed" {
				if _, werr := fmt.Fprintf(cmd.ErrOrStderr(),
					"revision %d was published but NOT distributed (%s): no host will pull it until a distributor is wired\n",
					out.Revision, safeCLIValue(out.Distribution, "")); werr != nil {
					return werr
				}
			}
			if !out.DriftComputed {
				_, werr := fmt.Fprintln(cmd.ErrOrStderr(),
					"drift was NOT computed (no observation or source): an empty drift list here means UNKNOWN, not clean")
				return werr
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&contentFile, "content-file", "", "the policy document, '-' for stdin (required)")
	cmd.Flags().StringVar(&note, "note", "", "why this revision exists (recorded on the revision)")
	_ = cmd.MarkFlagRequired("content-file")
	return cmd
}

func newClaudePolicyVersionsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "versions",
		Short:   "List and read published revisions of a surface",
		Long:    "Every publish appends a revision. `ls` lists them and `get` returns one with its full content.",
		Example: "  olivares claude-policy versions ls managed-settings",
	}
	cmd.AddCommand(newClaudePolicyVersionsListCmd(flags), newClaudePolicyVersionsGetCmd(flags))
	return cmd
}

func newClaudePolicyVersionsListCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "ls <surface>",
		Short: "List a surface's published revisions",
		Long: "ls lists every revision, newest first, without the document bodies.\n\n" +
			"It is NOT paginated by the engine: the whole history comes back in one answer,\n" +
			"so there is no --cursor to continue from.",
		Example: "  olivares claude-policy versions ls managed-settings\n" +
			"  olivares claude-policy versions ls hooks -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodGet, path: "/" + agentExecPathID(surface) + "/versions",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "this surface has no published revisions",
				[]string{"revision", "surface", "author", "created_at", "validated", "active", "note"})
		},
	}
}

func newClaudePolicyVersionsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <surface> <revision>",
		Short:   "Show one revision with its document content",
		Long:    "get returns one revision including the document that was published. Use -o json to pipe the content back into validate or publish.",
		Example: "  olivares claude-policy versions get managed-settings 3 -o json",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			revision, cerr := strconv.ParseInt(args[1], 10, 64)
			if cerr != nil || revision < 1 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("invalid revision %q (want a positive whole number)", args[1]))
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodGet,
				path:   "/" + agentExecPathID(surface) + "/versions/" + strconv.FormatInt(revision, 10),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newClaudePolicyArtifactCmd(flags *authClientFlags) *cobra.Command {
	var revision int64
	cmd := &cobra.Command{
		Use:   "artifact <surface>",
		Short: "Fetch the signed artifact a distribution agent would pull",
		Long: "artifact returns the rendered document a pull agent applies, with the sha256\n" +
			"and key fingerprint it must verify against. Without --revision it returns the\n" +
			"newest.\n\n" +
			"This is the read a distribution agent makes; `checkin` is what it reports back.",
		Example: "  olivares claude-policy artifact managed-settings\n" +
			"  olivares claude-policy artifact managed-settings --revision 3 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			if revision < 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--revision must be positive, got %d", revision))
			}
			q := url.Values{}
			if revision > 0 {
				q.Set("revision", strconv.FormatInt(revision, 10))
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodGet, path: "/" + agentExecPathID(surface) + "/artifact", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
	cmd.Flags().Int64Var(&revision, "revision", 0, "a specific revision (0 uses the newest)")
	return cmd
}

func newClaudePolicyCheckinCmd(flags *authClientFlags) *cobra.Command {
	var (
		scope          string
		revision       int64
		artifactSHA    string
		keyFingerprint string
		observedFile   string
	)
	cmd := &cobra.Command{
		Use:   "checkin <surface>",
		Short: "Report an agent's applied artifact and observed config (exit 7 when unverified)",
		Long: "checkin is what a distribution agent reports after applying an artifact: which\n" +
			"revision it verified, the hash and key fingerprint it echoes back, and the\n" +
			"configuration it now OBSERVES on the host.\n\n" +
			"THE EXIT CODE IS THE POINT. The route answers 200 whether or not the echoed\n" +
			"artifact matches what was signed, so this command reads `verified`: a mismatch\n" +
			"— a tampered or stale artifact — exits 7 and is recorded as a HIGH drift\n" +
			"finding by the engine.\n\n" +
			"TRUST MODEL, stated because it is easy to over-read this verb: the scope is the\n" +
			"agent's SELF-ASSERTED host identity, and any principal with write rights may\n" +
			"report any scope. Every check-in therefore records WHO reported it — forgery is\n" +
			"attributable, never silent.",
		Example: "  olivares claude-policy checkin managed-settings --scope host-7 --revision 3 --artifact-sha256 abc123 --observed-file observed.json\n" +
			"  olivares claude-policy checkin hooks --scope host-7 --revision 3 --artifact-sha256 abc123 --key-fingerprint SHA256:xyz",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			if revision < 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--revision must be zero or positive, got %d", revision))
			}
			body := map[string]any{"scope": scope}
			if revision > 0 {
				body["revision"] = revision
			}
			if artifactSHA != "" {
				body["artifact_sha256"] = artifactSHA
			}
			if keyFingerprint != "" {
				body["key_fingerprint"] = keyFingerprint
			}
			if observedFile != "" {
				observed, rerr := readAgentExecDocument(cmd, observedFile)
				if rerr != nil {
					return rerr
				}
				body["observed_content"] = string(observed)
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodPost, path: "/" + agentExecPathID(surface) + "/checkin", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			var out struct {
				Verified bool `json:"verified"`
				Drift    []struct {
					Severity string `json:"severity"`
				} `json:"drift"`
			}
			decodeErr := res.decode(&out)
			if rerr := renderAgentExecObject(cmd, flags, res, nil); rerr != nil {
				return rerr
			}
			if decodeErr != nil {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"the check-in verdict could not be parsed (%w); this is NOT a verified check-in", decodeErr))
			}
			if !out.Verified {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"check-in recorded but NOT VERIFIED: the artifact this scope reports does not match what was signed "+
						"(a tampered or stale artifact); %d drift finding(s) were raised", len(out.Drift)))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "the host id / distribution name this check-in reports for (required)")
	cmd.Flags().Int64Var(&revision, "revision", 0, "the revision the agent applied")
	cmd.Flags().StringVar(&artifactSHA, "artifact-sha256", "", "the artifact hash the agent verified")
	cmd.Flags().StringVar(&keyFingerprint, "key-fingerprint", "", "the signing key fingerprint the agent verified against")
	cmd.Flags().StringVar(&observedFile, "observed-file", "", "the config observed on the host, '-' for stdin")
	_ = cmd.MarkFlagRequired("scope")
	return cmd
}

func newClaudePolicyDistributionCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "distribution <surface>",
		Short: "Show published vs signed vs observed, scope by scope",
		Long: "distribution is the truth view: the newest published revision, the signed\n" +
			"artifact, and every scope that has checked in — with whether it verified and\n" +
			"whether it is on the current artifact.\n\n" +
			"Real state only. A scope that has never reported is absent rather than assumed\n" +
			"current, and `content_reported` says whether a scope sent its observed config\n" +
			"at all — an absent one is UNKNOWN, not matching.\n\n" +
			"It is NOT paginated by the engine.",
		Example: "  olivares claude-policy distribution managed-settings\n" +
			"  olivares claude-policy distribution hooks -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			surface, err := claudePolicySurface(args[0])
			if err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: claudePolicyModule,
				method: http.MethodGet, path: "/" + agentExecPathID(surface) + "/distribution",
			}.do(cmd)
			if err != nil {
				return err
			}
			var body struct {
				Surface        string            `json:"surface"`
				LatestRevision int64             `json:"latest_revision"`
				Scopes         []json.RawMessage `json:"scopes"`
			}
			if derr := res.decode(&body); derr != nil {
				return exitcode.New(exitcode.Server, derr)
			}
			return renderOut(cmd, func(out io.Writer) error {
				if _, werr := fmt.Fprintf(out, "surface: %s\nlatest_revision: %d\n",
					safeCLIValue(body.Surface, ""), body.LatestRevision); werr != nil {
					return werr
				}
				if len(body.Scopes) == 0 {
					_, werr := fmt.Fprintln(out, "no scope has ever checked in for this surface")
					return werr
				}
				return writeAgentExecTable(out, flags, body.Scopes,
					[]string{"scope", "reporter", "reported_revision", "verified", "current",
						"content_reported", "observed_sha256", "checked_in_at"})
			}, res.jsonValue())
		},
	}
}
