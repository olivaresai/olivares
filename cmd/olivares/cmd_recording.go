// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_recording.go drives the session-recording plane (/v1/m/recording,
// modules/recording/recording.go): the consent notice an operator acknowledges,
// the hash-chained frame trail of a recorded session, and the tenant policy that
// decides what is recorded at all.
//
// ONE EXIT-CODE DECISION THIS FILE MAKES, because the generic map cannot:
//
//	`sessions verify` maps a BROKEN CHAIN onto exit 7, not 0. The route answers
//	200 with a verdict — the chain either verifies or it does not — so a client
//	that only looked at the status code would exit 0 on a tampered trail and a
//	nightly integrity job would stay green forever. A verdict of "not ok" is the
//	textbook Degraded: the command ran fine and reports a degraded condition
//	(exitcode.go:27). The counterweight is tested: a chain that verifies exits 0.
//
// The recording policy is narrowing-only by design. `config set` can restrict
// which namespaces are recorded, but the break-glass floor is permission-based,
// not namespace-based, so no configuration this command can send will un-record
// emergency access. This CLI does not re-implement that rule; it just does not
// pretend to offer an escape from it.

const recordingModule = "recording"

func newRecordingCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "recording",
		Short: "Read the session-recording trail, verify its chain and set the recording policy",
		Long: "recording exercises the session-recording plane from a terminal: the consent\n" +
			"notice, the hash-chained frame trail of each recorded session, its verification,\n" +
			"export and seal, and the tenant policy that decides what gets recorded.\n\n" +
			"`sessions verify` is the machine-facing verb: a chain that does NOT verify\n" +
			"exits 7, so a nightly integrity job fails loudly instead of passing quietly.",
		Example: "  olivares recording notice\n" +
			"  olivares recording sessions ls --status active\n" +
			"  olivares recording sessions verify rs-1\n" +
			"  olivares recording config get -o json",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newRecordingNoticeCmd(&flags),
		newRecordingAckCmd(&flags),
		newRecordingSessionsCmd(&flags),
		newRecordingSweepCmd(&flags),
		newRecordingConfigCmd(&flags),
	)
	return root
}

// ---- notice and acknowledgement ------------------------------------------------

func newRecordingNoticeCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "notice",
		Short: "Show what is recorded for this caller, and whether consent is required",
		Long: "notice reports the recorded namespaces, whether break-glass is always recorded,\n" +
			"the consent mode, and whether this caller has already acknowledged it. It is\n" +
			"the read every operator is entitled to before working in a recorded session.",
		Example: "  olivares recording notice\n  olivares recording notice -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodGet, path: "/notice",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newRecordingAckCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "ack",
		Short: "Acknowledge the recording notice for this caller",
		Long: "ack records this caller's acknowledgement of the recording notice and returns\n" +
			"the session it was attached to. Where the tenant's consent mode is `required`,\n" +
			"this is the step that unblocks work in a recorded namespace.",
		Example: "  olivares recording ack",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodPost, path: "/ack",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, []string{"session_id", "acknowledged_at"})
		},
	}
}

// ---- sessions --------------------------------------------------------------------

var recordingSessionColumns = []string{
	"id", "subject", "subject_user", "status", "opened_at", "sealed_at",
	"seal_reason", "frames_written", "gap",
}

func newRecordingSessionsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sessions",
		Short:   "List, verify, export and seal recorded sessions",
		Long:    "A recorded session is a hash-chained trail of frames with an anchored open and a sealed close. Reading one is itself audited.",
		Example: "  olivares recording sessions ls --status sealed",
	}
	cmd.AddCommand(
		newRecordingSessionsListCmd(flags),
		newRecordingSessionsGetCmd(flags),
		newRecordingSessionsReplayCmd(flags),
		newRecordingSessionsUnifiedCmd(flags),
		newRecordingSessionsVerifyCmd(flags),
		newRecordingSessionsExportCmd(flags),
		newRecordingSessionsSealCmd(flags),
		newRecordingSessionsSummarizeCmd(flags),
	)
	return cmd
}

func newRecordingSessionsListCmd(flags *authClientFlags) *cobra.Command {
	var (
		page            agentExecPageFlags
		status          string
		subjectUser     string
		grant           string
		sealReason      string
		openedAfter     string
		openedBefore    string
		subjectContains string
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List recorded sessions",
		Long: "ls lists recorded sessions with their seal state and frame counters. Every\n" +
			"filter is applied server-side.\n\n" +
			"`gap` is the column to watch: it marks a session whose frame chain has a hole,\n" +
			"i.e. reserved sequence numbers that were never written.",
		Example: "  olivares recording sessions ls --status active\n" +
			"  olivares recording sessions ls --grant bg-7 --opened-after 2026-08-01T00:00:00Z",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "status", status)
			setQuery(q, "subject_user", subjectUser)
			setQuery(q, "grant", grant)
			setQuery(q, "seal_reason", sealReason)
			setQuery(q, "opened_after", openedAfter)
			setQuery(q, "opened_before", openedBefore)
			setQuery(q, "subject_contains", subjectContains)
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodGet, path: "/sessions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecList(cmd, flags, res, "no recorded sessions", recordingSessionColumns)
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&status, "status", "", "only sessions in this status")
	cmd.Flags().StringVar(&subjectUser, "subject-user", "", "only sessions of this user")
	cmd.Flags().StringVar(&grant, "grant", "", "only sessions opened under this break-glass grant")
	cmd.Flags().StringVar(&sealReason, "seal-reason", "", "only sessions sealed for this reason")
	cmd.Flags().StringVar(&openedAfter, "opened-after", "", "only sessions opened at or after this RFC3339 instant")
	cmd.Flags().StringVar(&openedBefore, "opened-before", "", "only sessions opened before this RFC3339 instant")
	cmd.Flags().StringVar(&subjectContains, "subject-contains", "", "only sessions whose subject contains this substring")
	return cmd
}

func newRecordingSessionsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <id>",
		Short:   "Show one recorded session",
		Long:    "get returns one session's header: subject, seal state, frame counters and chain tip.",
		Example: "  olivares recording sessions get rs-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodGet, path: "/sessions/" + agentExecPathID(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newRecordingSessionsReplayCmd(flags *authClientFlags) *cobra.Command {
	var page agentExecPageFlags
	cmd := &cobra.Command{
		Use:   "replay <id>",
		Short: "Reconstruct one session's frames and ledger window",
		Long: "replay returns the session header, a page of frames and the audit-ledger events\n" +
			"covering the session's window — a reconstruction, not a blob. Viewing it is\n" +
			"itself an audited action.\n\n" +
			"--limit and --cursor page the FRAMES.",
		Example: "  olivares recording sessions replay rs-1 --limit 200 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodGet, path: "/sessions/" + agentExecPathID(args[0]) + "/replay", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderRecordingReplay(cmd, flags, res)
		},
	}
	page.add(cmd)
	return cmd
}

// renderRecordingReplay renders the replay envelope, which nests a frame LIST
// inside an object. The truncation note names --cursor because the frames, not
// the envelope, are what pages.
func renderRecordingReplay(cmd *cobra.Command, flags *authClientFlags, res agentExecResult) error {
	var body struct {
		Frames struct {
			Cursor  string `json:"cursor"`
			HasMore bool   `json:"has_more"`
		} `json:"frames"`
		LedgerTruncated bool `json:"ledger_truncated"`
	}
	if err := res.decode(&body); err != nil {
		return exitcode.New(exitcode.Server, err)
	}
	rerr := renderAgentExecObject(cmd, flags, res, nil)
	if rerr != nil {
		return rerr
	}
	if body.Frames.HasMore || body.Frames.Cursor != "" {
		note := "more frames remain"
		if body.Frames.Cursor != "" {
			note = fmt.Sprintf("more frames remain; continue with --cursor %s",
				safeCLIValue(body.Frames.Cursor, ""))
		}
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), note); err != nil {
			return err
		}
	}
	if body.LedgerTruncated {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(),
			"the ledger window was TRUNCATED: this is not the whole audit trail for the session"); err != nil {
			return err
		}
	}
	return nil
}

func newRecordingSessionsUnifiedCmd(flags *authClientFlags) *cobra.Command {
	var (
		page        agentExecPageFlags
		frameCursor string
	)
	cmd := &cobra.Command{
		Use:   "unified <id>",
		Short: "Show one session's frames and audit timeline merged",
		Long: "unified merges the frame trail with the audit timeline so an incident reviewer\n" +
			"reads one sequence instead of correlating two.\n\n" +
			"--frame-cursor pages the FRAMES independently of the timeline, which is why it\n" +
			"exists alongside --cursor.",
		Example: "  olivares recording sessions unified rs-1 -o json\n" +
			"  olivares recording sessions unified rs-1 --frame-cursor f-200",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			setQuery(q, "frame_cursor", frameCursor)
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodGet, path: "/sessions/" + agentExecPathID(args[0]) + "/unified", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&frameCursor, "frame-cursor", "", "page the frames independently of the timeline")
	return cmd
}

func newRecordingSessionsVerifyCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "verify <id>",
		Short: "Verify a session's hash chain — exit 7 when it does not verify",
		Long: "verify recomputes the session's hash chain and reports the verdict.\n\n" +
			"THE EXIT CODE IS THE POINT. The route answers 200 whether or not the chain\n" +
			"holds, so this command reads the VERDICT: a chain that verifies exits 0, and a\n" +
			"chain that does not — a missing or reordered frame, a forged hash, a tip that\n" +
			"does not match, a ledger anchor that does not — exits 7 and says which. A\n" +
			"nightly integrity job can branch on it.\n\n" +
			"ONE CONDITION IS REPORTED WITHOUT FAILING: frame slots that were reserved and\n" +
			"never written. On a LIVE session that is a request still in flight, so it is\n" +
			"named on stderr rather than turned into an exit code that would redden every\n" +
			"run made while the estate was busy.",
		Example: "  olivares recording sessions verify rs-1\n" +
			"  olivares recording sessions verify rs-1 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodGet, path: "/sessions/" + agentExecPathID(args[0]) + "/verify",
			}.do(cmd)
			if err != nil {
				return err
			}
			var verdict struct {
				OK     bool   `json:"ok"`
				Gap    bool   `json:"gap"`
				Detail string `json:"detail"`
				Reason string `json:"reason"`
			}
			decodeErr := res.decode(&verdict)
			if rerr := renderAgentExecObject(cmd, flags, res, nil); rerr != nil {
				return rerr
			}
			// A body this cannot parse is NOT a pass. Refusing to conclude is the
			// honest answer: "I could not read the verdict" must never render as
			// "the chain is intact".
			if decodeErr != nil {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"the verification verdict could not be parsed (%w); this is NOT a passing chain", decodeErr))
			}
			if !verdict.OK {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"the recorded chain for %s DID NOT verify: %s",
					safeCLIValue(args[0], ""),
					firstNonEmptyCLI(verdict.Detail, verdict.Reason, "the engine reported ok=false")))
			}
			// `gap` is a SEPARATE fact from `ok` and it must not be dropped: it says
			// slots were reserved and never written (reserved > written,
			// handlers.go:712), i.e. the trail is missing frames it counted on.
			//
			// IT IS DELIBERATELY NOT AN EXIT CODE, and that was measured rather than
			// assumed from the word "gap": Reserve bumps `reserved` at the START of a
			// recorded request (recorder.go:252) and the frame lands when the request
			// finishes, so an ACTIVE session with work in flight legitimately reports
			// gap=true for that window. Failing on it would redden every nightly job
			// that ran while the estate was busy. The hole this command DOES fail on
			// is a frame missing BETWEEN two written ones, which the engine already
			// reports as ok=false with reason "idx-gap" (handlers.go:739).
			//
			// So it goes to stderr, where this lot already puts metadata about an
			// answer: a human sees it, `| jq` does not, and nothing vanishes quietly.
			if verdict.Gap {
				_, werr := fmt.Fprintln(cmd.ErrOrStderr(),
					"the chain verified, but this session has RESERVED FRAME SLOTS THAT WERE NEVER "+
						"WRITTEN: on a live session that is a request still in flight; on a sealed "+
						"one it is a lost frame")
				return werr
			}
			return nil
		},
	}
}

func newRecordingSessionsExportCmd(flags *authClientFlags) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "export <id>",
		Short: "Export one session as evidence (json or summary)",
		Long: "export produces the evidence artifact for one session.\n\n" +
			"--format here is a REAL export-format flag, exactly like the one on `audit\n" +
			"export`: it selects json (the full trail) or summary (the header and counters).\n" +
			"It is not a spelling of -o/--output, which still selects how this command\n" +
			"renders what it received.",
		Example: "  olivares recording sessions export rs-1 --format json\n" +
			"  olivares recording sessions export rs-1 --format summary",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch format {
			case "json", "summary":
			default:
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("invalid --format %q (use json or summary)", format))
			}
			q := url.Values{}
			q.Set("format", format)
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodGet, path: "/sessions/" + agentExecPathID(args[0]) + "/export", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
	cmd.Flags().StringVar(&format, "format", "json",
		"export format: json (full trail) or summary — NOT an alias of -o/--output")
	return cmd
}

func newRecordingSessionsSealCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "seal <id>",
		Short: "Close one active session explicitly",
		Long: "seal closes an ACTIVE session and anchors its chain. A session that is already\n" +
			"sealed is a conflict (exit 5), never a silent second seal — the first seal is\n" +
			"what the evidence is anchored to.",
		Example: "  olivares recording sessions seal rs-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodPost, path: "/sessions/" + agentExecPathID(args[0]) + "/seal",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, recordingSessionColumns)
		},
	}
}

func newRecordingSessionsSummarizeCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "summarize <id>",
		Short: "Produce the derived reviewer summary of a sealed session",
		Long: "summarize asks the wired summarizer for a reviewer summary of a SEALED session.\n" +
			"The summary is a DERIVED artifact and is marked as one: it never substitutes for\n" +
			"the frames.\n\n" +
			"Two refusals are normal and this command names them rather than calling either\n" +
			"a bug: with no summarizer wired the engine answers 501, and with the tenant's\n" +
			"ai_summaries switched off it answers 403 — the transcript would leave the trust\n" +
			"boundary, so each tenant opts in explicitly.",
		Example: "  olivares recording sessions summarize rs-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodPost, path: "/sessions/" + agentExecPathID(args[0]) + "/summarize",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

// ---- sweep and config -------------------------------------------------------------

func newRecordingSweepCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "sweep",
		Short: "Seal every idle active session (the lazy-seal safety net)",
		Long: "sweep seals every ACTIVE session that has been idle longer than the tenant's\n" +
			"idle_seconds. It is the safety net for credentials that never came back.\n\n" +
			"It is NOT gated behind --yes, and that is deliberate: sealing preserves\n" +
			"evidence, it does not destroy it. Each materialized seal is attributed to the\n" +
			"SYSTEM actor with its own sweep reason, while the sweep action itself is one\n" +
			"aggregate ledger event attributed to you.",
		Example: "  olivares recording sweep",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodPost, path: "/sweep",
			}.do(cmd)
			if err != nil {
				return err
			}
			var out struct {
				Sealed int64 `json:"sealed"`
			}
			// AN UNREADABLE ANSWER MUST NOT PRINT A COUNT. Dropping this error left
			// Sealed at its zero value and the command announced "sealed 0 idle
			// session(s)" — a number nobody measured, on exit 0, for a sweep whose
			// real count is unknown. Every other envelope reader in this lot treats
			// an unreadable body as a failure rather than as an empty answer
			// (renderAgentExecList, renderAgentExecGraph, renderRecordingReplay,
			// `redteam catalog`, `claude-policy distribution`).
			//
			// Degraded, not Server: the seals were already materialized by a 2xx, so
			// this is "it ran and I cannot report what it did", not "it failed".
			if derr := res.decode(&out); derr != nil {
				return exitcode.New(exitcode.Degraded, fmt.Errorf(
					"the sweep ran but its answer could not be parsed (%w): how many sessions were sealed is UNKNOWN", derr))
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "sealed %d idle session(s)\n", out.Sealed)
				return werr
			}, res.jsonValue())
		},
	}
}

func newRecordingConfigCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config",
		Short:   "Read and replace the tenant's recording policy",
		Long:    "The recording policy decides which namespaces are recorded, whether consent is merely noticed or required, when an idle session is swept, and how long trails are kept.",
		Example: "  olivares recording config get",
	}
	cmd.AddCommand(newRecordingConfigGetCmd(flags), newRecordingConfigSetCmd(flags))
	return cmd
}

func newRecordingConfigGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get",
		Short:   "Show the tenant's recording policy",
		Long:    "get returns the policy in force: recorded namespaces, consent mode, idle window, retention and whether AI summaries are permitted.",
		Example: "  olivares recording config get -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodGet, path: "/config",
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
}

func newRecordingConfigSetCmd(flags *authClientFlags) *cobra.Command {
	var (
		namespaces    []string
		consent       string
		idleSeconds   int64
		retentionDays int64
		aiSummaries   bool
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Replace the tenant's recording policy (PUT — the whole policy)",
		Long: "set REPLACES the policy. It is a PUT, not a patch, so every field you do not\n" +
			"pass takes this command's stated default rather than the stored value — read\n" +
			"`config get` first and pass the whole policy you intend.\n\n" +
			"Narrowing is the supported direction: a tenant may record less. It cannot\n" +
			"un-record emergency access, because the break-glass floor is permission-based\n" +
			"and no namespace list reaches it.\n\n" +
			"--ai-summaries is the explicit opt-in that lets a transcript leave the trust\n" +
			"boundary for summarization. It is off unless you pass it.",
		Example: "  olivares recording config set --namespace agents --namespace tools --consent notice --idle-seconds 900 --retention-days 90\n" +
			"  olivares recording config set --namespace agents --consent required --idle-seconds 600 --retention-days 365 --ai-summaries",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch consent {
			case "notice", "required":
			default:
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("invalid --consent %q (use notice or required)", consent))
			}
			if idleSeconds <= 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--idle-seconds must be positive, got %d", idleSeconds))
			}
			if retentionDays <= 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--retention-days must be positive, got %d", retentionDays))
			}
			if len(namespaces) == 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--namespace is required: pass every namespace this policy records "+
						"(this is a PUT — an omitted list would record nothing)"))
			}
			body := map[string]any{
				"namespaces":     namespaces,
				"consent":        consent,
				"idle_seconds":   idleSeconds,
				"retention_days": retentionDays,
				"ai_summaries":   aiSummaries,
			}
			res, err := agentExecCall{
				flags: flags, module: recordingModule,
				method: http.MethodPut, path: "/config", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderAgentExecObject(cmd, flags, res, nil)
		},
	}
	cmd.Flags().StringArrayVar(&namespaces, "namespace", nil, "a namespace to record, repeatable (required)")
	cmd.Flags().StringVar(&consent, "consent", "notice", "notice or required")
	cmd.Flags().Int64Var(&idleSeconds, "idle-seconds", 900, "seconds of inactivity before a sweep may seal a session")
	cmd.Flags().Int64Var(&retentionDays, "retention-days", 90, "days a sealed trail is retained")
	cmd.Flags().BoolVar(&aiSummaries, "ai-summaries", false,
		"permit AI summaries: the transcript LEAVES the trust boundary (off unless passed)")
	return cmd
}
