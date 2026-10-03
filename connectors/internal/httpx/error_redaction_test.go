// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDirectoryErrorsRedactReflectedCredentials(t *testing.T) {
	const credential = "directory-canary-opaque"
	for _, raw := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Vault-Token") != credential {
				t.Error("credential was not sent")
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("rejected " + credential + "\nCookie: unknown-cookie-canary"))
		}))
		c := New(s.URL, s.Client(), Header("X-Vault-Token", credential, credential), nil)
		var err error
		if raw {
			_, err = c.GetRaw(context.Background(), "/fail", nil)
		} else {
			err = c.GetJSON(context.Background(), "/fail", nil, nil)
		}
		s.Close()
		var status *StatusError
		if !errors.As(err, &status) || status.Status != http.StatusUnauthorized {
			t.Fatal("typed status lost")
		}
		for _, canary := range []string{credential, "unknown-cookie-canary"} {
			if strings.Contains(err.Error(), canary) || strings.Contains(status.Excerpt, canary) {
				t.Fatal("directory error retained synthetic secret")
			}
		}
	}
}
