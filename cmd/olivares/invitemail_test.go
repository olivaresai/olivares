// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/webaddr"
)

// TestInviteSenderSaysWhyANamedDestinationIsNotUsed: invite mode is unavailable
// without a mailer; an unset destination is the default and stays quiet, but a
// named one with no notification dispatcher says so in the log (#471).
func TestInviteSenderSaysWhyANamedDestinationIsNotUsed(t *testing.T) {
	for _, tc := range []struct {
		name, destination string
		warns             bool
	}{
		{"unset", "", false},
		{"named without a dispatcher", "ops-mail", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			getenv := func(k string) string {
				if k == inviteMailDestinationEnv {
					return tc.destination
				}
				return ""
			}
			if s := newInviteSender(getenv, nil, webaddr.Address{}, log); s != nil {
				t.Fatalf("invite sender = %v, want nil", s)
			}
			if got := strings.Contains(buf.String(), "invite mode is unavailable"); got != tc.warns {
				t.Fatalf("warned = %v, want %v; log: %s", got, tc.warns, buf.String())
			}
		})
	}
}
