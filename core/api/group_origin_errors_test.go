// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
)

func TestGroupOriginErrorsHaveFixedForbiddenEnvelopes(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, tc := range []struct {
		name, code, message string
		err                 error
	}{
		{"read-only", "group_origin_read_only", "group is managed by another provisioner", auth.ErrGroupOriginReadOnly},
		{"adoption", "group_origin_adopted", "group cannot change provisioner", auth.ErrGroupOriginAdopted},
	} {
		for _, wrapped := range []bool{false, true} {
			err := tc.err
			if wrapped {
				err = fmt.Errorf("internal provisioner context: %w", err)
			}
			t.Run(fmt.Sprintf("%s/wrapped=%t", tc.name, wrapped), func(t *testing.T) {
				r := httptest.NewRequest(http.MethodPut, "/v1/groups/test/role", nil)
				w := httptest.NewRecorder()
				s.writeError(w, r, err)
				var native errorBody
				if decodeErr := json.Unmarshal(w.Body.Bytes(), &native); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if w.Code != http.StatusForbidden || native.Error.Code != tc.code || native.Error.Message != tc.message {
					t.Errorf("native envelope: status=%d code=%q message=%q", w.Code, native.Error.Code, native.Error.Message)
				}
				w = httptest.NewRecorder()
				s.writeSCIMGroupError(w, r, err)
				var provision scim.Error
				if decodeErr := json.Unmarshal(w.Body.Bytes(), &provision); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if w.Code != http.StatusForbidden || provision.Status != "403" || provision.ScimType != scim.TypeMutability || provision.Detail != tc.message {
					t.Errorf("SCIM envelope: status=%d body=%+v", w.Code, provision)
				}
			})
		}
	}
}
