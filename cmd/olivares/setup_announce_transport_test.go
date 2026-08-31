// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// newAnnounceTestEngine boots a VIRGIN engine — no token minted — so announceSetup takes
// the `created` branch, which is the one that prints the transport sentence. The sibling
// fixture bootPendingSetup deliberately mints first and lands on the OTHER branch.
func newAnnounceTestEngine(t *testing.T) *engine {
	t.Helper()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// THE FIRST-BOOT BANNER MUST DESCRIBE THE TRANSPORT IT IS ACTUALLY SERVING (2026-08-06).
//
// It used to say, unconditionally, that the console "serves HTTPS with a self-signed
// certificate on first boot — your browser will warn once; that is expected". Under
// --insecure every clause of that is false: TLS is off, EnsureTLSCert never runs, the URL
// printed two lines above already says http://, and the warning the operator is told to
// expect never arrives. One screen contradicting itself, and the half that was right was
// the half nobody reads twice.
//
// The cause was a seam, not a sentence: consoleURL already took the posture and
// announceSetup did not, so the two halves of one message could not agree even in
// principle.
//
// What it costs is not cosmetic either, which is why the insecure branch has to SAY it: the
// single-use setup token printed in this very banner is posted back to the engine, and on a
// plaintext listener it travels in the clear.
func TestFirstBootBannerDescribesTheRealTransport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		insecure bool
		baseURL  string
		want     []string
		reject   []string
	}{
		{
			name:     "TLS on: the self-signed warning is true and stays",
			insecure: false,
			baseURL:  "https://127.0.0.1:8443",
			want:     []string{"serves HTTPS", "self-signed", "browser will warn once"},
			reject:   []string{"PLAIN HTTP", "in the clear"},
		},
		{
			name:     "--insecure: no HTTPS claim, no phantom browser warning, and the token exposure is named",
			insecure: true,
			baseURL:  "http://127.0.0.1:8901",
			want:     []string{"TLS is OFF", "PLAIN HTTP", "in the clear"},
			// The exact regression: an HTTPS promise on a plaintext listener, and a browser
			// warning the operator is told to expect and will never see.
			reject: []string{"serves HTTPS", "self-signed", "browser will warn"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := newAnnounceTestEngine(t)
			var out bytes.Buffer
			if err := announceSetup(context.Background(), &out, eng, tc.baseURL, tc.insecure); err != nil {
				t.Fatalf("announceSetup: %v", err)
			}
			got := out.String()
			if !strings.Contains(got, "FIRST-BOOT SETUP") {
				t.Fatalf("this fixture did not reach the first-boot branch, so it asserts nothing:\n%s", got)
			}
			// The URL and the prose must agree — that agreement IS the defect this closes.
			if !strings.Contains(got, tc.baseURL) {
				t.Errorf("the banner does not print the console URL %q:\n%s", tc.baseURL, got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("banner is missing %q:\n%s", w, got)
				}
			}
			for _, r := range tc.reject {
				if strings.Contains(got, r) {
					t.Errorf("banner still claims %q on a %s transport:\n%s", r,
						map[bool]string{true: "PLAINTEXT", false: "TLS"}[tc.insecure], got)
				}
			}
		})
	}
}
