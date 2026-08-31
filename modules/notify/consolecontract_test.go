// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// errConsoleDestDown is the permanent transport failure that drives a notification to
// the dead-letter queue in these tests.
var errConsoleDestDown = errors.New("destination down")

// THE CONSOLE'S OWN REQUEST, AGAINST THE REAL CONTRACT.
//
// THE TRAP THIS EXISTS FOR IS MEASURED, NOT HYPOTHETICAL. The sibling surface of this
// campaign — capabilities/tool-pinning — shipped a console whose two writes were a 400
// against the engine while its console cell was GREEN, because that cell asserted
// against a DOUBLE that accepts what production rejects. The engine-side proof of the
// real contract is modules/capabilities/toolpins_evidence_test.go:166-191; the console
// cell that stayed green next to it is capabilities.test.tsx:189-192.
//
// Two kinds of test each miss it, for different reasons:
//
//   - A console test mocks notifyApi (or `http`), so it asserts what the console MEANT
//     to send. A mock answers 200 to a request the engine would refuse.
//   - A Go test that RETYPES the path and body is a COPY of the console, and a copy is
//     exactly what drifts: retyping is how the console came to send {tool, from_drift}
//     while the engine demanded Idempotency-Key + expected_version.
//
// So this test reads the request out of the console's own source and issues THAT
// against the real router. If someone edits the client's path, or gives the bodyless
// POST a body, or the engine grows a precondition the console does not send, this goes
// red — and it is the only cell in the repository that can.
//
// It is deliberately fail-closed about its own instrument: if the console source cannot
// be read or parsed, the test FAILS rather than skips. "I could not look" is the third
// answer, and reporting it as the first is the most expensive defect in this repository.

// consoleAlertingClient is the hand-written typed client for notify, relative to this
// package. cmd/olivares/consoleroutes_test.go reads web/src the same way.
const (
	consoleAlertingClient = "../../web/src/features/alerting/api.ts"
	consoleAlertingView   = "../../web/src/features/alerting/alerting-view.tsx"
)

// consoleCall is one call site read out of the console's typed client: the HTTP verb,
// the path template exactly as written, and how many arguments the call passes. The
// argument COUNT is the load-bearing part for a bodyless route: http.post's second
// parameter is the body and it goes through JSON.stringify (web/src/lib/api/client.ts:139).
type consoleCall struct {
	verb     string
	template string
	args     int
}

// readConsoleFile reads a console source file or fails the test naming why.
func readConsoleFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the console source %s: %v\n"+
			"      a contract test that cannot READ the console has not measured it: that is "+
			"\"I could not look\", not \"it agrees\"", path, err)
	}
	return string(b)
}

// parseConsoleCall extracts the http.<verb>(...) call of one named method from the
// console's typed client. It scans the argument list with a real nesting counter rather
// than a regexp, because the arguments contain objects, generics and template literals
// and a regexp that mis-splits them would under-count the arguments — which is precisely
// the fact this test rests on.
func parseConsoleCall(t *testing.T, src, method string) consoleCall {
	t.Helper()
	at := strings.Index(src, "\n  "+method+": ")
	if at < 0 {
		t.Fatalf("the console client %s no longer defines %s — if it was renamed, rename it here too; "+
			"a contract test pointing at a method that does not exist measures nothing",
			consoleAlertingClient, method)
	}
	rest := src[at:]
	call := strings.Index(rest, "http.")
	// The http call must belong to THIS method. Without a bound, a method that stopped
	// calling http.* would silently borrow the NEXT method's call and the test would
	// assert the wrong route while staying green. `methodCallWindow` is generous enough
	// for the arrow-function header and any leading comment line, and far short of the
	// distance to another method's body.
	const methodCallWindow = 200
	if call < 0 || call > methodCallWindow {
		t.Fatalf("%s.%s does not call http.* within %d bytes of its declaration: if the console grew "+
			"a second fetch seam, this test is looking at the wrong one",
			consoleAlertingClient, method, methodCallWindow)
	}
	verbStart := call + len("http.")
	verbEnd := verbStart
	for verbEnd < len(rest) && rest[verbEnd] >= 'a' && rest[verbEnd] <= 'z' {
		verbEnd++
	}
	verb := strings.ToUpper(rest[verbStart:verbEnd])

	open := strings.IndexByte(rest[verbEnd:], '(')
	if open < 0 {
		t.Fatalf("%s.%s: could not find the argument list of its http call", consoleAlertingClient, method)
	}
	open += verbEnd
	args, ok := splitTopLevelArgs(rest[open+1:])
	if !ok {
		t.Fatalf("%s.%s: the argument list is unbalanced or unterminated — refusing to guess",
			consoleAlertingClient, method)
	}
	if len(args) == 0 {
		t.Fatalf("%s.%s: parsed ZERO arguments; the parser stopped discriminating and this "+
			"test would pass vacuously", consoleAlertingClient, method)
	}
	tmpl := strings.TrimSpace(args[0])
	if !strings.HasPrefix(tmpl, "`") || !strings.HasSuffix(tmpl, "`") {
		t.Fatalf("%s.%s: first argument %q is not a template literal; this test resolves the "+
			"path from the console and cannot resolve that form", consoleAlertingClient, method, tmpl)
	}
	return consoleCall{verb: verb, template: tmpl[1 : len(tmpl)-1], args: len(args)}
}

// splitTopLevelArgs splits an argument list at commas that are not nested inside
// (), [], {}, a template literal or a quoted string. It returns ok=false if the list
// never closes — an unreadable list must not read as a short one.
func splitTopLevelArgs(s string) ([]string, bool) {
	var args []string
	var cur strings.Builder
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			cur.WriteByte(c)
		case '(', '[', '{':
			depth++
			cur.WriteByte(c)
		case ')':
			if depth == 0 {
				if last := strings.TrimSpace(cur.String()); last != "" {
					args = append(args, last)
				}
				return args, true
			}
			depth--
			cur.WriteByte(c)
		case ']', '}':
			depth--
			cur.WriteByte(c)
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(cur.String()))
				cur.Reset()
				continue
			}
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	return nil, false
}

// pathParamRe is the ONLY interpolation this resolver accepts for a path parameter:
// ${encodeURIComponent(<identifier>)}. Nothing else, deliberately — see consolePath.
var pathParamRe = regexp.MustCompile(`^\$\{encodeURIComponent\([A-Za-z_$][\w$]*\)\}$`)

// consolePath resolves a template read from the client into a concrete path: ${BASE} is
// substituted from the client's own const, and the single remaining interpolation is the
// path parameter.
//
// ⚠ THIS DOES NOT EVALUATE JAVASCRIPT, AND THAT GAP WAS A DEMONSTRATED FALSE GREEN.
// The first version replaced any `${…}` with the test's id without looking inside it. The
// Contrast probed it by editing the console to
// `${encodeURIComponent(id + '-wrong')}` — every real browser request would then target
// the wrong row, and this test stayed green, because it substituted its own id over an
// expression it never read. A witness that rewrites the thing it is auditing is not a
// witness.
//
// The fix is deny-closed rather than an interpreter: exactly one expression form is
// recognized, and ANY other shape fails loudly instead of being silently normalised. A
// new legitimate form costs one edit here, and that price is the point — the alternative
// is a resolver that quietly agrees with whatever it is given.
func consolePath(t *testing.T, src, template, id string) string {
	t.Helper()
	base := consoleBase(t, src)
	p := strings.ReplaceAll(template, "${BASE}", base)
	start := strings.Index(p, "${")
	if start < 0 {
		return p
	}
	end := strings.Index(p[start:], "}")
	if end < 0 {
		t.Fatalf("unterminated interpolation in the console path %q", template)
	}
	expr := p[start : start+end+1]
	if !pathParamRe.MatchString(expr) {
		t.Fatalf("the console builds its path parameter with %s, which this witness does not "+
			"recognize (it accepts only ${encodeURIComponent(<identifier>)}).\n"+
			"      It will NOT substitute an expression it cannot read: doing that is how this "+
			"test once passed while the console addressed the wrong row.\n"+
			"      Either the console should encode its parameter plainly, or teach pathParamRe "+
			"the new form — deliberately, not by accident.\n"+
			"      full template: %q", expr, template)
	}
	p = p[:start] + id + p[start+end+1:]
	if strings.Contains(p, "${") {
		t.Fatalf("the console path %q carries more than one parameter; this resolver substitutes "+
			"exactly one and will not guess the rest", template)
	}
	return p
}

// consoleComponentBody returns the source of ONE top-level React component, from its
// `function <name>(` to the start of the next top-level declaration. Without it, a check
// for a literal inside a component is really a check for that literal anywhere in a
// 1300-line file — which is how the first version of the permission guard here stayed
// green under a mutant that re-gated the requeue on the wrong permission.
func consoleComponentBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "\nfunction "+name+"(")
	if start < 0 {
		t.Fatalf("%s no longer declares a top-level `function %s(`: this guard now looks at "+
			"nothing, which reads exactly like looking at something clean",
			consoleAlertingView, name)
	}
	rest := src[start+1:]
	if end := strings.Index(rest, "\nfunction "); end >= 0 {
		rest = rest[:end]
	}
	if len(rest) < 32 {
		t.Fatalf("the body of %s parsed to %d bytes: the extractor stopped discriminating",
			name, len(rest))
	}
	return rest
}

func consoleBase(t *testing.T, src string) string {
	t.Helper()
	const marker = "\nconst BASE = '"
	at := strings.Index(src, marker)
	if at < 0 {
		t.Fatalf("%s no longer declares `const BASE = '…'`", consoleAlertingClient)
	}
	rest := src[at+len(marker):]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		t.Fatalf("%s: unterminated BASE constant", consoleAlertingClient)
	}
	return rest[:end]
}

// TestConsoleDLQRequestsMatchTheEngineContract issues the console's OWN list and requeue
// requests — path and body taken from web/src/features/alerting/api.ts — against the
// real router, and asserts the engine accepts exactly what the console sends.
func TestConsoleDLQRequestsMatchTheEngineContract(t *testing.T) {
	src := readConsoleFile(t, consoleAlertingClient)
	list := parseConsoleCall(t, src, "listOutbox")
	requeue := parseConsoleCall(t, src, "redeliverOutbox")

	if list.verb != http.MethodGet {
		t.Errorf("the console lists the outbox with %s; the engine mounts GET (api.go:46)", list.verb)
	}
	if requeue.verb != http.MethodPost {
		t.Errorf("the console requeues with %s; the engine mounts POST (api.go:47)", requeue.verb)
	}

	// THE DOUBLE-ENCODING GUARD, and it is about a body that must NOT exist.
	// handleRedeliverOutbox reads the id from the path and decodes no body
	// (outbox_api.go:93-98). http.post's second argument is the body and it is passed
	// through JSON.stringify (web/src/lib/api/client.ts:139), so a string body would
	// arrive re-quoted and an object body would be a payload the engine never reads.
	// One argument means: no body, and no Content-Type at all.
	if requeue.args != 1 {
		t.Errorf("the console passes %d arguments to http.post for the requeue; the engine reads "+
			"NO body (outbox_api.go:93-98), so the call must pass the path alone. A second argument "+
			"is a body the engine ignores — and if it is a string, client.ts:139 double-encodes it",
			requeue.args)
	}

	// The console gates the requeue button on a permission; it must be the one the
	// ENGINE enforces for that route. The oracle here is the engine's own constant, not
	// a string retyped in this file.
	//
	// ⚠ SCOPED TO THE DLQ COMPONENT, and the reason is a mutant that SURVIVED the first
	// version of this check. Written against the whole file, `strings.Contains(view,
	// "can('notify:route:admin')")` was satisfied by RoutesTab, which holds that exact
	// call for the route delete/test buttons — so re-gating the requeue on
	// notify:route:write left the guard green. A literal that another part of the file
	// already satisfies is not a guard on THIS part; it is a guard on the file existing.
	dlq := consoleComponentBody(t, readConsoleFile(t, consoleAlertingView), "DeadLettersTab")
	if want := "can('" + string(permRouteAdmin) + "')"; !strings.Contains(dlq, want) {
		t.Errorf("DeadLettersTab does not gate the requeue on %s (looked for %q in %s); the engine "+
			"requires it at api.go:47, so a console gating on anything else either hides the action "+
			"from an admin or offers it to someone the engine will 403",
			permRouteAdmin, want, consoleAlertingView)
	}
	if want := "can('" + string(permDeliveryRead) + "')"; !strings.Contains(dlq, want) {
		t.Errorf("DeadLettersTab does not gate the outbox list on %s (looked for %q in %s); the engine "+
			"requires it at api.go:46", permDeliveryRead, want, consoleAlertingView)
	}

	// --- and now the requests themselves, against the real engine ---------------
	disp := &flakyDispatcher{dest: "d1", permanent: true, err: errConsoleDestDown}
	h := tinyRetryHarness(t, disp)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")
	h.mustCreateRoute(adminTok, tenant, map[string]any{
		"name": "sec", "destination": "d1", "match_kinds": []string{"security_*"},
	})

	// Drive one notification to the dead-letter queue.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	for i := 0; i < 5; i++ {
		h.clk.advance(100 * time.Millisecond)
		h.pumpOutbox(tenant)
	}
	// THE CONTROL ROW, and it is what makes ?status=dead load-bearing. A second
	// notification that is still QUEUED (one failed attempt, retries not exhausted) has
	// to be EXCLUDED from the DLQ view. Without it the fixture held a single dead row,
	// so "filtered" and "unfiltered" gave the same answer — the contrast proved it
	// by deleting the engine's status predicate (outbox_api.go:59-61) and watching this
	// test, and the whole notify suite, stay green.
	h.publishFinding(tenant, securitySource, finding("security_alive", sdkmodel.SeverityHigh, "agent", "a2", "still owed"))

	// The UNFILTERED list is the control: it proves the second row exists, so a fixture
	// that silently failed to create it cannot make the filtered assertion pass by
	// having nothing to exclude.
	basePath := consolePath(t, src, list.template, "")
	if all := h.do(list.verb, basePath, adminTok, nil, tenantHdr(tenant)); all.code != http.StatusOK {
		t.Fatalf("unfiltered outbox list %s = %d %s", basePath, all.code, all.raw)
	} else if got, _ := all.body["items"].([]any); len(got) != 2 {
		t.Fatalf("control: the unfiltered outbox must hold 2 rows (one dead, one queued), got %d: %s",
			len(got), all.raw)
	}

	// The console's LIST request, with the filter the DLQ tab opens on.
	listPath := basePath + "?status=dead"
	r := h.do(list.verb, listPath, adminTok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("the console's outbox list %s = %d %s", listPath, r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("the console's DLQ list returned %d rows, want exactly 1 — the queued row must be "+
			"EXCLUDED, or the DLQ shows notifications the engine has not given up on: %s",
			len(items), r.raw)
	}
	row, _ := items[0].(map[string]any)
	id, _ := row["id"].(string)
	if id == "" {
		t.Fatalf("the dead-letter row the console renders has no id: %v", row)
	}
	// EVERY field the console's NotifyOutboxEntry declares non-optional must be present:
	// a DTO that mirrors the Go struct is only a mirror if the engine actually sends it.
	for _, k := range []string{"id", "status", "attempts", "destination", "event_type", "finding_kind", "occurred_at"} {
		if _, ok := row[k]; !ok {
			t.Errorf("the engine's outbox row has no %q, but the console's NotifyOutboxEntry "+
				"declares it non-optional: %v", k, row)
		}
	}
	if row["status"] != obStatusDead {
		t.Fatalf("?status=dead returned a row with status %v", row["status"])
	}

	// The console's REQUEUE request: the path it builds, and NO BODY — the shape a
	// console that had copied tool-pinning's recipe would have got wrong.
	requeuePath := consolePath(t, src, requeue.template, id)
	r = h.do(requeue.verb, requeuePath, adminTok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("the console's requeue %s %s = %d %s\n"+
			"      this is the tool-pinning failure repeating: the console sends what it sends and "+
			"the engine refuses it", requeue.verb, requeuePath, r.code, r.raw)
	}
	// 200 means QUEUED, not delivered. The console's success toast is written against
	// this exact value (alerting-view.tsx, successMessage), so if the engine ever
	// answers something else the console must not keep claiming "requeued".
	if r.body["status"] != obStatusQueued {
		t.Errorf("requeue answered status=%v, want %q — the console reports this verbatim",
			r.body["status"], obStatusQueued)
	}
}

// TestConsoleRequeueBoundariesAreTheThirdAnswer covers the two outcomes that are neither
// "delivered" nor "nothing happened", because the console renders a DIFFERENT sentence
// for each and reads them BY STATUS: an editor is refused (403) and an in-flight row is
// refused (409). If either code changed, the console would render the wrong sentence
// while still looking like it handled the case.
func TestConsoleRequeueBoundariesAreTheThirdAnswer(t *testing.T) {
	src := readConsoleFile(t, consoleAlertingClient)
	requeue := parseConsoleCall(t, src, "redeliverOutbox")

	disp := &flakyDispatcher{dest: "d1", permanent: true, err: errConsoleDestDown}
	h := tinyRetryHarness(t, disp)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")
	editorTok := h.roleToken(admin, tenant, "e@x.io", "editor")
	h.mustCreateRoute(adminTok, tenant, map[string]any{
		"name": "sec", "destination": "d1", "match_kinds": []string{"security_*"},
	})

	// One failed attempt only: the row is still QUEUED, i.e. in flight. Asserted, not
	// assumed — a test whose precondition silently drifted to `dead` would get its 409
	// from nowhere and pass for the wrong reason.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	rows := h.outboxRows(tenant)
	if len(rows) != 1 || rows[0].String(colObStatus) != obStatusQueued {
		t.Fatalf("precondition: want exactly one QUEUED outbox row, got %v", rows)
	}
	id := rows[0].String(model.ColID)

	path := consolePath(t, src, requeue.template, id)

	// 403: the editor holds notify:route:write but the engine requires route:admin
	// (api.go:47). This is what the console's can('notify:route:admin') gate mirrors.
	if r := h.do(requeue.verb, path, editorTok, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("requeue as editor = %d, want 403: the console hides the button behind %s and "+
			"relies on the engine to be the authority; %s", r.code, permRouteAdmin, r.raw)
	}

	// 409: in flight. The console does not offer the action for a queued row, and this
	// is the race between that render and the click.
	if r := h.do(requeue.verb, path, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("requeue of an in-flight row = %d, want 409 (outbox_api.go:137-139): the console "+
			"renders \"already in flight\" for this code and would say the wrong thing; %s", r.code, r.raw)
	}

	// 404: a row that is not in this tenant's outbox. The console renders a third,
	// different sentence for it.
	gone := consolePath(t, src, requeue.template, "01890000-0000-7000-8000-000000000000")
	if r := h.do(requeue.verb, gone, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Errorf("requeue of an absent row = %d, want 404 (outbox_api.go:131-133); %s", r.code, r.raw)
	}
}
