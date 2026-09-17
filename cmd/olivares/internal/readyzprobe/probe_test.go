// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package readyzprobe

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckMapsLocalReadyzFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		want       Outcome
		wantStatus int
	}{
		{name: "ready", status: http.StatusOK, want: Ready, wantStatus: http.StatusOK},
		{name: "starting", status: http.StatusServiceUnavailable, want: NotReady, wantStatus: http.StatusServiceUnavailable},
		{name: "wrong endpoint", status: http.StatusNotFound, want: NotReady, wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/readyz" {
					t.Errorf("request = %s %s, want GET /readyz", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			got, err := Check(t.Context(), Config{Origin: server.URL, Timeout: time.Second})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if got.Outcome != tc.want || got.StatusCode != tc.wantStatus {
				t.Fatalf("result = %+v, want outcome=%v status=%d", got, tc.want, tc.wantStatus)
			}
		})
	}
}

// TestCheckRejectsReadyzDown is the mutation witness for the tempting
// `if true` / always-yes probe. scripts/test-compose-ready.sh applies that
// mutation to probe.go and requires this test to turn red.
func TestCheckRejectsReadyzDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	got, err := Check(t.Context(), Config{Origin: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.Outcome != NotReady || got.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz down = %+v, want not-ready HTTP 503", got)
	}
}

func TestCheckTrustsTheExplicitLocalCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	certPath := filepath.Join(t.TempDir(), "tls.crt")
	cert := server.Certificate()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(certPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Check(t.Context(), Config{Origin: server.URL, CACert: certPath, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Check with explicit CA: %v", err)
	}
	if got.Outcome != Ready {
		t.Fatalf("TLS result = %+v, want ready", got)
	}
}

func TestCheckReturnsUnmeasurableWithoutContactingNonLoopback(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{
		"https://example.com",
		"https://user:secret@127.0.0.1:8443",
		"https://127.0.0.1:8443/other",
		"https://127.0.0.1:8443?token=secret",
	} {
		got, err := Check(context.Background(), Config{Origin: origin, Timeout: time.Second})
		if err == nil || got.Outcome != Unmeasurable {
			t.Errorf("Check(%q) = (%+v, %v), want unmeasurable error", origin, got, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("error leaked URL credentials/query: %q", err)
		}
	}
}

func TestCheckDoesNotFollowRedirects(t *testing.T) {
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ready.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ready.URL+"/readyz", http.StatusFound)
	}))
	defer redirect.Close()

	got, err := Check(t.Context(), Config{Origin: redirect.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != NotReady || got.StatusCode != http.StatusFound {
		t.Fatalf("redirect result = %+v, want not-ready HTTP 302", got)
	}
}
