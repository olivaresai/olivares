// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// SR2 46ff P1: the output framer splits lines, drops a trailing CR/LF and cuts a
// stderr line at the cap before the per-frame redactor sees it, so an exact match
// on the whole value missed a multiline value, a value ending in a newline and the
// part of a value left before the cut.

// frames runs the production diagnostic framer over text and returns its frames
// after the session's redactor, as the bridge would store them.
func framedDiagnostics(t *testing.T, r *secretRedactor, lineCap int, text string) []string {
	t.Helper()
	out := make(chan OutputFrame, 64)
	proc := &procProcess{out: out, abandon: make(chan struct{})}
	(&procRunner{lineCap: lineCap}).pumpDiagnostics(proc, strings.NewReader(text))
	close(out)
	var frames []string
	for f := range out {
		frames = append(frames, string(r.apply(f.Data)))
	}
	return frames
}

// The canary values are assembled at run time, so the source holds no token-shaped
// literal for a secret scanner to report (the public export scans every file); the
// values the test runs with are unchanged.
var (
	leakCanaryPrefix   = "ghp" + "_"
	leakCanaryTrailing = leakCanaryPrefix + "LEAKCANARY_trailing_0123"
	leakCanaryCut      = leakCanaryPrefix + "LEAKCANARY_cut_0123456789"
)

func TestSecretEnv_FramedDiagnosticsNeverKeepAPartOfAValue(t *testing.T) {
	t.Parallel()
	key := "-----BEGIN FH TEST KEY-----\r\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\r\n-----END FH TEST KEY-----\n"
	for _, tc := range []struct {
		name, value, printed string
		lineCap              int
	}{
		{"a multiline value, one frame per line", key, "loaded " + key + "done\n", 0},
		{"a value ending in a newline", leakCanaryTrailing + "\n", "token " + leakCanaryTrailing + "\n", 0},
		{"a value cut at the line cap", leakCanaryCut, "error: token=" + leakCanaryCut + " rejected\n", 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSecretRedactor([]SecretEnvRef{{Env: "KEY", Secret: "env/key"}}, []EnvVar{{Name: "KEY", Value: tc.value}})
			frames := framedDiagnostics(t, r, tc.lineCap, tc.printed)
			all := strings.Join(frames, "\n")
			for _, fragment := range []string{"MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", "LEAKCANARY", leakCanaryPrefix + "LEAK", leakCanaryPrefix} {
				if strings.Contains(all, fragment) {
					t.Fatalf("a part of the value survived framing: %q", frames)
				}
			}
			if !strings.Contains(all, "[secret env/key]") {
				t.Fatalf("no marker in %q", frames)
			}
		})
	}
	// Text before the cut that is not the beginning of a value stays as it is.
	r := newSecretRedactor([]SecretEnvRef{{Env: "KEY", Secret: "env/key"}}, []EnvVar{{Name: "KEY", Value: leakCanaryCut}})
	if got := framedDiagnostics(t, r, 12, "plain words that run long\n"); len(got) != 1 || got[0] != "plain words "+diagnosticTruncatedMark {
		t.Fatalf("an unrelated cut line = %q", got)
	}
}

// The cut withholding is linear in the value: a 64 KiB value and a cut line of the
// same size cost one pass each, and the span covers the marker written.
func TestSecretEnv_CutValueSpanIsTheMarker(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("ab", 32*1024)
	r := newSecretRedactor([]SecretEnvRef{{Env: "BIG", Secret: "env/big"}}, []EnvVar{{Name: "BIG", Value: value}})
	data := []byte("x" + value[:len(value)-1] + diagnosticTruncatedMark)
	out, spans := r.applyWithSpans(data)
	if len(spans) != 1 || string(out[spans[0].Start:spans[0].End]) != "[secret env/big]" || string(out) != "x[secret env/big]"+diagnosticTruncatedMark {
		t.Fatalf("cut value = %q spans %v", out[:min(len(out), 80)], spans)
	}
}

// A line too short to withhold refuses the launch by the variable's name.
func TestSecretEnv_AValueWithAShortLineRefusesTheLaunchByVariable(t *testing.T) {
	t.Parallel()
	m, tenant, fr, vault, _ := secretEnvHarness(t)
	vault.mu.Lock()
	vault.values[tenant.String()+"|env/github"] = "ab\nLEAKCANARY_long_line_0123"
	vault.mu.Unlock()
	_, err := m.createRun(context.Background(), tenant, secretEnvParams(githubSecretEnv, true))
	if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "GITHUB_TOKEN") || strings.Contains(err.Error(), "LEAKCANARY") {
		t.Fatalf("a value with a short line = %v, want 409 naming GITHUB_TOKEN and not the value", err)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.specs) != 0 {
		t.Fatal("a refused binding reached the runner")
	}
}

// SR2 report192: the cut is read on the original line, before any value is
// replaced. A shorter value inside the cut beginning of a longer one, or a value
// that reaches across the cut, leaves no part of either before the marker.
func TestSecretEnv_TheCutIsReadOnTheOriginalLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, a, b, printed string
		lineCap             int
		fragments           []string
	}{
		{"a shorter value inside the cut beginning of a longer one", "abcdefgh", "12345678abcdefghZZZ", "12345678abcdefghZZZ\n", 18, []string{"12345678", "abcdefgh"}},
		// "x mid_value_AB_lo" + cut: B's beginning "AB_lo" ends the line and A ends inside it.
		{"a value that reaches across the cut", "mid_value_AB", "AB_long_value_9999", "x mid_value_AB_long_value_9999\n", 17, []string{"mid_value", "_lo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSecretRedactor(
				[]SecretEnvRef{{Env: "A", Secret: "env/a"}, {Env: "B", Secret: "env/b"}},
				[]EnvVar{{Name: "A", Value: tc.a}, {Name: "B", Value: tc.b}},
			)
			frames := framedDiagnostics(t, r, tc.lineCap, tc.printed)
			all := strings.Join(frames, "\n")
			for _, f := range tc.fragments {
				if strings.Contains(all, f) {
					t.Fatalf("%q survived the cut: %q", f, frames)
				}
			}
			if !strings.HasSuffix(all, diagnosticTruncatedMark) {
				t.Fatalf("the cut note is gone: %q", frames)
			}
		})
	}
}

// A value sharing text with the note the framer appends to a cut line could be
// carried or hidden by that note, so the launch refuses it by its variable.
func TestSecretEnv_AValueSharingTextWithTheCutNoteRefusesTheLaunch(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"diagnostic", " [diagnostic truncated]", "trailing-space-key "} {
		m, tenant, fr, vault, _ := secretEnvHarness(t)
		vault.mu.Lock()
		vault.values[tenant.String()+"|env/github"] = value
		vault.mu.Unlock()
		_, err := m.createRun(context.Background(), tenant, secretEnvParams(githubSecretEnv, true))
		if statusOf(err) != http.StatusConflict || !strings.Contains(err.Error(), "GITHUB_TOKEN") || strings.Contains(err.Error(), value) {
			t.Fatalf("value %q = %v, want 409 naming GITHUB_TOKEN and not the value", value, err)
		}
		fr.mu.Lock()
		spawned := len(fr.specs)
		fr.mu.Unlock()
		if spawned != 0 {
			t.Fatalf("value %q reached the runner", value)
		}
	}
}
