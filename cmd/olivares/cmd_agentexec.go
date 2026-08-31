// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_agentexec.go is the shared substrate for the EIGHT command trees that
// drive the agent-execution plane from a terminal: orchestration, sandbox,
// recording, deploy, redteam, voice, claude-policy and claude-agents. Every one
// of them is a thin, authenticated client over routes the engine already serves
// under /v1/m/<module>; not one line of authorization, validation or lifecycle
// logic lives here.
//
// WHY ONE FILE AND NOT EIGHT COPIES. These eight families share three decisions
// that must be made once or they drift:
//
//  1. THE EXIT-CODE CONTRACT. Four statuses in this plane mean something the
//     generic map (httpErr, cmd_agent.go:589) cannot know, and each is a branch
//     a script has to take. They are named in agentExecHTTPError and
//     reportAgentExecPending below, with the measurement that justifies each.
//
//  2. PARTIAL UPDATE. orchestration is the only family in the census with a
//     PATCH, so this lot is where "what does a partial update send" gets fixed:
//     ONLY the flags the operator actually typed. patchString/patchInt64/
//     patchBool consult cobra's Changed, never the value, so an omitted flag can
//     never clobber a field with a Go zero value. The engine's patch DTOs are
//     pointer-per-field for exactly this reason (schedules.go:89) and a client
//     that always sends every key would defeat them.
//
//  3. PAGINATION HONESTY. cursor+limit are added ONLY to the lists whose handler
//     actually calls listQuery(r). Seven routes in this lot return the whole set
//     and ignore both parameters; advertising a --cursor there would teach an
//     operator that they had paged when they had not. Each such command says so
//     in its help. See the census note in the session log.
//
// Everything else follows the neighbors: authClientFlags.addPersistent for the
// connection flags, resolveCLIConfig for identity/tenant, cliTransport + cliDo
// for the wire, renderOut for -o text/json, confirmDestructive for the
// irreversible verbs, and json.RawMessage for -o json so a field this CLI does
// not model still reaches the caller.

// agentExecBase is the API prefix every module in this plane mounts under.
const agentExecBase = "/v1/m/"

// maxAgentExecBodySize bounds a response read into memory. These are registers,
// scorecards, plans and timelines — kilobytes to a few megabytes. A body past
// this is a bug or a hostile endpoint, not a large legitimate answer.
const maxAgentExecBodySize = 8 << 20

// agentExecCall is one authenticated request against one module of the plane.
type agentExecCall struct {
	flags  *authClientFlags
	module string // "orchestration", "sandbox", … — the /v1/m/<module> segment
	method string
	path   string // module-relative, already escaped by the caller
	query  url.Values
	body   any
	// AllowEmptyBody sends a POST with no body at all. Several verbs here treat
	// an ABSENT body as phase 1 of a two-phase ceremony (orchestration's fire
	// gates on the presence of body bytes, schedules.go:958), so sending "{}"
	// where nothing was meant is not equivalent.
	allowEmptyBody bool
}

// agentExecResult carries what a caller renders AND the status, because on this
// plane the status is semantic: 200 and 202 are different outcomes of the same
// successful request.
type agentExecResult struct {
	status int
	raw    []byte
}

// jsonValue is the value every -o json rendering in this lot passes to
// renderOut: the engine's OWN bytes. A DTO the CLI models would silently drop
// every field it does not know about, and this plane adds fields faster than a
// client can track them.
func (r agentExecResult) jsonValue() json.RawMessage { return json.RawMessage(r.raw) }

func (r agentExecResult) decode(into any) error {
	if len(bytes.TrimSpace(r.raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.raw, into); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// do performs the request and classifies every failure exactly once.
//
// The unresolved-precondition branch is a USAGE error (exit 2), not a server
// failure (exit 6) — the same decision cmd_compliance.go:143 documents, for the
// same reason: a script that retries a 6 would retry a missing --tenant forever.
func (c agentExecCall) do(cmd *cobra.Command) (agentExecResult, error) {
	opts, err := c.flags.resolutionOptions(cmd)
	if err != nil {
		return agentExecResult{}, redactCoded(err, c.flags.token)
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return agentExecResult{}, redactCoded(err, c.flags.token)
	}
	switch {
	case resolved.Server == "":
		return agentExecResult{}, missingCLIValueError("server", "--server", "OLIVARES_SERVER_URL", resolved)
	case resolved.Token == "":
		return agentExecResult{}, missingCLIValueError("token", "--token", "OLIVARES_TOKEN", resolved)
	case resolved.Tenant == "":
		return agentExecResult{}, missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved,
		Insecure: c.flags.insecure,
		Timeout:  c.flags.timeout,
		Stderr:   cmd.ErrOrStderr(),
	})
	if err != nil {
		return agentExecResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}

	var payload io.Reader
	sendBody := c.body != nil && !c.allowEmptyBody
	if sendBody {
		encoded, merr := json.Marshal(c.body)
		if merr != nil {
			return agentExecResult{}, fmt.Errorf("encode request body: %w", merr)
		}
		payload = bytes.NewReader(encoded)
	}

	target := resolved.Server + agentExecBase + c.module + c.path
	if len(c.query) > 0 {
		target += "?" + c.query.Encode()
	}
	req, err := http.NewRequestWithContext(cmd.Context(), c.method, target, payload)
	if err != nil {
		return agentExecResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	if sendBody {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := cliDo(client, req)
	if err != nil {
		return agentExecResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, maxAgentExecBodySize+1))
	if rerr != nil {
		return agentExecResult{}, exitcode.New(exitcode.Server, fmt.Errorf("read response: %w", rerr))
	}
	if len(raw) > maxAgentExecBodySize {
		return agentExecResult{}, exitcode.New(exitcode.Server,
			fmt.Errorf("response exceeds %d bytes", maxAgentExecBodySize))
	}
	if resp.StatusCode >= 300 {
		return agentExecResult{status: resp.StatusCode, raw: raw}, agentExecHTTPError(resp.StatusCode, raw)
	}
	return agentExecResult{status: resp.StatusCode, raw: raw}, nil
}

// agentExecOpEnvelope is the shape the governed two-phase verbs of this plane
// answer with. Three modules spell the same idea slightly differently —
// orchestration's fireResponse uses op_status (schedules.go:107), deploy's
// mutationResponse uses status (lifecycle.go:57), voice's openResponse uses both
// (policies.go:70) — so both keys are read and opStatus() picks whichever the
// engine filled. Reading only one would silently report "" for two of the three.
type agentExecOpEnvelope struct {
	Op               string `json:"op"`
	OpStatus         string `json:"op_status"`
	Status           string `json:"status"`
	PlanHash         string `json:"plan_hash"`
	ApprovalRef      string `json:"approval_ref"`
	GateStatus       string `json:"gate_status"`
	DispatchRef      string `json:"dispatch_ref"`
	RequiresApproval bool   `json:"requires_approval"`
	PolicyVerdict    string `json:"policy_verdict"`
	Detail           string `json:"detail"`
}

func (e agentExecOpEnvelope) opStatus() string {
	return firstNonEmptyCLI(e.OpStatus, e.Status)
}

// agentExecErrorEnvelope is the module error shape ({"error":{"message":…}},
// helpers.go:60 in each module).
type agentExecErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func agentExecErrorMessage(body []byte) string {
	var env agentExecErrorEnvelope
	if json.Unmarshal(body, &env) == nil {
		if msg := strings.TrimSpace(env.Error.Message); msg != "" {
			return msg
		}
	}
	return strings.TrimSpace(string(body))
}

// agentExecHTTPError extends httpErr with the four statuses this plane uses that
// the generic map cannot classify. It never REPLACES an existing classification:
// everything it does not name falls through to httpErr, so 401/403/404/409/5xx
// keep the codes the rest of the CLI already gives them.
//
//	423 Locked → exit 5 (Conflict). The estate kill switch froze this actuation
//	(schedules.go:1229, lifecycle.go:618, policies.go:520). It is precisely "the
//	request contradicts current state" (exitcode.go:23), and it is NOT a
//	permission problem: better credentials will not lift a stop. Generic
//	classification left it at exit 1, indistinguishable from a parse failure.
//
//	422 Unprocessable → exit 2 (Usage). The only 422 in the lot is redteam's
//	refusal to register a target whose agent_ref is not in the tenant's own
//	inventory (consent.go:116). The remedy is to change the argument, so a
//	script must not retry it. Generic classification left it at exit 1.
//
//	501 Not Implemented → exit 1, but SAYING WHAT IS MISSING. Two different
//	product boundaries answer 501 here and neither is a fault: the recording
//	summarizer is unwired (handlers.go:971) and enterprise-only verbs are not
//	linked in an open-core build. "request failed" sends an operator hunting a
//	bug that does not exist.
//
//	403 carrying a gate/step-up denial → exit 3, UNCHANGED, but explained. This
//	is deliberately NOT reclassified: 403 means "the control plane rejected the
//	caller" everywhere else in this CLI and renumbering a live code breaks
//	scripts. What changes is the message — a denied approval or a missing
//	hardware step-up read as "you are missing a role", and the operator went
//	looking for a grant nobody had removed.
func agentExecHTTPError(status int, body []byte) error {
	switch status {
	case http.StatusLocked:
		var env agentExecOpEnvelope
		detail := strings.TrimSpace(env.Detail)
		if json.Unmarshal(body, &env) == nil {
			detail = strings.TrimSpace(env.Detail)
		}
		if detail == "" {
			detail = agentExecErrorMessage(body)
		}
		msg := "blocked: an estate kill switch or a hold is stopping this actuation (HTTP 423)"
		if detail != "" {
			msg += ": " + detail
		}
		msg += "\nno effect was actuated; lift the stop before retrying"
		return exitcode.New(exitcode.Conflict, fmt.Errorf("%s", msg))
	case http.StatusUnprocessableEntity:
		return exitcode.New(exitcode.Usage, fmt.Errorf(
			"the control plane rejected the argument (HTTP 422): %s", agentExecErrorMessage(body)))
	case http.StatusNotImplemented:
		return exitcode.New(exitcode.Err, fmt.Errorf(
			"this verb is not available in this build (HTTP 501): %s\n"+
				"this is a wiring/edition boundary, not a fault — the reads on this surface work without it",
			agentExecErrorMessage(body)))
	case http.StatusForbidden:
		if msg, ok := agentExecDenialMessage(body); ok {
			return exitcode.New(exitcode.Auth, fmt.Errorf("%s", msg))
		}
	}
	return httpErr(status, body)
}

// agentExecDenialMessage recognizes the two 403s of this plane that are NOT a
// missing role and says which one happened. It returns ok=false for an ordinary
// permission denial so httpErr keeps its wording.
func agentExecDenialMessage(body []byte) (string, bool) {
	var env agentExecErrorEnvelope
	if json.Unmarshal(body, &env) == nil && env.Error.Code == "step_up_required" {
		return "refused: this mutation needs a fresh hardware step-up on your session " +
			"(WebAuthn/PIV, AAL3) — your role is not the problem (HTTP 403)\n" +
			"re-authenticate with the second factor in the console, then retry", true
	}
	var op agentExecOpEnvelope
	if json.Unmarshal(body, &op) != nil {
		return "", false
	}
	if op.GateStatus == "" && !op.RequiresApproval && op.PolicyVerdict == "" {
		return "", false
	}
	var b strings.Builder
	b.WriteString("refused by governance, not by your role (HTTP 403)")
	if op.Op != "" {
		fmt.Fprintf(&b, "\n  op: %s", safeCLIValue(op.Op, ""))
	}
	if st := op.opStatus(); st != "" {
		fmt.Fprintf(&b, "\n  op_status: %s", safeCLIValue(st, ""))
	}
	if op.GateStatus != "" {
		fmt.Fprintf(&b, "\n  approval gate: %s", safeCLIValue(op.GateStatus, ""))
	}
	if op.PolicyVerdict != "" {
		fmt.Fprintf(&b, "\n  policy verdict: %s", safeCLIValue(op.PolicyVerdict, ""))
	}
	if op.ApprovalRef != "" {
		fmt.Fprintf(&b, "\n  approval ref: %s", safeCLIValue(op.ApprovalRef, ""))
	}
	if d := strings.TrimSpace(op.Detail); d != "" {
		fmt.Fprintf(&b, "\n  detail: %s", safeCLIValue(d, ""))
	}
	b.WriteString("\nnothing was actuated")
	return b.String(), true
}

// reportAgentExecPending renders a 202 and returns Degraded (exit 7).
//
// THIS IS THE CENTRAL EXIT-CODE DECISION OF THE LOT. A 202 on this plane means
// an approval was REQUESTED and the effect did not happen: no schedule fired
// (schedules.go:1266), no workflow ran, no deployment applied or retired
// (lifecycle.go:297,503), no voice session opened (policies.go:344). Exiting 0
// would tell a pipeline the deployment shipped. Exiting 1 would say it failed,
// which is also false — the request was recorded and the approval is pending.
// Degraded is documented as "succeeded but reports a degraded condition"
// (exitcode.go:27) and that is exactly this state.
//
// A body this cannot parse is STILL a 202: the decode error is reported and the
// Degraded code kept, so a malformed envelope can never quietly become exit 1.
func reportAgentExecPending(cmd *cobra.Command, res agentExecResult, what string) error {
	var env agentExecOpEnvelope
	decodeErr := res.decode(&env)
	rerr := renderOut(cmd, func(out io.Writer) error {
		if _, err := fmt.Fprintf(out, "%s: NOT DONE — waiting on a governance approval\n", what); err != nil {
			return err
		}
		if env.ApprovalRef != "" {
			if _, err := fmt.Fprintf(out, "approval_ref: %s\n", safeCLIValue(env.ApprovalRef, "")); err != nil {
				return err
			}
		}
		if env.GateStatus != "" {
			if _, err := fmt.Fprintf(out, "gate_status: %s\n", safeCLIValue(env.GateStatus, "")); err != nil {
				return err
			}
		}
		if env.PlanHash != "" {
			if _, err := fmt.Fprintf(out, "plan_hash: %s\n", safeCLIValue(env.PlanHash, "")); err != nil {
				return err
			}
		}
		if d := strings.TrimSpace(env.Detail); d != "" {
			if _, err := fmt.Fprintf(out, "detail: %s\n", safeCLIValue(d, "")); err != nil {
				return err
			}
		}
		if decodeErr != nil {
			if _, err := fmt.Fprintf(out, "note: the 202 body could not be parsed (%v); the raw body is above only in -o json\n", decodeErr); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(out, "re-run the same command with --approval-ref %s once it is approved\n",
			firstNonEmptyCLI(safeCLIValue(env.ApprovalRef, ""), "<ref>"))
		return err
	}, json.RawMessage(res.raw))
	if rerr != nil {
		return rerr
	}
	return exitcode.New(exitcode.Degraded, fmt.Errorf(
		"%s did not happen: a governance approval is pending", what))
}

// ---- pagination ---------------------------------------------------------------

// agentExecPageFlags is the cursor+limit pair. It is attached ONLY to the lists
// whose engine handler calls listQuery(r); see the header note.
type agentExecPageFlags struct {
	limit  int
	cursor string
}

func (p *agentExecPageFlags) add(cmd *cobra.Command) {
	cmd.Flags().IntVar(&p.limit, "limit", 0, "page size (0 uses the engine's default)")
	cmd.Flags().StringVar(&p.cursor, "cursor", "", "continue from the cursor a previous page reported")
}

// apply writes the page parameters into q, rejecting a negative limit as the
// usage error it is rather than letting the engine silently ignore it.
func (p *agentExecPageFlags) apply(q url.Values) error {
	if p.limit < 0 {
		return exitcode.New(exitcode.Usage, fmt.Errorf("--limit must be zero or positive, got %d", p.limit))
	}
	if p.limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", p.limit))
	}
	if p.cursor != "" {
		q.Set("cursor", p.cursor)
	}
	return nil
}

// agentExecListBody is the module list envelope (core/api/listresponse.go:56).
// Items stay as raw messages so -o json returns the engine's own bytes and a
// field this CLI does not model still reaches the caller.
type agentExecListBody struct {
	Items   []json.RawMessage `json:"items"`
	Cursor  string            `json:"cursor"`
	HasMore bool              `json:"has_more"`
}

// ---- rendering ----------------------------------------------------------------

// renderAgentExecList renders a paginated list: a column table for -o text, the
// engine's own bytes for -o json.
//
// TWO STREAM DECISIONS, both about being usable from a script:
//
//   - the EMPTY note goes to stdout, because it is the answer (renderListOut,
//     render.go:176, makes the same call: zero bytes cannot be told apart from a
//     swallowed command).
//   - the TRUNCATION note goes to STDERR, because it is metadata about the
//     answer. `olivares deploy operations | wc -l` must count operations, not a
//     sentence about paging. A human still sees it; awk does not.
func renderAgentExecList(cmd *cobra.Command, client *authClientFlags, res agentExecResult, emptyNote string, cols []string) error {
	var body agentExecListBody
	if err := res.decode(&body); err != nil {
		return exitcode.New(exitcode.Server, err)
	}
	rerr := renderOut(cmd, func(out io.Writer) error {
		if len(body.Items) == 0 {
			_, err := fmt.Fprintln(out, emptyNote)
			return err
		}
		return writeAgentExecTable(out, client, body.Items, cols)
	}, json.RawMessage(res.raw))
	if rerr != nil {
		return rerr
	}
	if body.HasMore || body.Cursor != "" {
		note := "more rows remain"
		if body.Cursor != "" {
			note = fmt.Sprintf("more rows remain; continue with --cursor %s", safeCLIValue(body.Cursor, ""))
		}
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), note); err != nil {
			return err
		}
	}
	return nil
}

// writeAgentExecTable writes one aligned table of the named columns. A column a
// row does not carry prints "-": the row is reported as the engine sent it, and
// a missing field is never confused with an empty one by dropping the column.
//
// This is the TEXT LEG of renderOut, not a bypass of it. All FOUR call sites —
// renderAgentExecList above, renderAgentExecGraph (cmd_orchestration.go),
// `redteam catalog` (cmd_redteam.go) and `claude-policy distribution`
// (cmd_claudepolicy.go) — are the closure renderOut itself invokes, so `out` is
// the writer renderOut chose and `-o json` never arrives here at all: it takes
// the other branch and emits the engine's own bytes.
//
// THE EXEMPTION IS ONLY GOOD WHILE THAT HOLDS, so it is witnessed rather than
// asserted: TestAgentExecTableIsOnlyReachedThroughRenderOut fails if a fifth call
// site ever formats a table outside a renderOut closure. The scan flags this line
// only because extracting the table into one function put the renderOut call in
// the caller instead of inside its window; inlining it back into eight command
// trees is the duplication this file exists to prevent.
//
// render-exempt: text leg of renderOut; every call site witnessed by a test.
func writeAgentExecTable(out io.Writer, client *authClientFlags, items []json.RawMessage, cols []string) error {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	headers := make([]string, 0, len(cols))
	for _, c := range cols {
		headers = append(headers, strings.ToUpper(c))
	}
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, item := range items {
		fields := agentExecFields(item)
		cells := make([]string, 0, len(cols))
		for _, c := range cols {
			cells = append(cells, agentExecCell(fields, c, client))
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// renderAgentExecObject renders one object: the named scalar fields as aligned
// `key: value` lines for -o text, the engine's own bytes for -o json.
func renderAgentExecObject(cmd *cobra.Command, client *authClientFlags, res agentExecResult, keys []string) error {
	fields := agentExecFields(res.raw)
	return renderOut(cmd, func(out io.Writer) error {
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		shown := keys
		if len(shown) == 0 {
			shown = agentExecSortedKeys(fields)
		}
		for _, k := range shown {
			if _, ok := fields[k]; !ok {
				continue
			}
			if _, err := fmt.Fprintf(tw, "%s\t%s\n", k, agentExecCell(fields, k, client)); err != nil {
				return err
			}
		}
		return tw.Flush()
	}, json.RawMessage(res.raw))
}

// renderAgentExecDeleted renders a successful delete. The engine answers 204 with
// NO BODY, so there is nothing to echo back — and a command that printed nothing
// at all would be indistinguishable from one that did nothing (the defect
// renderListOut exists to fix, render.go:176). It says what went, on stdout, and
// -o json gets a stable object a script can parse rather than zero bytes.
func renderAgentExecDeleted(cmd *cobra.Command, res agentExecResult, what string) error {
	if len(bytes.TrimSpace(res.raw)) > 0 {
		// The engine chose to answer with a body; show it rather than replacing
		// it with this command's summary.
		return renderAgentExecObject(cmd, nil, res, nil)
	}
	return renderOut(cmd, func(out io.Writer) error {
		_, err := fmt.Fprintf(out, "deleted %s\n", what)
		return err
	}, map[string]any{"deleted": what, "status": res.status})
}

// agentExecFields decodes one JSON object with UseNumber so an int64 id or a
// version number is never reformatted through float64 (1099511627776 must not
// print as 1.099511627776e+12).
func agentExecFields(raw json.RawMessage) map[string]any {
	fields := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return map[string]any{}
	}
	return fields
}

func agentExecSortedKeys(fields map[string]any) []string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// agentExecCell formats one field for the text form. Every value is passed
// through safeCLIValue: it comes from the network, and a control byte in it
// would otherwise reach the operator's terminal verbatim.
func agentExecCell(fields map[string]any, key string, client *authClientFlags) string {
	token := ""
	if client != nil {
		token = client.token
	}
	v, ok := fields[key]
	if !ok || v == nil {
		return "-"
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return "-"
		}
		return safeCLIValue(t, token)
	case bool:
		return fmt.Sprintf("%t", t)
	case json.Number:
		return t.String()
	default:
		encoded, err := json.Marshal(t)
		if err != nil {
			return "-"
		}
		return safeCLIValue(string(encoded), token)
	}
}

// ---- partial update -----------------------------------------------------------
//
// The three helpers below are the lot's answer to "how is a partial update
// expressed". They key off cobra's Changed, i.e. whether the operator TYPED the
// flag, never off the value: `--grace-factor 0` and an omitted --grace-factor
// are different requests, and only the first may reach the engine.

func patchString(cmd *cobra.Command, body map[string]any, flag, key, value string) {
	if cmd.Flags().Changed(flag) {
		body[key] = value
	}
}

func patchInt64(cmd *cobra.Command, body map[string]any, flag, key string, value int64) {
	if cmd.Flags().Changed(flag) {
		body[key] = value
	}
}

func patchBool(cmd *cobra.Command, body map[string]any, flag, key string, value bool) {
	if cmd.Flags().Changed(flag) {
		body[key] = value
	}
}

// ---- operator-supplied documents ----------------------------------------------

// maxAgentExecDocumentSize bounds a spec/policy document read from disk or
// stdin. These are configuration files; a hundredth of this would do.
const maxAgentExecDocumentSize = 4 << 20

// readAgentExecDocument reads an operator-supplied document from a path, or from
// stdin when the path is "-". It is deliberately NOT readSecretValue: these are
// specs and policies, not credentials, and trimming their trailing newline (which
// readSecretValue does, correctly, for a secret) would change the bytes the
// engine hashes into a spec_hash.
func readAgentExecDocument(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "" {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("no document given"))
	}
	var (
		raw []byte
		err error
	)
	if path == "-" {
		raw, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxAgentExecDocumentSize+1))
	} else {
		raw, err = readAgentExecFile(path)
	}
	if err != nil {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("read %s: %w", path, err))
	}
	if len(raw) > maxAgentExecDocumentSize {
		return nil, exitcode.New(exitcode.Usage,
			fmt.Errorf("%s exceeds %d bytes", path, maxAgentExecDocumentSize))
	}
	return raw, nil
}

// readAgentExecJSONDocument reads a document and refuses it if it is not valid
// JSON, so a typo is a usage error the caller sees BEFORE anything is sent —
// rather than a 400 the engine has to explain.
func readAgentExecJSONDocument(cmd *cobra.Command, path string) (json.RawMessage, error) {
	raw, err := readAgentExecDocument(cmd, path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("%s is not valid JSON", path))
	}
	return json.RawMessage(raw), nil
}

// readAgentExecJSONArray reads a document that must be a JSON ARRAY (a step
// graph, a mock list). The shape check is here rather than at each call site
// because "steps" and "mocks" are the same mistake in four commands.
func readAgentExecJSONArray(cmd *cobra.Command, path, what string) ([]json.RawMessage, error) {
	raw, err := readAgentExecDocument(cmd, path)
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, exitcode.New(exitcode.Usage,
			fmt.Errorf("%s must be a JSON array of %s objects: %w", path, what, err))
	}
	return items, nil
}

// readAgentExecFile is os.ReadFile with the bound applied by the caller. It is a
// separate function only so the gosec exemption sits on the one line that needs
// it rather than on the whole reader.
func readAgentExecFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-selected spec/policy document
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxAgentExecDocumentSize+1))
}

// ---- server-sent event streams ------------------------------------------------

// streamAgentExecEvents follows an SSE endpoint and prints ONE NDJSON OBJECT PER
// EVENT to stdout: {"event":"relation","data":{…}}. Keep-alive comments and
// stream notices go to stderr.
//
// NDJSON rather than the raw SSE frames is the script contract: `olivares
// orchestration stream | jq -r 'select(.event=="relation") | .data.worker_ref'`
// works, and nothing on stdout is a protocol artifact. It follows `agent session
// attach` (cmd_agent.go:411) for the transport — Unbounded, because a live
// stream must outlive the request timeout, and the caller's Ctrl-C ends it.
func streamAgentExecEvents(cmd *cobra.Command, flags *authClientFlags, module, path string, query url.Values) error {
	opts, err := flags.resolutionOptions(cmd)
	if err != nil {
		return err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return redactCoded(err, flags.token)
	}
	switch {
	case resolved.Server == "":
		return missingCLIValueError("server", "--server", "OLIVARES_SERVER_URL", resolved)
	case resolved.Token == "":
		return missingCLIValueError("token", "--token", "OLIVARES_TOKEN", resolved)
	case resolved.Tenant == "":
		return missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved:  resolved,
		Insecure:  flags.insecure,
		Unbounded: true,
		Stderr:    cmd.ErrOrStderr(),
	})
	if err != nil {
		return exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	target := resolved.Server + agentExecBase + module + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, target, nil)
	if err != nil {
		return exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "text/event-stream")
	resp, err := cliDo(client, req)
	if err != nil {
		return exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxAgentExecBodySize))
		return agentExecHTTPError(resp.StatusCode, raw)
	}
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), maxAgentExecDocumentSize)
	event := "message"
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			// A keep-alive comment. It proves the stream is alive, which an
			// operator watching an idle plane needs, so it is not dropped —
			// but it is not data, so it never touches stdout.
			fmt.Fprintln(errOut, "[stream] "+safeCLIValue(strings.TrimSpace(line), flags.token))
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			payload := json.RawMessage(data)
			if !json.Valid(payload) {
				// Never fabricate structure the server did not send: report the
				// unparseable frame as a string on stderr and keep following.
				fmt.Fprintln(errOut, "[stream] unparseable data frame dropped")
				continue
			}
			encoded, merr := json.Marshal(struct {
				Event string          `json:"event"`
				Data  json.RawMessage `json:"data"`
			}{Event: event, Data: payload})
			if merr != nil {
				return exitcode.New(exitcode.Server, merr)
			}
			if _, werr := fmt.Fprintln(out, string(encoded)); werr != nil {
				return werr
			}
			event = "message"
		}
	}
	if serr := sc.Err(); serr != nil {
		return exitcode.New(exitcode.Server, fmt.Errorf("stream ended: %w", serr))
	}
	return nil
}

// agentExecPathID escapes a caller-supplied identifier for use in a path
// segment. Without it, `olivares deploy definitions get ../../system/orgs`
// would address a route the operator never named.
func agentExecPathID(id string) string { return url.PathEscape(id) }

// setQuery writes k=v into q only when v is non-empty, so an unset filter is
// absent from the URL rather than present and empty (which several handlers
// treat as a real filter value).
func setQuery(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}
