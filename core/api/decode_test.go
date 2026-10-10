// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
)

// A REQUEST BODY IS ONE JSON DOCUMENT — the table anchor for every handler, because
// every request-body decode now goes through api.DecodeRequestBody
// (scripts/check-json-decoders.sh asserts that). The tails below are the ones
// dec.More() provably accepted: it peeks one byte and answers false for a tail
// opening with ']' or '}', so the per-package More() copies still took `{...}}`
// or `{...}\n]{}` as one document. The gitpublish fix (a2b73194) showed the only
// sound check: a second Decode must end in io.EOF.
//
// The cases are RAW strings on purpose: a marshaled fixture cannot express a
// trailing value, so it cannot detect the defect.
func TestDecodeRequestBodySingleDocument(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	const one = `{"name":"single"}`

	decode := func(t *testing.T, body string, spec api.RequestBodySpec) error {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(body))
		rec := httptest.NewRecorder()
		var v payload
		err := api.DecodeRequestBody(rec, req, &v, spec)
		if err == nil && body == one && v.Name != "single" {
			t.Fatalf("decoded without error but Name = %q, the fixture is not exercising the decode", v.Name)
		}
		return err
	}

	t.Run("control: one valid document succeeds", func(t *testing.T) {
		if err := decode(t, one, api.RequestBodySpec{}); err != nil {
			t.Fatalf("= %v, want nil — the fixture is wrong if the control fails", err)
		}
	})
	t.Run("control: trailing whitespace and newline are one document", func(t *testing.T) {
		if err := decode(t, one+" \n\t\n", api.RequestBodySpec{}); err != nil {
			t.Fatalf("= %v, want nil: whitespace is not a second document", err)
		}
	})

	for _, tc := range []struct{ name, tail string }{
		{"stray closing brace", `}`},
		{"stray closing bracket", `]`},
		{"newline bracket then object", "\n]" + `{"name":"ghost"}`},
		{"newline brace then null", "\n}null"},
		{"a second object", `{"name":"ghost"}`},
		{"a second object after a newline", "\n" + `{"name":"ghost"}`},
		{"a second scalar", ` 42`},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			err := decode(t, one+tc.tail, api.RequestBodySpec{})
			if !errors.Is(err, api.ErrTrailingJSON) {
				t.Fatalf("= %v, want ErrTrailingJSON: a body that is not ONE document must be refused", err)
			}
		})
		t.Run("rejects "+tc.name+" on an optional body", func(t *testing.T) {
			err := decode(t, one+tc.tail, api.RequestBodySpec{Optional: true})
			if !errors.Is(err, api.ErrTrailingJSON) {
				t.Fatalf("= %v, want ErrTrailingJSON: optional means empty is valid, not two documents", err)
			}
		})
	}

	t.Run("empty body is an error by default", func(t *testing.T) {
		if err := decode(t, "", api.RequestBodySpec{}); !errors.Is(err, io.EOF) {
			t.Fatalf("= %v, want io.EOF", err)
		}
	})
	t.Run("empty and whitespace-only bodies are valid when optional", func(t *testing.T) {
		for _, body := range []string{"", "  \n\t "} {
			if err := decode(t, body, api.RequestBodySpec{Optional: true}); err != nil {
				t.Fatalf("body %q = %v, want nil (v stays zero)", body, err)
			}
		}
	})
	t.Run("unknown fields are rejected by default", func(t *testing.T) {
		if err := decode(t, `{"name":"a","smuggled":true}`, api.RequestBodySpec{}); err == nil {
			t.Fatal("= nil, want an unknown-field rejection")
		}
	})
	t.Run("unknown fields pass when the route allows them", func(t *testing.T) {
		if err := decode(t, `{"name":"a","smuggled":true}`, api.RequestBodySpec{AllowUnknownFields: true}); err != nil {
			t.Fatalf("= %v, want nil", err)
		}
	})
}

func TestDecodeRequestBodySizeCapIsExact(t *testing.T) {
	var v struct {
		Name string `json:"name"`
	}
	decode := func(t *testing.T, body string, maxBytes int64) error {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(body))
		rec := httptest.NewRecorder()
		return api.DecodeRequestBody(rec, req, &v, api.RequestBodySpec{MaxBytes: maxBytes})
	}

	// `{"name":"` + padding + `"}` exactly at the cap must pass: the cap refuses a
	// body LONGER than the limit, never one that fits — io.LimitReader could not
	// tell the two apart and accepted a truncated body as whole.
	padding := strings.Repeat("a", 64-len(`{"name":""}`))
	exact := `{"name":"` + padding + `"}`
	if int64(len(exact)) != 64 {
		t.Fatalf("fixture length = %d, want exactly 64", len(exact))
	}
	if err := decode(t, exact, 64); err != nil {
		t.Fatalf("body at exactly the cap = %v, want nil", err)
	}
	if err := decode(t, exact+" ", 64); err == nil {
		t.Fatal("one byte past the cap = nil, want an error (rejected, never truncated)")
	}
	if err := decode(t, exact+`{"name":"ghost"}`, 64); err == nil {
		t.Fatal("a second document past the cap = nil, want an error")
	}
}

func TestDecodeRequestBodyFieldErrors(t *testing.T) {
	type nested struct {
		ID string `json:"id"`
	}
	type payload struct {
		Request nested `json:"request"`
	}
	for _, tc := range []struct{ name, body, want string }{
		{"unknown nested field", `{"request":{"action":"read"}}`, "request.action"},
		{"wrong nested type", `{"request":{"id":42}}`, "request.id"},
		{"wrong object type", `{"request":"private-value"}`, "request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			var v payload
			err := api.DecodeRequestBody(httptest.NewRecorder(), req, &v, api.RequestBodySpec{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want JSON field path %q", err, tc.want)
			}
			for _, leak := range []string{"api_test", "payload", "nested", "private-value", "Go struct"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error exposes %q: %v", leak, err)
				}
			}
		})
	}
}

func TestWorkspaceDecodeErrorsNameFields(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "decode-fields")
	for _, tc := range []struct {
		body  any
		field string
	}{
		{map[string]any{"bogus": 1}, "bogus"},
		{map[string]any{"name": 42}, "name"},
	} {
		got := h.do("POST", "/v1/workspaces", admin, tc.body, tenantHdr(tenant))
		envelope, ok := got.body["error"].(map[string]any)
		if !ok || got.code != http.StatusBadRequest || envelope["code"] != "bad_request" {
			t.Fatalf("error contract changed: %d %s", got.code, got.raw)
		}
		msg, _ := envelope["message"].(string)
		if !strings.Contains(msg, tc.field) {
			t.Errorf("message %q does not name %s", msg, tc.field)
		}
	}
}

func TestDecodeRequestBodyWirePaths(t *testing.T) {
	type leaf struct {
		ID string `json:"id"`
	}
	type payload struct {
		Attributes map[string]any `json:"attributes"`
		Request    leaf           `json:"request"`
	}
	type Embedded struct {
		Email string `json:"email"`
	}
	type One struct {
		X leaf `json:"one"`
	}
	type Two struct {
		X leaf `json:"two"`
	}
	type SharedOne struct {
		X leaf `json:"shared"`
	}
	type SharedTwo struct {
		Y leaf `json:"shared"`
	}
	// This ambiguity is intentional input to encoding/json; construct it at
	// runtime because go vet correctly rejects repeated tags in source structs.
	ambiguous := reflect.New(reflect.StructOf([]reflect.StructField{
		{Name: "SharedOne", Type: reflect.TypeFor[SharedOne](), Anonymous: true},
		{Name: "SharedTwo", Type: reflect.TypeFor[SharedTwo](), Anonymous: true},
	})).Interface()
	for _, tc := range []struct {
		name, body, path string
		dst              any
	}{
		{"map leaf does not hide unknown struct leaf", `{"attributes":{"action":"valid"},"request":{"action":"private-value"}}`, "request.action", &payload{}},
		{"custom value does not hide later unknown field", `{"metadata":{},"request":{"action":1}}`, "request.action", &struct {
			Metadata json.RawMessage `json:"metadata"`
			Request  leaf            `json:"request"`
		}{}},
		{"overflow number keeps its field", `{"request":{"id":1e1000}}`, "request.id", &payload{}},
		{"array unknown member", `{"items":[{"id":"a"},{"action":true}]}`, "items[1].action", &struct {
			Items []leaf `json:"items"`
		}{}},
		{"array type mismatch", `{"items":[{"id":"a"},{"id":42}]}`, "items[1].id", &struct {
			Items []leaf `json:"items"`
		}{}},
		{"promoted type mismatch uses wire name", `{"email":42}`, "email", &struct{ Embedded }{}},
		{"promoted unknown path uses wire name", `{"request":{"action":true}}`, "request.action", &struct{ payload }{}},
		{"tagged embedded object", `{"account":{"email":42}}`, "account.email", &struct {
			Embedded `json:"account"`
		}{}},
		{"JSON tags distinguish shadowed Go names", `{"one":{"bad":1},"tail":{"bad":1}}`, "one.bad", &struct {
			One
			Two
			Tail leaf `json:"tail"`
		}{}},
		{"ambiguous tags reject the parent", `{"shared":{"bad":1}}`, "shared", ambiguous},
		{"escaped member names stay quoted", `{"request":{"odd.name\n":42}}`, `request["odd.name\n"]`, &payload{}},
		{"invalid tag falls back to wire field name", `{"X":{"bad":1},"tail":{"bad":1}}`, "X.bad", &struct {
			X    leaf `json:"bad\\key"`
			Tail leaf `json:"tail"`
		}{}},
		{"root type mismatch", `"private-value"`, "$", &payload{}},
		{"typed map member mismatch", `{"request":{"id":42}}`, "request.id", &struct {
			Request map[string]string `json:"request"`
		}{}},
		{"typed map key mismatch", `{"request":{"bad":42}}`, "request.bad", &struct {
			Request map[int]int `json:"request"`
		}{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			err := api.DecodeRequestBody(httptest.NewRecorder(), req, tc.dst, api.RequestBodySpec{})
			want := strconv.QuoteToASCII(tc.path)
			if err == nil || !strings.HasSuffix(err.Error(), want) {
				t.Fatalf("error = %v, want wire path %s", err, want)
			}
			for _, leak := range []string{"private-value", "Go struct", "Embedded", "SharedOne", "SharedTwo"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error exposes %q: %v", leak, err)
				}
			}
		})
	}
}

func TestDecodeRequestBodyErrorMessagesPreserveOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		body string
		spec api.RequestBodySpec
	}{
		{`{not json`, api.RequestBodySpec{}},
		{`{"name":"private-value"} {}`, api.RequestBodySpec{}},
		{`{"name":"private-value"}`, api.RequestBodySpec{MaxBytes: 8}},
		{"", api.RequestBodySpec{}},
	} {
		var v struct {
			Name string `json:"name"`
		}
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		err := api.DecodeRequestBody(httptest.NewRecorder(), req, &v, tc.spec)
		if err == nil {
			t.Fatalf("invalid body accepted: %q", tc.body)
		}
		if got := api.RequestBodyErrorMessage(err, "existing message"); got != "existing message" {
			t.Fatalf("unclassified failure changed fallback: %q", got)
		}
	}
	if got := api.RequestBodyErrorMessage(errors.New("private-value"), "existing message"); got != "existing message" {
		t.Fatal(got)
	}
	if got := api.RequestBodyErrorMessage(nil, "existing message"); got != "existing message" {
		t.Fatal(got)
	}
	var v struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":42}`))
	err := api.DecodeRequestBody(httptest.NewRecorder(), req, &v, api.RequestBodySpec{})
	var mismatch *json.UnmarshalTypeError
	if !errors.As(err, &mismatch) {
		t.Fatalf("decoder cause lost: %v", err)
	}
	wrapped := fmt.Errorf("private wrapper: %w", err)
	if got := api.RequestBodyErrorMessage(wrapped, "existing message"); got != `existing message: invalid value for field "name"` {
		t.Fatalf("wrapped diagnostic = %q", got)
	}
}

func TestDecodeRequestBodyDiagnosticIsBounded(t *testing.T) {
	var v struct{}
	body := `{"` + strings.Repeat("x", 1024) + `":"private-value"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	err := api.DecodeRequestBody(httptest.NewRecorder(), req, &v, api.RequestBodySpec{})
	if err == nil || len(err.Error()) > 300 || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("unbounded or unsafe diagnostic: %v", err)
	}
}

func TestUserDecodeErrorDoesNotExposeEmbeddedGoName(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "decode-user-fields")
	got := h.do("POST", "/v1/users", admin, map[string]any{"email": 42}, tenantHdr(tenant))
	envelope, ok := got.body["error"].(map[string]any)
	if !ok || got.code != http.StatusBadRequest || envelope["code"] != "bad_request" {
		t.Fatalf("error contract changed: %d %s", got.code, got.raw)
	}
	msg, _ := envelope["message"].(string)
	if msg != `invalid JSON body: invalid value for field "email"` {
		t.Fatalf("wire name diagnostic = %q", msg)
	}
}

func TestDecodeRequestBodyLongPathDoesNotStall(t *testing.T) {
	type recursiveMap map[string]recursiveMap
	type recursiveSlice []recursiveSlice
	for _, tc := range []struct {
		name, body, path string
		dst              any
	}{
		{"long array member", `{"items":{"` + strings.Repeat("a", 248) + `":[1]},"bad":42}`, "bad", &struct {
			Items map[string]any `json:"items"`
			Bad   string         `json:"bad"`
		}{}},
		{"recursive map type", `{"bad":42}`, "bad", new(recursiveMap)},
		{"recursive slice type", `42`, "$", new(recursiveSlice)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
				done <- api.DecodeRequestBody(httptest.NewRecorder(), req, tc.dst, api.RequestBodySpec{})
			}()
			select {
			case err := <-done:
				if err == nil || !strings.HasSuffix(err.Error(), strconv.QuoteToASCII(tc.path)) {
					t.Fatalf("diagnostic = %v, want %s", err, tc.path)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("diagnostic traversal stalled")
			}
		})
	}
}

// relativeJSONText deliberately returns a standard type error with an offset
// relative to its own value, as a custom decoder may do.
type relativeJSONText string

func (v *relativeJSONText) UnmarshalJSON(body []byte) error {
	var text string
	return json.Unmarshal(body, &text)
}

type wrappedJSONText string

func (v *wrappedJSONText) UnmarshalJSON(body []byte) error {
	var text string
	if err := json.Unmarshal(body, &text); err != nil {
		return fmt.Errorf("private wrapper: %w", err)
	}
	return nil
}

func TestDecodeRequestBodyCustomTypeErrorsKeepFallback(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		dst        any
	}{
		{"direct", `{"prefix":"ok","x":42}`, &struct {
			Prefix string           `json:"prefix"`
			X      relativeJSONText `json:"x"`
		}{}},
		{"slice", `{"prefix":"ok","x":[42]}`, &struct {
			Prefix string             `json:"prefix"`
			X      []relativeJSONText `json:"x"`
		}{}},
		{"map", `{"x":42}`, &map[string]relativeJSONText{}},
		{"root", `42`, new(relativeJSONText)},
		{"wrapped", `{"x":42}`, &struct {
			X wrappedJSONText `json:"x"`
		}{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			err := api.DecodeRequestBody(httptest.NewRecorder(), req, tc.dst, api.RequestBodySpec{})
			var mismatch *json.UnmarshalTypeError
			if !errors.As(err, &mismatch) {
				t.Fatalf("custom decoder cause lost: %v", err)
			}
			if got := api.RequestBodyErrorMessage(err, "existing message"); got != "existing message" {
				t.Fatalf("relative custom offset must keep fallback, got %q", got)
			}
		})
	}
}
