// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package identitysource

import (
	"errors"
	"strings"
	"testing"
)

// TestRejectEchoedSecret covers the H-01 guard itself: equality and containment
// of a sent credential refuse, a legitimate mint passes, and the refusal never
// carries the credential value.
func TestRejectEchoedSecret(t *testing.T) {
	const cred = "hvs.CAESTESTTOKEN-secret-value"

	t.Run("exact echo refuses", func(t *testing.T) {
		err := RejectEchoedSecret(cred, cred)
		if !errors.Is(err, ErrCredentialEcho) {
			t.Fatalf("err = %v, want ErrCredentialEcho", err)
		}
		if strings.Contains(err.Error(), cred) {
			t.Fatalf("refusal must never carry the credential: %v", err)
		}
	})
	t.Run("contained echo refuses", func(t *testing.T) {
		if err := RejectEchoedSecret("rotated-"+cred+"-v2", cred); !errors.Is(err, ErrCredentialEcho) {
			t.Fatalf("err = %v, want ErrCredentialEcho", err)
		}
	})
	t.Run("any of several sent credentials matched refuses", func(t *testing.T) {
		if err := RejectEchoedSecret("x"+cred, "other-long-credential", cred); !errors.Is(err, ErrCredentialEcho) {
			t.Fatalf("err = %v, want ErrCredentialEcho", err)
		}
	})
	t.Run("independent mint passes", func(t *testing.T) {
		if err := RejectEchoedSecret("8f3k2-brand-new-secret-id", cred); err != nil {
			t.Fatalf("a legitimate mint must pass: %v", err)
		}
	})
	t.Run("empty secret passes (the caller's own empty check rules)", func(t *testing.T) {
		if err := RejectEchoedSecret("", cred); err != nil {
			t.Fatalf("empty secret is not an echo: %v", err)
		}
	})
	t.Run("exact equality is checked for any nonempty credential", func(t *testing.T) {
		// independent security review: a configured short token (Vault's documented dev ID
		// "root") is a supported credential, and an exact echo of it is
		// unambiguous — the floor must not exempt it.
		if err := RejectEchoedSecret("root", "root"); !errors.Is(err, ErrCredentialEcho) {
			t.Fatalf("exact short echo must refuse: %v", err)
		}
		if err := RejectEchoedSecret("no credential configured", ""); err != nil {
			t.Fatalf("empty credential must be skipped: %v", err)
		}
	})
	t.Run("the floor applies only to the substring test", func(t *testing.T) {
		if err := RejectEchoedSecret("xxabcxx", "abc"); err != nil {
			t.Fatalf("short needle inside a longer secret must not false-positive: %v", err)
		}
	})
}

// TestScrubCredentials covers the H-02 guard itself: sent-credential bytes are
// replaced by the fixed marker wherever they occur, the removal is reported,
// and a credential-free excerpt is returned byte-identical.
func TestScrubCredentials(t *testing.T) {
	const cred = "hvs.CAESTESTTOKEN-secret-value"

	t.Run("every occurrence of every credential is removed", func(t *testing.T) {
		got, removed := ScrubCredentials("denied: "+cred+" and again "+cred+" by other-cred-123", cred, "other-cred-123")
		if !removed {
			t.Fatal("removal must be reported")
		}
		if strings.Contains(got, cred) || strings.Contains(got, "other-cred-123") {
			t.Fatalf("scrub left credential bytes: %q", got)
		}
		if strings.Count(got, "[removed: request credential]") != 3 {
			t.Fatalf("marker count = %q, want one marker per occurrence", got)
		}
	})
	t.Run("credential-free excerpt is byte-identical", func(t *testing.T) {
		const clean = "http 403: permission denied"
		got, removed := ScrubCredentials(clean, cred)
		if removed || got != clean {
			t.Fatalf("clean excerpt moved: removed=%v got %q", removed, got)
		}
	})
	t.Run("every nonempty credential is scrubbed, however short", func(t *testing.T) {
		// independent security review: a short configured token (Vault's documented dev ID
		// "root") must not survive into a persisted diagnostic either.
		got, removed := ScrubCredentials("rejected request token: root", "root")
		if !removed || strings.Contains(got, "root") {
			t.Fatalf("short credential must be scrubbed: removed=%v got %q", removed, got)
		}
		got, removed = ScrubCredentials("abc abc abc", "")
		if removed || got != "abc abc abc" {
			t.Fatalf("empty credential must be skipped: removed=%v got %q", removed, got)
		}
	})
}
