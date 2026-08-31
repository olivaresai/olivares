// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// This file is E5: the compliance module's governed data-lifecycle verbs
// from a terminal.
//
// WHY IT EXISTS. The engine has run legal holds and GDPR erasure
// for many sessions, and `cmd_compliance.go` has never existed in this
// repository's history. So the only supported way to place a hold, register a
// DSAR or execute an erasure was a browser or a hand-rolled `curl`. An operator
// working an incident at 3am needs a path that is neither.
//
// TWO EXIT-CODE DECISIONS THIS FILE MAKES, because the generic map cannot.
// httpErr (cmd_agent.go:592) classifies 401/403/404/409/5xx and lets everything
// else fall through to exit 1. Two of this module's answers are exactly the ones
// a script must branch on:
//
//   202 pending_approval → exit 7 (Degraded), NOT 0. An execute that opens dual
//   control has destroyed nothing (erasure.go:753) and a release that opens it
//   has lifted nothing (holds.go:610). Exiting 0 would tell a DPO's automation
//   the deletion happened. Exiting 1 would say it failed, which is also false:
//   the request was recorded and the custody event sealed. Degraded is
//   documented as "succeeded but reports a degraded condition" (exitcode.go:27)
//   and that is precisely this state.
//
//   423 legal_hold → exit 5 (Conflict), NOT 1. Today an erasure vetoed by a
//   preservation order is indistinguishable from a generic failure. It is
//   "the request contradicts current state" (exitcode.go:23). The command also
//   PRINTS the covering holds, which the engine already returns in the body
//   (erasure.go:675-679) — "blocked" without "by what" is what sends an operator
//   back to curl, which is the thing this file exists to end.
//
// Every destructive verb goes through confirmDestructive: --yes, or an
// interactive terminal, or a refusal. A pipe is not consent.

const complianceBase = "/v1/m/compliance"

// The only two values that mean "this governed verb finished cleanly". Named
// once so the allowlist rule lives in one place rather than as loose strings.
const (
	erasureStatusCompletedLiteral = "completed"
	erasureStatusReceivedLiteral  = "received"
	holdStatusReleasedLiteral     = "released"
	holdStatusActiveLiteral       = "active"
)

// maxComplianceBodySize bounds a response the CLI reads into memory. These are
// registers, receipts and custody trails — kilobytes — so a body beyond this is
// a bug or a hostile endpoint, not a large legitimate answer.
const maxComplianceBodySize = 8 << 20

func newComplianceCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "compliance",
		Short: "Operate legal holds, GDPR erasure and regulatory artifacts",
		Long: "compliance exercises the governed data-lifecycle plane from a terminal:\n" +
			"preservation orders that veto every destruction path, the right-to-erasure\n" +
			"workflow with its dual-control gates and verifiable receipt, and the\n" +
			"regulatory artifacts the engine seals.\n\n" +
			"The dangerous verbs behave like the control plane, not like a script: a\n" +
			"verb that did not finish exits 7 and says exactly how far it got; an erasure\n" +
			"a legal hold vetoes exits 5 and names the holds that blocked it.",
		Example: "  olivares compliance holds ls\n" +
			"  olivares compliance holds place --matter CASE-42 --scope subject --subject-kind user --subject-ref u-7 --reason \"litigation\"\n" +
			"  olivares compliance erasure request --subject-ref u-7 --case-ref DSAR-9\n" +
			"  olivares compliance erasure execute er-123 --yes\n" +
			"  olivares compliance calendar -o json",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newComplianceHoldsCmd(&flags),
		newComplianceErasureCmd(&flags),
		newComplianceSubjectCmd(&flags),
		newComplianceCalendarCmd(&flags),
		newComplianceDoraCmd(&flags),
		newComplianceOscalCmd(&flags),
		newComplianceDepthCmd(&flags),
	)
	return root
}

// ---- transport ---------------------------------------------------------------

// complianceCall performs one authenticated request against the compliance
// module and returns the decoded body. It centralizes the exit-code contract so
// no individual verb can forget the 202/423 classification.
type complianceCall struct {
	flags  *authClientFlags
	method string
	path   string
	query  url.Values
	body   any
}

// complianceResult carries what a caller needs to render AND the status, because
// on this module the status is semantic: 200 and 202 are different outcomes of
// the same successful request.
type complianceResult struct {
	status int
	raw    []byte
}

func (r complianceResult) decode(into any) error {
	if len(bytes.TrimSpace(r.raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.raw, into); err != nil {
		return fmt.Errorf("decode compliance response: %w", err)
	}
	return nil
}

func (c complianceCall) do(cmd *cobra.Command) (complianceResult, error) {
	resolved, err := c.flags.resolve(cmd)
	if err != nil {
		return complianceResult{}, redactCoded(err, c.flags.effectiveToken())
	}
	// An unresolved server/token/tenant is a USAGE error (exit 2), not a server
	// failure (exit 6). Measured on this tree, the repo does it both ways:
	// `agent session ls` exits 2 through missingCLIValueError, `findings export`
	// exits 6 by falling into cliTransport's generic error. Two is right and the
	// message is the reason — missingCLIValueError names the flag, the env var
	// AND the client contexts, including which one is active and where the config
	// lives (clitransport.go:151, the E7 fix). "no server: set --server"
	// after a successful `auth login` is how an operator concludes the CLI is
	// broken.
	switch {
	case resolved.Server == "":
		return complianceResult{}, missingCLIValueError("server", "--server", "OLIVARES_SERVER_URL", resolved)
	case resolved.Token == "":
		return complianceResult{}, missingCLIValueError("token", "--token", "OLIVARES_TOKEN", resolved)
	case resolved.Tenant == "":
		return complianceResult{}, missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved:       resolved,
		Insecure:       c.flags.insecure,
		Timeout:        c.flags.timeout,
		Stderr:         cmd.ErrOrStderr(),
		AllowCleartext: c.flags.allowCleartext,
	})
	if err != nil {
		return complianceResult{}, redactCodedServer(err, resolved.Token)
	}

	var payload io.Reader
	if c.body != nil {
		encoded, merr := json.Marshal(c.body)
		if merr != nil {
			return complianceResult{}, fmt.Errorf("encode request body: %w", merr)
		}
		payload = bytes.NewReader(encoded)
	}

	target := resolved.Server + complianceBase + c.path
	if len(c.query) > 0 {
		target += "?" + c.query.Encode()
	}
	req, err := http.NewRequestWithContext(cmd.Context(), c.method, target, payload)
	if err != nil {
		return complianceResult{}, redactCodedServer(err, resolved.Token)
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := cliDo(client, req)
	if err != nil {
		return complianceResult{}, redactCodedServer(err, resolved.Token)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, maxComplianceBodySize))
	if rerr != nil {
		return complianceResult{}, exitcode.New(exitcode.Server, fmt.Errorf("read compliance response: %w", rerr))
	}
	if resp.StatusCode >= 300 {
		// The body is the plane's and complianceHTTPError embeds it (verbatim in the
		// unknown-423 branch, through httpErr elsewhere), so a proxy that reflects the
		// request headers would print this caller's own bearer. redactCoded scrubs it
		// without touching the classification — Conflict(5) for a hold, Err(1) for the
		// add-on boundary — which is why the redaction can be here at all.
		return complianceResult{status: resp.StatusCode, raw: raw},
			redactCoded(complianceHTTPError(resp.StatusCode, raw), resolved.Token)
	}
	return complianceResult{status: resp.StatusCode, raw: raw}, nil
}

// holdRefOut mirrors the engine's HoldRef (holds.go:76) for the 423 body.
type holdRefOut struct {
	ID        string `json:"id"`
	MatterRef string `json:"matter_ref"`
	ScopeKind string `json:"scope_kind"`
}

// complianceHTTPError extends httpErr with the two statuses this module uses
// that the generic map does not classify.
//
// 501 is called out by name because on this module it is a PRODUCT boundary, not
// a fault: the generators (DORA register, incident classification, OSCAL
// ingestion, depth packs) live in the enterprise add-on and the open-core engine
// answers 501 by design (regpackage.go:256 and friends). An operator who reads
// "request failed" there goes looking for a bug that does not exist.
func complianceHTTPError(status int, body []byte) error {
	if status == http.StatusLocked {
		// TWO different vetoes share this status and they have different remedies.
		// Classify by the envelope's CODE, not by the status: `legal_hold` means
		// go release a preservation order (erasure.go:675); `rtbf_coordinator`
		// means the enterprise readiness check refused and carries the actual
		// blockers (erasure.go:696). Reporting the second as a legal hold sends an
		// operator looking for an order that does not exist — found by the sol-max
		// contrast.
		var env struct {
			Error struct {
				Code     string       `json:"code"`
				Message  string       `json:"message"`
				Holds    []holdRefOut `json:"holds"`
				Blockers []string     `json:"blockers"`
				Warnings []string     `json:"warnings"`
			} `json:"error"`
		}
		decodeErr := json.Unmarshal(body, &env)
		var b strings.Builder
		switch env.Error.Code {
		case "rtbf_coordinator":
			b.WriteString("blocked by the enterprise RTBF readiness check (this is NOT a legal hold)")
			for _, x := range env.Error.Blockers {
				fmt.Fprintf(&b, "\n  blocker: %s", x)
			}
			for _, x := range env.Error.Warnings {
				fmt.Fprintf(&b, "\n  warning: %s", x)
			}
			b.WriteString("\nresolve the blockers above, then execute again")
		case "legal_hold":
			b.WriteString("blocked by an active legal hold: preservation wins over erasure")
			for _, h := range env.Error.Holds {
				fmt.Fprintf(&b, "\n  hold %s  matter %s  scope %s",
					h.ID, firstNonEmpty(h.MatterRef, "(matter reference missing from the response)"), h.ScopeKind)
			}
			if len(env.Error.Holds) == 0 {
				b.WriteString("\nthe response named no holds; check `compliance holds ls` before concluding what is preserved")
			} else {
				b.WriteString("\nrelease the hold under dual control before retrying this erasure")
			}
		default:
			// An unknown 423 is still a state conflict, but do not put words in
			// the engine's mouth about which one.
			b.WriteString("the control plane refused this request (HTTP 423)")
			if msg := strings.TrimSpace(env.Error.Message); msg != "" {
				fmt.Fprintf(&b, ": %s", msg)
			} else if decodeErr != nil {
				fmt.Fprintf(&b, ": %s", strings.TrimSpace(string(body)))
			}
		}
		return exitcode.New(exitcode.Conflict, fmt.Errorf("%s", b.String()))
	}
	if status == http.StatusNotImplemented {
		return exitcode.New(exitcode.Err, fmt.Errorf(
			"this verb is provided by the Olivares enterprise add-on and is not linked in this build (HTTP 501); "+
				"reads, exports and deletes on this surface work without it"))
	}
	return httpErr(status, body)
}

// pendingEnvelope is the shape both dual-control verbs answer 202 with.
type pendingEnvelope struct {
	Status      string `json:"status"`
	ApprovalRef string `json:"approval_ref,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// reportPending renders a 202 and returns the Degraded exit code. It is the one
// place that decides how "did not finish" reaches a script.
//
// THERE ARE TWO 202s AND THEY ARE DIFFERENT NEWS. `pending_approval` means
// nothing was destroyed (erasure.go:753, holds.go:610). `provider_pending`
// (erasure.go:1233) is returned AFTER local targets were erased and the account
// leg ran, when the provider still owes deletions — printing "NOTHING was
// erased" there would be false, and a first version of this file did exactly
// that. The sol-max contrast caught it.
//
// A body this cannot parse is still a 202: the exit code must not silently
// become 1 because the JSON was malformed, so the decode error is REPORTED and
// the Degraded code is kept.
func reportPending(cmd *cobra.Command, res complianceResult, what string) error {
	var env pendingEnvelope
	decodeErr := res.decode(&env)

	// ALLOWLIST again: the specific reason is only claimed when the body states a
	// reason this CLI knows. Anything else — empty, unparseable, valid JSON with
	// no status, or a status from a newer engine — gets the generic wording. The
	// default used to be "awaiting dual-control approval", which invented a reason
	// the response had not given for every one of those shapes.
	headline := what + ": DID NOT COMPLETE — the control plane returned 202 without a reason this CLI recognizes; check the request status before assuming anything"
	switch {
	case decodeErr != nil:
		headline = what + ": DID NOT COMPLETE — the control plane returned 202 with a body this CLI could not parse; check the request status before assuming anything"
	case env.Status == "provider_pending":
		headline = what + ": PARTIALLY DONE — local data and the account leg ran, but the model provider still has deletions outstanding. " +
			"The subject key was NOT shredded and no receipt is sealed; re-execute once the provider approvals are granted"
	case env.Status == "pending_approval":
		headline = what + ": NOT DONE — awaiting dual-control approval"
	case env.Status != "":
		headline = what + ": DID NOT COMPLETE — the control plane reported " + env.Status
	}

	err := renderOut(cmd, func(w io.Writer) error {
		if _, werr := fmt.Fprintf(w, "%s\n", headline); werr != nil {
			return werr
		}
		if env.Status != "" {
			if _, werr := fmt.Fprintf(w, "status: %s\n", env.Status); werr != nil {
				return werr
			}
		}
		if env.ApprovalRef != "" {
			if _, werr := fmt.Fprintf(w, "approval ref: %s\n", env.ApprovalRef); werr != nil {
				return werr
			}
		}
		if env.Detail != "" {
			if _, werr := fmt.Fprintf(w, "%s\n", env.Detail); werr != nil {
				return werr
			}
		}
		if decodeErr != nil {
			if _, werr := fmt.Fprintf(w, "raw body: %s\n", strings.TrimSpace(string(res.raw))); werr != nil {
				return werr
			}
		}
		return nil
	}, env)
	if err != nil {
		return err
	}
	// The report is already on stdout; the wrapper exists only for the code.
	return exitcode.New(exitcode.Degraded, nil)
}

// newTabWriter builds the text-mode table for this file's verbs. The guard scans
// for the constructor token by text, and here it sits in a shared helper rather
// than at each call site, so the renderer is one frame away instead of one line
// away. (Naming that token again in this comment would re-trip the same scan.)
//
// render-exempt: this helper never formats on its own. Every caller invokes it
// INSIDE the textFn that renderOut runs, and renderOut only runs textFn when the
// selected format is text — `-o json` short-circuits to the marshaled DTO and
// never reaches a table at all. TestComplianceHonoursJSONOutput asserts that
// against a real command rather than by inspection.
func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// confirmedCreate reports whether a create actually created what it claims.
//
// complianceCall.do only separates >=300, so ANY 2xx reaches the caller as
// success — including a 202, which on these routes would mean the write did not
// finish. Measured by the fourth-round contrast: a server answering
// `202 {"status":"pending_approval"}` made `holds place` print "hold  placed"
// and exit 0, with an empty id.
//
// Today's engine answers 201 with a full DTO on both creates, so this is not a
// live defect. It is the allowlist property applied to the two verbs the earlier
// rounds did not cover: an id and the expected status, or it is not done.
func confirmedCreate(status int, id, got, want string) (bool, string) {
	switch {
	case status != http.StatusCreated && status != http.StatusOK:
		return false, fmt.Sprintf("the control plane answered HTTP %d, which does not confirm the record was created", status)
	case strings.TrimSpace(id) == "":
		return false, "the control plane returned no id, so there is nothing to confirm"
	case want != "" && got != want:
		return false, fmt.Sprintf("the control plane returned status %q, not %q", got, want)
	}
	return true, ""
}

// ---- holds -------------------------------------------------------------------

type holdOut struct {
	ID                 string `json:"id"`
	MatterRef          string `json:"matter_ref"`
	Title              string `json:"title,omitempty"`
	ScopeKind          string `json:"scope_kind"`
	DataClass          string `json:"data_class,omitempty"`
	SubjectKind        string `json:"subject_kind,omitempty"`
	SubjectRef         string `json:"subject_ref,omitempty"`
	Reason             string `json:"reason"`
	Status             string `json:"status"`
	CreatedBy          string `json:"created_by"`
	CreatedAt          string `json:"created_at"`
	ReleasedBy         string `json:"released_by,omitempty"`
	ReleasedAt         string `json:"released_at,omitempty"`
	ReleaseApprovalRef string `json:"release_approval_ref,omitempty"`
}

type holdListOut struct {
	Items []holdOut `json:"items"`
}

type holdEventOut struct {
	HoldID      string   `json:"hold_id"`
	Event       string   `json:"event"`
	Actor       string   `json:"actor"`
	ActorKind   string   `json:"actor_kind"`
	Note        string   `json:"note,omitempty"`
	ApprovalRef string   `json:"approval_ref,omitempty"`
	Approvers   []string `json:"approvers,omitempty"`
	LedgerSeq   int64    `json:"ledger_seq"`
	LedgerHash  string   `json:"ledger_hash,omitempty"`
	OccurredAt  string   `json:"occurred_at"`
}

type holdEventListOut struct {
	Items []holdEventOut `json:"items"`
}

type holdDecisionOut struct {
	Held  bool         `json:"held"`
	Holds []holdRefOut `json:"holds,omitempty"`
}

func newComplianceHoldsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "holds",
		Short: "Place, inspect and release legal holds",
		Long: "holds operates the legal-hold preservation plane. Placing one takes effect\n" +
			"immediately — the duty to preserve admits no waiting. Releasing one is\n" +
			"dual-control with no break-glass path, so `release` can succeed without\n" +
			"having released anything yet (exit 7).",
		Example: "  olivares compliance holds ls --status active\n" +
			"  olivares compliance holds check --subject-kind user --subject-ref u-7\n" +
			"  olivares compliance holds release lh-1 --reason \"matter closed\" --yes",
	}
	cmd.AddCommand(
		newHoldsListCmd(flags),
		newHoldsGetCmd(flags),
		newHoldsCheckCmd(flags),
		newHoldsPlaceCmd(flags),
		newHoldsReleaseCmd(flags),
		newHoldsCustodyCmd(flags),
	)
	return cmd
}

func newHoldsListCmd(flags *authClientFlags) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List legal holds",
		Long:    "ls lists the legal holds on this tenant. A hold suspends deletion for the\ndata it names, so this is the answer to \"what is currently un-deletable and why\".\nFilter by --status to separate the active ones from those already released.",
		Example: "  olivares compliance holds ls\n  olivares compliance holds ls --status active -o json",
		Args:    cobra.NoArgs,
		Aliases: []string{"list"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			res, err := complianceCall{flags: flags, method: http.MethodGet, path: "/holds", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var out holdListOut
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(out.Items) == 0 {
					_, werr := fmt.Fprintln(w, "no legal holds")
					return werr
				}
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, "ID\tSTATUS\tMATTER\tSCOPE\tCOVERS\tPLACED"); werr != nil {
					return werr
				}
				for _, h := range out.Items {
					if _, werr := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						h.ID, h.Status, h.MatterRef, h.ScopeKind, holdCoversText(h), h.CreatedAt); werr != nil {
						return werr
					}
				}
				return tw.Flush()
			}, out)
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status (active, released)")
	return cmd
}

// holdCoversText states the matching rule in words, because "subject" alone does
// not tell an operator whether THEIR subject is preserved.
func holdCoversText(h holdOut) string {
	switch h.ScopeKind {
	case "tenant":
		return "everything in this tenant"
	case "data_class":
		return "data class " + h.DataClass
	case "subject":
		return h.SubjectKind + ":" + h.SubjectRef
	default:
		return "-"
	}
}

func newHoldsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <hold-id>",
		Short:   "Show one legal hold",
		Long:    "get shows one legal hold in full: what it preserves, who placed it, when, and\nunder which matter. Use it before releasing anything — release is dual-control and\nirreversible in the sense that the preservation duty it was carrying ends.",
		Example: "  olivares compliance holds get lh-1\n  olivares compliance holds get lh-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := complianceCall{
				flags: flags, method: http.MethodGet, path: "/holds/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var h holdOut
			if err := res.decode(&h); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "id\t%s\n", h.ID)
				fmt.Fprintf(tw, "status\t%s\n", h.Status)
				fmt.Fprintf(tw, "matter\t%s\n", h.MatterRef)
				fmt.Fprintf(tw, "scope\t%s\n", h.ScopeKind)
				fmt.Fprintf(tw, "covers\t%s\n", holdCoversText(h))
				fmt.Fprintf(tw, "reason\t%s\n", h.Reason)
				fmt.Fprintf(tw, "placed\t%s by %s\n", h.CreatedAt, h.CreatedBy)
				if h.ReleasedAt != "" {
					fmt.Fprintf(tw, "released\t%s by %s\n", h.ReleasedAt, h.ReleasedBy)
					fmt.Fprintf(tw, "approval\t%s\n", h.ReleaseApprovalRef)
				}
				return tw.Flush()
			}, h)
		},
	}
}

func newHoldsCheckCmd(flags *authClientFlags) *cobra.Command {
	var subjectKind, subjectRef, dataClass string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Ask whether any active hold already covers a subject or class",
		Long: "check runs the SAME matching rule the erasure path enforces before it\n" +
			"destroys anything. Use it to find out what is preserved before acting,\n" +
			"rather than discovering it from a rejected erasure.",
		Example: "  olivares compliance holds check --subject-kind user --subject-ref u-7\n" +
			"  olivares compliance holds check --data-class session_transcript",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (subjectKind == "") != (subjectRef == "") {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--subject-kind and --subject-ref must be given together"))
			}
			if subjectKind == "" && dataClass == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("give --subject-kind/--subject-ref, --data-class, or both"))
			}
			q := url.Values{}
			if subjectKind != "" {
				q.Set("subject_kind", subjectKind)
				q.Set("subject_ref", subjectRef)
			}
			if dataClass != "" {
				q.Set("data_class", dataClass)
			}
			res, err := complianceCall{flags: flags, method: http.MethodGet, path: "/holds/check", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var dec holdDecisionOut
			if err := res.decode(&dec); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if !dec.Held {
					_, werr := fmt.Fprintln(w, "not held: no active hold covers this")
					return werr
				}
				if _, werr := fmt.Fprintf(w, "HELD by %d active hold(s):\n", len(dec.Holds)); werr != nil {
					return werr
				}
				tw := newTabWriter(w)
				for _, h := range dec.Holds {
					fmt.Fprintf(tw, "  %s\tmatter %s\tscope %s\n", h.ID, h.MatterRef, h.ScopeKind)
				}
				return tw.Flush()
			}, dec)
		},
	}
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "", "subject kind (e.g. user)")
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "subject reference")
	cmd.Flags().StringVar(&dataClass, "data-class", "", "registered data class id")
	return cmd
}

func newHoldsPlaceCmd(flags *authClientFlags) *cobra.Command {
	var matter, title, scope, dataClass, subjectKind, subjectRef, reason, onBehalfOf string
	cmd := &cobra.Command{
		Use:   "place",
		Short: "Place a legal hold (takes effect immediately)",
		Long: "place records a preservation order that vetoes every destruction path\n" +
			"matching its scope. It is ungated on purpose: preservation is the safe\n" +
			"direction and the duty to preserve admits no waiting.",
		Example: "  olivares compliance holds place --matter CASE-42 --scope subject \\\n" +
			"    --subject-kind user --subject-ref u-7 --reason \"litigation hold\"",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]string{
				"matter_ref": matter,
				"scope_kind": scope,
				"reason":     reason,
			}
			if title != "" {
				body["title"] = title
			}
			if dataClass != "" {
				body["data_class"] = dataClass
			}
			if subjectKind != "" {
				body["subject_kind"] = subjectKind
			}
			if subjectRef != "" {
				body["subject_ref"] = subjectRef
			}
			if onBehalfOf != "" {
				body["on_behalf_of"] = onBehalfOf
			}
			res, err := complianceCall{
				flags: flags, method: http.MethodPost, path: "/holds", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			var h holdOut
			if err := res.decode(&h); err != nil {
				return err
			}
			ok, why := confirmedCreate(res.status, h.ID, h.Status, holdStatusActiveLiteral)
			rerr := renderOut(cmd, func(w io.Writer) error {
				if !ok {
					_, werr := fmt.Fprintf(w,
						"hold NOT confirmed as placed: %s — check `compliance holds ls` before relying on preservation\n", why)
					return werr
				}
				_, werr := fmt.Fprintf(w, "hold %s placed — now preserving %s\n", h.ID, holdCoversText(h))
				return werr
			}, h)
			if rerr != nil {
				return rerr
			}
			if !ok {
				return exitcode.New(exitcode.Degraded, nil)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&matter, "matter", "", "matter or case reference (required)")
	cmd.Flags().StringVar(&title, "title", "", "human-readable title")
	cmd.Flags().StringVar(&scope, "scope", "subject", "scope: tenant, data_class or subject")
	cmd.Flags().StringVar(&dataClass, "data-class", "", "registered data class id (scope=data_class)")
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "", "subject kind (scope=subject)")
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "subject reference (scope=subject)")
	cmd.Flags().StringVar(&reason, "reason", "", "why this hold exists (required; recorded in custody)")
	cmd.Flags().StringVar(&onBehalfOf, "on-behalf-of", "", "the person this order is placed for")
	_ = cmd.MarkFlagRequired("matter")
	_ = cmd.MarkFlagRequired("reason")
	_ = cmd.RegisterFlagCompletionFunc("scope", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"tenant", "data_class", "subject"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func newHoldsReleaseCmd(flags *authClientFlags) *cobra.Command {
	var reason, onBehalfOf string
	var yes bool
	cmd := &cobra.Command{
		Use:   "release <hold-id>",
		Short: "Release a legal hold (dual-control, no break-glass)",
		Long: "release lifts a preservation order. Data the hold protected becomes\n" +
			"eligible for deletion again, so it requires two distinct approver accounts\n" +
			"and has no emergency override.\n\n" +
			"Exit 7 means the approval was opened and the hold is STILL ACTIVE — not\n" +
			"that anything was released.",
		Example: "  olivares compliance holds release lh-1 --reason \"matter closed\" --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"release legal hold %s (data it preserved becomes deletable again)", id)); err != nil {
				return err
			}
			body := map[string]string{}
			if reason != "" {
				body["reason"] = reason
			}
			if onBehalfOf != "" {
				body["on_behalf_of"] = onBehalfOf
			}
			res, err := complianceCall{
				flags: flags, method: http.MethodPost,
				path: "/holds/" + url.PathEscape(id) + "/release", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportPending(cmd, res, "hold release")
			}
			var h holdOut
			if err := res.decode(&h); err != nil {
				return err
			}
			// Same allowlist rule: only a hold the engine reports as `released`
			// is a release. Announcing one from the HTTP status alone would assume
			// the outcome instead of reading it.
			released := h.Status == holdStatusReleasedLiteral
			rerr := renderOut(cmd, func(w io.Writer) error {
				if !released {
					_, werr := fmt.Fprintf(w,
						"hold %s: the control plane reports status %q, NOT released — preservation may still be in force\n",
						h.ID, h.Status)
					return werr
				}
				_, werr := fmt.Fprintf(w, "hold %s released (approval %s)\n", h.ID, h.ReleaseApprovalRef)
				return werr
			}, h)
			if rerr != nil {
				return rerr
			}
			if !released {
				return exitcode.New(exitcode.Degraded, nil)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the hold is being released (recorded in custody)")
	cmd.Flags().StringVar(&onBehalfOf, "on-behalf-of", "", "the person this release is made for")
	addYesFlag(cmd, &yes)
	return cmd
}

func newHoldsCustodyCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "custody <hold-id>",
		Short:   "Show a hold's append-only chain of custody",
		Long:    "custody prints the append-only chain of every action taken on one hold: placed,\nchecked, released, by whom and when. It is the evidence an auditor asks for when the\nquestion is not what the hold says now but what it has said all along.",
		Example: "  olivares compliance holds custody lh-1\n  olivares compliance holds custody lh-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := complianceCall{
				flags: flags, method: http.MethodGet,
				path: "/holds/" + url.PathEscape(args[0]) + "/events",
			}.do(cmd)
			if err != nil {
				return err
			}
			var out holdEventListOut
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(out.Items) == 0 {
					_, werr := fmt.Fprintln(w, "no custody events")
					return werr
				}
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, "WHEN\tEVENT\tACTOR\tLEDGER SEQ\tAPPROVERS"); werr != nil {
					return werr
				}
				for _, e := range out.Items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
						e.OccurredAt, e.Event, e.Actor, e.LedgerSeq, strings.Join(e.Approvers, ","))
				}
				return tw.Flush()
			}, out)
		},
	}
}

// ---- erasure -----------------------------------------------------------------

type erasureOut struct {
	ID           string   `json:"id"`
	SubjectKind  string   `json:"subject_kind"`
	SubjectToken string   `json:"subject_token"`
	Subject      string   `json:"subject,omitempty"`
	DataClasses  []string `json:"data_classes"`
	CaseRef      string   `json:"case_ref"`
	Reason       string   `json:"reason"`
	RequestedBy  string   `json:"requested_by"`
	Status       string   `json:"status"`
	ApprovalRef  string   `json:"approval_ref,omitempty"`
	CreatedAt    string   `json:"created_at"`
}

type erasureListOut struct {
	Items []erasureOut `json:"items"`
}

type retainedOut struct {
	Records string `json:"records"`
	Basis   string `json:"basis"`
}

type targetOut struct {
	Target string `json:"target,omitempty"`
	Label  string `json:"label,omitempty"`
	Rows   int64  `json:"rows,omitempty"`
	Note   string `json:"note,omitempty"`
}

type receiptOut struct {
	ErasureID    string        `json:"erasure_id"`
	SubjectKind  string        `json:"subject_kind"`
	SubjectToken string        `json:"subject_token"`
	Targets      []targetOut   `json:"targets"`
	Account      string        `json:"account_outcome"`
	Provider     string        `json:"provider_outcome"`
	KeyShredded  bool          `json:"key_shredded"`
	VerifyOK     bool          `json:"verify_ok"`
	VerifyN      int64         `json:"verify_checked"`
	VerifyWhy    string        `json:"verify_reason,omitempty"`
	Retained     []retainedOut `json:"retained"`
	CaseRef      string        `json:"case_ref"`
	ApprovalRef  string        `json:"approval_ref,omitempty"`
	LedgerSeq    int64         `json:"ledger_seq"`
	LedgerHash   string        `json:"ledger_hash,omitempty"`
	ManifestHash string        `json:"manifest_hash"`
	OccurredAt   string        `json:"occurred_at"`
	FloorDays    int           `json:"provider_floor_days,omitempty"`
	FloorKnown   bool          `json:"provider_floor_known,omitempty"`
	FloorSource  string        `json:"provider_floor_source,omitempty"`
}

func newComplianceErasureCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "erasure",
		Short: "Register, execute and evidence GDPR erasure requests",
		Long: "erasure works the right-to-erasure lifecycle. Registering a request is\n" +
			"safe — it mints the subject key and records the request. Executing one is\n" +
			"irreversible: it destroys the subject key, which permanently renders every\n" +
			"token for that person unintelligible.",
		Example: "  olivares compliance erasure ls --status pending_approval\n" +
			"  olivares compliance erasure execute er-1 --yes\n" +
			"  olivares compliance erasure receipt er-1 -o json",
	}
	cmd.AddCommand(
		newErasureListCmd(flags),
		newErasureGetCmd(flags),
		newErasureRequestCmd(flags),
		newErasureExecuteCmd(flags),
		newErasureReceiptCmd(flags),
		newErasureCustodyCmd(flags),
	)
	return cmd
}

func newErasureListCmd(flags *authClientFlags) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List erasure requests",
		Long:    "ls lists the right-to-be-forgotten requests on this tenant and where each one\nstands. A request can be blocked by a legal hold — the hold wins, and that is the\npoint of listing both surfaces from the same CLI.",
		Example: "  olivares compliance erasure ls\n  olivares compliance erasure ls -o json",
		Args:    cobra.NoArgs,
		Aliases: []string{"list"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			res, err := complianceCall{flags: flags, method: http.MethodGet, path: "/erasure", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var out erasureListOut
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(out.Items) == 0 {
					_, werr := fmt.Fprintln(w, "no erasure requests")
					return werr
				}
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, "ID\tSTATUS\tCASE\tKIND\tSUBJECT\tREQUESTED"); werr != nil {
					return werr
				}
				for _, e := range out.Items {
					subj := e.Subject
					if subj == "" {
						subj = e.SubjectToken
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						e.ID, e.Status, e.CaseRef, e.SubjectKind, subj, e.CreatedAt)
				}
				return tw.Flush()
			}, out)
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status (received, pending_approval, completed, ...)")
	return cmd
}

func newErasureGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <erasure-id>",
		Short:   "Show one erasure request",
		Long:    "get shows one erasure request in full: the subject, the scope, its current state\nand anything blocking it. An erasure that reports a provider leg as not_wired has NOT\nerased anything there, and this is where that is visible.",
		Example: "  olivares compliance erasure get er-1\n  olivares compliance erasure get er-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := complianceCall{
				flags: flags, method: http.MethodGet, path: "/erasure/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var e erasureOut
			if err := res.decode(&e); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "id\t%s\n", e.ID)
				fmt.Fprintf(tw, "status\t%s\n", e.Status)
				fmt.Fprintf(tw, "case\t%s\n", e.CaseRef)
				fmt.Fprintf(tw, "subject\t%s:%s\n", e.SubjectKind, firstNonEmpty(e.Subject, e.SubjectToken))
				fmt.Fprintf(tw, "classes\t%s\n", strings.Join(e.DataClasses, ", "))
				fmt.Fprintf(tw, "requested\t%s by %s\n", e.CreatedAt, e.RequestedBy)
				return tw.Flush()
			}, e)
		},
	}
}

func newErasureRequestCmd(flags *authClientFlags) *cobra.Command {
	var subjectKind, subjectRef, caseRef, reason string
	var aliases, classes []string
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Register an erasure request (destroys nothing)",
		Long: "request records a data-subject erasure request and mints the subject key.\n" +
			"It is the safe half of the workflow: nothing is destroyed until `execute`.",
		Example: "  olivares compliance erasure request --subject-ref u-7 --case-ref DSAR-9 \\\n" +
			"    --reason \"Art. 17 request\"",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{
				"subject_kind": subjectKind,
				"subject_ref":  subjectRef,
				"case_ref":     caseRef,
			}
			if reason != "" {
				body["reason"] = reason
			}
			if len(aliases) > 0 {
				body["aliases"] = aliases
			}
			if len(classes) > 0 {
				body["data_classes"] = classes
			}
			res, err := complianceCall{flags: flags, method: http.MethodPost, path: "/erasure", body: body}.do(cmd)
			if err != nil {
				return err
			}
			var e erasureOut
			if err := res.decode(&e); err != nil {
				return err
			}
			ok, why := confirmedCreate(res.status, e.ID, e.Status, erasureStatusReceivedLiteral)
			rerr := renderOut(cmd, func(w io.Writer) error {
				if !ok {
					_, werr := fmt.Fprintf(w,
						"erasure request NOT confirmed as registered: %s — check `compliance erasure ls` before relying on it\n", why)
					return werr
				}
				_, werr := fmt.Fprintf(w,
					"erasure request %s registered (case %s) — nothing has been destroyed; run `erasure execute %s` when ready\n",
					e.ID, e.CaseRef, e.ID)
				return werr
			}, e)
			if rerr != nil {
				return rerr
			}
			if !ok {
				return exitcode.New(exitcode.Degraded, nil)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "user", "subject kind")
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "subject reference (required)")
	cmd.Flags().StringVar(&caseRef, "case-ref", "", "your DSAR case reference (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "why this request exists")
	cmd.Flags().StringArrayVar(&aliases, "alias", nil, "additional identifier for the same person, repeatable")
	cmd.Flags().StringArrayVar(&classes, "data-class", nil, "narrow to these registered data classes, repeatable")
	_ = cmd.MarkFlagRequired("subject-ref")
	_ = cmd.MarkFlagRequired("case-ref")
	return cmd
}

func newErasureExecuteCmd(flags *authClientFlags) *cobra.Command {
	var reason string
	var providerIDs []string
	var yes bool
	cmd := &cobra.Command{
		Use:   "execute <erasure-id>",
		Short: "Execute an erasure (IRREVERSIBLE, dual-control)",
		Long: "execute runs the governed erasure: the legal-hold gate first, then the\n" +
			"dual-control approval, then physical erasure, then the crypto-shred that\n" +
			"destroys the subject key.\n\n" +
			"This cannot be undone. Exit 7 means the erasure did not FINISH — either the\n" +
			"approval was opened and nothing was erased, or the provider still owes\n" +
			"deletions after local data was already erased, or the run completed with\n" +
			"gaps. The command says which. Exit 5 with a hold listing means a\n" +
			"preservation order vetoed it.",
		Example: "  olivares compliance erasure execute er-1 --reason \"approved DSAR\" --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"irreversibly erase the subject of request %s and destroy their key (no undo, no restore)", id)); err != nil {
				return err
			}
			body := map[string]any{}
			if reason != "" {
				body["reason"] = reason
			}
			if len(providerIDs) > 0 {
				body["provider_user_ids"] = providerIDs
			}
			res, err := complianceCall{
				flags: flags, method: http.MethodPost,
				path: "/erasure/" + url.PathEscape(id) + "/execute", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportPending(cmd, res, "erasure")
			}
			return renderErasureOutcome(cmd, res, flags)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why this erasure is being executed")
	cmd.Flags().StringArrayVar(&providerIDs, "provider-user-id", nil, "provider-side user id to erase, repeatable")
	addYesFlag(cmd, &yes)
	return cmd
}

// terminalStatusOf reads back the status the engine PERSISTED for this erasure,
// because the 200 body (the receipt) does not carry one.
//
// Returns ("", true) when the status could not be established — the caller then
// reports the outcome as unconfirmed and still exits non-zero.
func terminalStatusOf(cmd *cobra.Command, receipt map[string]any, flags *authClientFlags) (string, bool) {
	id, _ := receipt["erasure_id"].(string)
	if strings.TrimSpace(id) == "" {
		return "", true
	}
	res, err := complianceCall{
		flags: flags, method: http.MethodGet, path: "/erasure/" + url.PathEscape(id),
	}.do(cmd)
	if err != nil {
		return "", true
	}
	var req struct {
		Status string `json:"status"`
	}
	if derr := res.decode(&req); derr != nil || strings.TrimSpace(req.Status) == "" {
		return "", true
	}
	return req.Status, false
}

// renderErasureOutcome prints whatever the engine returned for a 200 execute.
//
// A 200 IS NOT ALWAYS A CLEAN ERASURE. The engine marks a request
// completed_with_gaps (erasure.go:1346) when the account leg was not attempted,
// the provider eraser is not wired at all (provider_outcome "not_wired: …",
// erasure.go:1468) or the ledger re-verification failed. Those exit 7, the same
// Degraded code the contract defines for a not-fully-OK outcome.
//
// WHERE THAT STATUS ACTUALLY LIVES. The 200 body is the RECEIPT
// (erasure.go:1416), and erasureReceiptDTO has no status field at all
// (erasure.go:163-181). A previous version of this function read
// body["status"], which therefore never matched — the CLI kept exiting 0 — and
// the test written for it fabricated a body this endpoint cannot produce. The
// follow-up contrast caught both.
//
// So the terminal status is READ BACK from the request the engine persisted
// (GET /erasure/{id}), addressed by the receipt's own erasure_id. That is
// reading the engine's verdict, not recomputing it: the gap rule is
// `!accountAttempted || !provider.Wired || !verifyOK` (erasure.go:1345) and
// re-deriving it here would be a second implementation of a compliance decision,
// free to drift from the first.
//
// If the read-back cannot be made, the outcome is reported as UNCONFIRMED and
// still exits 7 — never 0. "I could not check" is not "it is clean".
func renderErasureOutcome(cmd *cobra.Command, res complianceResult, flags *authClientFlags) error {
	var generic map[string]any
	if err := res.decode(&generic); err != nil {
		return err
	}
	status, unconfirmed := terminalStatusOf(cmd, generic, flags)
	// ALLOWLIST, not denylist. Only `completed` is a clean erasure; every other
	// value — completed_with_gaps, a state this build does not know, or no state
	// at all — exits non-zero.
	//
	// This is the THIRD time this session wrote the same mistake: the 202 path was
	// inverted to "every 202 is incomplete unless proven otherwise", and then the
	// 200 path was written as "clean unless it says completed_with_gaps". Closing
	// a case instead of its class is how the next contrast finds the sibling.
	clean := status == erasureStatusCompletedLiteral && !unconfirmed
	// -o json must carry the same verdict the text form states, so a script that
	// parses the receipt is not left reading a clean-looking document after an
	// unconfirmed run.
	generic["olivares_cli_terminal_status"] = status
	generic["olivares_cli_confirmed"] = !unconfirmed
	generic["olivares_cli_clean"] = clean

	err := renderOut(cmd, func(w io.Writer) error {
		headline := "erasure " + status
		switch {
		case unconfirmed:
			headline = "erasure ran, but its final status could NOT be confirmed — read the receipt and check `compliance erasure get` before treating this as finished"
		case status == "completed_with_gaps":
			headline = "erasure COMPLETED WITH GAPS — parts of it could not be carried out; read the outcomes below and the receipt"
		case !clean:
			headline = "erasure ran, but the control plane reports it as " + status +
				" — that is NOT a clean completion; read the receipt before treating it as finished"
		}
		if _, werr := fmt.Fprintf(w, "%s\n", headline); werr != nil {
			return werr
		}
		if shredded, ok := generic["key_shredded"].(bool); ok {
			if _, werr := fmt.Fprintf(w, "key shredded: %t\n", shredded); werr != nil {
				return werr
			}
		}
		for _, leg := range []struct{ label, key string }{
			{"account leg", "account_outcome"},
			{"provider leg", "provider_outcome"},
		} {
			if v, ok := generic[leg.key].(string); ok && v != "" {
				if _, werr := fmt.Fprintf(w, "%s: %s\n", leg.label, v); werr != nil {
					return werr
				}
			}
		}
		if ref, ok := generic["approval_ref"].(string); ok && ref != "" {
			if _, werr := fmt.Fprintf(w, "approval ref: %s\n", ref); werr != nil {
				return werr
			}
		}
		return nil
	}, generic)
	if err != nil {
		return err
	}
	if !clean {
		return exitcode.New(exitcode.Degraded, nil)
	}
	return nil
}

func newErasureReceiptCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "receipt <erasure-id>",
		Example: "  olivares compliance erasure receipt er-1\n  olivares compliance erasure receipt er-1 -o json",
		Short:   "Show the sealed, ledger-anchored erasure receipt",
		Long: "receipt is the verifiable artifact of an executed erasure: what was\n" +
			"destroyed, what is deliberately retained and under which legal basis, and\n" +
			"the provider floor that survives regardless.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := complianceCall{
				flags: flags, method: http.MethodGet,
				path: "/erasure/" + url.PathEscape(args[0]) + "/receipt",
			}.do(cmd)
			if err != nil {
				return err
			}
			var r receiptOut
			if err := res.decode(&r); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "erasure\t%s\n", r.ErasureID)
				fmt.Fprintf(tw, "case\t%s\n", r.CaseRef)
				fmt.Fprintf(tw, "key shredded\t%t\n", r.KeyShredded)
				// The legs the engine reports separately: a not-wired provider is
				// exactly why a request ends completed_with_gaps.
				fmt.Fprintf(tw, "account leg\t%s\n", firstNonEmpty(r.Account, "-"))
				fmt.Fprintf(tw, "provider leg\t%s\n", firstNonEmpty(r.Provider, "-"))
				fmt.Fprintf(tw, "ledger verified\t%t (%d events)\n", r.VerifyOK, r.VerifyN)
				if r.VerifyWhy != "" {
					fmt.Fprintf(tw, "verify note\t%s\n", r.VerifyWhy)
				}
				fmt.Fprintf(tw, "manifest\t%s\n", r.ManifestHash)
				fmt.Fprintf(tw, "ledger seq\t%d\n", r.LedgerSeq)
				fmt.Fprintf(tw, "sealed\t%s\n", r.OccurredAt)
				if err := tw.Flush(); err != nil {
					return err
				}
				if len(r.Targets) > 0 {
					fmt.Fprintln(w, "\nerased:")
					for _, tg := range r.Targets {
						fmt.Fprintf(w, "  %s  %d rows  %s\n", firstNonEmpty(tg.Label, tg.Target), tg.Rows, tg.Note)
					}
				}
				// What SURVIVES is not an appendix: it is what makes the receipt
				// defensible, so the text form always prints it.
				if len(r.Retained) > 0 {
					fmt.Fprintln(w, "\nretained (and why this is lawful):")
					for _, rr := range r.Retained {
						fmt.Fprintf(w, "  %s\n    %s\n", rr.Records, rr.Basis)
					}
				}
				if r.FloorKnown {
					fmt.Fprintf(w,
						"\nprovider floor: the model provider retains input/output for %d days regardless of this erasure (%s)\n",
						r.FloorDays, r.FloorSource)
				}
				return nil
			}, r)
		},
	}
}

func newErasureCustodyCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "custody <erasure-id>",
		Short:   "Show an erasure's append-only chain of custody",
		Long:    "custody prints the append-only chain of every action taken on one erasure request,\nfrom receipt to execution. It is what proves the request was handled within its statutory\nwindow, and by whom, long after the data itself is gone.",
		Example: "  olivares compliance erasure custody er-1\n  olivares compliance erasure custody er-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := complianceCall{
				flags: flags, method: http.MethodGet,
				path: "/erasure/" + url.PathEscape(args[0]) + "/events",
			}.do(cmd)
			if err != nil {
				return err
			}
			var out struct {
				Items []struct {
					Event      string   `json:"event"`
					Actor      string   `json:"actor"`
					Note       string   `json:"note,omitempty"`
					Approvers  []string `json:"approvers,omitempty"`
					LedgerSeq  int64    `json:"ledger_seq"`
					OccurredAt string   `json:"occurred_at"`
				} `json:"items"`
			}
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(out.Items) == 0 {
					_, werr := fmt.Fprintln(w, "no custody events")
					return werr
				}
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, "WHEN\tEVENT\tACTOR\tLEDGER SEQ\tNOTE"); werr != nil {
					return werr
				}
				for _, e := range out.Items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", e.OccurredAt, e.Event, e.Actor, e.LedgerSeq, e.Note)
				}
				return tw.Flush()
			}, out)
		},
	}
}

// ---- data subjects -----------------------------------------------------------

func newComplianceSubjectCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subject",
		Short: "Answer a data subject's erasure request by subject id",
		Long: "subject addresses a person directly rather than a request id — the shape\n" +
			"an Article 15 or 17 answer actually takes.",
		Example: "  olivares compliance subject status u-7\n" +
			"  olivares compliance subject erase u-7 --case-ref DSAR-9 --yes",
	}
	cmd.AddCommand(newSubjectStatusCmd(flags), newSubjectEraseCmd(flags))
	return cmd
}

func newSubjectStatusCmd(flags *authClientFlags) *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:     "status <subject-id>",
		Short:   "Show erasure status for one data subject",
		Long:    "status answers, for ONE data subject, whether anything about them is still held and\nwhat has already been erased. It is the question a regulator or the subject themselves\nasks, and it spans every erasure request that ever named them.",
		Example: "  olivares compliance subject status sub-42\n  olivares compliance subject status sub-42 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if kind != "" {
				q.Set("subject_kind", kind)
			}
			res, err := complianceCall{
				flags: flags, method: http.MethodGet,
				path:  "/data-subjects/" + url.PathEscape(args[0]) + "/erasure-status",
				query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var out struct {
				SubjectID    string `json:"subject_id"`
				SubjectKind  string `json:"subject_kind"`
				State        string `json:"state"`
				KeyShredded  bool   `json:"key_shredded"`
				Verified     bool   `json:"verified"`
				VerifyReason string `json:"verify_reason,omitempty"`
				ApprovalRef  string `json:"approval_ref,omitempty"`
				Disclaimer   string `json:"disclaimer"`
			}
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "subject\t%s:%s\n", out.SubjectKind, out.SubjectID)
				fmt.Fprintf(tw, "state\t%s\n", out.State)
				fmt.Fprintf(tw, "key shredded\t%t\n", out.KeyShredded)
				fmt.Fprintf(tw, "verified\t%t\n", out.Verified)
				if out.VerifyReason != "" {
					fmt.Fprintf(tw, "verify note\t%s\n", out.VerifyReason)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				_, werr := fmt.Fprintf(w, "\n%s\n", out.Disclaimer)
				return werr
			}, out)
		},
	}
	cmd.Flags().StringVar(&kind, "subject-kind", "", "subject kind (default: user)")
	return cmd
}

func newSubjectEraseCmd(flags *authClientFlags) *cobra.Command {
	var kind, caseRef, reason string
	var aliases, classes, providerIDs []string
	var yes bool
	cmd := &cobra.Command{
		Use:   "erase <subject-id>",
		Short: "Register and execute an erasure for one subject (IRREVERSIBLE)",
		Long: "erase is the one-shot path: it registers the request if needed and then\n" +
			"executes it under the same two gates. It cannot be undone.\n\n" +
			"Exit 7 means the erasure did not FINISH — read what the command prints: it\n" +
			"may be awaiting approval with nothing erased, or partially erased with the\n" +
			"provider still owing deletions. Exit 5 with a hold listing means a\n" +
			"preservation order vetoed it.",
		Example: "  olivares compliance subject erase u-7 --case-ref DSAR-9 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"irreversibly erase data subject %s and destroy their key (no undo, no restore)", id)); err != nil {
				return err
			}
			body := map[string]any{}
			if kind != "" {
				body["subject_kind"] = kind
			}
			if caseRef != "" {
				body["case_ref"] = caseRef
			}
			if reason != "" {
				body["reason"] = reason
			}
			if len(aliases) > 0 {
				body["aliases"] = aliases
			}
			if len(classes) > 0 {
				body["data_classes"] = classes
			}
			if len(providerIDs) > 0 {
				body["provider_user_ids"] = providerIDs
			}
			res, err := complianceCall{
				flags: flags, method: http.MethodPost,
				path: "/data-subjects/" + url.PathEscape(id) + "/erase", body: body,
			}.do(cmd)
			if err != nil {
				return err
			}
			if res.status == http.StatusAccepted {
				return reportPending(cmd, res, "erasure")
			}
			return renderErasureOutcome(cmd, res, flags)
		},
	}
	cmd.Flags().StringVar(&kind, "subject-kind", "", "subject kind (default: user)")
	cmd.Flags().StringVar(&caseRef, "case-ref", "", "your DSAR case reference")
	cmd.Flags().StringVar(&reason, "reason", "", "why this erasure is being executed")
	cmd.Flags().StringArrayVar(&aliases, "alias", nil, "additional identifier for the same person, repeatable")
	cmd.Flags().StringArrayVar(&classes, "data-class", nil, "narrow to these registered data classes, repeatable")
	cmd.Flags().StringArrayVar(&providerIDs, "provider-user-id", nil, "provider-side user id to erase, repeatable")
	addYesFlag(cmd, &yes)
	return cmd
}

// ---- regulatory calendar -----------------------------------------------------

func newComplianceCalendarCmd(flags *authClientFlags) *cobra.Command {
	var framework string
	cmd := &cobra.Command{
		Use:   "calendar",
		Short: "Show the regulatory calendar and watchlist",
		Long: "calendar lists dated regulatory milestones, each with its primary source\n" +
			"and the date it was verified, plus the watchlist of rulemakings and draft\n" +
			"standards that have no date to cite yet.\n\n" +
			"Provisional agreements and texts adopted pending publication are NOT\n" +
			"in-force law; the status column says so per row.",
		Example: "  olivares compliance calendar\n" +
			"  olivares compliance calendar --framework eu_ai_act -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if framework != "" {
				q.Set("framework", framework)
			}
			res, err := complianceCall{flags: flags, method: http.MethodGet, path: "/calendar", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var out struct {
				Milestones []struct {
					ID         string `json:"id"`
					Regime     string `json:"regime"`
					Date       string `json:"date"`
					Title      string `json:"title"`
					Effect     string `json:"effect"`
					Status     string `json:"status"`
					VerifiedOn string `json:"verified_on"`
					Source     struct {
						URL       string `json:"url"`
						Title     string `json:"title"`
						Publisher string `json:"publisher"`
					} `json:"source"`
				} `json:"milestones"`
				Watchlist []struct {
					ID         string `json:"id"`
					Name       string `json:"name"`
					Status     string `json:"status"`
					Expected   string `json:"expected,omitempty"`
					VerifiedOn string `json:"verified_on"`
				} `json:"watchlist"`
				Disclaimer string `json:"disclaimer"`
			}
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, "DATE\tSTATUS\tREGIME\tMILESTONE\tVERIFIED"); werr != nil {
					return werr
				}
				for _, m := range out.Milestones {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.Date, m.Status, m.Regime, m.Title, m.VerifiedOn)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if len(out.Watchlist) > 0 {
					fmt.Fprintln(w, "\nwatchlist (no date to cite yet):")
					wt := newTabWriter(w)
					for _, it := range out.Watchlist {
						fmt.Fprintf(wt, "  %s\t%s\t%s\n", it.Status, it.Name, it.VerifiedOn)
					}
					if err := wt.Flush(); err != nil {
						return err
					}
				}
				_, werr := fmt.Fprintf(w, "\n%s\n", out.Disclaimer)
				return werr
			}, out)
		},
	}
	cmd.Flags().StringVar(&framework, "framework", "", "filter to one framework id")
	return cmd
}

// ---- DORA / OSCAL / depth (read surfaces) ------------------------------------

func newComplianceDoraCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dora",
		Short: "Inspect DORA registers and classified incidents",
		Long: "dora lists the Register of Information and the classified major-incident\n" +
			"records this build has stored. Generating them is provided by the\n" +
			"enterprise add-on.\n\n" +
			"Only listing is exposed here today; the console exports registers and\n" +
			"incident reports.",
		Example: "  olivares compliance dora registers\n  olivares compliance dora incidents -o json",
	}
	cmd.AddCommand(
		simpleListCmd(flags, "registers", "List DORA registers of information",
			"Lists the DORA Registers of Information generated for this tenant. A register is the\ninventory of ICT third-party arrangements a financial entity must keep and file with its\ncompetent authority; generation is an enterprise capability, and this read lists what exists.",
			"  olivares compliance dora registers\n  olivares compliance dora registers -o json",
			"/dora/register",
			[]string{"ID", "REGULATION", "ENTITY", "ERRORS", "GENERATED"},
			func(m map[string]any) []any {
				return []any{
					str(m, "id"), str(m, "regulation"),
					firstNonEmpty(str(m, "entity_name"), str(m, "entity_lei")),
					numOf(m, "error_count"), str(m, "generated_at"),
				}
			}),
		simpleListCmd(flags, "incidents", "List classified DORA incidents",
			"Lists ICT incidents classified under DORA's major-incident criteria. Classification is an\nenterprise capability; this read shows what has been classified and when, so the reporting\nclock for each one can be audited after the fact.",
			"  olivares compliance dora incidents\n  olivares compliance dora incidents -o json",
			"/dora/incidents",
			[]string{"ID", "REFERENCE", "MAJOR", "CLASSIFIED"},
			func(m map[string]any) []any {
				major := "no"
				if b, ok := m["major"].(bool); ok && b {
					major = "YES"
				}
				return []any{str(m, "id"), str(m, "reference"), major, str(m, "classified_at")}
			}),
	)
	return cmd
}

func newComplianceOscalCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "oscal",
		Long:    "oscal exposes the NIST OSCAL surface: the machine-readable control catalogs,\nprofiles and system security plans a US federal program exchanges instead of\nspreadsheets. Ingestion of profiles and SSPs is an enterprise capability and answers\n501 without it; the export of what this deployment already knows is open.",
		Short:   "Inspect ingested OSCAL profiles and SSPs",
		Example: "  olivares compliance oscal ls",
	}
	cmd.AddCommand(
		simpleListCmd(flags, "ls", "List registered OSCAL documents",
			"Lists the OSCAL profiles registered on this deployment. OSCAL is NIST's machine-readable\ncontrol catalog format; a profile selects and tailors controls from a catalog. Profile and\nSSP ingestion is an enterprise capability, so on an open build this list is honestly empty.",
			"  olivares compliance oscal ls\n  olivares compliance oscal ls -o json",
			"/oscal/profiles",
			[]string{"ID", "FRAMEWORK", "KIND", "SELECTED", "REGISTERED"},
			func(m map[string]any) []any {
				return []any{
					str(m, "id"), str(m, "framework"), str(m, "doc_kind"),
					numOf(m, "selected_count"), str(m, "registered_at"),
				}
			}),
	)
	return cmd
}

func newComplianceDepthCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "depth",
		Short:   "Inspect compliance-depth packs and control monitoring",
		Long:    "depth groups the surfaces that go BELOW the base framework catalog: the\njurisdiction packs (US state law), the sector overlays, and continuous control\nmonitoring with its snapshots and drift. All four are read-only listings; pack\ngeneration and monitoring are enterprise capabilities, so an open build lists\nhonestly empty rather than fabricating rows.",
		Example: "  olivares compliance depth us-law\n  olivares compliance depth drift",
	}
	cmd.AddCommand(
		simpleListCmd(flags, "us-law", "List US state-law packs",
			"Lists the US state-law compliance packs loaded on this deployment, with how many errors each\ncarries and when it was generated. A pack maps one state regulation onto the control catalog\nso posture can be reported per jurisdiction rather than per framework.",
			"  olivares compliance depth us-law\n  olivares compliance depth us-law -o json",
			"/depth/us-law",
			[]string{"ID", "REGULATION", "ERRORS", "GENERATED"},
			func(m map[string]any) []any {
				return []any{str(m, "id"), str(m, "regulation"), numOf(m, "error_count"), str(m, "generated_at")}
			}),
		simpleListCmd(flags, "sector", "List sector overlay packs",
			"Lists the sector overlay packs loaded on this deployment. An overlay adds the controls a\nparticular industry is held to — health, finance, public sector — on top of the base catalog,\nso one estate can be measured against several regimes at once.",
			"  olivares compliance depth sector\n  olivares compliance depth sector -o json",
			"/depth/sector",
			[]string{"ID", "REGULATION", "ERRORS", "GENERATED"},
			func(m map[string]any) []any {
				return []any{str(m, "id"), str(m, "regulation"), numOf(m, "error_count"), str(m, "generated_at")}
			}),
		simpleListCmd(flags, "snapshots", "List CCM control snapshots",
			"Lists the continuous control monitoring snapshots taken on this deployment. Each snapshot is\nthe observed status of every mapped control at one instant; drift is derived by comparing two\nof them, so a snapshot is the evidence a drift finding points back to.",
			"  olivares compliance depth snapshots\n  olivares compliance depth snapshots -o json",
			"/depth/ccm/snapshots",
			[]string{"ID", "SNAPSHOT AT"},
			func(m map[string]any) []any { return []any{str(m, "id"), str(m, "snapshot_at")} }),
		simpleListCmd(flags, "drift", "List detected control drift",
			"Lists controls whose observed status CHANGED between two continuous-monitoring snapshots,\nwith the status it moved from and to. Drift is the signal that posture degraded without anyone\nfiling a change; an empty list on a fresh install means nothing has been compared yet.",
			"  olivares compliance depth drift\n  olivares compliance depth drift -o json",
			"/depth/ccm/drift",
			[]string{"ID", "FRAMEWORK", "CONTROL", "FROM", "TO"},
			func(m map[string]any) []any {
				return []any{
					str(m, "id"), str(m, "framework"), str(m, "control_id"),
					str(m, "from_status"), str(m, "to_status"),
				}
			}),
	)
	return cmd
}

// simpleListCmd builds a read-only list verb over one `{items:[…]}` route.
//
// These surfaces share a shape and differ only in their columns, so the
// alternative is nine near-identical command constructors — the kind of
// duplication where one copy quietly stops honoring -o json.
//
// long and example are REQUIRED parameters rather than optional extras (2026-08-05).
// The factory used to set Use/Short/Args/Aliases and nothing else, so all seven
// commands it builds shipped with an empty Long and an empty Example — one omission
// inherited seven times, which TestCLIHelpCompleteness caught on main after the merge.
// Making them parameters is what forces the compiler to enumerate every call site;
// a defaulted field would have let the next one inherit the hole again.
func simpleListCmd(
	flags *authClientFlags,
	use, short, long, example, path string,
	headers []string,
	row func(map[string]any) []any,
) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    long,
		Example: example,
		Args:    cobra.NoArgs,
		Aliases: []string{"list"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := complianceCall{flags: flags, method: http.MethodGet, path: path}.do(cmd)
			if err != nil {
				return err
			}
			var out struct {
				Items []map[string]any `json:"items"`
			}
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(out.Items) == 0 {
					_, werr := fmt.Fprintln(w, "none")
					return werr
				}
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, strings.Join(headers, "\t")); werr != nil {
					return werr
				}
				for _, item := range out.Items {
					cells := row(item)
					parts := make([]string, len(cells))
					for i, c := range cells {
						parts[i] = fmt.Sprintf("%v", c)
					}
					if _, werr := fmt.Fprintln(tw, strings.Join(parts, "\t")); werr != nil {
						return werr
					}
				}
				return tw.Flush()
			}, out)
		},
	}
}

// numOf reads a JSON number as an int64. encoding/json decodes into float64 for
// `any`, so a plain type assertion to int would silently render every count as 0.
func numOf(m map[string]any, k string) int64 {
	if v, ok := m[k].(float64); ok {
		return int64(v)
	}
	return 0
}
