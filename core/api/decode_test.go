// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
