// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package accountname is the naming rule of a provider account: the shape every
// name has, whoever chose it, and the sequence the server proposes when nobody
// did.
//
// It is pure on purpose. It never reads a database and it never decides that a
// name is free: it proposes the first name that is not in the set it is given,
// and the unique index of the account table decides. A proposal that loses to a
// concurrent writer is the caller's to retry.
package accountname

import (
	"errors"
	"fmt"
)

// MaxLen is the longest account name, in bytes. Every valid name is ASCII, so it
// is also the longest name in characters.
const MaxLen = 32

var (
	// ErrInvalidName is wrapped by every refusal of a name's shape.
	ErrInvalidName = errors.New("invalid account name")
	// ErrNoFreeName is wrapped when every name of a driver's sequence that fits
	// in MaxLen is taken. It can only happen to a driver key close to MaxLen.
	ErrNoFreeName = errors.New("no free account name")
)

// Validate reports whether name has the shape of an account name: lowercase
// ASCII, a letter first, then letters, digits or '-', at most MaxLen characters.
// The name is checked exactly as given; nothing is trimmed or lowercased,
// because a name the operator did not type is a name the operator will not find.
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("%w: a name is required", ErrInvalidName)
	}
	if len(name) > MaxLen {
		return fmt.Errorf("%w: %d characters, the limit is %d", ErrInvalidName, len(name), MaxLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return fmt.Errorf("%w: it must be lowercase ASCII, start with a letter and use only "+
				"letters, digits and '-'", ErrInvalidName)
		}
	}
	return nil
}

// Candidate returns the n-th name of driver's sequence, counting from 1: the bare
// driver name, then driver-b … driver-z, driver-aa, driver-ab and so on. The
// suffix is bijective base 26, so "-a" is never produced: the bare name is
// already the first account.
func Candidate(driver string, n int) string {
	if n <= 1 {
		return driver
	}
	return driver + "-" + letters(n)
}

// letters renders n >= 1 in bijective base 26 over a…z: 1 is "a", 26 is "z",
// 27 is "aa".
func letters(n int) string {
	var b []byte
	for n > 0 {
		n--
		b = append(b, byte('a'+n%26))
		n /= 26
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// NextName returns the first name of driver's sequence that is not in taken.
//
// taken holds every name in use in the account's scope — every driver and every
// state, archived included — because a name is unique per scope and not per
// driver. The search is bounded by the size of taken: among len(taken)+1
// distinct candidates at least one is free.
func NextName(driver string, taken map[string]bool) (string, error) {
	if err := Validate(driver); err != nil {
		return "", fmt.Errorf("the driver %q cannot seed an account name: %w", driver, err)
	}
	for n := 1; n <= len(taken)+1; n++ {
		name := Candidate(driver, n)
		if len(name) > MaxLen {
			break
		}
		if !taken[name] {
			return name, nil
		}
	}
	return "", fmt.Errorf("%w: no name of at most %d characters is free for driver %q",
		ErrNoFreeName, MaxLen, driver)
}
