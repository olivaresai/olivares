// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The governed-data lane: the three module APIs that share the `source` /
// `binding` vocabulary. knowledge owns the corpus (KBs, data products, DLP);
// sourcescope decides WHICH sources a workspace or agent may reach; catalog is
// the admission gate the connectors and MCP servers carrying that data pass
// through. They are one lane because a change in any of them moves the same
// boundary, and an operator scripting one of them needs the other two to behave
// identically.
//
// Every route below is served by a MODULE (`/v1/m/<namespace>/…`, mounted by
// modules/{knowledge,sourcescope,catalog}/api.go). An engine built without the
// module answers 404 for the whole namespace: that is the control plane's
// answer, and this client reports it as one (exit 4) rather than inventing a
// friendlier verdict it cannot support.
const (
	knowledgeAPIBase   = "/v1/m/knowledge"
	sourcescopeAPIBase = "/v1/m/sourcescope"
	catalogAPIBase     = "/v1/m/catalog"

	// maxDatalaneResponseSize bounds a response body, exactly as the MCP client
	// bounds its own (cmd_mcp.go). A module list page is capped at 1000 rows by
	// the store, so 1 MiB is a real ceiling, not a guess.
	maxDatalaneResponseSize = 1 << 20

	// datalaneCellWidth truncates a nested value rendered into a text cell. The
	// full value is always in `-o json`; a table is a projection, never the
	// record.
	datalaneCellWidth = 40
)

// datalaneClient is the shared HTTP client of the three families. It is the
// mcpPinsClient shape (cmd_mcp.go:205-254) with a configurable base path: the
// same flag resolution (resolveCLIConfig), the same transport and trust
// controls (cliTransport), the same redaction, the same 1 MiB bound.
//
// NO AUTHORIZATION DECISION LIVES HERE. The client carries the caller's
// credential and the tenant header the engine resolves; every allow/deny is the
// control plane's. The local checks in this file only ever REFUSE (a malformed
// argument, an unconfirmed destructive verb, a body that is not JSON) — none of
// them can turn a denial into a pass.
type datalaneClient struct {
	flags *authClientFlags
	base  string // "/v1/m/knowledge"
	what  string // "knowledge", for error text
}

// do issues one request. path must already be a rooted, escaped path relative to
// the client's base (build it with datalanePath). query may be nil.
func (c datalaneClient) do(cmd *cobra.Command, method, path string, query url.Values, body any) ([]byte, int, error) {
	opts, err := c.flags.resolutionOptions(cmd)
	if err != nil {
		return nil, 0, err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return nil, 0, redactCoded(err, c.flags.token)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved,
		Insecure: c.flags.insecure,
		Timeout:  c.flags.timeout,
		Stderr:   cmd.ErrOrStderr(),
	})
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		requestBody = bytes.NewReader(encoded)
	}
	target := resolved.Server + c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, target, requestBody)
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDatalaneResponseSize+1))
	if err != nil {
		return nil, resp.StatusCode, exitcode.New(exitcode.Server,
			fmt.Errorf("read %s response: %w", c.what, err))
	}
	if len(raw) > maxDatalaneResponseSize {
		return nil, resp.StatusCode, exitcode.New(exitcode.Server,
			fmt.Errorf("%s response exceeds %d bytes", c.what, maxDatalaneResponseSize))
	}
	return raw, resp.StatusCode, nil
}

// doRawBody posts a body this CLI must not re-encode. `knowledge memory import`
// takes the newline-delimited bundle `memory export` produced, whose manifest
// line SIGNS the bytes that follow: decoding and re-marshaling it would break
// the signature verification the import performs before writing anything. So the
// bytes go up exactly as they came off disk.
func (c datalaneClient) doRawBody(cmd *cobra.Command, method, path string, body []byte) ([]byte, int, error) {
	opts, err := c.flags.resolutionOptions(cmd)
	if err != nil {
		return nil, 0, err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return nil, 0, redactCoded(err, c.flags.token)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved,
		Insecure: c.flags.insecure,
		Timeout:  c.flags.timeout,
		Stderr:   cmd.ErrOrStderr(),
	})
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, resolved.Server+c.base+path,
		bytes.NewReader(body))
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := cliDo(client, req)
	if err != nil {
		return nil, 0, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDatalaneResponseSize+1))
	if err != nil {
		return nil, resp.StatusCode, exitcode.New(exitcode.Server,
			fmt.Errorf("read %s response: %w", c.what, err))
	}
	// The +1 on the LimitReader is what makes an overflow DETECTABLE, and the
	// comparison is what makes it REPORTED. Without it the sentinel byte is read
	// and thrown away and this path hands back a body it has silently clipped —
	// the same defect datalaneTextArg carried on the request side. `do` has always
	// refused it by name; the two paths must not disagree about whether a response
	// that does not fit is an error, because the caller cannot tell them apart.
	if len(raw) > maxDatalaneResponseSize {
		return nil, resp.StatusCode, exitcode.New(exitcode.Server,
			fmt.Errorf("%s response exceeds %d bytes", c.what, maxDatalaneResponseSize))
	}
	return raw, resp.StatusCode, nil
}

// datalanePath joins segments under the client's base, percent-escaping EACH ONE
// so a positional argument lands as exactly one path segment.
//
// It is the control that stops `knowledge kbs rm '../../v1/system/orgs/t_x'`
// from addressing another route: url.PathEscape turns the separators into %2F,
// so the whole argument is one (nonexistent) id. url.PathEscape leaves a bare
// "." or ".." alone — those are unreserved characters — and a server that
// normalizes the path would then walk up, so datalaneSegment rejects those two
// spellings outright. A rejection, never an approval: a caller who passes a real
// id is unaffected.
func datalanePath(segments ...string) (string, error) {
	var sb strings.Builder
	for _, seg := range segments {
		escaped, err := datalaneSegment(seg)
		if err != nil {
			return "", err
		}
		sb.WriteString("/")
		sb.WriteString(escaped)
	}
	return sb.String(), nil
}

func datalaneSegment(seg string) (string, error) {
	switch strings.TrimSpace(seg) {
	case "":
		return "", exitcode.New(exitcode.Usage, fmt.Errorf("empty path argument"))
	case ".", "..":
		return "", exitcode.New(exitcode.Usage,
			fmt.Errorf("%q is not an identifier: it is a relative path element", seg))
	}
	return url.PathEscape(seg), nil
}

// datalaneHTTPError classifies a refusal. It delegates to httpErr — the ONE
// mapping this CLI has (401/403→3, 404→4, 409→5, 5xx→6) — so these families
// cannot answer the same refusal with a different number than `agent`, `mcp` or
// `compliance` do. 404 additionally names the likeliest cause on a module route.
func datalaneHTTPError(what string, status int, body []byte) error {
	if status == http.StatusNotFound {
		return exitcode.New(exitcode.NotFound, fmt.Errorf(
			"request failed: HTTP 404: %s (either the entity does not exist, or this engine "+
				"was built without the %s module, which serves the whole /v1/m/%s namespace)",
			strings.TrimSpace(string(body)), what, what))
	}
	return httpErr(status, body)
}

// datalaneOK reports whether status is a success this lane accepts. 202 is
// included ON PURPOSE and is NOT the same event as 200: sourcescope answers 202
// when a change RELAXES an existing confinement, which records a dual-controlled
// proposal instead of applying anything (modules/sourcescope/posture.go). A
// script that read 202 as "done" would believe access had been widened — or
// narrowed — when nothing had changed yet, so datalaneResult says so on stderr
// and the body carries the pending request.
func datalaneOK(status int) bool {
	return status >= 200 && status < 300
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

// datalanePageFlags is the cursor+limit pair every list route in the three
// families declares (listQuery in each module's dto.go/helpers.go).
type datalanePageFlags struct {
	limit  int
	cursor string
}

func (p *datalanePageFlags) add(cmd *cobra.Command) {
	cmd.Flags().IntVar(&p.limit, "limit", 0, "maximum rows per page (server default when unset)")
	cmd.Flags().StringVar(&p.cursor, "cursor", "", "opaque cursor from a previous page's has_more result")
}

// values validates before it encodes: a negative or zero --limit is a usage
// error the caller learns about WITHOUT a request being sent.
func (p *datalanePageFlags) values(cmd *cobra.Command, q url.Values) (url.Values, error) {
	if q == nil {
		q = url.Values{}
	}
	if cmd.Flags().Changed("limit") {
		if p.limit <= 0 {
			return nil, exitcode.New(exitcode.Usage,
				fmt.Errorf("--limit must be a positive number of rows, got %d", p.limit))
		}
		q.Set("limit", strconv.Itoa(p.limit))
	}
	if cursor := strings.TrimSpace(p.cursor); cursor != "" {
		q.Set("cursor", cursor)
	}
	return q, nil
}

// datalaneFilter adds one optional query filter, and only when the caller asked
// for it. Setting a filter to its empty value is not the same request as not
// setting it, and the module handlers ignore an empty value anyway.
func datalaneFilter(q url.Values, key, value string) url.Values {
	if q == nil {
		q = url.Values{}
	}
	if v := strings.TrimSpace(value); v != "" {
		q.Set(key, v)
	}
	return q
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// datalaneList is the engine-wide list envelope (core/api/listresponse.go).
// Items stay as decoded maps: the text form is a PROJECTION of a few columns,
// while `-o json` emits the untouched response, so a field this CLI does not
// model is never lost.
type datalaneList struct {
	Items   []map[string]any `json:"items"`
	Cursor  string           `json:"cursor"`
	HasMore bool             `json:"has_more"`
}

// datalaneColumn is one text column: its header and the response key it reads.
type datalaneColumn struct {
	head string
	key  string
}

func datalaneCols(pairs ...string) []datalaneColumn {
	cols := make([]datalaneColumn, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		cols = append(cols, datalaneColumn{head: pairs[i], key: pairs[i+1]})
	}
	return cols
}

// datalaneRenderList renders a list page: an aligned table for text, the RAW
// response for JSON, and — on stderr, so a `$(…)` capture never swallows it — a
// line naming the cursor when the page is truncated.
//
// The truncation note is the difference between a script that pages and one that
// silently processes the first page forever. It goes to stderr in BOTH formats:
// in JSON the cursor is also in the payload, and telling the operator twice
// costs nothing next to not telling them at all.
func datalaneRenderList(cmd *cobra.Command, what string, raw []byte, emptyNote string, cols []datalaneColumn) error {
	var page datalaneList
	if err := json.Unmarshal(raw, &page); err != nil {
		return exitcode.New(exitcode.Server, fmt.Errorf("decode %s list response: %w", what, err))
	}
	err := renderOut(cmd, func(out io.Writer) error {
		if len(page.Items) == 0 {
			_, err := fmt.Fprintln(out, emptyNote)
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		heads := make([]string, 0, len(cols))
		for _, c := range cols {
			heads = append(heads, c.head)
		}
		fmt.Fprintln(tw, strings.Join(heads, "\t"))
		for _, item := range page.Items {
			cells := make([]string, 0, len(cols))
			for _, c := range cols {
				cells = append(cells, datalaneCell(item, c.key))
			}
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
		return tw.Flush()
	}, json.RawMessage(raw))
	if err != nil {
		return err
	}
	if page.HasMore {
		if page.Cursor != "" {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"more rows: this is one page — re-run with --cursor %s\n", safeCLIValue(page.Cursor, ""))
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"more rows: the control plane reported has_more with no cursor, so this page cannot be continued")
		}
	}
	return nil
}

// datalaneResult renders a single-entity or action response.
//
// A body is rendered as sorted `key<TAB>value` lines for text and as the RAW
// response for JSON (renderStatusOut). An endpoint that answers 204 has no body
// to render, so text prints the command's own note and JSON reports the CLI's
// own envelope — {"ok":true,"http_status":204} — rather than an empty stdout a
// script cannot parse. That envelope is documented on every verb that can reach
// it; it is the ONE place this lane emits a value the control plane did not
// send.
func datalaneResult(cmd *cobra.Command, raw []byte, status int, emptyNote string) error {
	if status == http.StatusAccepted {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"accepted as a PROPOSAL (HTTP 202): the change is recorded for a second approver "+
				"and is NOT in effect — see `olivares sourcescope posture-requests ls`")
	}
	var rerr error
	if len(bytes.TrimSpace(raw)) == 0 {
		rerr = renderOut(cmd, func(out io.Writer) error {
			_, err := fmt.Fprintln(out, emptyNote)
			return err
		}, map[string]any{"ok": true, "http_status": status})
	} else {
		rerr = renderStatusOut(cmd, json.RawMessage(raw))
	}
	if rerr != nil {
		return rerr
	}
	// A 202 EXITS 7, and until 2026-08-18 it exited 0.
	//
	// THE DEFECT, found by the internal adversarial panel standing in for the Codex
	// contrast. This lane's own C08-03 unified the exit-code contract, and the same
	// event was answered two different ways inside it: reportAgentExecPending
	// (cmd_agentexec.go) returns Degraded for "a governance approval is pending",
	// while this leg printed "the change is recorded for a second approver and is NOT
	// in effect" and then returned nil.
	//
	// Nil is 0, and 0 is success to every consumer. The warning goes to STDERR, which
	// is precisely what a script does not read, so a script that applies a governed
	// posture change and reads 0 believes it took effect. That is not a wrong code —
	// it is an UNDUE ZERO, the most expensive shape in this family, because the caller
	// carries on.
	//
	// Degraded is documented as "the command succeeded but reports a degraded
	// condition" (exitcode.go:26-29): the request WAS accepted and recorded, and it is
	// NOT in effect. That is the state, and it is the same verdict its sibling already
	// gives. The body still renders first, in both text and JSON, so nothing a caller
	// parses is lost — only the code changes, and a render failure still wins over it.
	if status == http.StatusAccepted {
		return exitcode.New(exitcode.Degraded,
			errors.New("recorded as a proposal for a second approver: NOT in effect"))
	}
	return nil
}

// datalaneRaw writes a non-JSON body through verbatim. `knowledge memory export`
// answers application/x-ndjson (modules/knowledge/portability.go:219) and its
// manifest line carries a SIGNATURE over the bytes: re-encoding it as one JSON
// document would break the verification the export exists for. So the bundle is
// forwarded unchanged in both output modes, and every verb that uses this says
// so in its own help.
func datalaneRaw(cmd *cobra.Command, raw []byte, outFile string) error {
	if outFile != "" {
		if err := os.WriteFile(outFile, raw, 0o600); err != nil {
			return exitcode.New(exitcode.Err, fmt.Errorf("write %s: %w", outFile, err))
		}
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d bytes to %s\n", len(raw), outFile)
		return err
	}
	_, err := cmd.OutOrStdout().Write(raw)
	return err
}

// datalaneCell formats one response value for a text table.
func datalaneCell(item map[string]any, key string) string {
	value, ok := item[key]
	if !ok || value == nil {
		return "-"
	}
	switch v := value.(type) {
	case string:
		if v == "" {
			return "-"
		}
		return safeCLIValue(v, "")
	case bool:
		return strconv.FormatBool(v)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'g', -1, 64)
	case []any:
		if len(v) == 0 {
			return "-"
		}
		parts := make([]string, 0, len(v))
		for _, e := range v {
			parts = append(parts, fmt.Sprintf("%v", e))
		}
		return datalaneTruncate(safeCLIValue(strings.Join(parts, ","), ""))
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "-"
		}
		return datalaneTruncate(safeCLIValue(string(encoded), ""))
	}
}

func datalaneTruncate(s string) string {
	if len(s) <= datalaneCellWidth {
		return s
	}
	return s[:datalaneCellWidth] + "…"
}

// ---------------------------------------------------------------------------
// Bodies
// ---------------------------------------------------------------------------

// datalaneJSONArg resolves a JSON-valued flag from a literal or a file (- is
// stdin), and REFUSES anything that is not valid JSON before a connection is
// opened.
//
// The module decoders run with DisallowUnknownFields (each module's dto.go), so
// a body this CLI cannot vouch for is answered 400 by the engine. Validating the
// syntax locally turns the most common mistake — a shell that mangled the quotes
// — into a usage error (exit 2) with the file named, instead of a round trip
// that reports a generic bad request.
func datalaneJSONArg(cmd *cobra.Command, name, literal, file string) (json.RawMessage, error) {
	literalSet := cmd.Flags().Changed(name)
	fileSet := cmd.Flags().Changed(name + "-file")
	switch {
	case literalSet && fileSet:
		return nil, exitcode.New(exitcode.Usage,
			fmt.Errorf("--%s and --%s-file are two spellings of the same value: pass one", name, name))
	case !literalSet && !fileSet:
		return nil, nil
	}
	body := []byte(literal)
	if fileSet {
		var (
			read []byte
			err  error
		)
		if file == "-" {
			read, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxDatalaneResponseSize+1))
		} else {
			read, err = os.ReadFile(file) // #nosec G304 -- an operator-named input file is the point of the flag
		}
		if err != nil {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf("read --%s-file: %w", name, err))
		}
		if len(read) > maxDatalaneResponseSize {
			return nil, exitcode.New(exitcode.Usage,
				fmt.Errorf("--%s-file exceeds %d bytes", name, maxDatalaneResponseSize))
		}
		body = read
	}
	if !json.Valid(body) {
		source := "--" + name
		if fileSet {
			source = "--" + name + "-file " + file
		}
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("%s is not valid JSON", source))
	}
	return json.RawMessage(body), nil
}

// datalaneKeyValues parses repeated `key=value` flags into a map, refusing a
// pair with no `=` rather than guessing what the operator meant.
func datalaneKeyValues(flag string, pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, exitcode.New(exitcode.Usage,
				fmt.Errorf("--%s %q must be key=value", flag, pair))
		}
		out[key] = value
	}
	return out, nil
}

// datalaneTextArg resolves a value from a literal or a file (- is stdin), the
// readSecretValue shape (cmd_secrets.go:253) for content that does not belong on
// a command line — a prompt template, a memory entry, a retrieval query.
//
// A STDIN PAYLOAD OVER THE BOUND IS REFUSED, NEVER TRUNCATED. The LimitReader
// takes maxDatalaneResponseSize+1 bytes precisely so the overflow is DETECTABLE:
// without the comparison the sentinel byte is read and thrown away, and the
// command sends the first mebibyte of a longer memory entry, prompt template or
// retrieval query and exits 0 — the operator is told the whole value was stored
// when a prefix of it was. Measured on this lane: `knowledge memory put
// --content-file -`, `knowledge prompts create --template-file -`, `knowledge
// prompts revisions add --template-file -`, `knowledge kbs query --query-file -`
// and `knowledge memory import --bundle-file -` each shipped 1048577 of 1052672
// supplied bytes and reported success. datalaneJSONArg has always refused the
// same overflow by name; this is the same refusal, not a new policy.
//
// The bound applies to STDIN ONLY, because that is the only path that was
// silently losing bytes. A named file is read whole, as it always has been: the
// import route alone accepts 64 MiB (modules/knowledge/portability.go:59), and
// capping the file form at 1 MiB would withdraw a capability the control plane
// grants instead of fixing the one that lies.
func datalaneTextArg(cmd *cobra.Command, name, literal, file string) (string, bool, error) {
	literalSet := cmd.Flags().Changed(name)
	fileSet := cmd.Flags().Changed(name + "-file")
	switch {
	case literalSet && fileSet:
		return "", false, exitcode.New(exitcode.Usage,
			fmt.Errorf("--%s and --%s-file are two spellings of the same value: pass one", name, name))
	case !literalSet && !fileSet:
		return "", false, nil
	case literalSet:
		return literal, true, nil
	}
	var (
		read []byte
		err  error
	)
	if file == "-" {
		read, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxDatalaneResponseSize+1))
		if err == nil && len(read) > maxDatalaneResponseSize {
			return "", false, exitcode.New(exitcode.Usage, fmt.Errorf(
				"--%s-file - exceeds %d bytes read from stdin: pass the value as a named file "+
					"(--%s-file <path>) so none of it is dropped", name, maxDatalaneResponseSize, name))
		}
	} else {
		read, err = os.ReadFile(file) // #nosec G304 -- an operator-named input file is the point of the flag
	}
	if err != nil {
		return "", false, exitcode.New(exitcode.Usage, fmt.Errorf("read --%s-file: %w", name, err))
	}
	return strings.TrimRight(string(read), "\r\n"), true, nil
}

// datalaneRawArg reads a SIGNED artifact from a file (- is stdin) and hands back
// the bytes UNCHANGED.
//
// It exists because `knowledge memory import` was reading its bundle with
// datalaneTextArg, whose documented contract is to strip the trailing newline —
// correct for a prompt template or a memory entry an operator typed, and wrong
// for a bundle whose manifest line SIGNS the payload. The bytes were then posted
// through doRawBody, which says in its own doc that they "go up exactly as they
// came off disk". They did not: every export ends in a newline, so every import
// silently edited the artifact it was told not to re-encode.
//
// Today's control plane happens not to notice — handleImportMemory streams the
// body through a json.Decoder and recomputes the digest over a CANONICAL
// re-marshal of each decoded row (modules/knowledge/portability.go:269-340), so
// trailing whitespace is outside what it verifies. That is a property of the
// current verifier, not a promise: a body digest, a detached signature over the
// file, or any middlebox that hashes what it forwards would all break on bytes
// the client quietly removed, and the break would be a signature failure with no
// hint that the CLI was the editor.
//
// Every control datalaneTextArg applies to stdin is kept, including the bound
// whose absence used to truncate silently, and so is its deliberate asymmetry: a
// NAMED file is read whole, because the import route accepts 64 MiB
// (modules/knowledge/portability.go:59) and capping it here would withdraw a
// capability the control plane grants.
func datalaneRawArg(cmd *cobra.Command, name, file string) ([]byte, error) {
	if file == "-" {
		read, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxDatalaneResponseSize+1))
		if err != nil {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf("read --%s-file: %w", name, err))
		}
		if len(read) > maxDatalaneResponseSize {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
				"--%s-file - exceeds %d bytes read from stdin: pass the value as a named file "+
					"(--%s-file <path>) so none of it is dropped", name, maxDatalaneResponseSize, name))
		}
		return read, nil
	}
	read, err := os.ReadFile(file) // #nosec G304 -- an operator-named input file is the point of the flag
	if err != nil {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("read --%s-file: %w", name, err))
	}
	return read, nil
}

// ---------------------------------------------------------------------------
// The replace guard
// ---------------------------------------------------------------------------

// datalaneRequireCompleteReplace refuses a PARTIALLY specified body for an
// endpoint whose PUT replaces the stored row rather than patching it.
//
// This is not a style preference, it is a measured hazard. `PUT
// /v1/m/knowledge/kbs/{id}` reads the whole request into a fresh kbRequest and
// writes every column from it (modules/knowledge/kb.go:265-313): an omitted
// classification is normalized to "internal", an omitted residency_region to
// "global", an omitted default_acl to the empty list, an omitted status to
// "active". So `kbs set <id> --status archived` — which reads like a patch —
// would ALSO rewrite the classification, the residency and the ACL of a governed
// corpus, silently, and the CLI would report success. The same shape holds for
// sourcescope bindings/assignments/workspace-connectors, catalog entries and the
// two admission policies.
//
// The rule this installs: either name every field the endpoint replaces, or pass
// --replace to say the defaults are what you meant. It only ever refuses; with
// the flags supplied it does nothing at all.
//
// fields lists ONLY what the endpoint actually rewrites, measured handler by
// handler — not every flag the command carries. Several of these PUTs force an
// immutable natural key back from the stored row (sourcescope bindings'
// source_type/source_ref, assignments' connector_name/workspace_ref,
// workspace-connectors' name/kind/workspace_ref), and demanding those would
// teach an operator that passing them matters when the control plane discards
// them. An entry may name two spellings of one value as "spec|spec-file";
// either satisfies it.
func datalaneRequireCompleteReplace(cmd *cobra.Command, accepted bool, fields []string) error {
	if accepted {
		return nil
	}
	missing := make([]string, 0, len(fields))
	for _, f := range fields {
		satisfied := false
		for _, spelling := range strings.Split(f, "|") {
			if cmd.Flags().Changed(spelling) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, "--"+strings.Split(f, "|")[0])
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return exitcode.New(exitcode.Usage, fmt.Errorf(
		"this endpoint REPLACES the stored row, it does not patch it: %s would be reset to "+
			"their server defaults. Pass them explicitly, or pass --replace to accept the reset",
		strings.Join(missing, ", ")))
}

// addDatalaneReplaceFlag declares --replace with one wording across the lane.
func addDatalaneReplaceFlag(cmd *cobra.Command, replace *bool) {
	cmd.Flags().BoolVar(replace, "replace", false,
		"accept that every field not passed is RESET to its server default (this endpoint replaces, it does not patch)")
}

// datalaneListLong appends the paging contract to a list command's help, once,
// so the sentence an operator learns is the same on all 40-odd of them.
func datalaneListLong(body string) string {
	return body + "\n\n" +
		"Paging: --limit and --cursor. Text output is a projection of a few columns;\n" +
		"-o json is the untouched API response, including cursor and has_more. A\n" +
		"truncated page is announced on stderr, naming the cursor to continue from."
}
