// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"

	"github.com/olivaresai/olivares/core/secure"
)

const memoryPortabilityKeyFile = "memory-portability.key"

// Portability has its own installation key. Read-only commands and restored or
// shared-custody installations load only; they must never replace missing custody.
func loadMemoryPortabilityKey(dataDir string, opts ...keyLoadOption) (loadedSigningKey, error) {
	path := filepath.Join(dataDir, memoryPortabilityKeyFile)
	var key ed25519.PrivateKey
	var created bool
	var err error
	info, statErr := os.Lstat(path)
	switch {
	case statErr == nil:
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return loadedSigningKey{}, fmt.Errorf("memory portability key must be an owner-only regular file (0600)")
		}
		key, err = secure.LoadSigningKey(path)
	case os.IsNotExist(statErr) && !applyKeyLoadOptions(opts).noMint:
		key, created, err = secure.LoadOrCreateSigningKey(path)
	default:
		return loadedSigningKey{}, statErr
	}
	if err != nil {
		return loadedSigningKey{}, err
	}
	if !bytes.Equal(key, ed25519.NewKeyFromSeed(key.Seed())) {
		return loadedSigningKey{}, fmt.Errorf("memory portability key has inconsistent Ed25519 key material")
	}
	return mintedSigningKey(key, created), nil
}
