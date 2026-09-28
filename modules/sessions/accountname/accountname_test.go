// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accountname

import (
	"errors"
	"strings"
	"testing"
)

// takenSet builds the set of names already in use in one (tenant, environment),
// whatever driver or state holds them.
func takenSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// The first account of a driver keeps the bare driver name; the next ones take
// -b … -z, then -aa, -ab, bijectively. -a is never generated, because the bare
// name is already the first.
func TestNextName_BareFirstThenLetterSuffix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		taken map[string]bool
		want  string
	}{
		{"empty environment", takenSet(), "claude"},
		{"bare taken", takenSet("claude"), "claude-b"},
		{"bare and -b taken", takenSet("claude", "claude-b"), "claude-c"},
		{"a hole is reused before the tail", takenSet("claude", "claude-c"), "claude-b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextName("claude", tc.taken)
			if err != nil {
				t.Fatalf("NextName: %v", err)
			}
			if got != tc.want {
				t.Fatalf("NextName = %q, want %q", got, tc.want)
			}
		})
	}

	// Walk the sequence by filling the environment one generated name at a time.
	// Position 26 is -z and position 27 is -aa: the suffix is bijective base 26,
	// so there is no -a and no gap.
	taken := takenSet()
	var sequence []string
	for i := 0; i < 60; i++ {
		got, err := NextName("claude", taken)
		if err != nil {
			t.Fatalf("NextName at position %d: %v", i+1, err)
		}
		if taken[got] {
			t.Fatalf("NextName returned %q, which is already taken", got)
		}
		taken[got] = true
		sequence = append(sequence, got)
	}
	wantAt := map[int]string{1: "claude", 2: "claude-b", 3: "claude-c", 26: "claude-z", 27: "claude-aa", 28: "claude-ab", 52: "claude-az", 53: "claude-ba"}
	for pos, want := range wantAt {
		if sequence[pos-1] != want {
			t.Errorf("position %d = %q, want %q", pos, sequence[pos-1], want)
		}
	}
	for _, got := range sequence {
		if got == "claude-a" {
			t.Fatal("claude-a was generated; the bare name is the first account")
		}
		if err := Validate(got); err != nil {
			t.Fatalf("generated name %q fails the shape it imposes on operators: %v", got, err)
		}
	}
}

// A name is unique per (tenant, environment), not per driver: a Codex account
// named claude-b blocks claude-b for Claude, and a Claude account named codex
// blocks the bare name of the Codex driver.
func TestNextName_TakenInTheEnvironmentIsSkippedWhateverTheDriver(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		driver string
		taken  map[string]bool
		want   string
	}{
		{"a codex account named claude-b", "claude", takenSet("claude", "claude-b"), "claude-c"},
		{"a claude account named codex", "codex", takenSet("claude", "codex"), "codex-b"},
		{"other drivers' generated names do not collide", "codex", takenSet("claude", "claude-b", "grok"), "codex"},
		{"an archived account still holds its name", "grok", takenSet("grok", "grok-b", "grok-c"), "grok-d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextName(tc.driver, tc.taken)
			if err != nil {
				t.Fatalf("NextName: %v", err)
			}
			if got != tc.want {
				t.Fatalf("NextName(%q) = %q, want %q", tc.driver, got, tc.want)
			}
		})
	}
}

// The shape is validated for an operator-given name exactly as for a generated
// one: lowercase ASCII, a letter first, [a-z0-9-] after it, at most 32
// characters. An explicit numeric name such as claude-1 stays possible.
func TestName_ShapeIsValidatedForOperatorGivenNames(t *testing.T) {
	t.Parallel()

	valid := []string{
		"claude", "claude-1", "claude-b", "codex-2", "a", "x9",
		strings.Repeat("a", MaxLen),
	}
	for _, name := range valid {
		if err := Validate(name); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"", "Claude", "1claude", "-claude", "claude_b", "claude.b", "claude b",
		" claude", "claude ", "clau\tde", "claudé", strings.Repeat("a", MaxLen+1),
	}
	for _, name := range invalid {
		err := Validate(name)
		if err == nil {
			t.Errorf("Validate(%q) = nil, want a refusal", name)
			continue
		}
		if !errors.Is(err, ErrInvalidName) {
			t.Errorf("Validate(%q) = %v, want an error wrapping ErrInvalidName", name, err)
		}
	}

	// A driver whose key cannot be an account name cannot seed a generated one:
	// the refusal names the shape rather than inventing a spelling.
	if _, err := NextName("open_code", takenSet()); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("NextName(open_code) = %v, want an error wrapping ErrInvalidName", err)
	}
}
