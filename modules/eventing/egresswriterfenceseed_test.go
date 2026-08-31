// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package eventing

import (
	"errors"
	"strings"
	"testing"
)

// A seeding failure must be distinguishable from a refusal WITHOUT reading the message, because the
// seeds write governed rows and therefore fail with the fence's own text.
func TestASeedFailureIsNotAFenceRefusal(t *testing.T) {
	refusal := errors.New("constraint failed: olivares: eventing egress writer fence: this write carries no capability attestation (1811)")
	tagged := seedErr(refusal)

	if !errors.Is(tagged, ErrFenceProbeSeedFailed) {
		t.Fatal("a seeding error must carry ErrFenceProbeSeedFailed, or the dispatcher cannot tell the phase apart")
	}
	if !errors.Is(tagged, refusal) {
		t.Fatal("the cause must stay inspectable")
	}
	// The trap: the text alone says "refused by the fence", and that is exactly why phase cannot be
	// inferred from it.
	if !strings.Contains(tagged.Error(), "eventing egress writer fence") {
		t.Fatal("premise broken: this test only means something while the seed error carries the fence's own words")
	}
	if seedErr(nil) != nil {
		t.Fatal("seedErr(nil) must stay nil")
	}
}
