// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// task_round5_test.go — Stage 4, review ROUND-5 regressions.
//
// Round 5 reviewed the recovery surface itself as an attack surface and found
// one P0 and four P1s. Each is pinned here:
//
//	R5-01 (P0) a malformed tasks/get body was accepted as terminal PROOF, which
//	           made a live record retirable — and `retire` DELETED it;
//	R5-02      the retained-inventory bound was a racy SNAPSHOT: N concurrent
//	           callers all passed it (the reviewer's probe reached 12 under a
//	           per-owner cap of 2);
//	R5-03      the inventory was incomplete (proven-terminal records consumed the
//	           bound invisibly), unpageable past 512 rows, and had no instance
//	           identity;
//	R5-04      the status action returned the ENTIRE raw upstream body — tool
//	           output, errors and pending input requests — to a tenant-wide
//	           metadata scope;
//	R5-05      the synthesized tasks/get was not a conforming Tasks-extension
//	           request (no per-request capability declaration) and the production
//	           forwarder emitted no MCP routing headers, so a strict upstream
//	           refuses the read and the record can never be drained.

// conformingGetTaskResult renders a COMPLETE, conforming SEP-2663
// `GetTaskResult` (= `Result` & `DetailedTask`): the mandatory `complete`
// discriminator, the task identity, the mandatory `createdAt`/`lastUpdatedAt`/
// `ttlMs` Task fields and the status-specific payload.
//
// It exists because round-4's fakes answered abbreviated bodies like
// `{"resultType":"complete","taskId":"t","status":"canceled"}` — which round-5
// R5-01 showed the gateway was accepting as authoritative proof of termination.
// Correcting a fake to the shape the extension actually mandates is a
// fake-IMPLEMENTATION fix; every assertion around these fakes is untouched.
func conformingGetTaskResult(taskID, status string) string {
	return conformingGetTaskResultTTL(taskID, status, "null")
}

// conformingGetTaskResultTTL is the same body with an explicit `ttlMs` LEXEME.
//
// ROUND-8 R8-04 (fake-DATA correction): `Task.ttlMs` is measured from creation and
// MAY change over a task's lifetime, so the gateway now APPLIES the value a
// conforming report carries instead of validating it and throwing it away. That
// gives `"ttlMs":null` — the default above — its true meaning, "this task never
// expires", which contradicts the premise of any test whose scenario is a record
// its own TTL forgets. Such a test states the TTL its scenario needs; no assertion
// is relaxed by it.
func conformingGetTaskResultTTL(taskID, status, ttl string) string {
	payload := ""
	switch status {
	case taskStatusCompleted:
		payload = `,"result":{"content":[]}`
	case taskStatusFailed:
		payload = `,"error":{"code":-32000,"message":"upstream execution failed"}`
	case taskStatusInputRequired:
		payload = `,"inputRequests":{"req-1":{"method":"elicitation/create"}}`
	}
	return fmt.Sprintf(
		`{"resultType":"complete","taskId":%q,"status":%q,"createdAt":"2026-06-08T12:00:00Z",`+
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":%s%s}`,
		taskID, status, ttl, payload)
}

// getTaskResultStem is the mandatory head of a `GetTaskResult` for `task-proof`,
// with NORMATIVE `Task` timestamps. Round-5 wrote `"createdAt":"t"` in its
// negative cases; once round-6 R6-01 started validating the timestamps for real,
// that fake data would have made every one of those cases fail for the WRONG
// reason and stop discriminating anything. Correcting a fake to the shape the
// extension mandates is a fake-data fix — no assertion here is relaxed.
const getTaskResultStem = `"taskId":"task-proof","createdAt":"2026-06-08T12:00:00Z",` +
	`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null`

// TestGetTaskResultProofIsCompleteOrRefused is the R5-01 unit pin, corrected by
// round-6 R6-01: the ONE parser in front of `confirmStatus` must accept every
// CONFORMING `GetTaskResult` — including the open `Result` extensions the schema
// deliberately permits — and refuse every partial, mistargeted or ambiguous body,
// because the status it confirms is what clears the cancellation ambiguity and
// what makes a record `retirable()`.
//
// Round-5 closed the open extension namespace and additionally forbade the other
// statuses' payload members, so a conforming server was answered 502 and its
// record could not be drained. `GetTaskResult = Result & DetailedTask` and the
// base `Result` carries `[key: string]: unknown`; the accepted set below is that
// rule, and the refused set is what the gateway actually consumes.
func TestGetTaskResultProofIsCompleteOrRefused(t *testing.T) {
	const id = "task-proof"
	for _, status := range []string{
		taskStatusWorking, taskStatusInputRequired, taskStatusCompleted,
		taskStatusFailed, taskStatusCanceled,
	} {
		rep, err := strictGetTaskResult(id, json.RawMessage(conformingGetTaskResult(id, status)))
		if err != nil || rep.Status != status {
			t.Errorf("conforming %q GetTaskResult = (%q, %v), want (%q, nil)", status, rep.Status, err, status)
		}
	}
	// R6-01: conforming bodies the round-5 validator REFUSED. Every one of them
	// satisfies `Result & DetailedTask`; refusing them made a drainable record
	// undrainable against a conforming server.
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"open Result extension member": {
			`{"resultType":"complete",` + getTaskResultStem + `,"status":"cancelled",` +
				`"com.example/resultExtension":{"version":1}}`, taskStatusCanceled},
		"several extension members": {
			`{"resultType":"complete",` + getTaskResultStem + `,"status":"working",` +
				`"io.example/trace":"abc","io.example/attempts":3}`, taskStatusWorking},
		"_meta object": {
			`{"resultType":"complete",` + getTaskResultStem + `,"status":"cancelled",` +
				`"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"srv"}}}`, taskStatusCanceled},
		"canceled carrying a result": {
			`{"resultType":"complete",` + getTaskResultStem + `,"status":"cancelled","result":{}}`, taskStatusCanceled},
		"completed carrying an error": {
			`{"resultType":"complete",` + getTaskResultStem + `,"status":"completed","result":{},` +
				`"error":{"code":-1,"message":"x"}}`, taskStatusCompleted},
		"requestState on a non-input_required task": {
			`{"resultType":"complete",` + getTaskResultStem + `,"status":"working","requestState":"x"}`, taskStatusWorking},
		"zero ttlMs and pollIntervalMs": {
			`{"resultType":"complete","taskId":"task-proof","createdAt":"2026-06-08T12:00:00Z",` +
				`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":0,"pollIntervalMs":0,"status":"working"}`, taskStatusWorking},
		"ttlMs beyond the local duration range": {
			`{"resultType":"complete","taskId":"task-proof","createdAt":"2026-06-08T12:00:00Z",` +
				`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":9223372036854775807,"status":"working"}`, taskStatusWorking},
		"basic-format ISO 8601 timestamps": {
			`{"resultType":"complete","taskId":"task-proof","createdAt":"20260608T120000Z",` +
				`"lastUpdatedAt":"2026-06-08T12:00:01+0200","ttlMs":null,"status":"cancelled"}`, taskStatusCanceled},
	} {
		rep, err := strictGetTaskResult(id, json.RawMessage(tc.body))
		if err != nil || rep.Status != tc.want {
			t.Errorf("R6-01 %s: strictGetTaskResult = (%q, %v), want (%q, nil); body=%s", name, rep.Status, err, tc.want, tc.body)
		}
	}
	for name, body := range map[string]string{
		// The reviewer's exact round-5 P0 body.
		"status only":         `{"status":"completed"}`,
		"no discriminator":    `{` + getTaskResultStem + `,"status":"completed","result":{}}`,
		"wrong discriminator": `{"resultType":"task",` + getTaskResultStem + `,"status":"completed","result":{}}`,
		"no task id": `{"resultType":"complete","createdAt":"2026-06-08T12:00:00Z",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,"status":"completed","result":{}}`,
		"another task id": `{"resultType":"complete","taskId":"other","createdAt":"2026-06-08T12:00:00Z",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,"status":"completed","result":{}}`,
		"no createdAt": `{"resultType":"complete","taskId":"task-proof",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,"status":"completed","result":{}}`,
		"empty createdAt": `{"resultType":"complete","taskId":"task-proof","createdAt":"  ",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,"status":"completed","result":{}}`,
		// R6-01: round-5 only checked non-emptiness, so a single character satisfied a
		// mandatory Task identity field of the body the deletion rule calls PROOF.
		"createdAt is not a timestamp": `{"resultType":"complete","taskId":"task-proof","createdAt":"t",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,"status":"completed","result":{}}`,
		"lastUpdatedAt is not a timestamp": `{"resultType":"complete","taskId":"task-proof",` +
			`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"yesterday","ttlMs":null,"status":"completed","result":{}}`,
		"createdAt is not a string": `{"resultType":"complete","taskId":"task-proof","createdAt":1749384000,` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,"status":"completed","result":{}}`,
		"no lastUpdatedAt": `{"resultType":"complete","taskId":"task-proof","createdAt":"2026-06-08T12:00:00Z",` +
			`"ttlMs":null,"status":"completed","result":{}}`,
		"no ttlMs": `{"resultType":"complete","taskId":"task-proof","createdAt":"2026-06-08T12:00:00Z",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","status":"completed","result":{}}`,
		"negative ttlMs": `{"resultType":"complete","taskId":"task-proof","createdAt":"2026-06-08T12:00:00Z",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":-5,"status":"completed","result":{}}`,
		"non-numeric ttlMs": `{"resultType":"complete","taskId":"task-proof","createdAt":"2026-06-08T12:00:00Z",` +
			`"lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":"none","status":"completed","result":{}}`,
		"negative pollIntervalMs": `{"resultType":"complete",` + getTaskResultStem +
			`,"pollIntervalMs":-1,"status":"working"}`,
		// R6-01: `Result._meta` is a ResultMetaObject; round-5 permitted the member and
		// then never checked its type.
		"_meta is not an object":      `{"resultType":"complete",` + getTaskResultStem + `,"status":"cancelled","_meta":"nope"}`,
		"completed without result":    `{"resultType":"complete",` + getTaskResultStem + `,"status":"completed"}`,
		"completed result not object": `{"resultType":"complete",` + getTaskResultStem + `,"status":"completed","result":"done"}`,
		"failed without error":        `{"resultType":"complete",` + getTaskResultStem + `,"status":"failed"}`,
		"input_required without reqs": `{"resultType":"complete",` + getTaskResultStem + `,"status":"input_required"}`,
		"unknown status":              `{"resultType":"complete",` + getTaskResultStem + `,"status":"vanished"}`,
		"status is not a string":      `{"resultType":"complete",` + getTaskResultStem + `,"status":7}`,
		"no status":                   `{"resultType":"complete",` + getTaskResultStem + `}`,
		// The open namespace admits NEW names, never a second spelling of a name this
		// gateway resolves by exact case.
		"case-variant alias": `{"resultType":"complete",` + getTaskResultStem +
			`,"Status":"cancelled","status":"working"}`,
		"duplicate status": `{"resultType":"complete",` + getTaskResultStem +
			`,"status":"working","status":"completed","result":{}}`,
		"absent body":   ``,
		"not an object": `"completed"`,
	} {
		if _, err := strictGetTaskResult(id, json.RawMessage(body)); err == nil {
			t.Errorf("%s: %s was ACCEPTED as authoritative terminal proof, want a refusal", name, body)
		}
	}
}

// TestReconcileStatusRefusesMalformedProofAndRetainsTheRecord is R5-01 end to
// end: the configured upstream answers the reconciliation read with the
// reviewer's body. Round-4 marked the status confirmed, made the record retirable
// and the very next `retire` DELETED a live record. The read must now refuse, the
// record must stay, and the retirement must stay refused.
func TestReconcileStatusRefusesMalformedProofAndRetainsTheRecord(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	conforming := false
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			if conforming {
				return json.RawMessage(conformingGetTaskResult("task-bad-proof", taskStatusCanceled)), nil
			}
			// The exact round-5 P0 body: method-correlated, schema-invalid.
			return json.RawMessage(`{"status":"completed"}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-bad-proof")

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("R5-01: reconciliation status on a malformed proof = %d, want 502; body=%s", w.Code, w.Body.String())
	}
	rec, ok := rs.taskLedger.lookup("task-bad-proof")
	if !ok {
		t.Fatal("R5-01: an invalid upstream report must never remove the record")
	}
	if rec.retirable() {
		t.Errorf("R5-01: record after a malformed report = %+v, want it NOT retirable", rec)
	}
	if !rec.CancelUnconfirmed {
		t.Error("R5-01: a malformed report must not clear the cancellation ambiguity")
	}
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(stored, "")))
	if wr.Code != http.StatusConflict {
		t.Errorf("R5-01: retire after a malformed report = %d, want 409; body=%s", wr.Code, wr.Body.String())
	}
	if _, still := rs.taskLedger.lookup("task-bad-proof"); !still {
		t.Fatal("R5-01: the live record was DELETED on the strength of an invalid proof")
	}
	// The same record drains normally once the upstream answers conformingly.
	conforming = true
	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status on a conforming report = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	if rec, _ := rs.taskLedger.lookup("task-bad-proof"); !rec.retirable() {
		t.Errorf("record after a CONFORMING terminal report = %+v, want it retirable", rec)
	}
}

// TestRetainedInventoryBoundHoldsUnderConcurrentAdmission is R5-02: the bound is
// an atomic RESERVATION, not a snapshot. Twelve same-owner tools/call requests
// are held together inside the upstream forward, exactly as the reviewer's probe
// held them; round-4 admitted all twelve (they all read retained==0 before the
// mutex was released) and retained twelve records under a per-owner cap of 2.
func TestRetainedInventoryBoundHoldsUnderConcurrentAdmission(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	const callers = 12
	var next int
	var mu sync.Mutex
	release := make(chan struct{})
	// arrived is BUFFERED for every caller: under the round-4 snapshot check all
	// twelve reach the forward, and the harness must observe that as a failed
	// assertion, not as a blocked or panicking barrier.
	arrived := make(chan struct{}, callers)
	up := &taskUpstream{}
	up.gate = func(req UpstreamRequest) {
		if req.Method != "tools/call" {
			return
		}
		// Every admitted caller parks HERE — inside the forward, after admission and
		// before any task exists. That is the whole window R5-02 is about.
		arrived <- struct{}{}
		<-release
	}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method != "tools/call" {
			return json.RawMessage(normativeCompleteResult), nil
		}
		mu.Lock()
		next++
		id := next
		mu.Unlock()
		return json.RawMessage(fmt.Sprintf(
			`{"resultType":"task","taskId":"task-conc-%d","status":"working"}`, id)), nil
	}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 1, nil)
	cap := rs.taskLedger.retainedCapPerOwner()

	codes := make(chan int, callers)
	var done sync.WaitGroup
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			w := httptest.NewRecorder()
			rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
			codes <- w.Code
		}()
	}
	// Once `cap` callers are parked in the forward, every other caller must have
	// been refused BEFORE reaching it: the reservation, not a snapshot.
	for i := 0; i < cap; i++ {
		<-arrived
	}
	close(release)
	done.Wait()
	close(codes)

	governed, denied := 0, 0
	for code := range codes {
		switch code {
		case http.StatusOK:
			governed++
		case http.StatusTooManyRequests:
			// Either refused at ADMISSION (no forward) or, for a call that was
			// admitted and then hit the tighter ACTIVE cap, denied after creation with
			// the created task compensated and retained. Both are 429; the invariant
			// that separates round-4 from round-5 is the forward count below.
			denied++
		default:
			t.Errorf("unexpected tools/call status %d", code)
		}
	}
	if governed+denied != callers || governed == 0 {
		t.Errorf("R5-02: %d governed / %d denied of %d callers, want every call answered and at least one admitted",
			governed, denied, callers)
	}
	// THE invariant: the number of TASK-PRODUCING forwards is bounded by the
	// reservation, not by how many callers happened to read the same snapshot.
	// Round-4 let all twelve through (the reviewer's probe reached 12 retained
	// under this very cap of 2).
	if got := up.count("tools/call"); got != cap {
		t.Errorf("R5-02: %d concurrent task-producing forwards under a per-owner retention cap of %d, want exactly %d",
			got, cap, cap)
	}
	if got := ledgerSize(rs.taskLedger); got > cap {
		t.Errorf("R5-02: retained records = %d under a per-owner cap of %d (the bound is not a bound)", got, cap)
	}
}

// TestAdmissionReservationIsReleasedBySynchronousResults pins the other half of
// R5-02: a reservation that is never consumed must not leak, or a gateway whose
// tools are ordinary synchronous ones would saturate itself.
func TestAdmissionReservationIsReleasedBySynchronousResults(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	up := &taskUpstream{}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 1, nil)
	for i := 0; i < 20; i++ {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
		if w.Code != http.StatusOK {
			t.Fatalf("synchronous tools/call %d = %d, want 200; body=%s", i, w.Code, w.Body.String())
		}
	}
	if sat := rs.taskLedger.admissionState(taskOwner{Tenant: rs.tenant, Issuer: rsIssuer, Subject: "agent:claude"}); sat.OwnerRetained != 0 {
		t.Errorf("owner retention after 20 synchronous calls = %d, want 0 (no reservation may leak)", sat.OwnerRetained)
	}
}

// TestReconcileInventoryListsEveryBoundConsumingRecord is R5-03(a): the listing
// rule cannot drift from the bound. A proven-terminal normal record with
// `ttlMs: null` consumes the retention bound forever, and round-4 did not list it
// at all — so an owner could be denied every tools/call (429) while the operator
// inventory reported an EMPTY backlog and nothing to drain.
func TestReconcileInventoryListsEveryBoundConsumingRecord(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	var next int
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			next++
			return json.RawMessage(fmt.Sprintf(
				`{"resultType":"task","taskId":"task-vis-%d","status":"working","ttlMs":null}`, next)), nil
		case methodTasksGet:
			var p struct {
				TaskID string `json:"taskId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			return json.RawMessage(conformingGetTaskResult(p.TaskID, taskStatusCompleted)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskRS(t, jwks, up, &taskAuditor{}, nil, nil, 1, nil)

	// Two governed tasks, both CONFIRMED terminal by an authoritative client read.
	for i := 0; i < rs.taskLedger.retainedCapPerOwner(); i++ {
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, toolsCallReq(token, "search", `{}`))
		if w.Code != http.StatusOK {
			t.Fatalf("tools/call %d = %d, want 200; body=%s", i, w.Code, w.Body.String())
		}
		id := fmt.Sprintf("task-vis-%d", i+1)
		wg := httptest.NewRecorder()
		rs.ServeHTTP(wg, taskReq(token, methodTasksGet, `{"taskId":"`+id+`"}`))
		if wg.Code != http.StatusOK {
			t.Fatalf("tasks/get %s = %d, want 200; body=%s", id, wg.Code, wg.Body.String())
		}
		rec, ok := rs.taskLedger.lookup(id)
		if !ok || !rec.retirable() {
			t.Fatalf("record %s = %+v ok=%t, want a CONFIRMED terminal status", id, rec, ok)
		}
	}
	// The owner is now saturated...
	sat := httptest.NewRecorder()
	rs.ServeHTTP(sat, toolsCallReq(token, "search", `{}`))
	if sat.Code != http.StatusTooManyRequests {
		t.Fatalf("saturated tools/call = %d, want 429; body=%s", sat.Code, sat.Body.String())
	}
	// ...and every record that saturated it is VISIBLE, classified and drainable.
	rows := listReconcileRows(t, rs, token, "")
	if len(rows.Records) != rs.taskLedger.retainedCapPerOwner() {
		t.Fatalf("R5-03: inventory = %d rows, want the %d records that consume the bound; body=%+v",
			len(rows.Records), rs.taskLedger.retainedCapPerOwner(), rows.Records)
	}
	for _, row := range rows.Records {
		if row.Class != taskReconcileRowTerminalConfirmed || !row.Retirable || !row.Actionable {
			t.Errorf("R5-03: row %+v, want the actionable terminal-confirmed class", row)
		}
		wr := httptest.NewRecorder()
		rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire,
			`{"taskId":"`+row.TaskID+`","generation":"`+row.Generation+`","ownerDigest":"`+row.OwnerDigest+`"}`))
		if wr.Code != http.StatusOK {
			t.Fatalf("R5-03: safe drain of %s = %d, want 200; body=%s", row.TaskID, wr.Code, wr.Body.String())
		}
	}
	after := httptest.NewRecorder()
	rs.ServeHTTP(after, toolsCallReq(token, "search", `{}`))
	if after.Code != http.StatusOK {
		t.Errorf("R5-03: tools/call after the drain = %d, want 200; body=%s", after.Code, after.Body.String())
	}
}

// reconcileListEnvelope is the decoded inventory response.
type reconcileListEnvelope struct {
	Instance          string              `json:"instance"`
	Records           []taskReconcileView `json:"records"`
	NextCursor        string              `json:"nextCursor"`
	Truncated         bool                `json:"truncated"`
	Total             int                 `json:"total"`
	NewerThanSnapshot int                 `json:"newerThanSnapshot"`
	// Saturated/Retained/Cap are the admission view the response has always
	// carried; round-7 R7-04 decodes them here because `retained` (which counts
	// outstanding admission TICKETS as well as rows) is one of the three checks the
	// documented process-drain procedure requires, and no test could read it.
	Saturated bool `json:"saturated"`
	Retained  int  `json:"retained"`
	Cap       int  `json:"retainedCap"`
}

func listReconcileRows(t *testing.T, rs *ResourceServer, token, cursor string) reconcileListEnvelope {
	t.Helper()
	params := `{}`
	if cursor != "" {
		params = `{"cursor":"` + cursor + `"}`
	}
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileList, params))
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation list = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Result reconcileListEnvelope `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode inventory: %v; body=%s", err, w.Body.String())
	}
	return env.Result
}

// TestReconcileInventoryPaginatesEveryRow is R5-03(b): round-4 sorted the whole
// inventory and returned the first 512 rows with `truncated:true` and no way to
// ask for more, so a prefix of records that cannot yet be retired made every
// later row permanently undiscoverable — and a row nobody can discover is a row
// nobody can drain, because the mutating actions require the generation and owner
// digest only the listing hands out.
func TestReconcileInventoryPaginatesEveryRow(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	rs := newTaskEvidenceRS(t, jwks, &taskUpstream{}, &taskAuditor{}, nil, nil, nil)
	const total = maxReconcileListRecords + 7
	for i := 0; i < total; i++ {
		q := rs.taskLedger.quarantine(TaskRecord{
			TaskID: fmt.Sprintf("task-page-%04d", i), Tenant: rs.tenant, Issuer: rsIssuer,
			Subject: "agent:claude", Tool: "search", RequiredScope: "tools:read",
			Status: taskStatusWorking, CreatedAt: rs.clock(),
		}, sdk.EvidenceBinding{}, "orphan")
		if !q.retained() {
			t.Fatalf("orphan %d not retained: %+v", i, q)
		}
	}
	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		page := listReconcileRows(t, rs, token, cursor)
		pages++
		if page.Instance == "" {
			t.Fatal("R5-03: the inventory must name the gateway INSTANCE whose process-local ledger it came from")
		}
		if page.Total != total {
			t.Errorf("page total = %d, want %d", page.Total, total)
		}
		for _, row := range page.Records {
			seen[row.TaskID]++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != total {
		t.Errorf("R5-03: %d distinct rows reachable across %d pages, want all %d", len(seen), pages, total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %s returned %d times: the page order is not a stable total order", id, n)
		}
	}
	// A cursor from another instance is REFUSED, never silently re-paginated
	// against a different process-local inventory. It is minted with THIS instance's
	// key so the refusal proves the instance binding, not merely the MAC (round-6
	// R6-03 authenticates the token; the instance check is the operator-facing half).
	other, oerr := rs.issueReconcileCursor(reconcileCursor{
		Instance: "not-this-instance", Snapshot: 1, TaskID: "task-page-0000", Generation: "g",
	})
	if oerr != nil {
		t.Fatalf("issue foreign cursor: %v", oerr)
	}
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileList, `{"cursor":"`+other+`"}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("foreign-instance cursor = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	wm := httptest.NewRecorder()
	rs.ServeHTTP(wm, taskReq(token, methodTasksReconcileList, `{"cursor":"!!!not-base64!!!"}`))
	if wm.Code != http.StatusBadRequest {
		t.Errorf("malformed cursor = %d, want 400; body=%s", wm.Code, wm.Body.String())
	}
}

// TestReconcileStatusDisclosesNoTaskContent is R5-04: the reconciliation status
// response is the governance PROJECTION only. SEP-2663 puts the original tool
// result in `GetTaskResult.result` (and errors / pending input requests in the
// failed and input_required variants), so returning the raw body turned a
// tenant-wide operational scope into a cross-owner content read.
func TestReconcileStatusDisclosesNoTaskContent(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	const secret = "ROUND5-TOP-SECRET"
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			return json.RawMessage(`{"resultType":"complete","taskId":"task-secret","status":"completed",` +
				`"createdAt":"2026-06-08T12:00:00Z","lastUpdatedAt":"2026-06-08T12:00:01Z","ttlMs":null,` +
				`"statusMessage":"` + secret + `-message",` +
				`"result":{"content":[{"type":"text","text":"` + secret + `"}]}}`), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-secret")

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Errorf("R5-04: the reconciliation response disclosed task content: %s", w.Body.String())
	}
	var env struct {
		Result struct {
			Applied bool              `json:"statusApplied"`
			Record  taskReconcileView `json:"record"`
			Digest  string            `json:"upstreamResultDigest"`
			Raw     json.RawMessage   `json:"upstreamResult"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode status: %v; body=%s", err, w.Body.String())
	}
	if len(env.Result.Raw) != 0 {
		t.Errorf("R5-04: a raw upstream body is still returned: %s", env.Result.Raw)
	}
	if !env.Result.Applied || env.Result.Record.Status != taskStatusCompleted {
		t.Errorf("status projection = %+v, want the confirmed status applied", env.Result)
	}
	if env.Result.Digest == "" {
		t.Error("the projection must carry the upstream result DIGEST so the read correlates with its evidence")
	}
	// The same rule holds for the inventory: no upstream free text anywhere.
	rows := listReconcileRows(t, rs, token, "")
	if len(rows.Records) == 0 {
		t.Fatal("inventory unexpectedly empty")
	}
	body, _ := json.Marshal(rows)
	if strings.Contains(string(body), secret) {
		t.Errorf("R5-04: the inventory disclosed upstream free text: %s", body)
	}
}

// TestReconcileStatusSynthesizesAConformingTasksRequest is R5-05 at the connector
// seam: the synthesized `tasks/get` must declare the tasks extension in its
// per-request client capabilities (a server MUST NOT honor an extension the
// request did not declare, -32021) and must carry the protocol version — which is
// also what the transport mirrors into the routing headers.
func TestReconcileStatusSynthesizesAConformingTasksRequest(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		if req.Method == methodTasksGet {
			return json.RawMessage(conformingGetTaskResult("task-conform", taskStatusCanceled)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-conform")

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	fwd, ok := up.last(methodTasksGet)
	if !ok {
		t.Fatal("the reconciliation read never reached the upstream")
	}
	var params struct {
		TaskID string `json:"taskId"`
		Meta   struct {
			Version      string `json:"io.modelcontextprotocol/protocolVersion"`
			Capabilities struct {
				Extensions map[string]json.RawMessage `json:"extensions"`
			} `json:"io.modelcontextprotocol/clientCapabilities"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(fwd.Params, &params); err != nil {
		t.Fatalf("decode synthesized params: %v; raw=%s", err, fwd.Params)
	}
	if params.TaskID != "task-conform" {
		t.Errorf("synthesized taskId = %q, want the record's", params.TaskID)
	}
	if params.Meta.Version != revision20260728 {
		t.Errorf("synthesized protocol version = %q, want %q", params.Meta.Version, revision20260728)
	}
	if _, declared := params.Meta.Capabilities.Extensions[extensionTasks]; !declared {
		t.Errorf("R5-05: the synthesized tasks/get does not declare %q; a conforming server answers -32021: %s",
			extensionTasks, fwd.Params)
	}
	// The routing mirrors an adapter must emit are derived from those very params.
	headers := UpstreamRoutingHeaders(methodTasksGet, fwd.Params)
	if headers[headerMcpMethod] != methodTasksGet {
		t.Errorf("Mcp-Method = %q, want %q", headers[headerMcpMethod], methodTasksGet)
	}
	if headers[headerMcpName] != "task-conform" {
		t.Errorf("R5-05: Mcp-Name = %q, want the taskId (SEP-2663 sticky routing)", headers[headerMcpName])
	}
	if headers[headerMCPProtocolVersion] != revision20260728 {
		t.Errorf("MCP-Protocol-Version = %q, want %q", headers[headerMCPProtocolVersion], revision20260728)
	}
}

// TestReconcileStatusRefusesAGuessedLegacyRequest pins the other half of R5-05:
// the upstream revision is RECORDED, not guessed. An upstream declared as a
// revision whose Tasks extension this connector does not implement gets the read
// REFUSED — never a fabricated request whose answer would then be treated as
// proof.
func TestReconcileStatusRefusesAGuessedLegacyRequest(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	rs, stored := newReconcileRS(t, jwks, up, &taskAuditor{}, token, "task-legacy")
	rs.upstreamRevision = revision20251125
	before := up.count(methodTasksGet)

	w := httptest.NewRecorder()
	rs.ServeHTTP(w, taskReq(token, methodTasksReconcileStatus, reconcileParams(stored, "")))
	if w.Code != http.StatusBadGateway {
		t.Errorf("reconciliation status against a legacy upstream = %d, want 502; body=%s", w.Code, w.Body.String())
	}
	if got := up.count(methodTasksGet) - before; got != 0 {
		t.Errorf("R5-05: %d guessed tasks/get requests reached a legacy upstream, want 0", got)
	}
	if _, still := rs.taskLedger.lookup("task-legacy"); !still {
		t.Error("a refused read must never remove the record")
	}
}

// TestReconcileScopeIsDiscoverable is the R5-03 reachability fix: an operator
// entitled to the recovery surface could not DISCOVER the scope it needs — the
// PRM scope union carried only configured and tool scopes.
func TestReconcileScopeIsDiscoverable(t *testing.T) {
	_, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	rs := newTaskEvidenceRS(t, jwks, &taskUpstream{}, &taskAuditor{}, nil, nil, nil)
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, httptest.NewRequest(http.MethodGet, wellKnownProtectedResource, nil))
	var doc struct {
		Scopes []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode PRM: %v; body=%s", err, w.Body.String())
	}
	found := false
	for _, s := range doc.Scopes {
		if s == scopeTasksReconcile {
			found = true
		}
	}
	if !found {
		t.Errorf("scopes_supported = %v, want the enforced %q to be discoverable", doc.Scopes, scopeTasksReconcile)
	}
}
