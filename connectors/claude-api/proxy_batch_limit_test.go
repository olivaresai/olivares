// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// batchBodyCanary marks the first entry of entryBatch, so a refusal that echoed the request
// body would carry it.
const batchBodyCanary = "BATCH-BODY-CANARY"

// entryBatch builds a Message Batches body of n minimal entries (n >= 1).
func entryBatch(n int) string {
	return `{"requests":` + entryArray(n) + `}`
}

// entryArray builds a JSON array of n minimal batch entries (n >= 1).
func entryArray(n int) string {
	var b strings.Builder
	b.Grow(3*n + 64)
	b.WriteString(`[{"custom_id":"` + batchBodyCanary + `"}`)
	for range n - 1 {
		b.WriteString(`,{}`)
	}
	b.WriteString(`]`)
	return b.String()
}

// countDecodes makes p count its full decodes of a batch body into entries.
func countDecodes(p *MessagesProxy) *int {
	n := new(int)
	p.decodeBatch = func(body []byte, v any) error {
		*n++
		return json.Unmarshal(body, v)
	}
	return n
}

// TestBatchAtTheEntryLimitIsAdmitted pins that a batch of exactly 100,000 entries, the
// upstream's documented per-batch limit, is decoded once, reaches admission and is forwarded.
func TestBatchAtTheEntryLimitIsAdmitted(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: upstreamBatchJSON, contentType: "application/json"}
	dec := &countingDecider{fakeDecider: fakeDecider{batchDecision: ProxyBatchDecision{Allow: true}, batchDenyAt: -1}}
	p := newProxy(t, doer, dec, nil)
	decodes := countDecodes(p)

	w := postBatch(t, p, entryBatch(100000), "k")

	if *decodes != 1 {
		t.Errorf("full decodes = %d, want 1: a batch at the limit is decoded", *decodes)
	}
	if dec.authorizeBatch != 1 || len(dec.gotBatch) != 100000 {
		t.Fatalf("admission ran %d times over %d entries, want once over 100000", dec.authorizeBatch, len(dec.gotBatch))
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%.200s", w.Code, w.Body.String())
	}
	if doer.calls != 1 {
		t.Errorf("upstream calls = %d, want 1", doer.calls)
	}
	if dec.finalizeBatch != 1 || dec.batchFinalizeOut.Entries != 100000 {
		t.Errorf("FinalizeBatch ran %d times with Entries=%d, want once with 100000", dec.finalizeBatch, dec.batchFinalizeOut.Entries)
	}
}

// TestBatchOverTheEntryLimitIsRefusedBeforeAdmission pins that a batch of 100,001 entries is
// refused before its entries are decoded and before the decider sees it: no full decode, no
// admission, no settlement or ledger write, no forward. The 413 names the limit and the count
// and does not echo the body. The decode matches the "requests" key ignoring case and keeps
// its last occurrence, so an earlier "requests" key must not hide the array that follows,
// whether its value is null or a number out of float range.
func TestBatchOverTheEntryLimitIsRefusedBeforeAdmission(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"plain", entryBatch(100001)},
		{"after a null key, in other case", `{"requests":null,"REQUESTS":` + entryArray(100001) + `}`},
		{"after an out-of-range number, in other case", `{"requests":1e999,"REQUESTS":` + entryArray(100001) + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doer := &proxyStubDoer{status: 200, body: upstreamBatchJSON, contentType: "application/json"}
			dec := &countingDecider{fakeDecider: fakeDecider{batchDecision: ProxyBatchDecision{Allow: true}, batchDenyAt: -1}}
			p := newProxy(t, doer, dec, nil)
			decodes := countDecodes(p)

			w := postBatch(t, p, tc.body, "k")

			if *decodes != 0 {
				t.Fatalf("full decodes = %d for a batch over the entry limit, want 0: the entries are counted before any decode", *decodes)
			}
			if dec.authorizeBatch != 0 {
				t.Fatalf("admission ran %d times for a batch over the entry limit, want 0: the count is checked before any admission", dec.authorizeBatch)
			}
			if dec.finalizeBatch != 0 {
				t.Errorf("FinalizeBatch ran %d times, want 0: a refused batch writes nothing", dec.finalizeBatch)
			}
			if doer.calls != 0 {
				t.Errorf("upstream calls = %d, want 0: a refused batch forwards nothing", doer.calls)
			}
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413; body=%.200s", w.Code, w.Body.String())
			}
			errType, msg := decodeProxyError(t, w.Body.Bytes())
			if errType != "invalid_request_error" || !strings.Contains(msg, "100000") || !strings.Contains(msg, "100001") {
				t.Errorf("refusal = %s/%q, want invalid_request_error naming the limit 100000 and the count 100001", errType, msg)
			}
			if strings.Contains(w.Body.String(), batchBodyCanary) {
				t.Errorf("refusal echoed the request body: %.200s", w.Body.String())
			}
		})
	}
}

// TestBatchEmptyIsRefusedBeforeAdmission pins that the entry limit leaves the empty batch's
// answer as it was: a 400 before admission, with nothing forwarded.
func TestBatchEmptyIsRefusedBeforeAdmission(t *testing.T) {
	doer := &proxyStubDoer{status: 200, body: upstreamBatchJSON, contentType: "application/json"}
	dec := &countingDecider{fakeDecider: fakeDecider{batchDecision: ProxyBatchDecision{Allow: true}, batchDenyAt: -1}}
	p := newProxy(t, doer, dec, nil)

	w := postBatch(t, p, `{"requests":[]}`, "k")

	if dec.authorizeBatch != 0 || doer.calls != 0 {
		t.Fatalf("admission ran %d times and upstream %d times for an empty batch, want 0 and 0", dec.authorizeBatch, doer.calls)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if errType, msg := decodeProxyError(t, w.Body.Bytes()); errType != "invalid_request_error" || msg != "batch contains no requests" {
		t.Errorf("refusal = %s/%q, want invalid_request_error/%q", errType, msg, "batch contains no requests")
	}
}

// TestBatchMalformedKeepsItsRefusal pins the 400 for a body that is not a batch request: one
// that is not valid JSON, however many entries precede its defect, and a valid one whose top
// level is not an object. Each is refused before the full decode, whichever decoder would read
// it, and reaches neither admission nor the upstream.
func TestBatchMalformedKeepsItsRefusal(t *testing.T) {
	long := entryBatch(100001)
	for _, tc := range []struct{ name, body string }{
		{"short", `{"requests":[{}`},
		{"over the limit before its defect", long[:len(long)-2]},
		{"a null top level", `null`},   // guard: the scan already refuses it before the decode
		{"an array top level", `[{}]`}, // guard: the scan already refuses it before the decode
		// Guards for the validity check itself: the scan closes both bodies and accepts them, so
		// only the check keeps them from the decode.
		{"trailing bytes after a closed object", `{"requests":[{}]}x`},
		{"a trailing comma in the array", `{"requests":[{},]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doer := &proxyStubDoer{status: 200, body: upstreamBatchJSON, contentType: "application/json"}
			dec := &countingDecider{fakeDecider: fakeDecider{batchDecision: ProxyBatchDecision{Allow: true}, batchDenyAt: -1}}
			p := newProxy(t, doer, dec, nil)
			decodes := countDecodes(p)

			w := postBatch(t, p, tc.body, "k")

			if *decodes != 0 {
				t.Fatalf("full decodes = %d for a body that is not a batch request, want 0: it is refused before the decode", *decodes)
			}
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%.200s", w.Code, w.Body.String())
			}
			const want = "request body is not a valid Message Batches request"
			if errType, msg := decodeProxyError(t, w.Body.Bytes()); errType != "invalid_request_error" || msg != want {
				t.Errorf("refusal = %s/%q, want invalid_request_error/%q", errType, msg, want)
			}
			if dec.authorizeBatch != 0 || doer.calls != 0 {
				t.Errorf("admission ran %d times and upstream %d times for a body that is not a batch request, want 0 and 0", dec.authorizeBatch, doer.calls)
			}
		})
	}
}
