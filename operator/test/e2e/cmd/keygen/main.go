// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command keygen prints a base64-encoded ed25519 private key for the e2e
// harness's shared audit-signing secret (every HA replica must sign with
// the same key or the ledger hash-chain forks at failover).
//
// It exists as a real package because its previous form — a heredoc written to
// a FIXED scratch path followed by `go mod init` — was the only non-idempotent
// operation in the kind e2e workflow: on a persistent self-hosted runner the
// first green run left a go.mod behind and every later run died on
// "go.mod already exists" before a single scenario executed (runs 30506024260
// and 30509948860, 2026-07-30). A package in the tree has no init step to
// poison and is covered by the ordinary build gate.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

func main() {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	fmt.Print(base64.StdEncoding.EncodeToString(priv))
}
