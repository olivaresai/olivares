// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
)

// ErrAuditTreeIncomplete is returned by a read-only AuditTreeHead when the tree has
// fewer leaves than the ledger has events (a ledger sealed before the tree existed,
// not yet completed by a writable scope). A partial head is never reported as a head.
var ErrAuditTreeIncomplete = errors.New("audit tree is incomplete: a writable scope completes it")

// AuditTreeHead is the RFC 6962 Merkle tree head over a tenant's audit chain:
// the number of leaves and their root hash. Every event sealed into the ledger is
// one leaf, in sequence order (an audit.gap marker is one leaf and the declared
// hole is not), and the leaf data is the event's chain hash, so the root commits
// to the same fields the hash chain does.
type AuditTreeHead struct {
	// Size is the number of leaves. 0 is an empty tree.
	Size int64
	// Root is the 32-byte RFC 6962 tree hash, nil when Size is 0.
	Root []byte
}

// AuditInclusion proves that one event is a leaf of the tree at a given size.
type AuditInclusion struct {
	// LeafIndex is the zero-based position of the event in the tree.
	LeafIndex int64
	// Proof is the RFC 6962 audit path, 32-byte hashes.
	Proof [][]byte
}

// AuditTreeReader is an OPTIONAL AuditLog capability: the Merkle tree the store
// writes in the same transaction as each audit row. The tree sits beside the hash
// chain and the Ed25519 signatures and replaces neither. It adds what a chain
// cannot give: a constant-size head an operator can save, and a short proof that one
// event is inside a head.
type AuditTreeReader interface {
	// AuditTreeHead returns the current head for the bound tenant. In a writable
	// scope it first completes a tree that predates the table (a ledger sealed
	// by a release before it), so the head always covers every event; a
	// read-only scope reports ErrAuditTreeIncomplete rather than a partial head.
	AuditTreeHead(ctx context.Context) (AuditTreeHead, error)
	// AuditInclusionProof proves the event with sequence seq is a leaf of the tree
	// of the given size. found is false when no such event is in that tree.
	AuditInclusionProof(ctx context.Context, seq, size int64) (proof AuditInclusion, found bool, err error)
}
