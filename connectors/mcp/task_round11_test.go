// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// task_round11_test.go — Stage 4, review ROUND-11 regression.
//
// Round 11 returned exactly ONE blocking finding:
//
//	N11-01 the ordinary write-error branch of the handle relay classified the
//	       outcome as NEVER-DELIVERED, and that classification is not established by
//	       the evidence the branch has. `writeResult` calls
//	       `json.NewEncoder(w).Encode(resp)`; the encoder marshals the COMPLETE JSON
//	       value, appends '\n', makes ONE `Write(b)` call and DISCARDS the reported
//	       byte count ($GOROOT/src/encoding/json/stream.go:204-236). The io.Writer
//	       contract permits `0 <= n <= len(p)` with a non-nil error when
//	       `n < len(p)` ($GOROOT/src/io/io.go), so a CONFORMING
//	       `http.ResponseWriter` may accept `p[:len(p)-1]` — the whole JSON-RPC
//	       response, missing only the encoder's newline, which JSON-RPC does not
//	       require — and still report an error. The owner then holds a complete,
//	       parseable response carrying the usable task id while the ledger recorded
//	       `HandleRelayed:false`, `ownerCollectionSatisfied` returned true purely
//	       because of it, and an operator-confirmed terminal status let
//	       `compareDeleteTerminalLocked` DELETE the unread result.

// errRound11ShortWrite is the error the all-but-newline writer reports together with
// its `n < len(p)` count — the exact io.Writer shape the finding is about.
var errRound11ShortWrite = errors.New("mcp-test: the transport rejected the trailing newline")

// allButNewlineResponseWriter is a CONFORMING `http.ResponseWriter` that accepts the
// complete encoded JSON value and rejects only the newline `json.Encoder` appends
// after it. It returns `(len(p)-1, err)` — permitted by io.Writer, which requires a
// non-nil error exactly when `n < len(p)` — so the bytes it accepted are a complete,
// parseable JSON-RPC response containing the unguessable task id.
//
// It is deliberately NOT `failingResponseWriter` (task_round7_test.go), which reports
// `(0, err)`: that one is a PROVEN ZERO-BYTE write and must keep its never-delivered
// classification (TestRound8AFailedHandleRelayIsNeverDeliveredAndDrainable pins it).
// The two are different facts and must not be collapsed in either direction.
type allButNewlineResponseWriter struct {
	hdr      http.Header
	status   int
	accepted []byte
	writes   int
}

func (a *allButNewlineResponseWriter) Header() http.Header {
	if a.hdr == nil {
		a.hdr = http.Header{}
	}
	return a.hdr
}

func (a *allButNewlineResponseWriter) WriteHeader(code int) { a.status = code }

func (a *allButNewlineResponseWriter) Write(p []byte) (int, error) {
	a.writes++
	if n := len(p); n > 0 && p[n-1] == '\n' {
		a.accepted = append(a.accepted, p[:n-1]...)
		return n - 1, errRound11ShortWrite
	}
	a.accepted = append(a.accepted, p...)
	return len(p), nil
}

// TestRound11AWriteErrorAfterAcceptedBytesAssumesPossibleDelivery is the reviewer's
// N11-01 counterexample, driven through the full handler.
//
// The premise is asserted, not assumed: the bytes the writer ACCEPTED are decoded
// back into a JSON-RPC response and the task id is read out of it, so the test only
// makes its governance claim once it has shown a conforming owner really can hold a
// usable identifier after this "failed" write.
func TestRound11AWriteErrorAfterAcceptedBytesAssumesPossibleDelivery(t *testing.T) {
	const id = "task-r11-shortwrite"
	token, jwks := mintAccessToken(t, "k1", rsResource, reconcileScopes, validExp())
	up := &taskUpstream{}
	up.fn = func(req UpstreamRequest) (json.RawMessage, error) {
		switch req.Method {
		case "tools/call":
			// No ttlMs — the row cannot be quietly forgotten, so what happens to it is
			// decided by the classification and nothing else.
			return round8TaskHandle(id, ""), nil
		case methodTasksGet:
			return json.RawMessage(conformingGetTaskResult(id, taskStatusCompleted)), nil
		}
		return json.RawMessage(normativeCompleteResult), nil
	}
	rs := newTaskEvidenceRS(t, jwks, up, &taskAuditor{}, nil, nil, nil)

	aw := &allButNewlineResponseWriter{}
	rs.ServeHTTP(aw, toolsCallReq(token, "search", `{}`))
	if aw.writes == 0 {
		t.Fatal("the handler never attempted to write the handle; the scenario did not run")
	}
	// The premise: what the writer accepted IS a usable response.
	var got struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			TaskID string `json:"taskId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(aw.accepted, &got); err != nil {
		t.Fatalf("the accepted bytes must be a complete JSON-RPC response: %v; bytes=%s", err, aw.accepted)
	}
	if got.JSONRPC != "2.0" || got.Result.TaskID != id {
		t.Fatalf("the accepted bytes must carry the usable task id, got %+v; bytes=%s", got, aw.accepted)
	}

	rec, ok := rs.taskLedger.lookup(id)
	if !ok {
		t.Fatal("the upstream task exists: its record must NEVER be forgotten (F-03)")
	}
	if !rec.HandleRelayed {
		t.Error("N11-01: a write error that followed ACCEPTED response bytes was recorded as certainly-never-delivered, " +
			"which authorizes an operator to delete a result the owner is holding the identifier for")
	}
	if !rec.Quarantined || rec.QuarantineReason != taskQuarantineHandlePartial {
		t.Errorf("N11-01: record after an ambiguous handle write = %+v, want the ambiguous reconciliation state", rec)
	}
	if rec.operable() {
		t.Error("N11-01: an unprovable registration stayed client-operable after an ambiguous handle write")
	}
	if round9Pinned(rs.taskLedger, rec.Generation) || round9LeaseCount(rs.taskLedger) != 0 {
		t.Error("N11-01: the generation pin was stranded by the ambiguous relay")
	}

	// The consequence the classification exists for: an operator who proves the task
	// terminal still may NOT delete it, because the owner may hold the handle.
	ws := httptest.NewRecorder()
	rs.ServeHTTP(ws, taskReq(token, methodTasksReconcileStatus, reconcileParams(rec, "")))
	if ws.Code != http.StatusOK {
		t.Fatalf("reconciliation status = %d, want 200; body=%s", ws.Code, ws.Body.String())
	}
	wr := httptest.NewRecorder()
	rs.ServeHTTP(wr, taskReq(token, methodTasksReconcileRetire, reconcileParams(rec, "")))
	if wr.Code != http.StatusConflict {
		t.Fatalf("N11-01: retire after an ambiguous handle write = %d, want 409; body=%s", wr.Code, wr.Body.String())
	}
	if _, still := rs.taskLedger.lookup(id); !still {
		t.Error("N11-01: a refused retirement must never delete the record")
	}
}
