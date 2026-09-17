// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func opaReq() auth.Request {
	return auth.Request{
		Principal:  auth.Principal{Kind: auth.KindToken, CredID: "tok-1", Superadmin: false},
		Permission: "agent:write",
		Tenant:     model.TenantID("t-1"),
		Resource:   auth.ResourceAttrs{Kind: "agent", ID: "a-1", Sensitivity: "high"},
	}
}

// opaServer returns a test OPA Data API: it records the last request and replies with
// the given status and body.
func opaServer(t *testing.T, status int, body string, capture *[]byte, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			*capture, _ = io.ReadAll(r.Body)
		}
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/data/") {
			t.Errorf("unexpected OPA path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestOPAAllowDenyUndefined(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantAllow bool
	}{
		{"true permits", `{"result":true,"decision_id":"d1"}`, true},
		{"false restricts", `{"result":false,"decision_id":"d2"}`, false},
		{"absent restricts", `{}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := opaServer(t, 200, c.body, nil, nil)
			defer srv.Close()
			oe, err := NewOPAEvaluator(srv.URL, "authz.allow", "", nil)
			if err != nil {
				t.Fatalf("NewOPAEvaluator: %v", err)
			}
			dec, err := oe.Evaluate(context.Background(), opaReq())
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if dec.Allow != c.wantAllow {
				t.Errorf("Allow = %v, want %v (%+v)", dec.Allow, c.wantAllow, dec)
			}
		})
	}
}

func TestOPAFailsClosedOnError(t *testing.T) {
	srv := opaServer(t, 500, `boom`, nil, nil)
	defer srv.Close()
	oe, _ := NewOPAEvaluator(srv.URL, "authz/allow", "", nil)
	if _, err := oe.Evaluate(context.Background(), opaReq()); err == nil {
		t.Fatal("a non-2xx OPA response must return an error (Authorizer fails closed)")
	}
}

func TestOPARequestShape(t *testing.T) {
	var body []byte
	var gotAuth string
	srv := opaServer(t, 200, `{"result":true}`, &body, &gotAuth)
	defer srv.Close()
	oe, _ := NewOPAEvaluator(srv.URL, "authz.allow", "secret-token", nil)
	if _, err := oe.Evaluate(context.Background(), opaReq()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// The decision path maps to /v1/data/authz/allow and the bearer token is sent.
	if oe.dataURL != srv.URL+"/v1/data/authz/allow" {
		t.Errorf("dataURL = %q", oe.dataURL)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("authorization header = %q", gotAuth)
	}
	// The input document carries the principal/permission/resource.
	var sent struct {
		Input opaInput `json:"input"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode sent input: %v", err)
	}
	if sent.Input.Permission != "agent:write" || sent.Input.Resource.Sensitivity != "high" ||
		sent.Input.Principal.CredID != "tok-1" || sent.Input.Tenant != "t-1" {
		t.Errorf("opa input = %+v", sent.Input)
	}
}

func TestOPAConfigErrors(t *testing.T) {
	if _, err := NewOPAEvaluator("", "authz/allow", "", nil); err == nil {
		t.Error("empty base url must error")
	}
	if _, err := NewOPAEvaluator("http://opa", "", "", nil); err == nil {
		t.Error("empty decision path must error")
	}
}

const (
	opaBodyReadErrText     = "governance: opa response body read failed"
	opaBodyTooLargeErrText = "governance: opa response body too large"
	opaLeakMarker          = "OPA-FIXTURE-MARKER-9f2c1e7b"
)

// staticDoer is an owned fake httpDoer. It never dials.
type staticDoer struct {
	resp *http.Response
	err  error
}

func (s staticDoer) Do(*http.Request) (*http.Response, error) {
	return s.resp, s.err
}

type scriptedRead struct {
	p   []byte
	err error
}

// scriptedBody is an owned ReadCloser that can return bytes together with a
// non-EOF error on the same Read, or fail on a later Read.
type scriptedBody struct {
	steps  []scriptedRead
	i      int
	got    int
	closed int
}

func (s *scriptedBody) Read(p []byte) (int, error) {
	if s.i >= len(s.steps) {
		return 0, io.EOF
	}
	st := &s.steps[s.i]
	if len(st.p) == 0 {
		err := st.err
		s.i++
		return 0, err
	}
	n := copy(p, st.p)
	st.p = st.p[n:]
	s.got += n
	if len(st.p) > 0 {
		return n, nil
	}
	s.i++
	return n, st.err
}

func (s *scriptedBody) Close() error {
	s.closed++
	return nil
}

// fillBody serves a prefix then a fill byte up to total without allocating the
// whole advertised length. It counts bytes actually copied to the caller.
type fillBody struct {
	prefix []byte
	fill   byte
	total  int
	off    int
	got    int
	closed int
}

func (f *fillBody) Read(p []byte) (int, error) {
	if f.off >= f.total {
		return 0, io.EOF
	}
	n := len(p)
	if remain := f.total - f.off; n > remain {
		n = remain
	}
	for i := 0; i < n; i++ {
		idx := f.off + i
		if idx < len(f.prefix) {
			p[i] = f.prefix[idx]
		} else {
			p[i] = f.fill
		}
	}
	f.off += n
	f.got += n
	if f.off >= f.total {
		return n, io.EOF
	}
	return n, nil
}

func (f *fillBody) Close() error {
	f.closed++
	return nil
}

func opaJSONPadded(jsonBody string, n int) []byte {
	if n < len(jsonBody) {
		panic("pad shorter than json")
	}
	b := make([]byte, n)
	copy(b, jsonBody)
	for i := len(jsonBody); i < n; i++ {
		b[i] = ' '
	}
	return b
}

func opaEvalBody(t *testing.T, body io.ReadCloser) (*OPAEvaluator, auth.Decision, error) {
	t.Helper()
	oe, err := NewOPAEvaluator("http://127.0.0.1:1", "authz.allow", "", staticDoer{
		resp: &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)},
	})
	if err != nil {
		t.Fatalf("NewOPAEvaluator: %v", err)
	}
	dec, err := oe.Evaluate(context.Background(), opaReq())
	return oe, dec, err
}

func assertZeroDecisionError(t *testing.T, dec auth.Decision, err error, wantText string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Evaluate returned no error, decision=%+v", dec)
	}
	if dec != (auth.Decision{}) {
		t.Errorf("decision = %+v, want zero", dec)
	}
	if dec.Allow {
		t.Errorf("Allow = true, want no permit")
	}
	if err.Error() != wantText {
		t.Errorf("error = %q, want %q", err.Error(), wantText)
	}
	if strings.Contains(err.Error(), opaLeakMarker) {
		t.Errorf("error leaked fixture marker: %v", err)
	}
	if strings.Contains(err.Error(), `{"result":true}`) {
		t.Errorf("error leaked response body: %v", err)
	}
}

func TestOPAReadErrorDoesNotPermit(t *testing.T) {
	t.Run("same-Read bytes and non-EOF error", func(t *testing.T) {
		body := &scriptedBody{steps: []scriptedRead{{
			p:   []byte(`{"result":true}`),
			err: errors.New(opaLeakMarker),
		}}}
		_, dec, err := opaEvalBody(t, body)
		assertZeroDecisionError(t, dec, err, opaBodyReadErrText)
		if body.closed != 1 {
			t.Errorf("Close called %d times, want 1", body.closed)
		}
	})
	t.Run("later read failure", func(t *testing.T) {
		body := &scriptedBody{steps: []scriptedRead{
			{p: []byte(`{"result":true}`), err: nil},
			{p: nil, err: errors.New(opaLeakMarker)},
		}}
		_, dec, err := opaEvalBody(t, body)
		assertZeroDecisionError(t, dec, err, opaBodyReadErrText)
		if body.closed != 1 {
			t.Errorf("Close called %d times, want 1", body.closed)
		}
	})
}

func TestOPAOversizedResponseRejected(t *testing.T) {
	const prefix = `{"result":true}`
	t.Run("json plus whitespace to cap then one extra byte", func(t *testing.T) {
		payload := opaJSONPadded(prefix, maxOPABody+1)
		payload[maxOPABody] = 'X'
		body := &scriptedBody{steps: []scriptedRead{{p: payload}}}
		_, dec, err := opaEvalBody(t, body)
		assertZeroDecisionError(t, dec, err, opaBodyTooLargeErrText)
		if body.closed != 1 {
			t.Errorf("Close called %d times, want 1", body.closed)
		}
		if body.got > maxOPABody+1 {
			t.Errorf("read %d bytes, want at most cap+1=%d", body.got, maxOPABody+1)
		}
	})
	t.Run("oversized whitespace-only suffix", func(t *testing.T) {
		payload := opaJSONPadded(prefix, maxOPABody+1)
		body := &scriptedBody{steps: []scriptedRead{{p: payload}}}
		_, dec, err := opaEvalBody(t, body)
		assertZeroDecisionError(t, dec, err, opaBodyTooLargeErrText)
		if body.closed != 1 {
			t.Errorf("Close called %d times, want 1", body.closed)
		}
		if body.got > maxOPABody+1 {
			t.Errorf("read %d bytes, want at most cap+1=%d", body.got, maxOPABody+1)
		}
	})
}

func TestOPAExactlyCapJSONWhitespaceRetained(t *testing.T) {
	payload := opaJSONPadded(`{"result":true,"decision_id":"cap"}`, maxOPABody)
	body := &scriptedBody{steps: []scriptedRead{{p: payload, err: io.EOF}}}
	_, dec, err := opaEvalBody(t, body)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !dec.Allow {
		t.Errorf("Allow = false, want permit for a complete cap-sized body")
	}
	if !strings.Contains(dec.Reason, "opa: permitted") || !strings.Contains(dec.Reason, "decision_id=cap") {
		t.Errorf("Reason = %q", dec.Reason)
	}
	if body.closed != 1 {
		t.Errorf("Close called %d times, want 1", body.closed)
	}
}

func TestOPAShortTrueFalseAbsentRetained(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantAllow bool
		wantClass auth.DecisionClass
		reason    string
	}{
		{"true permits", `{"result":true,"decision_id":"d1"}`, true, auth.ClassInvariant, "opa: permitted"},
		{"false restricts", `{"result":false,"decision_id":"d2"}`, false, auth.ClassPolicy, "opa: denied by policy"},
		{"absent restricts", `{}`, false, auth.ClassInvariant, "opa: decision undefined"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := &scriptedBody{steps: []scriptedRead{{p: []byte(c.body), err: io.EOF}}}
			_, dec, err := opaEvalBody(t, body)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if dec.Allow != c.wantAllow {
				t.Errorf("Allow = %v, want %v (%+v)", dec.Allow, c.wantAllow, dec)
			}
			if dec.Class != c.wantClass {
				t.Errorf("Class = %v, want %v", dec.Class, c.wantClass)
			}
			if !strings.Contains(dec.Reason, c.reason) {
				t.Errorf("Reason = %q, want substring %q", dec.Reason, c.reason)
			}
			if body.closed != 1 {
				t.Errorf("Close called %d times, want 1", body.closed)
			}
		})
	}
}

func TestOPAMalformedAndTrailingJSONUnderCap(t *testing.T) {
	cases := []string{
		`{not json}`,
		`{"result":true} trailing`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			body := &scriptedBody{steps: []scriptedRead{{p: []byte(raw), err: io.EOF}}}
			_, dec, err := opaEvalBody(t, body)
			if err == nil {
				t.Fatalf("Evaluate succeeded with decision %+v, want decode error", dec)
			}
			if dec != (auth.Decision{}) || dec.Allow {
				t.Errorf("decision = %+v, want zero", dec)
			}
			if !strings.Contains(err.Error(), "governance: opa decode:") {
				t.Errorf("error = %q, want opa decode", err.Error())
			}
			if body.closed != 1 {
				t.Errorf("Close called %d times, want 1", body.closed)
			}
		})
	}
}

func TestOPABodyCloseOnSuccessAndError(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		body := &scriptedBody{steps: []scriptedRead{{p: []byte(`{"result":false}`), err: io.EOF}}}
		_, dec, err := opaEvalBody(t, body)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if dec.Allow {
			t.Errorf("Allow = true, want policy deny")
		}
		if body.closed != 1 {
			t.Errorf("Close called %d times, want 1", body.closed)
		}
	})
	t.Run("read error", func(t *testing.T) {
		body := &scriptedBody{steps: []scriptedRead{{p: []byte(`{"result":true}`), err: errors.New(opaLeakMarker)}}}
		_, _, err := opaEvalBody(t, body)
		if err == nil {
			t.Fatal("Evaluate succeeded, want read error")
		}
		if body.closed != 1 {
			t.Errorf("Close called %d times, want 1", body.closed)
		}
	})
	t.Run("too large", func(t *testing.T) {
		body := &fillBody{prefix: []byte(`{"result":true}`), fill: ' ', total: maxOPABody + 1}
		_, _, err := opaEvalBody(t, body)
		if err == nil {
			t.Fatal("Evaluate succeeded, want too-large error")
		}
		if body.closed != 1 {
			t.Errorf("Close called %d times, want 1", body.closed)
		}
	})
}

func TestOPAOversizedReaderNotReadPastCapPlusOne(t *testing.T) {
	body := &fillBody{
		prefix: []byte(`{"result":true}`),
		fill:   'A',
		total:  8 << 20,
	}
	_, dec, err := opaEvalBody(t, body)
	assertZeroDecisionError(t, dec, err, opaBodyTooLargeErrText)
	if body.got > maxOPABody+1 {
		t.Errorf("underlying Read delivered %d bytes, want at most cap+1=%d", body.got, maxOPABody+1)
	}
	if body.closed != 1 {
		t.Errorf("Close called %d times, want 1", body.closed)
	}
}

func TestOPAReadErrorPropagatesThroughChain(t *testing.T) {
	body := &scriptedBody{steps: []scriptedRead{{
		p:   []byte(`{"result":true}`),
		err: errors.New(opaLeakMarker),
	}}}
	oe, err := NewOPAEvaluator("http://127.0.0.1:1", "authz.allow", "", staticDoer{
		resp: &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)},
	})
	if err != nil {
		t.Fatalf("NewOPAEvaluator: %v", err)
	}
	ch := composeEvaluators(nil, stubEval{allow: true}, oe)
	dec, err := ch.Evaluate(context.Background(), opaReq())
	assertZeroDecisionError(t, dec, err, opaBodyReadErrText)
	if body.closed != 1 {
		t.Errorf("Close called %d times, want 1", body.closed)
	}
}
