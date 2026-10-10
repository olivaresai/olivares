// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"testing"
	"time"
)

func TestRetryAfterSecondsCannotWrapIntoShortBackoff(t *testing.T) {
	for _, value := range []string{"9223372037", "9223372036854775807"} {
		delay := parseRetryAfter(value)
		if delay < time.Hour {
			t.Fatalf("oversized seconds wrapped into %s", delay)
		}
	}
}
