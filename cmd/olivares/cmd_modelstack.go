// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
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

// The model stack is the three API modules that share the "model" vocabulary:
// the model registry and its governance (models), the gateway that sits in front
// of those models (inferenceproxy), and what spending against them costs
// (finops). They are wired here as one substrate because the alternative is
// three near-identical copies of the same client, and copies drift — which is
// exactly the defect C08-03 measured across four of them.
//
// WHAT THIS FILE IS NOT. It holds no authorization decision of any kind. Every
// route below is mounted by the engine behind authentication, tenant resolution
// and a declared permission (core/api/modules.go:37); the CLI carries the
// caller's credential and the tenant header and nothing else. The only local
// refusals are about the INVOCATION — a missing credential, an unreadable
// --data, an unconfirmed destructive verb — and each of them can only ever
// REFUSE. None can approve anything the engine would not.
const (
	modelsAPIBase         = "/v1/m/models"
	finopsAPIBase         = "/v1/m/finops"
	inferenceProxyAPIBase = "/v1/m/inferenceproxy"

	// maxModelstackResponseSize bounds a response the CLI will hold in memory.
	// It is larger than the 1 MiB the MCP client uses because three routes in
	// this lot are EXPORTS whose whole purpose is bulk: GET /spend/export,
	// GET /statements/{id}/export and the CycloneDX/SPDX AIBOM documents. The
	// limit is enforced by REFUSING, never by truncating: a silently short
	// export is a wrong answer that looks like a right one.
	maxModelstackResponseSize = 16 << 20

	// maxModelstackRequestSize bounds what --data may carry. It matches the
	// engine's own body limit (modules/*/dto.go decodeJSON reads through an
	// io.LimitReader of 1 MiB), so an oversized body is named here as a usage
	// error instead of coming back as an opaque 400.
	maxModelstackRequestSize = 1 << 20

	// modelstackMaxPages bounds --all. A cursor loop with no ceiling is a hang,
	// and a hang in a script is worse than an error.
	modelstackMaxPages = 500

	// modelstackCellWidth is where a text cell is elided. -o json is always
	// complete, so the table is allowed to be readable.
	modelstackCellWidth = 48
)

// modelstackClient is the HTTP substrate for one module namespace. It is the
// shape of mcpPinsClient (cmd_mcp.go:205-254) with three additions the routes in
// this lot need: a query string, the response's content type (three routes serve
// CSV/Markdown), and a credential pre-flight.
type modelstackClient struct {
	flags *authClientFlags
	// base is the module mount point, e.g. /v1/m/models.
	base string
	// family names the module in error text ("models", "finops", …).
	family string
}

// modelstackResult is one HTTP response: the body, its status and its declared
// content type. The content type is carried because three routes in this lot do
// not answer JSON, and rendering CSV through a JSON decoder is how an export
// silently becomes an error message.
type modelstackResult struct {
	Raw         []byte
	Status      int
	ContentType string
}

// do performs one request against the module namespace. path is
// module-relative ("/owned-models"); query is an already-encoded query string.
func (c modelstackClient) do(cmd *cobra.Command, method, path, query string, body []byte) (modelstackResult, error) {
	opts, err := c.flags.resolutionOptions(cmd)
	if err != nil {
		return modelstackResult{}, err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return modelstackResult{}, redactCoded(err, c.flags.token)
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved,
		Insecure: c.flags.insecure,
		Timeout:  c.flags.timeout,
		Stderr:   cmd.ErrOrStderr(),
	})
	if err != nil {
		return modelstackResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	// A missing credential is refused HERE, before a connection is opened. Every
	// route in this lot is behind the engine's authenticator, so a call without
	// one can only come back 401 — and 401 exits 3, "the control plane rejected
	// the caller", which is a different fact from "this invocation never had a
	// credential to be rejected". The second is a usage error (2), and saying so
	// without a round trip also means an unconfigured caller learns nothing about
	// which hosts answer.
	if resolved.Token == "" {
		return modelstackResult{}, missingCLIValueError("API token", "--token", "OLIVARES_TOKEN", resolved)
	}
	target := resolved.Server + c.base + path
	if query != "" {
		target += "?" + query
	}
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, target, requestBody)
	if err != nil {
		return modelstackResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return modelstackResult{}, exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelstackResponseSize+1))
	if err != nil {
		return modelstackResult{}, exitcode.New(exitcode.Server,
			fmt.Errorf("read %s response: %w", c.family, err))
	}
	if len(raw) > maxModelstackResponseSize {
		return modelstackResult{}, exitcode.New(exitcode.Server, fmt.Errorf(
			"%s response exceeds %d bytes; narrow the request (a filter, a smaller --limit, a shorter window) rather than reading a truncated one",
			c.family, maxModelstackResponseSize))
	}
	return modelstackResult{Raw: raw, Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type")}, nil
}

// modelstackHTTPError maps the statuses these three modules actually emit onto
// the published exit contract. It EXTENDS httpErr (cmd_agent.go:589) rather than
// replacing it: the four codes httpErr already assigns keep the values every
// other command in this binary gives them, and only the statuses httpErr leaves
// unclassified are named here.
//
// The three additions, and why each is the code it is:
//
//   - 400 → 2 (usage). A rejected --data or an unparseable filter is the
//     caller's invocation, not a plane failure. Precedent: workHTTPError
//     (cmd_work.go:913) already classifies 400 this way; httpErr's default of 1
//     would tell a script "generic failure" for a typo.
//   - 402 / 429 → 5 (conflict). These are the FinOps budget cap on a routing
//     resolve/execute (models/api.go:675-678: 402 block, 429 throttle). The
//     request contradicts current state — the budget is at its cap — which is
//     exactly what 5 means. It is deliberately NOT 6: a budget denial from a
//     perfectly healthy control plane must never send an operator to look at the
//     server, which is the defect C08-03 removed from four other clients.
//   - 410 / 422 / 423 → 5 (conflict). An expired device code, a semantically
//     invalid deployment, a locked resource: state, not transport.
//
// Everything else keeps httpErr's mapping, including 5xx → 6.
func modelstackHTTPError(res modelstackResult) error {
	body := strings.TrimSpace(string(res.Raw))
	base := fmt.Errorf("request failed: HTTP %d: %s", res.Status, body)
	switch res.Status {
	case http.StatusBadRequest:
		return exitcode.New(exitcode.Usage, base)
	case http.StatusPaymentRequired, http.StatusTooManyRequests:
		return exitcode.New(exitcode.Conflict, fmt.Errorf(
			"%w (an enforcing budget is at its cap: %s)", base, modelstackBudgetHint(res.Status)))
	case http.StatusGone, http.StatusUnprocessableEntity, http.StatusLocked:
		return exitcode.New(exitcode.Conflict, base)
	case http.StatusServiceUnavailable:
		return exitcode.New(exitcode.Server, fmt.Errorf(
			"%w — this route is deny-closed when its executor or store is not provisioned; it is a configuration gap, not a crash", base))
	default:
		return httpErr(res.Status, res.Raw)
	}
}

// modelstackBudgetHint names the remedy the two budget statuses have, which are
// not the same remedy: a throttle clears when the window rolls over, a block
// does not clear until the budget is raised.
func modelstackBudgetHint(status int) string {
	if status == http.StatusTooManyRequests {
		return "throttled — it clears when the budget window rolls over, so a retry with backoff is meaningful"
	}
	return "blocked — retrying will not clear it; raise or re-scope the budget"
}

// --- request targets ---------------------------------------------------------

// modelstackTarget builds a request path from a collection, an optional nested
// segment and the positional identifiers the route takes. It exists so that
// EVERY identifier in this lot is escaped in one place: a positional argument is
// operator-supplied text, and an unescaped one can re-aim the request at another
// route entirely.
type modelstackTarget struct {
	// Collection is the module-relative collection, e.g. "/owned-models".
	Collection string
	// Nested is the sub-resource, e.g. "aibom" or "mappings".
	Nested string
	// IDs is how many positional identifiers the path consumes: 0, 1 or 2.
	// With 2, the second one follows Nested (…/cost-centers/{id}/mappings/{mid}).
	IDs int
}

func (t modelstackTarget) path(args []string) (string, error) {
	if len(args) < t.IDs {
		return "", exitcode.New(exitcode.Usage,
			fmt.Errorf("expected %d identifier(s), got %d", t.IDs, len(args)))
	}
	path := t.Collection
	if t.IDs >= 1 {
		seg, err := modelstackIDSegment(args[0])
		if err != nil {
			return "", err
		}
		path += "/" + seg
	}
	if t.Nested != "" {
		path += "/" + t.Nested
	}
	if t.IDs >= 2 {
		seg, err := modelstackIDSegment(args[1])
		if err != nil {
			return "", err
		}
		path += "/" + seg
	}
	return path, nil
}

// modelstackIDSegment escapes one operator-supplied identifier for use as a
// single path segment.
//
// url.PathEscape percent-encodes "/", so `rm ../../v1/system/orgs/t_victim`
// lands under this collection as one (nonexistent) identifier instead of
// addressing another module's route. A blank identifier is refused rather than
// silently addressing the collection: DELETE on a collection is a different
// request than DELETE on a member, and the difference is everything.
func modelstackIDSegment(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", exitcode.New(exitcode.Usage,
			fmt.Errorf("the identifier is empty; a blank identifier would address the collection, not a member"))
	}
	if strings.ContainsAny(id, "\x00\r\n") {
		return "", exitcode.New(exitcode.Usage,
			fmt.Errorf("the identifier contains a control character"))
	}
	return url.PathEscape(id), nil
}

// --- query parameters --------------------------------------------------------

// modelstackFilterSpec declares one query-parameter flag. Only parameters the
// handler was MEASURED to read are declared: a flag the engine ignores is worse
// than a missing flag, because the operator believes the result is filtered.
type modelstackFilterSpec struct {
	Flag  string
	Query string
	Usage string
}

// modelstackValues collects the declared filters into a deterministic query.
// url.Values.Encode sorts by key, so two identical invocations produce the same
// request line — which is what makes a recorded request comparable in a test.
func modelstackValues(specs []modelstackFilterSpec, values []string) url.Values {
	out := url.Values{}
	for i, spec := range specs {
		if i >= len(values) {
			break
		}
		if v := strings.TrimSpace(values[i]); v != "" {
			out.Set(spec.Query, v)
		}
	}
	return out
}

func addModelstackFilters(cmd *cobra.Command, specs []modelstackFilterSpec, values []string) {
	for i := range specs {
		cmd.Flags().StringVar(&values[i], specs[i].Flag, "", specs[i].Usage)
	}
}

// --- request bodies ----------------------------------------------------------

// modelstackBodyMode says whether a write verb needs a --data document.
type modelstackBodyMode int

const (
	modelstackBodyNone modelstackBodyMode = iota
	modelstackBodyRequired
	modelstackBodyOptional
)

const modelstackDataUsage = "request document: inline JSON, @FILE, or - for stdin"

// modelstackReadBody resolves --data into the bytes to send.
//
// The three spellings are the ones an operator already knows from every other
// tool that takes a document, and they are what makes these verbs usable from a
// script: the payload can be generated, piped, or held in a file under review.
// The document is VALIDATED as JSON here so that a shell quoting mistake is a
// usage error naming the flag, not an opaque 400 from the engine.
func modelstackReadBody(cmd *cobra.Command, data string, mode modelstackBodyMode) ([]byte, error) {
	if mode == modelstackBodyNone {
		return nil, nil
	}
	raw := strings.TrimSpace(data)
	if raw == "" {
		if mode == modelstackBodyOptional {
			return nil, nil
		}
		return nil, exitcode.New(exitcode.Usage,
			fmt.Errorf("--data is required: pass inline JSON, @FILE, or - to read the document from stdin"))
	}
	var (
		body []byte
		err  error
	)
	switch {
	case raw == "-":
		body, err = modelstackReadAll(cmd.InOrStdin(), "stdin")
	case strings.HasPrefix(raw, "@"):
		name := strings.TrimSpace(raw[1:])
		if name == "" {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf("--data @ names no file"))
		}
		var f *os.File
		f, err = os.Open(name) //nolint:gosec // operator-selected request document
		if err == nil {
			defer func() { _ = f.Close() }()
			body, err = modelstackReadAll(f, name)
		} else {
			err = exitcode.New(exitcode.Usage, fmt.Errorf("read --data file: %w", err))
		}
	default:
		body = []byte(data)
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
			"--data is not valid JSON (%d bytes read); the control plane takes one JSON document per request", len(body)))
	}
	return body, nil
}

func modelstackReadAll(r io.Reader, what string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxModelstackRequestSize+1))
	if err != nil {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("read --data from %s: %w", what, err))
	}
	if len(body) > maxModelstackRequestSize {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
			"--data from %s exceeds %d bytes, which is the control plane's own body limit", what, maxModelstackRequestSize))
	}
	return body, nil
}

func addModelstackDataFlag(cmd *cobra.Command, data *string) {
	cmd.Flags().StringVar(data, "data", "", modelstackDataUsage)
}

// --- rendering ---------------------------------------------------------------

// modelstackColumn is one text-table column, named by the JSON field it reads.
type modelstackColumn struct {
	Header string
	Key    string
}

// modelstackRenderReport renders a JSON payload that is not a list: the aligned
// key/value form for text and the SERVER'S OWN bytes for json.
//
// Preserving the raw bytes matters more than it looks: these DTOs carry fields
// the CLI does not model, and a re-marshaled struct would quietly drop every
// one of them from -o json — the output a script reads.
func modelstackRenderReport(cmd *cobra.Command, res modelstackResult) error {
	var decoded any
	if err := json.Unmarshal(res.Raw, &decoded); err != nil {
		return exitcode.New(exitcode.Server, fmt.Errorf("decode response: %w", err))
	}
	return renderOut(cmd, func(out io.Writer) error {
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		writeStatusLines(tw, "", decoded)
		return tw.Flush()
	}, json.RawMessage(res.Raw))
}

// modelstackRenderPayload renders a response that MAY NOT be JSON.
//
// Three routes in this lot serve another media type by design: the model card
// with ?format=md, the FOCUS spend export, and a statement export. For those the
// body goes to stdout VERBATIM — a byte-exact export is the point of the verb,
// and re-encoding it would corrupt it — with a note on STDERR naming the media
// type, so `-o json` cannot silently look like it was honored. stdout stays
// exactly what the caller asked to redirect.
func modelstackRenderPayload(cmd *cobra.Command, res modelstackResult) error {
	if modelstackIsJSON(res.ContentType) || res.ContentType == "" {
		return modelstackRenderReport(cmd, res)
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"note: the control plane answered %s, not JSON; stdout carries it verbatim and -o does not apply\n",
		strings.TrimSpace(res.ContentType))
	_, err := cmd.OutOrStdout().Write(res.Raw)
	return err
}

func modelstackIsJSON(contentType string) bool {
	media := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	return media == "application/json" || strings.HasSuffix(media, "+json")
}

// modelstackCell formats one decoded JSON value for a text table.
//
// It renders through safeCLIValue, which strips control characters: every value
// here arrives from the control plane and lands in a terminal, and a raw escape
// sequence in a model name is a rendering the operator did not ask for.
func modelstackCell(value any) string {
	switch v := value.(type) {
	case nil:
		return "-"
	case string:
		if strings.TrimSpace(v) == "" {
			return "-"
		}
		return modelstackElide(safeCLIValue(v, ""))
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
		for _, item := range v {
			parts = append(parts, modelstackCell(item))
		}
		return modelstackElide(strings.Join(parts, ","))
	case map[string]any:
		if len(v) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+modelstackCell(v[k]))
		}
		return modelstackElide(strings.Join(parts, " "))
	default:
		return modelstackElide(safeCLIValue(fmt.Sprintf("%v", v), ""))
	}
}

func modelstackElide(s string) string {
	if len(s) <= modelstackCellWidth {
		return s
	}
	return s[:modelstackCellWidth] + "…"
}

// --- list pages --------------------------------------------------------------

// modelstackPage is one decoded page of a collection. Items stay RAW so that
// --all can concatenate pages without re-encoding a single field.
type modelstackPage struct {
	Items   []json.RawMessage
	Cursor  string
	HasMore bool
}

// modelstackItemsKey is the array field of the list envelope every collection
// route in this lot returns (core/api/listresponse.go). It is a constant rather
// than a per-command knob because all 25 collection routes in these three
// modules were measured to use the same envelope, and a configurable key nobody
// configures is a lie about what varies.
const modelstackItemsKey = "items"

// modelstackDecodePage reads one page of that envelope.
//
// A missing array is an ERROR, not an empty page. "the field is absent" and
// "there is nothing here" are different facts, and reporting the first as the
// second is how a shape change becomes a silent zero in a report.
func modelstackDecodePage(raw []byte, itemsKey string) (modelstackPage, error) {
	if itemsKey == "" {
		itemsKey = modelstackItemsKey
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return modelstackPage{}, exitcode.New(exitcode.Server, fmt.Errorf("decode list response: %w", err))
	}
	rawItems, ok := envelope[itemsKey]
	if !ok {
		return modelstackPage{}, exitcode.New(exitcode.Server, fmt.Errorf(
			"the response carries no %q array; the control plane answered a shape this command does not know", itemsKey))
	}
	page := modelstackPage{}
	if err := json.Unmarshal(rawItems, &page.Items); err != nil {
		return modelstackPage{}, exitcode.New(exitcode.Server,
			fmt.Errorf("decode %q array: %w", itemsKey, err))
	}
	if rawCursor, ok := envelope["cursor"]; ok {
		_ = json.Unmarshal(rawCursor, &page.Cursor)
	}
	if rawMore, ok := envelope["has_more"]; ok {
		_ = json.Unmarshal(rawMore, &page.HasMore)
	}
	return page, nil
}

// modelstackListSpec declares one collection-listing verb.
type modelstackListSpec struct {
	Use, Short, Long, Example string
	// Aliases is explicit rather than a hardcoded {"list"}: several listing
	// verbs in this lot are not called "ls" (`models catalog`, `finops alerts`),
	// and giving each of them the same alias would register two commands under
	// one parent answering to the same word.
	Aliases []string
	Target  modelstackTarget
	// ItemsKey is the array field in the response; "" means "items".
	ItemsKey string
	// EmptyNote is this command's own words for an empty result.
	EmptyNote string
	Columns   []modelstackColumn
	Filters   []modelstackFilterSpec
	// Paginated is true ONLY where the handler was measured to read ?cursor and
	// ?limit. Where it is false the flags are not offered, because a flag the
	// engine ignores tells the operator a lie about their own result.
	Paginated bool
	// CapNote explains a server-side cap on a route that cannot be paged.
	CapNote string
}

func newModelstackListCmd(c modelstackClient, spec modelstackListSpec) *cobra.Command {
	var (
		limit   int
		cursor  string
		all     bool
		filters = make([]string, len(spec.Filters))
	)
	cmd := &cobra.Command{
		Use:     spec.Use,
		Aliases: spec.Aliases,
		Short:   spec.Short,
		Long:    spec.Long,
		Example: spec.Example,
		Args:    cobra.ExactArgs(spec.Target.IDs),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := spec.Target.path(args)
			if err != nil {
				return err
			}
			query := modelstackValues(spec.Filters, filters)
			if spec.Paginated {
				if limit < 0 {
					return exitcode.New(exitcode.Usage, fmt.Errorf("--limit cannot be negative"))
				}
				if limit > 0 {
					query.Set("limit", strconv.Itoa(limit))
				}
				if cursor != "" {
					query.Set("cursor", cursor)
				}
			}
			if !all {
				res, err := c.do(cmd, http.MethodGet, path, query.Encode(), nil)
				if err != nil {
					return err
				}
				if res.Status != http.StatusOK {
					return modelstackHTTPError(res)
				}
				page, err := modelstackDecodePage(res.Raw, spec.ItemsKey)
				if err != nil {
					return err
				}
				if err := modelstackRenderRows(cmd, spec, page.Items, json.RawMessage(res.Raw)); err != nil {
					return err
				}
				modelstackNotePartialPage(cmd, spec, page)
				return nil
			}
			items, err := modelstackCollectAllPages(cmd, c, spec, path, query)
			if err != nil {
				return err
			}
			merged, err := json.Marshal(struct {
				Items   []json.RawMessage `json:"items"`
				HasMore bool              `json:"has_more"`
			}{Items: items, HasMore: false})
			if err != nil {
				return exitcode.New(exitcode.Err, fmt.Errorf("encode merged pages: %w", err))
			}
			return modelstackRenderRows(cmd, spec, items, json.RawMessage(merged))
		},
	}
	addModelstackFilters(cmd, spec.Filters, filters)
	if spec.Paginated {
		cmd.Flags().IntVar(&limit, "limit", 0, "page size to request (0 leaves the control plane's default)")
		cmd.Flags().StringVar(&cursor, "cursor", "", "opaque cursor from a previous page's cursor field")
		cmd.Flags().BoolVar(&all, "all", false,
			"follow the cursor to the end and emit one merged page (json output carries has_more:false and no cursor)")
	}
	return cmd
}

// modelstackCollectAllPages follows the cursor to the end of the collection.
//
// Two ways this can fail to terminate are refused explicitly rather than hung
// on: a control plane that repeats a cursor, and one that never stops issuing
// them. Both exit with a message naming what happened, because a script that
// hangs reports nothing at all.
func modelstackCollectAllPages(cmd *cobra.Command, c modelstackClient, spec modelstackListSpec,
	path string, query url.Values,
) ([]json.RawMessage, error) {
	var (
		items []json.RawMessage
		seen  = map[string]bool{}
	)
	for pages := 0; ; pages++ {
		if pages >= modelstackMaxPages {
			return nil, exitcode.New(exitcode.Server, fmt.Errorf(
				"--all stopped after %d pages without reaching the end; narrow the request with a filter", modelstackMaxPages))
		}
		res, err := c.do(cmd, http.MethodGet, path, query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if res.Status != http.StatusOK {
			return nil, modelstackHTTPError(res)
		}
		page, err := modelstackDecodePage(res.Raw, spec.ItemsKey)
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if !page.HasMore || page.Cursor == "" {
			return items, nil
		}
		if seen[page.Cursor] {
			return nil, exitcode.New(exitcode.Server, fmt.Errorf(
				"the control plane returned the same cursor twice; refusing to page forever"))
		}
		seen[page.Cursor] = true
		query.Set("cursor", page.Cursor)
	}
}

func modelstackRenderRows(cmd *cobra.Command, spec modelstackListSpec, items []json.RawMessage, jsonVal any) error {
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var row map[string]any
		if err := json.Unmarshal(item, &row); err != nil {
			// A non-object element is still worth showing; render it as one cell
			// under the first column rather than failing the whole listing.
			row = map[string]any{}
			if len(spec.Columns) > 0 {
				var scalar any
				_ = json.Unmarshal(item, &scalar)
				row[spec.Columns[0].Key] = scalar
			}
		}
		rows = append(rows, row)
	}
	return renderOut(cmd, func(out io.Writer) error {
		if len(rows) == 0 {
			_, err := fmt.Fprintln(out, spec.EmptyNote)
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		headers := make([]string, 0, len(spec.Columns))
		for _, col := range spec.Columns {
			headers = append(headers, col.Header)
		}
		fmt.Fprintln(tw, strings.Join(headers, "\t"))
		for _, row := range rows {
			cells := make([]string, 0, len(spec.Columns))
			for _, col := range spec.Columns {
				cells = append(cells, modelstackCell(row[col.Key]))
			}
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
		return tw.Flush()
	}, jsonVal)
}

// modelstackNotePartialPage says on STDERR that more rows exist.
//
// STDERR, not stdout, and that is the whole point: a truncation note mixed into
// a text table becomes a row in whatever parses it. An operator reading a
// terminal sees both streams; a pipeline sees only the data.
func modelstackNotePartialPage(cmd *cobra.Command, spec modelstackListSpec, page modelstackPage) {
	if !page.HasMore {
		return
	}
	switch {
	case spec.Paginated && page.Cursor != "":
		fmt.Fprintf(cmd.ErrOrStderr(),
			"note: more rows exist; continue with --cursor %s, or pass --all\n", page.Cursor)
	case spec.CapNote != "":
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %s\n", spec.CapNote)
	default:
		fmt.Fprintln(cmd.ErrOrStderr(), "note: the control plane reports more rows than this page carries")
	}
}

// --- single reads ------------------------------------------------------------

// modelstackGetSpec declares a verb that reads one document: a member of a
// collection, a singleton, or a generated report.
type modelstackGetSpec struct {
	Use, Short, Long, Example string
	Target                    modelstackTarget
	Filters                   []modelstackFilterSpec
	// Raw marks a route whose payload may not be JSON (an export, a card).
	Raw bool
}

func newModelstackGetCmd(c modelstackClient, spec modelstackGetSpec) *cobra.Command {
	filters := make([]string, len(spec.Filters))
	cmd := &cobra.Command{
		Use:     spec.Use,
		Short:   spec.Short,
		Long:    spec.Long,
		Example: spec.Example,
		Args:    cobra.ExactArgs(spec.Target.IDs),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := spec.Target.path(args)
			if err != nil {
				return err
			}
			res, err := c.do(cmd, http.MethodGet, path, modelstackValues(spec.Filters, filters).Encode(), nil)
			if err != nil {
				return err
			}
			if res.Status != http.StatusOK {
				return modelstackHTTPError(res)
			}
			if spec.Raw {
				return modelstackRenderPayload(cmd, res)
			}
			return modelstackRenderReport(cmd, res)
		},
	}
	addModelstackFilters(cmd, spec.Filters, filters)
	return cmd
}

// --- writes ------------------------------------------------------------------

// modelstackWriteSpec declares a POST or PUT verb.
type modelstackWriteSpec struct {
	Use, Short, Long, Example string
	Method                    string
	Target                    modelstackTarget
	Body                      modelstackBodyMode
	Filters                   []modelstackFilterSpec
}

func newModelstackWriteCmd(c modelstackClient, spec modelstackWriteSpec) *cobra.Command {
	var (
		data    string
		filters = make([]string, len(spec.Filters))
	)
	cmd := &cobra.Command{
		Use:     spec.Use,
		Short:   spec.Short,
		Long:    spec.Long,
		Example: spec.Example,
		Args:    cobra.ExactArgs(spec.Target.IDs),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := spec.Target.path(args)
			if err != nil {
				return err
			}
			body, err := modelstackReadBody(cmd, data, spec.Body)
			if err != nil {
				return err
			}
			res, err := c.do(cmd, spec.Method, path, modelstackValues(spec.Filters, filters).Encode(), body)
			if err != nil {
				return err
			}
			return modelstackRenderWriteResult(cmd, res)
		},
	}
	if spec.Body != modelstackBodyNone {
		addModelstackDataFlag(cmd, &data)
	}
	addModelstackFilters(cmd, spec.Filters, filters)
	return cmd
}

// modelstackRenderWriteResult renders whatever a successful write answered.
//
// The engine uses 200, 201 and 202 across these routes and a body-less 204 on a
// few; all four are success. A 204 still has to produce something a script can
// read, so it produces a small object the CLI owns rather than zero bytes: zero
// bytes and "the command did nothing" are indistinguishable.
func modelstackRenderWriteResult(cmd *cobra.Command, res modelstackResult) error {
	switch res.Status {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
		if len(bytes.TrimSpace(res.Raw)) == 0 {
			return modelstackRenderAcknowledged(cmd, res.Status)
		}
		return modelstackRenderPayload(cmd, res)
	case http.StatusNoContent:
		return modelstackRenderAcknowledged(cmd, res.Status)
	default:
		return modelstackHTTPError(res)
	}
}

func modelstackRenderAcknowledged(cmd *cobra.Command, status int) error {
	return renderOut(cmd, func(out io.Writer) error {
		_, err := fmt.Fprintf(out, "accepted (HTTP %d, no document returned)\n", status)
		return err
	}, map[string]any{"accepted": true, "status": status})
}

// --- deletes -----------------------------------------------------------------

// modelstackDeleteSpec declares a destructive verb.
type modelstackDeleteSpec struct {
	Use, Short, Long, Example string
	Target                    modelstackTarget
	// Noun names what is being destroyed, in the operator's words, for the
	// confirmation prompt: "routing policy", "model version".
	Noun string
	// Blast is the consequence the operator is being asked to check, appended to
	// the prompt. A prompt that names only the verb asks about a category; the
	// operator has to be asked about a target.
	Blast string
}

func newModelstackDeleteCmd(c modelstackClient, spec modelstackDeleteSpec) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     spec.Use,
		Aliases: modelstackDeleteAliases(spec.Use),
		Short:   spec.Short,
		Long:    spec.Long,
		Example: spec.Example,
		Args:    cobra.ExactArgs(spec.Target.IDs),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := spec.Target.path(args)
			if err != nil {
				return err
			}
			what := fmt.Sprintf("delete %s %s", spec.Noun, safeCLIValue(strings.Join(args, "/"), ""))
			if spec.Blast != "" {
				what += " (" + spec.Blast + ")"
			}
			if err := confirmDestructive(cmd, yes, what); err != nil {
				return err
			}
			res, err := c.do(cmd, http.MethodDelete, path, "", nil)
			if err != nil {
				return err
			}
			switch res.Status {
			case http.StatusNoContent, http.StatusOK:
				if len(bytes.TrimSpace(res.Raw)) > 0 && modelstackIsJSON(res.ContentType) {
					return modelstackRenderReport(cmd, res)
				}
				return renderOut(cmd, func(out io.Writer) error {
					_, err := fmt.Fprintf(out, "deleted %s %s\n", spec.Noun, safeCLIValue(strings.Join(args, "/"), ""))
					return err
				}, map[string]any{"deleted": true, "resource": spec.Noun, "id": args})
			default:
				return modelstackHTTPError(res)
			}
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

// modelstackDeleteAliases keeps the canonical name out of its own alias list —
// cobra would otherwise register a command that is its own alias.
func modelstackDeleteAliases(use string) []string {
	name := use
	if i := strings.IndexByte(use, ' '); i >= 0 {
		name = use[:i]
	}
	out := make([]string, 0, 2)
	for _, alias := range []string{"rm", "delete"} {
		if alias != name {
			out = append(out, alias)
		}
	}
	return out
}
