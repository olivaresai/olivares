// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// task_round7_test.go — Stage 4, review ROUND-7 regressions.
//
// Round 7 returned four blocking findings, and three of them (R7-02, R7-03,
// R7-05) were consequences of ONE round-6 mechanism: the retired-handoff FIFO
// cache. Round 7 does not patch them individually — it removes the cache and
// gates retirement on PROOF OF DELIVERY instead, so the code paths those findings
// described no longer exist. The tests below prove the elimination behaviorally
// rather than asserting it:
//
//	R7-01 the GetTaskResult validator was two-sided wrong for a third time
//	      (nullability, integer VALUES vs base-10 lexemes, `_meta`, and an ISO 8601
//	      profile that trimmed whitespace, took a bare date for a timestamp and
//	      refused reduced time precision);
//	R7-02 the handoff FIFO evicted UNREAD results at its bound and let a leased row
//	      escape that bound entirely (549 rows under a 512 cap);
//	R7-03 the cache was discharged by a response write whose failure was discarded;
//	R7-04 "no actionable rows" was being sold as a process-drain completion rule;
//	R7-05 a privileged status read could rewrite a supposedly final retired row;
//	R7-06 the stable validation-class taxonomy did not reach tasks/update, so an
//	      upstream property name reached the durable audit reason verbatim.

// --- R7-01: the GetTaskResult scalar and timestamp profile --------------------

// round7ProofBody builds a conforming GetTaskResult and applies member overrides
// (an empty override value DELETES the member).
func round7ProofBody(taskID string, overrides map[string]string) string {
	members := map[string]string{
		"resultType":    `"complete"`,
		"taskId":        `"` + taskID + `"`,
		"status":        `"cancelled"`,
		"createdAt":     `"2026-06-08T12:00:00Z"`,
		"lastUpdatedAt": `"2026-06-08T12:00:01Z"`,
		"ttlMs":         `null`,
	}
	for k, v := range overrides {
		if v == "" {
			delete(members, k)
			continue
		}
		members[k] = v
	}
	order := []string{
		"resultType", "taskId", "status", "createdAt", "lastUpdatedAt",
		"ttlMs", "pollIntervalMs", "statusMessage", "_meta",
	}
	parts := make([]string, 0, len(members))
	for _, k := range order {
		if v, ok := members[k]; ok {
			parts = append(parts, `"`+k+`":`+v)
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// TestRound7GetTaskResultScalarAndTimestampProfile is R7-01. It carries the
// reviewer's six probe cases plus the boundaries each of them implies, and it is
// deliberately a SINGLE table with both directions in it: every previous round
// fixed one side of this validator and broke the other.
func TestRound7GetTaskResultScalarAndTimestampProfile(t *testing.T) {
	for name, tc := range map[string]struct {
		overrides map[string]string
		accepted  bool
	}{
		// --- under-strict scenarios round-6 accepted -------------------------------
		// Only `ttlMs` is `number | null` (ext-tasks Task); `pollIntervalMs?: number`
		// is optional, never null.
		"pollIntervalMs null":     {map[string]string{"pollIntervalMs": "null"}, false},
		"pollIntervalMs absent":   {map[string]string{}, true},
		"pollIntervalMs integer":  {map[string]string{"pollIntervalMs": "2000"}, true},
		"ttlMs null is permitted": {map[string]string{"ttlMs": "null"}, true},
		// `_meta?: ResultMetaObject` — optional, and an OBJECT when present.
		"_meta null":   {map[string]string{"_meta": "null"}, false},
		"_meta scalar": {map[string]string{"_meta": `"nope"`}, false},
		"_meta object": {map[string]string{"_meta": `{"io.modelcontextprotocol/serverInfo":{"name":"s"}}`}, true},
		// The wire text is validated EXACTLY as received: round-6 trimmed first, so a
		// padded value no conforming server sends became valid proof.
		"timestamp padded with spaces": {map[string]string{"createdAt": `" 2026-06-08T12:00:00Z "`}, false},
		"timestamp with a leading tab": {map[string]string{"createdAt": "\"\\t2026-06-08T12:00:00Z\""}, false},
		// A bare DATE is an ISO 8601 date, not a date-time: it cannot order two
		// reports of one task, and round-6 called it a timestamp.
		"bare extended date": {map[string]string{"createdAt": `"2026-06-08"`}, false},
		"bare basic date":    {map[string]string{"lastUpdatedAt": `"20260608"`}, false},

		// --- over-strict scenarios round-6 refused ---------------------------------
		// The VALUE decides, not the base-10 lexeme: `1e3` and `1000.0` are the
		// integer millisecond count 1000.
		"ttlMs 1e3":           {map[string]string{"ttlMs": "1e3"}, true},
		"ttlMs 1000.0":        {map[string]string{"ttlMs": "1000.0"}, true},
		"ttlMs 1.0E+3":        {map[string]string{"ttlMs": "1.0E+3"}, true},
		"pollIntervalMs 5e2":  {map[string]string{"pollIntervalMs": "5e2"}, true},
		"ttlMs 5000e-3":       {map[string]string{"ttlMs": "5000e-3"}, true},
		"ttlMs minus zero":    {map[string]string{"ttlMs": "-0.0"}, true},
		"ttlMs zero":          {map[string]string{"ttlMs": "0"}, true},
		"ttlMs beyond int64":  {map[string]string{"ttlMs": "9223372036854775808"}, true},
		"ttlMs far beyond":    {map[string]string{"ttlMs": "1e40"}, true},
		"ttlMs huge exponent": {map[string]string{"ttlMs": "1e2000000000"}, true},
		"ttlMs int64 max":     {map[string]string{"ttlMs": "9223372036854775807"}, true},
		"reduced time precision": {map[string]string{
			"createdAt": `"2026-06-08T12:00Z"`, "lastUpdatedAt": `"2026-06-08T12:01Z"`}, true},
		"basic format survives": {map[string]string{
			"createdAt": `"20260608T120000Z"`, "lastUpdatedAt": `"2026-06-08T12:00:01+0200"`}, true},
		"basic reduced precision": {map[string]string{"createdAt": `"20260608T1200Z"`}, true},
		"fractional seconds":      {map[string]string{"createdAt": `"2026-06-08T12:00:00.123456Z"`}, true},
		"zoneless local time":     {map[string]string{"createdAt": `"2026-06-08T12:00:00"`}, true},

		// --- ROUND-8 R8-03, in the COMPLETE-RESULT table -----------------------------
		// (the matching helper tables are TestRound8AllZeroExponentIsExponentZero and
		// TestRound8TimestampProfileIsAnExplicitPredicate.)
		//
		// An all-zero exponent is the exponent ZERO: `1e-0` is a conforming RFC 8259
		// spelling of the integer 1, and round-7 saturated it to `1e-1073741824` and
		// called the value non-integral.
		"ttlMs 1e0":            {map[string]string{"ttlMs": "1e0"}, true},
		"ttlMs 1e+0":           {map[string]string{"ttlMs": "1e+0"}, true},
		"ttlMs 1e-0":           {map[string]string{"ttlMs": "1e-0"}, true},
		"pollIntervalMs 5e-0":  {map[string]string{"pollIntervalMs": "5e-0"}, true},
		"offset hour boundary": {map[string]string{"createdAt": `"2026-06-08T12:00:00+23:59"`}, true},
		"period fraction":      {map[string]string{"createdAt": `"2026-06-08T12:00:00.5Z"`}, true},
		// ...and the three spellings Go's layout parser accepted although this file's
		// own profile prose excludes them.
		"offset hour out of range":   {map[string]string{"createdAt": `"2026-06-08T12:00:00+24:00"`}, false},
		"offset minute out of range": {map[string]string{"createdAt": `"2026-06-08T12:00:00+23:60"`}, false},
		"comma fraction":             {map[string]string{"createdAt": `"2026-06-08T12:00:00,5Z"`}, false},

		// --- values that are not integers, or not non-negative ----------------------
		"ttlMs 1.5":                 {map[string]string{"ttlMs": "1.5"}, false},
		"ttlMs 5e-3":                {map[string]string{"ttlMs": "5e-3"}, false},
		"ttlMs negative":            {map[string]string{"ttlMs": "-5"}, false},
		"ttlMs negative exponent":   {map[string]string{"ttlMs": "-1e3"}, false},
		"pollIntervalMs negative":   {map[string]string{"pollIntervalMs": "-1"}, false},
		"pollIntervalMs fractional": {map[string]string{"pollIntervalMs": "0.5"}, false},
		"ttlMs is a string":         {map[string]string{"ttlMs": `"none"`}, false},
		"ttlMs absent":              {map[string]string{"ttlMs": ""}, false},

		// --- non-timestamps and calendar nonsense ----------------------------------
		"createdAt single letter": {map[string]string{"createdAt": `"t"`}, false},
		"createdAt prose":         {map[string]string{"createdAt": `"yesterday"`}, false},
		"createdAt empty":         {map[string]string{"createdAt": `""`}, false},
		"createdAt whitespace":    {map[string]string{"createdAt": `"  "`}, false},
		"impossible calendar day": {map[string]string{"createdAt": `"2026-02-30T12:00:00Z"`}, false},
		"hour out of range":       {map[string]string{"createdAt": `"2026-06-08T25:00:00Z"`}, false},
	} {
		body := round7ProofBody("task-r7", tc.overrides)
		rep, err := strictGetTaskResult("task-r7", json.RawMessage(body))
		if (err == nil) != tc.accepted {
			t.Errorf("R7-01 %s: strictGetTaskResult = (%q, %v), wantAccepted=%t; body=%s",
				name, rep.Status, err, tc.accepted, body)
			continue
		}
		if tc.accepted && rep.Status != taskStatusCanceled {
			t.Errorf("R7-01 %s: status = %q, want %q", name, rep.Status, taskStatusCanceled)
		}
	}
}

// TestRound7OpenExtensionAndMalformedProofRegressionsSurvive re-pins, against the
// round-7 validator, the two properties earlier rounds paid for: the base
// `Result`'s OPEN extension namespace is still open (R6-01), and the original
// R5-01 malformed proofs are still refused. The round-7 scalar/timestamp work
// must not have reopened either.
func TestRound7OpenExtensionAndMalformedProofRegressionsSurvive(t *testing.T) {
	stem := `"taskId":"task-r7","createdAt":"2026-06-08T12:00:00Z",` +
		`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null`
	for name, body := range map[string]string{
		"open Result extension member": `{"resultType":"complete",` + stem +
			`,"status":"cancelled","com.example/resultExtension":{"version":1}}`,
		"several extension members": `{"resultType":"complete",` + stem +
			`,"status":"cancelled","io.example/trace":"abc","io.example/attempts":3}`,
		"canceled carrying a result": `{"resultType":"complete",` + stem + `,"status":"cancelled","result":{}}`,
	} {
		if _, err := strictGetTaskResult("task-r7", json.RawMessage(body)); err != nil {
			t.Errorf("%s: a conforming open-Result body was refused: %v", name, err)
		}
	}
	for name, body := range map[string]string{
		"status only":         `{"status":"completed"}`,
		"no discriminator":    `{` + stem + `,"status":"completed","result":{}}`,
		"wrong discriminator": `{"resultType":"task",` + stem + `,"status":"completed","result":{}}`,
		"another task id": `{"resultType":"complete","taskId":"other","createdAt":"2026-06-08T12:00:00Z",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,"status":"completed","result":{}}`,
		"completed without result": `{"resultType":"complete",` + stem + `,"status":"completed"}`,
		"failed without error":     `{"resultType":"complete",` + stem + `,"status":"failed"}`,
		"unknown status":           `{"resultType":"complete",` + stem + `,"status":"vanished"}`,
		"case-variant alias":       `{"resultType":"complete",` + stem + `,"Status":"cancelled","status":"working"}`,
		"duplicate status":         `{"resultType":"complete",` + stem + `,"status":"working","status":"completed"}`,
		"absent body":              ``,
		"not an object":            `"completed"`,
	} {
		if _, err := strictGetTaskResult("task-r7", json.RawMessage(body)); err == nil {
			t.Errorf("%s: %s was accepted as authoritative terminal proof, want a refusal", name, body)
		}
	}
}

// TestRound7JSONNumberIntegralityIsValueBased pins the helper R7-01 turns on,
// including the cases that must NOT be materialized: `1e2000000000` denotes a
// finite integral non-negative value, and evaluating it (big.Rat/big.Float) would
// let an upstream body allocate gigabytes through the one code path the deletion
// rule calls proof.
func TestRound7JSONNumberIntegralityIsValueBased(t *testing.T) {
	for lexeme, want := range map[string]bool{
		"0": true, "-0": true, "-0.0": true, "1000": true, "1000.0": true, "1e3": true,
		"1.0E+3": true, "5000e-3": true, "9223372036854775808": true, "1e40": true,
		"1e2000000000": true, "0.0e10": true, "-0e5": true,
		"1.5": false, "5e-3": false, "-1": false, "-1e3": false, "0.1": false,
		"1e-2000000000": false,
		// ROUND-8 R8-03: an ALL-ZERO exponent is the exponent zero, in either sign.
		"1e0": true, "1e+0": true, "1e-0": true, "1e-00": true,
	} {
		if _, _, ok := jsonNumberNonNegativeInteger(lexeme); ok != want {
			t.Errorf("jsonNumberNonNegativeInteger(%q) ok = %t, want %t", lexeme, ok, want)
		}
	}
	// The decomposition is exact where it is used to STORE a value.
	for lexeme, want := range map[string]string{
		"1e3": "1", "1000": "1", "1000.0": "1", "5000e-3": "5",
	} {
		digits, _, ok := jsonNumberNonNegativeInteger(lexeme)
		if !ok || !strings.HasPrefix(digits, want) {
			t.Errorf("jsonNumberNonNegativeInteger(%q) digits = %q ok=%t, want a significand starting %q",
				lexeme, digits, ok, want)
		}
	}
	// ...and the HANDLE parser, which does store it, still refuses a TTL that would
	// wrap time.Duration (round-2 N-05) while accepting the same value spelled
	// exponentially.
	handle := func(ttl string) json.RawMessage {
		return json.RawMessage(`{"resultType":"task","taskId":"task-r7","status":"working","ttlMs":` + ttl + `}`)
	}
	task, ok, err := strictTaskFromResult(handle("6e4"))
	if !ok || err != nil || task.TTLMs == nil || *task.TTLMs != 60000 {
		t.Errorf("handle ttlMs 6e4 = %+v ok=%t err=%v, want the stored value 60000", task.TTLMs, ok, err)
	}
	for _, ttl := range []string{"9223372036854775807", "1e40", "0", "-1"} {
		if _, hok, herr := strictTaskFromResult(handle(ttl)); hok || herr == nil {
			t.Errorf("handle ttlMs %s = ok %t err %v, want a refusal", ttl, hok, herr)
		}
	}
}

// --- R7-02 / R7-03 / R7-05: the handoff cache is GONE, not bounded ------------

// mcpPackageSources reads every non-test Go source of this package.
func mcpPackageSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Clean(name))
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		out[name] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("no package sources found; the source-level guards below would pass vacuously")
	}
	return out
}

// TestRound7RetiredHandoffCacheIsRemovedFromTheSource is the structural half of
// the R7-02/R7-03/R7-05 elimination: the mechanism those findings lived in must
// be ABSENT, not merely unused. A future fix that reintroduces a parallel cache
// under any of these names fails here before it can regrow the same findings.
func TestRound7RetiredHandoffCacheIsRemovedFromTheSource(t *testing.T) {
	forbidden := []string{
		"retiredHandoffs",
		"maxRetiredHandoffs",
		"retiredHandoffCount",
		"enforceRetiredHandoffBound",
		"dropRetiredHandoff",
		"forgetRetired",
		"readableAfterRetirement",
		"RetiredAt",
	}
	for file, src := range mcpPackageSources(t) {
		for _, ident := range forbidden {
			if strings.Contains(src, ident) {
				t.Errorf("%s still contains %q: round-7 replaced the retired-handoff cache with "+
					"proof-of-delivery retirement, and a parallel cache reintroduces R7-02/R7-03/R7-05", file, ident)
			}
		}
	}
}

// TestRound7UncollectedTerminalRowsAreOrdinaryBoundedRows is the behavioral half
// of the R7-02 elimination, at the scale the reviewer's probe used.
//
// Round-6 retired each of these rows into a 512-entry FIFO, so the 513th
// retirement DELETED the oldest UNREAD terminal result purely for being oldest —
// its owner's next tasks/get then got 403 with no upstream call, which is R6-02
// again under FIFO pressure. Round-7 never retires them at all: each is an
// ordinary retained row, counted against the ordinary cap, and every one of the
// results is still there.
func TestRound7UncollectedTerminalRowsAreOrdinaryBoundedRows(t *testing.T) {
	ledger := newTaskLedger(1<<20, nil)
	const rows = 600
	generations := make([]string, rows)
	for i := 0; i < rows; i++ {
		id := fmt.Sprintf("task-r7-%05d", i)
		stored, err := ledger.insert(TaskRecord{
			TaskID: id, Tenant: "t", Issuer: rsIssuer, Subject: "agent:claude",
			Tool: "search", RequiredScope: "tools:read", Status: taskStatusWorking,
			// ROUND-8 R8-01 (fake-DATA correction, no assertion touched): "an owner that
			// has not collected its result" presupposes an owner that HOLDS the handle.
			// The retirement rule now reads that immutable provenance instead of
			// inferring it from current state, so the fixture has to state it.
			HandleRelayed: true,
		})
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		generations[i] = stored.Generation
		if _, ok := ledger.confirmStatus(id, stored.Generation, taskReport{
			Status: taskStatusCompleted, Digest: "report-" + id,
		}); !ok {
			t.Fatalf("confirm %s", id)
		}
		ok, why := ledger.retireReconciled(id, stored.Generation)
		if ok {
			t.Fatalf("R7-02: %s was retired although its owner never collected the result", id)
		}
		if !strings.Contains(why, "has not yet collected") {
			t.Fatalf("R7-02: refusal of %s = %q, want it to name the missing delivery", id, why)
		}
	}
	// NOTHING was evicted, and nothing is exempt from the bound: the rows are
	// ordinary retained rows under the ordinary cap.
	if got := ledgerSize(ledger); got != rows {
		t.Errorf("R7-02: ledger rows = %d, want all %d (no row may be dropped for being oldest)", got, rows)
	}
	if got := retainedLedgerSize(ledger); got != rows {
		t.Errorf("R7-02: bound-consuming rows = %d, want all %d (an uncollected result is not exempt)", got, rows)
	}
	for _, id := range []string{"task-r7-00000", fmt.Sprintf("task-r7-%05d", rows-1)} {
		if _, still := ledger.lookup(id); !still {
			t.Errorf("R7-02: %s disappeared; no bound may destroy an uncollected result", id)
		}
	}
	// Each row drains as soon as its owner collects — one at a time, in any order,
	// with no cache and no eviction anywhere.
	for i := 0; i < rows; i++ {
		id := fmt.Sprintf("task-r7-%05d", i)
		if !ledger.recordOwnerTerminalCollection(id, generations[i], "report-"+id) {
			t.Fatalf("record collection of %s", id)
		}
		if ok, why := ledger.retireReconciled(id, generations[i]); !ok {
			t.Fatalf("retire of the collected %s: %s", id, why)
		}
	}
	if got := ledgerSize(ledger); got != 0 {
		t.Errorf("R7-02: ledger rows after every owner collected = %d, want 0", got)
	}
}

// TestRound7ALeasedGenerationCannotEscapeAnyBound is R7-02's second half. Round-6
// removed a leased row's id from the FIFO and skipped its deletion, so the row
// stayed in `byID` outside BOTH the FIFO and the retained count for ever: the
// reviewer's probe reached 549 rows under a 512-entry bound.
//
// There is no second bound to escape now. A lease blocks the retirement itself,
// and the row stays fully counted while it is held — which is the only accounting
// a leased row can have when there is exactly one inventory.
func TestRound7ALeasedGenerationCannotEscapeAnyBound(t *testing.T) {
	ledger := newTaskLedger(1<<20, nil)
	stored, err := ledger.insert(TaskRecord{
		TaskID: "task-r7-leased", Tenant: "t", Issuer: rsIssuer, Subject: "agent:claude",
		Tool: "search", RequiredScope: "tools:read", Status: taskStatusWorking,
		HandleRelayed: true,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, ok := ledger.confirmStatus("task-r7-leased", stored.Generation, taskReport{
		Status: taskStatusCompleted, Digest: "report-leased",
	}); !ok {
		t.Fatal("confirm")
	}
	if !ledger.recordOwnerTerminalCollection("task-r7-leased", stored.Generation, "report-leased") {
		t.Fatal("record collection")
	}
	if err := ledger.acquireEffectLease("task-r7-leased", stored.Generation, taskEffectClientRead); err != nil {
		t.Fatalf("lease: %v", err)
	}
	ok, why := ledger.retireReconciled("task-r7-leased", stored.Generation)
	if ok || !strings.Contains(why, "in flight") {
		t.Errorf("retire of a leased generation = ok %t (%q), want a refusal naming the in-flight effect", ok, why)
	}
	if got := retainedLedgerSize(ledger); got != 1 {
		t.Errorf("R7-02: a leased row counts %d against the bound, want 1 (nothing may sit outside the count)", got)
	}
	ledger.releaseEffectLease(stored.Generation)
	if ok, why := ledger.retireReconciled("task-r7-leased", stored.Generation); !ok {
		t.Fatalf("retire after the lease ended: %s", why)
	}
	if got := ledgerSize(ledger); got != 0 {
		t.Errorf("ledger rows after the drain = %d, want 0", got)
	}
}

// failingResponseWriter fails every body write, exactly as a client that
// disconnected between the upstream round trip and the relay does.
type failingResponseWriter struct {
	hdr    http.Header
	status int
	writes int
}

func (f *failingResponseWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = http.Header{}
	}
	return f.hdr
}

func (f *failingResponseWriter) Write([]byte) (int, error) {
	f.writes++
	return 0, fmt.Errorf("mcp-test: the client went away")
}

func (f *failingResponseWriter) WriteHeader(code int) { f.status = code }

// TestRound7AFailedRelayRecordsNoDelivery is the R7-03 elimination. Round-6
// discarded the encoder error in `writeResult` and then called `forgetRetired`
// anyway, so a disconnected owner's record was destroyed on the strength of a
// write that never happened. Round-7 has no post-write forget at all; what the
// write result decides is only whether PROOF OF DELIVERY is recorded, and a
// failed write records none — so the row stays, stays counted, and retirement
// stays refused.
func TestRound7AFailedRelayRecordsNoDelivery(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			return json.RawMessage(conformingGetTaskResult("task-r7-write", taskStatusCompleted)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-r7-write")

	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	// The owner's read reaches the upstream and gets a conforming TERMINAL report —
	// and then the relay to the owner fails.
	fw := &failingResponseWriter{}
	rs.ServeHTTP(fw, taskReq(token, methodTasksGet, `{"taskId":"task-r7-write"}`))
	if fw.writes == 0 {
		t.Fatal("the handler never attempted to write a response; the scenario did not run")
	}
	rec, still := rs.taskLedger.lookup("task-r7-write")
	if !still {
		t.Fatal("R7-03: the record was destroyed although the terminal response never reached its owner")
	}
	if rec.ownerCollectedCurrentTerminalReport() {
		t.Error("R7-03: a FAILED write was recorded as a delivery")
	}
	if rec.retireReady() {
		t.Error("R7-03: a record whose owner never received the result must not be retire-ready")
	}
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusConflict {
		t.Errorf("retire after a failed relay = %d, want 409; body=%s", wr.Code, wr.Body.String())
	}
	// The owner comes back and collects, and only then does the row drain.
	wg := httptest.NewRecorder()
	rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"task-r7-write"}`))
	if wg.Code != http.StatusOK {
		t.Fatalf("the owner's retried read = %d, want 200; body=%s", wg.Code, wg.Body.String())
	}
	wr2 := httptest.NewRecorder()
	rs.ServeHTTP(wr2, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr2.Code != http.StatusOK {
		t.Errorf("retire after the successful relay = %d, want 200; body=%s", wr2.Code, wr2.Body.String())
	}
}

// TestRound7ReconciliationCannotActOnARetiredRow is the R7-05 elimination.
// Round-6 kept the retired row resolvable, `taskEffectReconcile` was admitted
// before the retired branch, and a later `working` report was applied through
// `confirmStatus` — rewriting a supposedly final governance decision and
// repopulating upstream-controlled StatusReason on a row nobody could cancel.
//
// A retired row no longer exists, so there is nothing to rewrite: target
// resolution refuses every reconciliation action against the retired generation,
// and no upstream call is made at all.
func TestRound7ReconciliationCannotActOnARetiredRow(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	working := false
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			if working {
				return json.RawMessage(conformingGetTaskResult("task-r7-reopen", taskStatusWorking)), nil
			}
			return json.RawMessage(conformingGetTaskResult("task-r7-reopen", taskStatusCanceled)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-r7-reopen")

	for _, seq := range []struct {
		method string
		want   int
	}{
		{methodTasksReconcileStatus, http.StatusOK},
		{methodTasksGet, http.StatusOK},
		{methodTasksReconcileRetire, http.StatusOK},
	} {
		w := httptest.NewRecorder()
		params := reconcileParams(stored, "")
		if seq.method == methodTasksGet {
			params = `{"taskId":"task-r7-reopen"}`
		}
		rs.ServeHTTP(w, taskReq(token, seq.method, params))
		if w.Code != seq.want {
			t.Fatalf("%s = %d, want %d; body=%s", seq.method, w.Code, seq.want, w.Body.String())
		}
	}
	working = true
	before := up.count(methodTasksGet)
	for _, method := range []string{
		methodTasksReconcileStatus, methodTasksReconcileClear, methodTasksReconcileRetire,
	} {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, taskReq(token, method, reconcileParams(stored, "")))
		if w.Code != http.StatusForbidden {
			t.Errorf("R7-05: %s against a retired generation = %d, want 403; body=%s", method, w.Code, w.Body.String())
		}
	}
	if got := up.count(methodTasksGet) - before; got != 0 {
		t.Errorf("R7-05: %d upstream reads were made for a retired generation, want 0", got)
	}
	if rec, still := rs.taskLedger.lookup("task-r7-reopen"); still {
		t.Errorf("R7-05: the retired row is still resolvable (%+v); a later report could rewrite it", rec)
	}
}

// --- R7-04: what the reconciliation surface may and may not claim -------------

// TestRound7ZeroActionableRowsIsNotAProcessDrain is R7-04, stated as the
// counterexample the round-6 wording admitted: three different ledger states
// produce a complete traversal with ZERO actionable rows while the ledger still
// holds task state a restart destroys.
//
// The surface therefore reports two DIFFERENT things, and the test pins the
// difference: `actionable` counts a human backlog, while `total`/`retained`
// (read after an EXTERNAL admission quiescence the gateway does not implement)
// are what a restart-safety decision needs.
func TestRound7ZeroActionableRowsIsNotAProcessDrain(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			return json.RawMessage(conformingGetTaskResult("task-r7-live", taskStatusWorking)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)

	// (a) a healthy WORKING task and (b) a registration whose settlement is still
	// pending: both are non-actionable, both are destroyed by a restart.
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-r7-live", Status: taskStatusWorking})
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-r7-pending", Status: taskStatusWorking, Pending: true})

	rows := listReconcileRows(t, rs, token, "")
	if rows.Total != 2 || rows.NewerThanSnapshot != 0 {
		t.Fatalf("inventory = total %d newer %d, want 2 / 0", rows.Total, rows.NewerThanSnapshot)
	}
	classes := map[string]bool{}
	for _, row := range rows.Records {
		classes[row.Class] = true
		if row.Actionable {
			t.Errorf("R7-04: row %+v is actionable; the scenario needs a backlog-free ledger", row)
		}
	}
	if !classes[taskReconcileRowLive] || !classes[taskReconcileRowPending] {
		t.Fatalf("row classes = %v, want both %q and %q", classes, taskReconcileRowLive, taskReconcileRowPending)
	}
	// THE finding: this traversal is complete, has no cursor left and reports no
	// human backlog — and the ledger is emphatically NOT drained. The two questions
	// have different answers, and only the second one is about restart safety.
	if rows.NextCursor != "" {
		t.Fatal("the scenario needs a single-page traversal")
	}
	if rows.Retained == 0 {
		t.Error("R7-04: `retained` must still report the rows a restart would destroy")
	}
	// (c) a proven-terminal result its owner has not collected is the third state
	// round-6 called non-actionable (`terminal-retired`). It is actionable now,
	// precisely so it cannot be mistaken for an empty backlog.
	stored := mustInsertTask(t, rs, TaskRecord{TaskID: "task-r7-uncollected", Status: taskStatusWorking})
	if _, ok := rs.taskLedger.confirmStatus("task-r7-uncollected", stored.Generation, taskReport{
		Status: taskStatusCompleted, Digest: "report-uncollected",
	}); !ok {
		t.Fatal("confirm")
	}
	after := listReconcileRows(t, rs, token, "")
	found := false
	for _, row := range after.Records {
		if row.TaskID != "task-r7-uncollected" {
			continue
		}
		found = true
		if row.Class != taskReconcileRowTerminalUncollected || !row.Actionable || row.Retirable {
			t.Errorf("R7-04: uncollected terminal row = %+v, want the actionable %q class with retirable=false",
				row, taskReconcileRowTerminalUncollected)
		}
	}
	if !found {
		t.Error("R7-04: the uncollected terminal row is missing from the inventory")
	}
	if after.Total != 3 || after.Retained < 3 {
		t.Errorf("R7-04: inventory = total %d retained %d, want 3 rows all counted", after.Total, after.Retained)
	}
}

// --- R7-06: the stable validation-class taxonomy reaches EVERY validator ------

// TestRound7TaskUpdateValidationAuditCarriesAClassNotUpstreamText is R7-06's
// concrete scenario: an upstream answers tasks/update with a property whose NAME
// is the secret. Round-6 closed this channel on the two tasks/get validators and
// left tasks/update returning bare `fmt.Errorf` values that the handler
// concatenated into the persisted audit reason.
func TestRound7TaskUpdateValidationAuditCarriesAClassNotUpstreamText(t *testing.T) {
	const secret = "ROUND7-UPDATE-SECRET-PROPERTY"
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksUpdate {
			return json.RawMessage(`{"resultType":"complete","` + secret + `":true}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	aud := &taskAuditor{}
	rs := newTaskEvidenceRS(t, jwks, up, aud, nil, nil, nil)
	mustInsertTask(t, rs, TaskRecord{TaskID: "task-r7-update"})

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksUpdate,
		`{"taskId":"task-r7-update","inputResponses":{"req-1":{"ok":true}}}`))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("malformed tasks/update ack = %d, want 502; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Errorf("R7-06: the response echoed the upstream property name: %s", w.Body.String())
	}
	classed := false
	for _, d := range aud.decisions {
		if strings.Contains(d.Reason, secret) {
			t.Errorf("R7-06: an audit reason carries the upstream property name: %q", d.Reason)
		}
		if strings.Contains(d.Reason, defectUpdateAckTaskState) {
			classed = true
		}
	}
	if !classed {
		t.Errorf("R7-06: no audit reason carries the stable class %q; reasons=%v",
			defectUpdateAckTaskState, auditReasons(aud))
	}
}

// round7UpstreamBodyValidators is the DECLARED inventory of this package's
// upstream-body validators. The test below re-derives a set from the SOURCE and
// requires the two to agree.
//
// ROUND-8 R8-05, narrowing a claim round-7 overstated: this is a CONVENTION CHECK,
// not mechanical coverage of every future validator. What it actually enforces is
// that a top-level function named `strict*` taking a `json.RawMessage` parameter is
// declared here, and that no audit call site passes `.Error()` of a value assigned
// (within three passes) from one of the declared producers. A validator written as
// a METHOD, or named `validateFoo`, or taking `[]byte` or a type alias, or reached
// through a wrapper, is NOT discovered; a leak spelled `fmt.Sprintf("%v", err)` is
// NOT detected. Nobody may read a green run here as "a new validator cannot be
// added silently" — it means "a new validator following this package's naming
// convention cannot be added silently". Complete coverage needs an explicit
// architectural registry or a linter, which is stage 7's item, not this test's.
var round7UpstreamBodyValidators = map[string]struct {
	// leak is the UPSTREAM-CONTROLLED text this probe forces into the failure
	// DETAIL. Naming it per validator keeps the class assertion from passing
	// vacuously against a validator whose detail happens to quote nothing.
	leak  string
	probe func() error
}{
	// The handle parser's leak surface is the reserved-alias rejector, whose text
	// quotes the offending upstream SPELLING verbatim.
	"strictTaskFromResult": {
		leak: "TaSkId",
		probe: func() error {
			_, _, err := strictTaskFromResult(json.RawMessage(
				`{"resultType":"task","TaSkId":"shadow","taskId":"t"}`))
			return err
		},
	},
	"strictGetTaskResult": {
		leak: "ROUND7-ENUMERATED-SECRET-PROPERTY",
		probe: func() error {
			_, err := strictGetTaskResult("t", json.RawMessage(
				`{"resultType":"complete","taskId":"t","status":"cancelled","createdAt":"2026-06-08T12:00:00Z",`+
					`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,`+
					`"ROUND7-ENUMERATED-SECRET-PROPERTY":1,"ROUND7-ENUMERATED-SECRET-PROPERTY":2}`))
			return err
		},
	},
	"strictTaskUpdateAck": {
		leak: "ROUND7-ENUMERATED-SECRET-PROPERTY",
		probe: func() error {
			return strictTaskUpdateAck(json.RawMessage(
				`{"resultType":"complete","ROUND7-ENUMERATED-SECRET-PROPERTY":true}`))
		},
	},
}

// TestRound7EveryUpstreamBodyValidatorIsClassified is the ENUMERATING regression
// R7-06 asks for, within the convention limits stated on
// `round7UpstreamBodyValidators` (round-8 R8-05). It does three things:
//
//  1. it discovers the package's CONVENTION-MATCHING upstream-body validators from
//     the source (a top-level `strict*` function taking a `json.RawMessage`) and
//     requires that set to equal the declared one — a validator that follows the
//     convention cannot be added without declaring it;
//  2. it drives each of them with an upstream-chosen property NAME and requires a
//     classified failure whose class does not contain that name;
//  3. it scans every audit call site in the package and refuses any that passes
//     `.Error()` of a value produced by one of those validators — the exact shape
//     of R6-05 and R7-06.
func TestRound7EveryUpstreamBodyValidatorIsClassified(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["mcp"]
	if !ok {
		t.Fatal("package mcp not found")
	}

	// (1) DISCOVERY.
	discovered := map[string]bool{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, isFn := decl.(*ast.FuncDecl)
			if !isFn || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "strict") {
				continue
			}
			for _, param := range fn.Type.Params.List {
				sel, isSel := param.Type.(*ast.SelectorExpr)
				if !isSel || sel.Sel.Name != "RawMessage" {
					continue
				}
				if ident, isIdent := sel.X.(*ast.Ident); isIdent && ident.Name == "json" {
					discovered[fn.Name.Name] = true
				}
			}
		}
	}
	for name := range discovered {
		if _, declared := round7UpstreamBodyValidators[name]; !declared {
			t.Errorf("R7-06: %s validates an upstream body but is NOT in round7UpstreamBodyValidators; "+
				"add it and its stable classes, or the next audit site leaks its parser text", name)
		}
	}
	for name := range round7UpstreamBodyValidators {
		if !discovered[name] {
			t.Errorf("R7-06: %s is declared as an upstream-body validator but no longer exists in the source", name)
		}
	}

	// (2) EVERY validator classifies, and no class carries upstream text.
	for name, v := range round7UpstreamBodyValidators {
		err := v.probe()
		if err == nil {
			t.Errorf("R7-06: %s accepted the malformed body its leak probe supplies", name)
			continue
		}
		if !strings.Contains(err.Error(), v.leak) {
			// Not a defect in production code — but it means the probe is not
			// exercising the leak path, so the class assertion below would be vacuous.
			t.Errorf("R7-06: the %s probe detail %q does not carry the upstream-controlled text %q; "+
				"the class assertion would pass vacuously", name, err.Error(), v.leak)
			continue
		}
		class := taskDefectClass(err)
		if class == taskDefectUnclassified {
			t.Errorf("R7-06: %s returned an UNCLASSIFIED error; its failures cannot be audited safely", name)
		}
		if strings.Contains(class, v.leak) {
			t.Errorf("R7-06: the stable class of %s carries upstream-controlled text: %q", name, class)
		}
	}

	// (3) NO audit site may persist a validator's parser text.
	auditFns := map[string]bool{
		"auditTraced": true, "auditTaskTraced": true, "auditTaskRecordTraced": true,
	}
	producers := map[string]bool{"syncTaskStatusFromResult": true}
	for name := range round7UpstreamBodyValidators {
		producers[name] = true
	}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, isFn := decl.(*ast.FuncDecl)
			if !isFn || fn.Body == nil {
				continue
			}
			tainted := map[string]bool{}
			// First pass: every identifier assigned from a validator (directly, or
			// from another tainted identifier) is tainted.
			for round := 0; round < 3; round++ {
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					assign, isAssign := n.(*ast.AssignStmt)
					if !isAssign {
						return true
					}
					from := false
					for _, rhs := range assign.Rhs {
						ast.Inspect(rhs, func(sub ast.Node) bool {
							switch e := sub.(type) {
							case *ast.CallExpr:
								switch callee := e.Fun.(type) {
								case *ast.Ident:
									from = from || producers[callee.Name]
								case *ast.SelectorExpr:
									from = from || producers[callee.Sel.Name]
								}
							case *ast.Ident:
								from = from || tainted[e.Name]
							}
							return true
						})
					}
					if !from {
						return true
					}
					for _, lhs := range assign.Lhs {
						if ident, isIdent := lhs.(*ast.Ident); isIdent {
							tainted[ident.Name] = true
						}
					}
					return true
				})
			}
			// Second pass: no audit call may contain `<tainted>.Error()`.
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				sel, isSel := call.Fun.(*ast.SelectorExpr)
				if !isSel || !auditFns[sel.Sel.Name] {
					return true
				}
				for _, arg := range call.Args {
					ast.Inspect(arg, func(sub ast.Node) bool {
						inner, isInner := sub.(*ast.CallExpr)
						if !isInner {
							return true
						}
						errSel, isErrSel := inner.Fun.(*ast.SelectorExpr)
						if !isErrSel || errSel.Sel.Name != "Error" {
							return true
						}
						recv, isIdent := errSel.X.(*ast.Ident)
						if isIdent && tainted[recv.Name] {
							t.Errorf("R6-05/R7-06: %s at %s persists %s.Error() — the parser text of an "+
								"upstream-body validator quotes upstream-controlled property names; audit the "+
								"stable class (taskDefectClass) instead",
								fn.Name.Name, fset.Position(inner.Pos()), recv.Name)
						}
						return true
					})
				}
				return true
			})
		}
	}
}
