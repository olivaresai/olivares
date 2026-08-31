// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// This file is the shared transport for the OBSERVE-AND-REPORT lane: the ten
// module namespaces whose affinity is reading and announcing state rather than
// changing it — reporting, notify, health, accessmap, observability,
// consoleviews, adoption, identity, inventory and posture.
//
// It is ONE client, not ten, for the reason C08-01 gave for not writing a third
// tenant resolver: a per-family copy is a per-family opportunity to classify the
// same refusal differently. Every verb in the lane therefore reaches the control
// plane through observeCall.do, so the exit contract below is a property of the
// lane, not of whichever file a reader happens to open.
//
// WHAT IT DELIBERATELY DOES NOT DO.
//
//   - It does not re-map HTTP statuses. httpErr (cmd_agent.go:589) owns
//     401/403 → 3, 404 → 4, 409 → 5, 5xx → 6, and everything else falls to 1.
//     A 400 from a rejected query value is arguably a usage error, but `mcp`,
//     `findings`, `compliance` and `agent` all surface it as 1 through httpErr,
//     and a lane that answered 2 to the same refusal would make the CLI's exit
//     contract depend on which noun the operator typed. What this lane does
//     instead is refuse LOCALLY, before the connection, whatever it can decide
//     from the arguments alone — those refusals are exit 2 and cost zero
//     requests.
//
//   - It does not add authorization. The credential and the tenant header go to
//     the engine and the engine decides. The local checks in this lane only ever
//     REFUSE (a missing required query parameter, a contradictory pair of
//     flags); none of them can turn a "no" into a "yes".
//
//   - It does not invent pagination. Five of the ten namespaces expose a keyset
//     cursor, one exposes a top-N limit with no cursor, and four expose neither;
//     addObservePageFlags is only called where the engine reads the parameter.
//     See the comment on that function — copying --cursor onto a route that
//     ignores it is how a script comes to believe it has paged through a list it
//     only ever saw the first page of.

// observeBase is the module-route root every namespace in this lane hangs off.
const observeBase = "/v1/m/"

// maxObserveBodySize bounds a response this lane reads into memory. These are
// status projections, ledgers, graphs and rendered reports; a PDF posture pack
// is the largest legitimate answer, so the cap is generous but finite — an
// unbounded read is how a hostile or broken endpoint turns a CLI into an OOM.
const maxObserveBodySize = 32 << 20

// observeCall is one authenticated request against one module namespace.
//
// body and rawBody are exclusive: body is marshaled as JSON, rawBody is sent
// verbatim under contentType. The second form exists because this lane has a
// route that genuinely takes a non-JSON document — PUT /templates/{type} stores
// an HTML report template (modules/reporting/enterprise.go:312) — and encoding
// it as a JSON string would store the wrong bytes.
type observeCall struct {
	flags       *authClientFlags
	ns          string // module namespace, e.g. "reporting"
	method      string
	path        string // path under the namespace, e.g. "/schedules"
	query       url.Values
	body        any
	rawBody     []byte
	contentType string // only with rawBody
	accept      string // defaults to application/json
}

// observeResult carries the bytes, the status and the content type.
//
// The content type is not decoration. Three routes in this lane answer with a
// rendered artifact rather than a document — GET /reports/{type} is HTML or PDF
// (api.go:393), GET /templates/{type} is HTML (enterprise.go:307), and GET
// /schedules/{id}/runs/{rid} is JSON when the run stored no output and the
// artifact bytes when it did (enterprise.go:228-253). A client that assumed JSON
// would report a parse failure for a perfectly good report.
type observeResult struct {
	status      int
	raw         []byte
	contentType string
}

// decode unmarshals a JSON body. An empty body is not an error: several routes
// in this lane answer 200 with nothing at all.
func (r observeResult) decode(into any) error {
	if len(bytes.TrimSpace(r.raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.raw, into); err != nil {
		return exitcode.New(exitcode.Server,
			fmt.Errorf("the control plane answered HTTP %d with a body this command could not parse as JSON: %w",
				r.status, err))
	}
	return nil
}

// isJSON reports whether the answer's media type is JSON, ignoring parameters.
func (r observeResult) isJSON() bool {
	if r.contentType == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(r.contentType)
	if err != nil {
		return false
	}
	return mt == "application/json"
}

func (c observeCall) do(cmd *cobra.Command) (observeResult, error) {
	opts, err := c.flags.resolutionOptions(cmd)
	if err != nil {
		return observeResult{}, err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return observeResult{}, redactCoded(err, c.flags.token)
	}
	// An unresolved server/token/tenant is a USAGE error (exit 2), and it is
	// decided HERE — before a socket is opened. Two properties follow, and the
	// tests assert both: a caller with no credential learns nothing about
	// whether the host answers, and a script gets 2 (fix the invocation) rather
	// than 6 (retry the server), which is the C08-03 defect one layer up.
	switch {
	case resolved.Server == "":
		return observeResult{}, missingCLIValueError("server", "--server", "OLIVARES_SERVER_URL", resolved)
	case resolved.Token == "":
		return observeResult{}, missingCLIValueError("token", "--token", "OLIVARES_TOKEN", resolved)
	case resolved.Tenant == "":
		return observeResult{}, missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved,
		Insecure: c.flags.insecure,
		Timeout:  c.flags.timeout,
		Stderr:   cmd.ErrOrStderr(),
	})
	if err != nil {
		// Or, not New: cliTransport classifies its refusals about the caller's
		// own arguments as Usage, and overruling that is exactly what C08-03
		// removed from the four clients that did it.
		return observeResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}

	var payload io.Reader
	sentType := ""
	switch {
	case c.rawBody != nil:
		payload = bytes.NewReader(c.rawBody)
		sentType = c.contentType
	case c.body != nil:
		encoded, merr := json.Marshal(c.body)
		if merr != nil {
			return observeResult{}, exitcode.New(exitcode.Usage, fmt.Errorf("encode request body: %w", merr))
		}
		payload = bytes.NewReader(encoded)
		sentType = "application/json"
	}

	target := resolved.Server + observeBase + c.ns + c.path
	if len(c.query) > 0 {
		target += "?" + c.query.Encode()
	}
	req, err := http.NewRequestWithContext(cmd.Context(), c.method, target, payload)
	if err != nil {
		return observeResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req.Header = headers.Clone()
	accept := c.accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	if sentType != "" {
		req.Header.Set("Content-Type", sentType)
	}

	resp, err := cliDo(client, req)
	if err != nil {
		return observeResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	defer func() { _ = resp.Body.Close() }()

	// LimitReader at cap+1 so an over-cap body is DETECTED rather than silently
	// truncated into something that parses.
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, maxObserveBodySize+1))
	if rerr != nil {
		return observeResult{}, exitcode.New(exitcode.Server, fmt.Errorf("read %s response: %w", c.ns, rerr))
	}
	if len(raw) > maxObserveBodySize {
		return observeResult{}, exitcode.New(exitcode.Server,
			fmt.Errorf("%s response exceeds %d bytes", c.ns, maxObserveBodySize))
	}
	res := observeResult{status: resp.StatusCode, raw: raw, contentType: resp.Header.Get("Content-Type")}
	if resp.StatusCode >= 300 {
		return res, observeHTTPError(resp.StatusCode, raw)
	}
	return res, nil
}

// observeHTTPError classifies a refusal. It extends httpErr with the one status
// this lane answers that the generic map reads as an ordinary failure.
//
// 501 IS A PRODUCT BOUNDARY, NOT A FAULT. Every enterprise route in reporting
// answers it through writeNotWired (enterprise.go:109) when its seam is nil, and
// posture/adoption modules do the same for unwired capabilities. An operator who
// reads "request failed: HTTP 501" goes looking for a bug; the correct reading
// is "this build does not link that add-on, and the rest of the namespace works".
// The code stays exitcode.Err (1) to match the identical decision in
// cmd_compliance.go:269 — the lane does not get its own dialect of the contract.
func observeHTTPError(status int, body []byte) error {
	if status == http.StatusNotImplemented {
		detail := observeErrorMessage(body)
		msg := "this capability is not wired in this build (HTTP 501); the rest of this namespace is unaffected"
		if detail != "" {
			msg = detail + " (HTTP 501); the rest of this namespace is unaffected"
		}
		return exitcode.New(exitcode.Err, fmt.Errorf("%s", msg))
	}
	return httpErr(status, body)
}

// observeErrorMessage pulls the message out of the two error envelopes the
// modules in this lane emit: {"error":{"message":...}} (the module helper) and
// {"error":"..."} (core/api and a few module handlers). It returns "" when the
// body is neither, so a caller never prints an invented reason.
func observeErrorMessage(body []byte) string {
	var nested struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &nested); err == nil && strings.TrimSpace(nested.Error.Message) != "" {
		return strings.TrimSpace(nested.Error.Message)
	}
	var flat struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &flat); err == nil && strings.TrimSpace(flat.Error) != "" {
		return strings.TrimSpace(flat.Error)
	}
	return ""
}

// ---- pagination ----------------------------------------------------------------

// observePage is the list envelope five namespaces in this lane share
// (api.ListResponse: items + cursor + has_more). Embedding it keeps every list
// verb reporting truncation the same way.
type observePage struct {
	Cursor  string `json:"cursor,omitempty"`
	HasMore bool   `json:"has_more,omitempty"`
}

// observePageFlags holds the two paging flags, so a verb declares them by
// embedding rather than by re-typing the help text.
type observePageFlags struct {
	limit  int
	cursor string
}

// addObservePageFlags declares --limit/--cursor. CALL IT ONLY ON A ROUTE WHOSE
// ENGINE READS THEM.
//
// The lane is split and the split is measured, not assumed: notify
// (helpers.go:102), health (helpers.go:127), accessmap (api.go:66),
// observability (traces.go:324) and inventory (api.go:211) parse both;
// adoption parses `limit` only, as a top-N (dto.go:207) and has no cursor;
// reporting, consoleviews, posture and identity parse NEITHER — identity's four
// routes read no query parameter at all (governance/identityconsole.go:127-131).
//
// A --cursor bolted onto a route that ignores it is worse than a missing flag:
// the command accepts it, the engine drops it, and the second page is the first
// page again — a loop that never terminates and a script that believes it read
// the whole list.
func addObservePageFlags(cmd *cobra.Command, f *observePageFlags) {
	cmd.Flags().IntVar(&f.limit, "limit", 0, "maximum rows to return in one page (0 = the engine's default)")
	cmd.Flags().StringVar(&f.cursor, "cursor", "", "continue from the cursor printed by the previous page")
}

// apply writes the paging flags into a query, rejecting a negative limit before
// the request rather than letting the engine silently ignore it.
func (f observePageFlags) apply(q url.Values) error {
	if f.limit < 0 {
		return exitcode.New(exitcode.Usage, fmt.Errorf("--limit must not be negative, got %d", f.limit))
	}
	if f.limit > 0 {
		q.Set("limit", strconv.Itoa(f.limit))
	}
	if f.cursor != "" {
		q.Set("cursor", f.cursor)
	}
	return nil
}

// observeTruncationNote is the line a paginated text listing ends with when the
// engine says there is more. It NAMES the cursor, because "truncated" without
// the token to continue from tells an operator they have a problem and not how
// to finish. JSON needs no note: has_more and cursor are already in the value.
func observeTruncationNote(w io.Writer, page observePage, cmdPath string) error {
	if !page.HasMore {
		return nil
	}
	if page.Cursor == "" {
		_, err := fmt.Fprintln(w,
			"more rows exist but the control plane returned no cursor: this page is NOT the whole list")
		return err
	}
	_, err := fmt.Fprintf(w, "more rows exist — continue with: %s --cursor %s\n",
		cmdPath, safeCLIValue(page.Cursor, ""))
	return err
}

// ---- rendering -----------------------------------------------------------------

// observeValue is the fallback text rendering for a route whose body this CLI
// does not model field by field: the graph projections, the attestation
// document, the enterprise report payloads. It prints the JSON the engine sent,
// indented — which is honest — rather than a hand-picked subset that would go
// stale the moment the engine adds a field.
//
// It exists so `-o json` and `-o text` differ only in framing, never in CONTENT:
// a command that dropped fields in text mode would quietly answer a different
// question depending on a flag.
func observeValue(cmd *cobra.Command, raw []byte, headline string) error {
	var pretty json.RawMessage = raw
	if len(bytes.TrimSpace(raw)) == 0 {
		pretty = json.RawMessage("null")
	}
	return renderOut(cmd, func(w io.Writer) error {
		if headline != "" {
			if _, err := fmt.Fprintln(w, headline); err != nil {
				return err
			}
		}
		var buf bytes.Buffer
		// render-exempt: this IS the text branch renderOut invoked. The indent is
		// the human framing of bytes the engine already chose; the json branch of
		// renderOut re-marshals the same RawMessage.
		if err := json.Indent(&buf, pretty, "", "  "); err != nil {
			// Not JSON after all — print what arrived rather than claim a parse.
			_, werr := fmt.Fprintln(w, safeCLIValue(string(raw), ""))
			return werr
		}
		_, werr := fmt.Fprintln(w, buf.String())
		return werr
	}, pretty)
}

// observeJSON is what every verb in this lane hands renderOut as its JSON value.
//
// IT IS THE ENGINE'S BYTES, NOT THE CLI'S STRUCT, and that is the whole point.
// The typed structs in these files exist to build a TABLE — they name the
// handful of columns worth a terminal's width. Marshaling one of those structs
// back out for `-o json` would silently drop every field the CLI does not model:
// an edge's attribution_reason, a check's detail hash, a route's owner. Worse,
// it would drop fields ADDED to the engine later, so a `-o json` consumer would
// stop seeing new data the day the engine started sending it, with no error
// anywhere. `mcp pins ls` established this shape (cmd_mcp.go:197); this lane
// follows it everywhere rather than per-command.
//
// An empty body becomes `null` because an empty RawMessage is not valid JSON and
// would fail the marshal with a confusing error about the CLI rather than an
// honest report of what arrived.
func observeJSON(raw []byte) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(raw)
}

// observeCell renders one table cell: never empty (an empty column silently
// shifts a tabwriter row) and never carrying control characters from the server
// into the operator's terminal.
func observeCell(v string) string {
	v = safeCLIValue(v, "")
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

// observeBool renders a boolean as a word rather than Go's true/false, which
// reads as a value rather than a state in a status column.
func observeBool(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

// ---- artifacts -----------------------------------------------------------------

// observeArtifactFlag declares --out for the three routes in this lane that
// answer with a rendered artifact instead of a document.
//
// IT IS REQUIRED, and that is a decision rather than an oversight. A PDF posture
// report written to a terminal by default is a mangled screen at best; and
// "sometimes bytes, sometimes a summary, depending on whether stdout is a tty"
// is precisely the kind of contract a script cannot rely on. `--out -` names
// stdout explicitly, so the piping case is one word away and unambiguous.
func observeArtifactFlag(cmd *cobra.Command, out *string) {
	cmd.Flags().StringVar(out, "out", "",
		"write the artifact here; `-` means stdout (required: these routes answer with a rendered document, not JSON)")
}

// writeObserveArtifact stores the bytes and reports what was stored.
//
// The receipt goes to STDERR when the artifact goes to stdout, and to stdout
// otherwise. Without that split, `olivares reporting reports get … --out - >
// report.html` would append the receipt to the report — the same reason
// `tokens issue` puts its "shown once" warning on stderr so a `$(...)` capture
// gets the secret alone.
func writeObserveArtifact(cmd *cobra.Command, out string, res observeResult, what string) error {
	if strings.TrimSpace(out) == "" {
		return exitcode.New(exitcode.Usage, fmt.Errorf(
			"--out is required: %s answers with a rendered document, not JSON — pass a file path, or `-` for stdout", what))
	}
	mediaType := res.contentType
	if mt, _, err := mime.ParseMediaType(res.contentType); err == nil {
		mediaType = mt
	}
	receipt := struct {
		Wrote       string `json:"wrote"`
		Bytes       int    `json:"bytes"`
		ContentType string `json:"content_type,omitempty"`
	}{Wrote: out, Bytes: len(res.raw), ContentType: mediaType}

	if out == "-" {
		if _, err := cmd.OutOrStdout().Write(res.raw); err != nil {
			return exitcode.New(exitcode.Err, fmt.Errorf("write %s to stdout: %w", what, err))
		}
		// The artifact owns stdout, so the receipt goes to stderr — and it goes
		// there in BOTH output modes, because -o json must not put a second
		// document in front of the bytes a pipe is reading.
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d bytes of %s to stdout (%s)\n",
			receipt.Bytes, what, observeCell(mediaType))
		return err
	}
	// #nosec G304 -- the path is the operator's own --out argument.
	if err := os.WriteFile(filepath.Clean(out), res.raw, 0o600); err != nil {
		return exitcode.New(exitcode.Err, fmt.Errorf("write %s to %s: %w", what, out, err))
	}
	return renderOut(cmd, func(w io.Writer) error {
		_, werr := fmt.Fprintf(w, "wrote %d bytes of %s to %s (%s)\n",
			receipt.Bytes, what, receipt.Wrote, observeCell(mediaType))
		return werr
	}, receipt)
}

// readObserveDocument reads a document argument the operator supplies as a file
// path or as `-` for stdin. It is the input half of writeObserveArtifact, used
// by the one route that stores a raw HTML template.
func readObserveDocument(cmd *cobra.Command, path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("no document given: pass a file path, or `-` to read stdin"))
	}
	var (
		raw []byte
		err error
	)
	if path == "-" {
		raw, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxObserveBodySize+1))
	} else {
		// #nosec G304 -- the path is the operator's own argument.
		raw, err = os.ReadFile(filepath.Clean(path))
	}
	if err != nil {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("read document: %w", err))
	}
	if len(raw) > maxObserveBodySize {
		return nil, exitcode.New(exitcode.Usage,
			fmt.Errorf("document exceeds %d bytes", maxObserveBodySize))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		// The engine rejects an empty template with a 400 (enterprise.go:333).
		// Refusing here costs a round trip less and says the same thing.
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("document is empty: there is nothing to store"))
	}
	return raw, nil
}

// ---- shared argument checks ------------------------------------------------------

// requireObserveQuery refuses, from the arguments alone, a request the engine
// would answer 400 for want of a required query parameter.
//
// It is the local-refusal half of this lane's exit contract: the caller learns
// the flag is missing at exit 2 with no request sent, instead of exit 1 carrying
// the engine's 400. It can only ever REFUSE — no path through it authorizes
// anything.
func requireObserveQuery(q url.Values, param, flag string) error {
	if strings.TrimSpace(q.Get(param)) == "" {
		return exitcode.New(exitcode.Usage, fmt.Errorf("%s is required by this route", flag))
	}
	return nil
}

// observeIDPath joins a path segment safely. Every id in this lane arrives as a
// positional argument, and a positional argument is attacker-adjacent input the
// moment a script interpolates it: without escaping, `../../` in an id would
// retarget the request at a different route entirely.
func observeIDPath(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("/")
		b.WriteString(url.PathEscape(p))
	}
	return b.String()
}
