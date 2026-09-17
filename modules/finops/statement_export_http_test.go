// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/finops"
)

// exactMicroUSD is 2^53+1: the smallest integer that a float64 cannot hold. It is the
// ORACLE of this test, not decoration. CSV carries it as decimal text and int64 keeps
// every digit; anything that routed the value through a JSON number and an IEEE double
// on the way out would answer 9007199254740992 — one micro-dollar short, silently.
const exactMicroUSD = "9007199254740993"

// rawResp is one HTTP answer with the parts a media-type contract is made of. The
// shared harness `do` helper unmarshals into map[string]any and drops the headers,
// which is exactly what cannot be used to judge a CSV response.
type rawResp struct {
	code   int
	header http.Header
	body   []byte
}

func (h *harness) doRawHTTP(method, path, token string, hdr map[string]string) rawResp {
	h.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(nil))
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rawResp{code: rec.Code, header: rec.Result().Header, body: rec.Body.Bytes()}
}

// TestStatementExportServesExactCSVOverHTTP is the real-HTTP oracle of the statement
// export contract: a statement and its line are produced through the mounted routes,
// and the export is read back over the same server the console and the SDKs talk to.
//
// It is deliberately GREEN before the OpenAPI classifier is corrected and after it: the
// handler was always right. It is here so the document can be measured against the
// server rather than against itself — the published contract said this 200 was a JSON
// object, and only an end-to-end read proves which of the two was lying.
func TestStatementExportServesExactCSVOverHTTP(t *testing.T) {
	h := newHarness(t, finops.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "statement-export")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/finops/cost-centers", admin,
		map[string]any{"code": "ENG-01", "name": "Engineering"}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create cost center = %d %s", r.code, r.raw)
	}
	ccID := r.body["id"].(string)

	r = h.do("POST", "/v1/m/finops/cost-centers/"+ccID+"/mappings", admin,
		map[string]any{"source_dimension": "team", "source_key": "eng", "priority": 10}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create mapping = %d %s", r.code, r.raw)
	}

	// The ingest body carries the money as a JSON integer literal; costIngestRequest
	// decodes it into an int64, so the ledger holds all 16 digits.
	r = h.do("POST", "/v1/m/finops/cost", admin, json.RawMessage(`{
		"provider_ref": "anthropic", "model_ref": "claude-opus-5",
		"input_tokens": 100, "output_tokens": 50,
		"cost_micro_usd": `+exactMicroUSD+`,
		"occurred_at": "2026-06-10T12:00:00Z",
		"labels": {"team": "eng"}
	}`), hdr)
	if r.code != http.StatusAccepted {
		t.Fatalf("ingest cost = %d %s", r.code, r.raw)
	}
	h.waitCosts(tenant, 1)

	r = h.do("POST", "/v1/m/finops/statements/generate", admin,
		map[string]any{"period": "monthly", "period_start": "2026-06-01T00:00:00Z"}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("generate statements = %d %s", r.code, r.raw)
	}
	stmts := r.body["statements"].([]any)
	if len(stmts) != 1 {
		t.Fatalf("generated %d statements, want 1: %s", len(stmts), r.raw)
	}
	stmtID := stmts[0].(map[string]any)["id"].(string)

	export := h.doRawHTTP(http.MethodGet, "/v1/m/finops/statements/"+stmtID+"/export", admin, hdr)
	if export.code != http.StatusOK {
		t.Fatalf("export = %d %s", export.code, export.body)
	}

	// The media type is parsed, never string-compared: "text/csv; charset=utf-8" is one
	// concrete spelling of the type the document must publish as the key `text/csv`.
	mediaType, params, err := mime.ParseMediaType(export.header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type %q does not parse: %v", export.header.Get("Content-Type"), err)
	}
	if mediaType != "text/csv" {
		t.Errorf("media type = %q, want text/csv", mediaType)
	}
	if got := strings.ToLower(params["charset"]); got != "utf-8" {
		t.Errorf("charset = %q, want utf-8", got)
	}
	if got := export.header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment; filename=") ||
		!strings.Contains(got, "chargeback_ENG-01_2026-06-01.csv") {
		t.Errorf("Content-Disposition = %q, want an attachment named for the cost center and period", got)
	}

	records, err := csv.NewReader(bytes.NewReader(export.body)).ReadAll()
	if err != nil {
		t.Fatalf("body is not parseable CSV: %v\n%s", err, export.body)
	}
	wantHeader := []string{
		"cost_center_code", "cost_center_name", "model", "provider", "agent",
		"input_tokens", "output_tokens", "cost_micro_usd", "sample_count",
	}
	if len(records) != 2 {
		t.Fatalf("got %d CSV records, want header + one line: %q", len(records), records)
	}
	for i, want := range wantHeader {
		if records[0][i] != want {
			t.Errorf("header column %d = %q, want %q", i, records[0][i], want)
		}
	}
	if len(records[0]) != len(wantHeader) {
		t.Fatalf("header has %d columns, want %d", len(records[0]), len(wantHeader))
	}

	row := records[1]
	for i, want := range []string{"ENG-01", "Engineering", "claude-opus-5", "anthropic", "", "100", "50", exactMicroUSD, "1"} {
		if row[i] != want {
			t.Errorf("row column %d (%s) = %q, want %q", i, wantHeader[i], row[i], want)
		}
	}

	// A well-formed id that does not exist is still a JSON 404: only the success path
	// is CSV, which is what makes the "errors stay application/json" half of the
	// document contract true.
	missing := h.doRawHTTP(http.MethodGet,
		"/v1/m/finops/statements/00000000-0000-4000-8000-0000000000ff/export", admin, hdr)
	if missing.code != http.StatusNotFound {
		t.Fatalf("missing statement = %d %s", missing.code, missing.body)
	}
	missingType, _, err := mime.ParseMediaType(missing.header.Get("Content-Type"))
	if err != nil || missingType != "application/json" {
		t.Errorf("404 Content-Type = %q (parse err %v), want application/json", missing.header.Get("Content-Type"), err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(missing.body, &envelope); err != nil {
		t.Fatalf("404 body is not JSON: %v\n%s", err, missing.body)
	}
	if envelope["error"] == nil {
		t.Errorf("404 body = %s, want the shared error envelope", missing.body)
	}
}
