// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// setupTokenBytes is the entropy of a setup token (256 bits).
const setupTokenBytes = 32

// setupPrefix prefixes the setup token so it is recognizable in operator output.
const setupPrefix = "olst_"

var setupB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// SetupToken manages the first-boot setup token at a fixed path. Only the SHA-256
// of the token is persisted (0600), so the file leaking does not reveal the
// token; the plaintext is returned exactly once, to be printed to stdout.
type SetupToken struct {
	path string
}

// NewSetupToken manages the setup token stored at path.
func NewSetupToken(path string) *SetupToken { return &SetupToken{path: path} }

// Exists reports whether a setup token is currently active. A token that cannot be
// looked at counts as absent, which is right for the engine (it owns the file, and
// Verify must fail closed) and wrong for a caller that reports on the state: that
// caller uses Check.
func (s *SetupToken) Exists() bool {
	active, err := s.Check()
	return err == nil && active
}

// Check is Exists that does not mistake "could not look" for "no token". Only a
// missing file is absent; any other failure to stat it (a data directory the caller
// has no search permission on) is returned, because the state is then unknown.
func (s *SetupToken) Check() (active bool, err error) {
	info, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// Ensure mints a setup token if none exists, returning the plaintext to print
// once (created=true). If one already exists it returns created=false and no
// plaintext (the original was shown at mint time and cannot be recovered).
func (s *SetupToken) Ensure() (plaintext string, created bool, err error) {
	return s.EnsureAs(-1, -1)
}

// EnsureAs is Ensure for a process that mints on the engine's behalf, such as root
// running `olivares first-boot --new-token`: the token file belongs to uid:gid, the
// account the engine reads it as, from the moment it exists. A negative uid keeps the
// caller's own account, which is Ensure.
func (s *SetupToken) EnsureAs(uid, gid int) (plaintext string, created bool, err error) {
	if s.Exists() {
		return "", false, nil
	}
	raw := make([]byte, setupTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", false, fmt.Errorf("secure: read entropy: %w", err)
	}
	token := setupPrefix + setupB32.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	if err := writeSecretAs(s.path, []byte(setupB32.EncodeToString(sum[:])+"\n"), uid, gid); err != nil {
		return "", false, err
	}
	return token, true, nil
}

// ErrSetupTokenUnreadable is Match's refusal when the token file exists but this
// process cannot read a token hash from it: another account owns it, its mode is
// wider than 0600, or it does not hold a SHA-256. No presented token can match then,
// and the reason is the deployment's, not the caller's (#530).
var ErrSetupTokenUnreadable = errors.New("secure: the setup token file cannot be read")

// Verify reports whether presented matches the active setup token, in constant
// time. It is false if no token is active or the token cannot be read.
func (s *SetupToken) Verify(presented string) bool {
	ok, err := s.Match(presented)
	return ok && err == nil
}

// Match is Verify that tells an unreadable token from a wrong one: it returns
// ErrSetupTokenUnreadable, wrapping the cause with the path and the errno, when the
// file is there and no token hash can be read from it. A missing or consumed token
// is (false, nil).
func (s *SetupToken) Match(presented string) (bool, error) {
	active, err := s.Check()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrSetupTokenUnreadable, err)
	}
	if !active {
		return false, nil
	}
	b, err := readSecret(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // consumed since Check
	}
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrSetupTokenUnreadable, err)
	}
	want, err := setupB32.DecodeString(strings.TrimSpace(string(b)))
	if err == nil && len(want) != sha256.Size {
		err = fmt.Errorf("%d bytes, not %d", len(want), sha256.Size)
	}
	if err != nil {
		return false, fmt.Errorf("%w: %s does not hold a setup token hash: %w", ErrSetupTokenUnreadable, s.path, err)
	}
	got := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(got[:], want) == 1, nil
}

// Consume invalidates the setup token (single-use), called after the first
// superadmin is created. It is idempotent.
func (s *SetupToken) Consume() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("secure: consume setup token: %w", err)
	}
	return nil
}
