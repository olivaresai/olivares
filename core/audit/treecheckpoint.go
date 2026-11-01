// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/mod/sumdb/note"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Tree checkpoints are the C2SP tlog-checkpoint text over the RFC 6962 Merkle
// tree the store keeps beside each tenant's hash chain (store.AuditTreeReader),
// signed as a C2SP signed note (golang.org/x/mod/sumdb/note) with the engine's
// Ed25519 key. They are a second, independent view of the same ledger: the chain
// and the per-event signatures are unchanged, and an operator who saved one
// checkpoint can later prove, offline, that the ledger still holds the history it
// committed to (VerifyTreeAgainstLedger) and that a single event was inside it
// (VerifyInclusion).
//
// ponytail: the note is signed with the on-box Ed25519 key even when an off-box
// checkpoint key is configured, because a signed note carries the signature of the
// verifier's key and the off-box seam is not wired for it. Against a host compromise
// the saved checkpoint plus its public key held elsewhere is the control; add the
// off-box seam when a KMS-backed deployment asks for it.

// TreeOrigin is the C2SP origin line of a tenant's tree: a schema-less,
// URL-like identifier unique to the tenant, so a checkpoint of one tenant can
// never verify as another's.
func TreeOrigin(tenant model.TenantID) string {
	return "olivares.ai/audit/" + tenant.String()
}

// TreeCheckpoint is the content of a verified C2SP checkpoint.
type TreeCheckpoint struct {
	Origin string
	Size   int64
	Root   []byte
}

// formatTreeCheckpoint renders the three-line C2SP checkpoint body.
func formatTreeCheckpoint(origin string, head store.AuditTreeHead) []byte {
	return []byte(origin + "\n" + strconv.FormatInt(head.Size, 10) + "\n" +
		base64.StdEncoding.EncodeToString(head.Root) + "\n")
}

// TreeVerifierKey is the C2SP verifier key (name+hash+base64 key) for a tenant's
// checkpoints under pub. Hand it to anyone who should verify them.
func TreeVerifierKey(tenant model.TenantID, pub ed25519.PublicKey) (string, error) {
	return note.NewEd25519VerifierKey(TreeOrigin(tenant), pub)
}

// treeNoteSigner signs a note with the engine's Ed25519 key under the key hash
// the verifier key derives, so the signature line verifies under TreeVerifierKey.
type treeNoteSigner struct {
	origin string
	hash   uint32
	priv   ed25519.PrivateKey
}

func (s treeNoteSigner) Name() string                    { return s.origin }
func (s treeNoteSigner) KeyHash() uint32                 { return s.hash }
func (s treeNoteSigner) Sign(msg []byte) ([]byte, error) { return ed25519.Sign(s.priv, msg), nil }

// TreeCheckpoint publishes the signed C2SP checkpoint of a tenant's Merkle tree.
// It runs through store.Custody like Checkpoint, because publishing a head is a
// custodial act that must keep working for a tenant whose service was withdrawn.
// An empty ledger has no tree and returns ok=false. The returned head is the one
// the note commits to.
func (s *Signer) TreeCheckpoint(ctx context.Context, st store.Store, tenant model.TenantID) (signed []byte, head store.AuditTreeHead, ok bool, err error) {
	err = st.Custody(ctx, tenant, func(sc store.CustodyScope) error {
		tr, can := sc.Audit().(store.AuditTreeReader)
		if !can {
			return errors.New("audit: this store keeps no Merkle tree, so there is no tree checkpoint to publish")
		}
		var herr error
		head, herr = tr.AuditTreeHead(ctx)
		return herr
	})
	if err != nil {
		return nil, store.AuditTreeHead{}, false, err
	}
	if head.Size == 0 {
		return nil, store.AuditTreeHead{}, false, nil
	}
	vkey, err := TreeVerifierKey(tenant, s.PublicKey())
	if err != nil {
		return nil, store.AuditTreeHead{}, false, err
	}
	v, err := note.NewVerifier(vkey)
	if err != nil {
		return nil, store.AuditTreeHead{}, false, err
	}
	origin := TreeOrigin(tenant)
	signed, err = note.Sign(&note.Note{Text: string(formatTreeCheckpoint(origin, head))},
		treeNoteSigner{origin: origin, hash: v.KeyHash(), priv: s.priv})
	if err != nil {
		return nil, store.AuditTreeHead{}, false, fmt.Errorf("audit: sign tree checkpoint: %w", err)
	}
	return signed, head, true, nil
}

// TreeInclusion is the portable proof that one event is a leaf of a checkpoint's
// tree: the event's chain hash, its leaf index and the RFC 6962 audit path. What it
// proves is that a leaf with EventHash is at LeafIndex; Seq is the prover's label
// for it and is bound only by the event hash, which commits to the sequence.
type TreeInclusion struct {
	Seq       int64    `json:"seq"`
	EventHash []byte   `json:"event_hash"`
	LeafIndex int64    `json:"leaf_index"`
	TreeSize  int64    `json:"tree_size"`
	Path      [][]byte `json:"path"`
}
