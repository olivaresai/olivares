// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfmcpportals

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnvelopeErrorRedactsReflectedCredential(t *testing.T) {
	const canary = "cf-opaque-credential-canary"
	for _, status := range []int{200, 403} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+canary {
				t.Error("credential not sent")
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"rejected cf-opaque-credential-canary"}]}`))
		}))
		_, err := newClient(s.URL, canary, s.Client()).fetch(context.Background(), "/fixture", nil)
		s.Close()
		var fault *apiFault
		if !errors.As(err, &fault) || fault.status != status || len(fault.errs) != 1 || fault.errs[0].Code != 1000 {
			t.Fatal("HTTP or provider status lost")
		}
		if strings.Contains(err.Error(), canary) || strings.Contains(fault.errs[0].Message, canary) {
			t.Fatal("structured provider error retained credential")
		}
	}
}
