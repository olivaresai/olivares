// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/license"
)

// the two engine defects the Codex sol max contrast of the NIS 2 console surfaced
// (F1 and F2). Both are about
// the same thing from opposite ends: an operator being told something that is not true about
// their own document.

// --- F1: an over-length evidence document is REJECTED, never truncated ------------

// TestBoundedBodyRejectsOverLengthRatherThanTruncating pins the fix for the P0.
//
// Before it, readBoundedBody read through io.LimitReader(r.Body, maxReqBytes) and returned
// whatever came back. A 1 MiB + 1 byte document therefore arrived at the caller as a COMPLETE
// 1 MiB document: hashed into the minimal-data anchor, handed to the packager, persisted, and
// answered with a 201. Nothing in the stack could see the difference, because a JSON parser
// that accepts one value followed by EOF accepts the prefix too.
//
// The boundary cases are the test: exactly at the cap must still pass, or the fix would have
// traded a silent truncation for a silent refusal of valid documents.
func TestBoundedBodyRejectsOverLengthRatherThanTruncating(t *testing.T) {
	for _, tc := range []struct {
		name     string
		size     int
		wantCode int
		wantOK   bool
	}{
		{name: "one byte under the cap", size: maxReqBytes - 1, wantCode: http.StatusOK, wantOK: true},
		{name: "exactly at the cap", size: maxReqBytes, wantCode: http.StatusOK, wantOK: true},
		{name: "one byte over the cap", size: maxReqBytes + 1, wantCode: http.StatusRequestEntityTooLarge},
		{name: "far over the cap", size: maxReqBytes * 2, wantCode: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.Repeat([]byte("a"), tc.size)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/x", bytes.NewReader(body))

			got, ok := readBoundedBody(rec, req, "incident impact document")

			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (status %d, body %s)", ok, tc.wantOK, rec.Code, rec.Body.String())
			}
			if !tc.wantOK {
				if rec.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantCode, rec.Body.String())
				}
				// The refusal has to say WHICH document and WHY, or an operator cannot act
				// on it — and "never truncated" is the part that stops them assuming the
				// engine kept a usable prefix.
				for _, want := range []string{"incident impact document", "never truncated"} {
					if !strings.Contains(rec.Body.String(), want) {
						t.Errorf("refusal %s does not mention %q", rec.Body.String(), want)
					}
				}
				// AND NOTHING IS HANDED BACK. Returning the prefix alongside ok=false would
				// leave the old defect one careless caller away.
				if got != nil {
					t.Errorf("an over-length body must yield no bytes at all; got %d", len(got))
				}
				return
			}
			// The accepted cases are byte-identical: this helper exists so the exact bytes
			// can be hashed, so a fix that shortened a VALID document would be the same bug.
			if len(got) != tc.size || !bytes.Equal(got, body) {
				t.Fatalf("accepted body is not verbatim: got %d bytes, want %d", len(got), tc.size)
			}
		})
	}
}

// TestBoundedBodyStillRefusesAnEmptyDocument is the direction of non-firing: a helper that
// rejected everything would pass every "rejects" assertion above.
func TestBoundedBodyStillRefusesAnEmptyDocument(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", bytes.NewReader(nil))

	if _, ok := readBoundedBody(rec, req, "OSCAL document"); ok {
		t.Fatal("an empty body must be refused")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OSCAL document") {
		t.Errorf("the empty-body refusal must name the caller's document: %s", rec.Body.String())
	}
}

// --- F2: an entitlement refusal is 403, not "your document was rejected" -----------

// TestEntitlementRefusalIsNot422OnTheInterceptingWriters pins the fix for the second P0.
//
// addonrefusal_test.go asserts the module maps license.ErrAddonRequiresLicense to 403, and its
// comment claims that mapping it "once here … covers every handler in the module" because the
// writers "both fall through to writeStoreError". That was FALSE for the two writers that
// intercept first: writeNIS2Error and writeRegisterError each ran an errors.As on their own
// rejection wrapper before falling through, and both handlers wrap EVERY packager error in
// that wrapper (nis2incident.go:149, doraincident.go:119, regpackage.go:275).
//
// So a linked-but-unentitled add-on came out as 422 "classification rejected" — the console
// showed an operator that their impact document was bad, and they would edit a document that
// was never the problem while the commercial boundary appeared nowhere.
func TestEntitlementRefusalIsNot422OnTheInterceptingWriters(t *testing.T) {
	writers := map[string]func(http.ResponseWriter, error){
		"writeNIS2Error":     writeNIS2Error,
		"writeRegisterError": writeRegisterError,
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			// Exactly how it arrives in production: the packager's typed refusal, wrapped by
			// the handler in its own rejection type on the way out.
			refusal := license.AddonRequired("nis2incident", "compliance.nis2.incident.classify")
			for wrapName, wrapped := range map[string]error{
				"bare":                  refusal,
				"through the wrapper":   errNIS2Rejected{refusal},
				"through the DORA wrap": errRegisterRejected{refusal},
			} {
				t.Run(wrapName, func(t *testing.T) {
					rec := httptest.NewRecorder()
					write(rec, wrapped)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
					}
					body := rec.Body.String()
					// It has to name the add-on and say what still works, or 403 reads as
					// "ask your admin for a permission" — which nobody can grant.
					for _, want := range []string{"nis2incident", "exporting your data are unaffected"} {
						if !strings.Contains(body, want) {
							t.Errorf("body %s does not mention %q", body, want)
						}
					}
					if strings.Contains(body, "rejected") {
						t.Errorf("an entitlement refusal must not be reported as a rejected document: %s", body)
					}
				})
			}
		})
	}
}

// TestDocumentRejectionIsStill422 is the direction of non-firing for the fix above: a writer
// that sent everything to writeStoreError would pass every 403 assertion and silently turn a
// genuinely bad document into a 500.
func TestDocumentRejectionIsStill422(t *testing.T) {
	for name, tc := range map[string]struct {
		write http.HandlerFunc
		err   error
		want  string
	}{
		"nis2": {
			write: func(w http.ResponseWriter, _ *http.Request) {},
			err:   errNIS2Rejected{errors.New("awareness_at is required")},
			want:  "awareness_at is required",
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeNIS2Error(rec, tc.err)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("the packager's reason must survive: %s", rec.Body.String())
			}
		})
	}

	rec := httptest.NewRecorder()
	writeRegisterError(rec, errRegisterRejected{errors.New("B_01.01.0010 is missing")})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("register rejection status = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "B_01.01.0010 is missing") {
		t.Errorf("the packager's reason must survive: %s", rec.Body.String())
	}
}
