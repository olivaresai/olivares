// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/modules/sessions"
)

// tokenFileEnv builds the getenv the composition root actually reads, wiring the
// FILE credential path (and nothing else) at the given path.
func tokenFileEnv(path string) func(string) string {
	return func(k string) string {
		if k == envSessionTokenFile {
			return path
		}
		return ""
	}
}

// wiredTokenFileSource returns the REAL adapter the boot path wires: no fake stands
// in for executor.FileTokenSource, and the diagnostic logger is the real one.
func wiredTokenFileSource(t *testing.T, path string) (sessions.CredentialSource, *bytes.Buffer) {
	t.Helper()
	var diag bytes.Buffer
	log := slog.New(slog.NewTextHandler(&diag, &slog.HandlerOptions{Level: slog.LevelDebug}))
	src, kind := sessionCredentialSourceWithDiagnostics(tokenFileEnv(path), nil, log)
	if src == nil {
		t.Fatalf("a configured %s wired NO credential source", envSessionTokenFile)
	}
	if !strings.Contains(kind, "token file") {
		t.Fatalf("credential kind = %q, want the rotated token file source", kind)
	}
	return src, &diag
}

func mintThroughBoot(t *testing.T, ctx context.Context, src sessions.CredentialSource) (sessions.Credential, error) {
	t.Helper()
	return src.Mint(ctx, sessions.CredentialRequest{
		RunRef: "run_01JTOKENFILE", Transport: sessions.TransportStreamJSON,
	})
}

// The control: a real, readable token file still mints. Without it every assertion
// below would pass on an adapter that refused unconditionally.
func TestTokenFileSourceStillMintsAReadableToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "session-token")
	if err := os.WriteFile(path, []byte("  sk-ant-oat-local-fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, _ := wiredTokenFileSource(t, path)

	cred, err := mintThroughBoot(t, context.Background(), src)
	if err != nil {
		t.Fatalf("a readable token file was refused: %v", err)
	}
	if cred.Token != "sk-ant-oat-local-fixture" {
		t.Fatalf("minted token = %q, want the file's trimmed content", cred.Token)
	}
	if cred.Scheme != "session-token-file" || cred.ID == "" || cred.NotAfter.IsZero() {
		t.Fatalf("credential = %+v, want the non-sensitive id/scheme/expiry the module records", cred)
	}
	if strings.Contains(cred.ID, cred.Token) {
		t.Fatalf("the credential id %q carries the token itself", cred.ID)
	}
}

// A CONFIGURED token file that cannot be read must reach module II as the
// classification that answers 503, while KEEPING the executor's own cause for
// internal inspection. Before this wiring the raw mint error travelled
// unclassified and the operator received 500 "internal error".
func TestTokenFileReadFailureIsClassifiedAsCredentialUnavailable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty-token")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(dir, "unreadable-token")
	if err := os.WriteFile(unreadable, []byte("sk-ant-oat-local-fixture"), 0o000); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		skip bool
	}{
		{name: "missing", path: filepath.Join(dir, "absent", "session-token")},
		{name: "empty", path: empty},
		// A root process reads a 0o000 file regardless, which would make this case
		// assert nothing; it is skipped rather than silently green.
		{name: "unreadable", path: unreadable, skip: os.Geteuid() == 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip {
				t.Skip("running as uid 0: file permissions cannot make a read fail")
			}
			src, diag := wiredTokenFileSource(t, tc.path)
			cred, err := mintThroughBoot(t, context.Background(), src)
			if err == nil {
				t.Fatalf("an %s token file MINTED %+v", tc.name, cred)
			}
			if !errors.Is(err, sessions.ErrCredentialUnavailable) {
				t.Fatalf("err = %v, which module II cannot classify: it falls through "+
					"denyClosedErr and the operator receives 500 internal error", err)
			}
			// Identity is PRESERVED for internal inspection: the executor's own
			// deny-closed sentinel is still reachable through the wrapper.
			if !errors.Is(err, executor.ErrNoCredentialSource) {
				t.Fatalf("err = %v lost the executor cause; classification must not replace identity", err)
			}
			// REDACTION on both the error an operator may see logged and the
			// diagnostic line: no token bytes, no expanded path, no run ref.
			for what, text := range map[string]string{"error": err.Error(), "diagnostic": diag.String()} {
				for _, forbidden := range []string{"sk-ant-oat-local-fixture", tc.path, dir, "run_01JTOKENFILE"} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("the %s value %q carries %q", what, text, forbidden)
					}
				}
			}
			// The diagnostic still has to be USEFUL: it names the variable the
			// operator must fix, which is the whole point of the boundary.
			if !strings.Contains(diag.String(), envSessionTokenFile) {
				t.Fatalf("the operator diagnostic %q does not name %s", diag.String(), envSessionTokenFile)
			}
		})
	}
}

// Canceled or expired work is not a verdict about the credential source. If the
// adapter classified it, a client that hung up would be reported to the next
// operator as a broken deployment.
func TestTokenFileSourceKeepsCancellationDistinct(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "session-token")
	if err := os.WriteFile(path, []byte("sk-ant-oat-local-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, _ := wiredTokenFileSource(t, filepath.Join(filepath.Dir(path), "absent-token"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := mintThroughBoot(t, ctx, src)
	if err == nil {
		t.Fatal("a canceled mint returned a credential")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation the caller caused", err)
	}
	if errors.Is(err, sessions.ErrCredentialUnavailable) {
		t.Fatalf("a CANCELED launch was reported as an unavailable credential source: %v", err)
	}
}

// An UNWIRED source stays unwired: this correction must not turn "you configured
// nothing" into "your file is broken".
func TestUnwiredTokenFileStaysUnwired(t *testing.T) {
	t.Parallel()

	if src, kind := sessionCredentialSourceWithDiagnostics(func(string) string { return "" }, nil, nil); src != nil {
		t.Fatalf("an unconfigured environment wired the %q source; launches must stay deny-closed", kind)
	}
}
