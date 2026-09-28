// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package aptrefresh

import (
	"errors"
	"strings"
	"testing"
)

const validCycle = "0123456789abcdef0123456789abcdef"

// TestCheckCycleRefusesEveryOtherForm: --cycle is exactly 32 characters of [0-9a-f] (Interface Q3 r3
// §3.1.5.1); anything else is cycle_invalid. Each case differs from a valid cycle by the one defect its
// name gives, so a refusal can only come from that defect.
func TestCheckCycleRefusesEveryOtherForm(t *testing.T) {
	cases := []struct{ name, cycle string }{
		{"31 characters", validCycle[:31]},
		{"33 characters", validCycle + "0"},
		{"upper case", "0123456789ABCDEF0123456789abcdef"},
		{"a slash", "0123456789abcdef/123456789abcdef"},
		{"empty", ""},
		{"a dot-dot segment", "../3456789abcdef0123456789abcdef"},
		{"a non-hex letter", "0123456789abcdeg0123456789abcdef"},
		{"a trailing newline", validCycle[:31] + "\n"},
	}
	for _, tc := range cases {
		err := CheckCycle(tc.cycle)
		if !errors.Is(err, ErrCycleInvalid) {
			t.Errorf("%s: CheckCycle(%q) = %v, want %s", tc.name, tc.cycle, err, CodeCycleInvalid)
			continue
		}
		if !strings.HasPrefix(err.Error(), CodeCycleInvalid+": ") {
			t.Errorf("%s: the refusal %q does not lead with its code %s", tc.name, err, CodeCycleInvalid)
		}
	}
}

// TestCheckCycleAcceptsThirtyTwoLowercaseHex is the control of the cell above.
func TestCheckCycleAcceptsThirtyTwoLowercaseHex(t *testing.T) {
	for _, c := range []string{validCycle, "ffffffffffffffffffffffffffffffff", "00000000000000000000000000000000"} {
		if err := CheckCycle(c); err != nil {
			t.Errorf("CheckCycle(%q) = %v, want nil", c, err)
		}
	}
}

func invocationEnv(t *testing.T, value string) func(string) string {
	return func(key string) string {
		if key != "INVOCATION_ID" {
			t.Errorf("Invocation read the environment variable %s", key)
			return ""
		}
		return value
	}
}

// TestInvocationCopiesAValidInvocationID: $INVOCATION_ID of exactly 32 lowercase hex characters is copied
// into the handoff's invocation (Interface Q3 r3 §3.1.5.4).
func TestInvocationCopiesAValidInvocationID(t *testing.T) {
	const id = "4f1c0a2b9e8d7c6b5a4f3e2d1c0b9a88"
	got := Invocation(invocationEnv(t, id))
	if got == nil || *got != id {
		t.Fatalf("Invocation = %v, want %q", got, id)
	}
}

// TestInvocationIsNullWhenAbsentOrInvalid: absent or any other form writes null, so a run by hand never
// produces a handoff the helper accepts.
func TestInvocationIsNullWhenAbsentOrInvalid(t *testing.T) {
	const id = "4f1c0a2b9e8d7c6b5a4f3e2d1c0b9a88"
	for _, bad := range []string{
		"",
		id[:31],
		id + "0",
		strings.ToUpper(id),
		"4f1c0a2b-9e8d-7c6b-5a4f-3e2d1c0b9a88",
		id[:31] + "g",
		" " + id[1:],
		id[:31] + "\n",
	} {
		if got := Invocation(invocationEnv(t, bad)); got != nil {
			t.Errorf("Invocation(%q) = %q, want null", bad, *got)
		}
	}
}
