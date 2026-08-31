// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestWriteStoreErrorMapsKnownStoreErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, http.StatusOK},
		{"not_found", store.ErrNotFound, http.StatusNotFound},
		{"conflict", store.ErrConflict, http.StatusConflict},
		{"bad_query", store.ErrCursorWithSort, http.StatusBadRequest},
		{"unknown", assertErr("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeStoreError(rec, tt.err)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestDecodeJSONRejectsUnknownFieldsAndListQueryParsesBounds(t *testing.T) {
	var req registerTargetRequest
	rec := httptest.NewRecorder()
	ok := decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"agent_ref":"a","extra":true}`)), &req)
	if ok || rec.Code != http.StatusBadRequest {
		t.Fatalf("decodeJSON unknown field ok=%v code=%d", ok, rec.Code)
	}

	q := listQuery(httptest.NewRequest(http.MethodGet, "/?cursor=abc&limit=25", nil))
	if q.Cursor != "abc" || q.Limit != 25 {
		t.Fatalf("listQuery = %+v, want cursor abc limit 25", q)
	}
	q = listQuery(httptest.NewRequest(http.MethodGet, "/?limit=not-int", nil))
	if q.Limit != 0 {
		t.Fatalf("invalid limit parsed as %d, want zero", q.Limit)
	}
}

func TestSmallHelpersClampIDsAndSeverity(t *testing.T) {
	wantClamp := "abc" + string(rune(0x2026))
	if got := clamp("abcdef", 3); got != wantClamp {
		t.Fatalf("clamp = %q, want %q", got, wantClamp)
	}
	id := model.NewID()
	if got, ok := idParam(" " + id.String() + " "); !ok || got != id {
		t.Fatalf("idParam = %q/%v, want %q/true", got, ok, id)
	}
	if _, ok := idParam(""); ok {
		t.Fatal("empty idParam ok=true, want false")
	}
	if sevToCore(sdkmodel.SeverityCritical) != model.SeverityCritical {
		t.Fatal("critical severity did not map to core critical")
	}
	if sevToCore(sdkmodel.SeverityInfo) != model.SeverityLow {
		t.Fatal("info severity did not collapse to low")
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
