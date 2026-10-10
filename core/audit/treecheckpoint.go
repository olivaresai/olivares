// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/mod/sumdb/note"
	"golang.org/x/mod/sumdb/tlog"

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

// VerifyTreeCheckpoint verifies a signed C2SP checkpoint offline: the note must
// carry a valid signature by pub and name this tenant's origin. Nothing else is
// trusted, and the engine need not be running.
func VerifyTreeCheckpoint(signed []byte, tenant model.TenantID, pub ed25519.PublicKey) (TreeCheckpoint, error) {
	vkey, err := TreeVerifierKey(tenant, pub)
	if err != nil {
		return TreeCheckpoint{}, fmt.Errorf("audit: tree checkpoint key: %w", err)
	}
	v, err := note.NewVerifier(vkey)
	if err != nil {
		return TreeCheckpoint{}, fmt.Errorf("audit: tree checkpoint key: %w", err)
	}
	n, err := note.Open(signed, note.VerifierList(v))
	if err != nil {
		return TreeCheckpoint{}, fmt.Errorf("audit: tree checkpoint is not signed by this key for tenant %s: %w", tenant, err)
	}
	cp, err := parseTreeCheckpoint(n.Text)
	if err != nil {
		return TreeCheckpoint{}, err
	}
	if want := TreeOrigin(tenant); cp.Origin != want {
		return TreeCheckpoint{}, fmt.Errorf("audit: tree checkpoint origin %q is not %q", cp.Origin, want)
	}
	return cp, nil
}

// parseTreeCheckpoint reads the C2SP body: origin, decimal size, base64 root.
// Extension lines are allowed by the format and ignored here.
func parseTreeCheckpoint(text string) (TreeCheckpoint, error) {
	lines := strings.Split(text, "\n")
	if len(lines) < 4 || lines[len(lines)-1] != "" {
		return TreeCheckpoint{}, errors.New("audit: tree checkpoint body is not origin, size and root lines")
	}
	size, err := strconv.ParseInt(lines[1], 10, 64)
	if err != nil || size < 1 || strconv.FormatInt(size, 10) != lines[1] {
		return TreeCheckpoint{}, fmt.Errorf("audit: tree checkpoint size %q is not a positive decimal", lines[1])
	}
	root, err := base64.StdEncoding.DecodeString(lines[2])
	if err != nil || len(root) != tlog.HashSize {
		return TreeCheckpoint{}, errors.New("audit: tree checkpoint root is not a base64 SHA-256 hash")
	}
	return TreeCheckpoint{Origin: lines[0], Size: size, Root: root}, nil
}

// errWalkDone stops a ledger walk once the events asked for are read.
var errWalkDone = errors.New("walk done")

// VerifyTreeAgainstLedger checks that the first cp.Size events of the ledger
// hash, as RFC 6962 leaves, to cp.Root: the ledger still holds exactly the
// history the checkpoint committed to, however much it has grown since. It
// recomputes the root from the events themselves and does not read the stored
// tree, so a damaged or rewritten tree table cannot vouch for the ledger. The
// events' own hashes are proved by store.AuditLog.Verify (`audit verify`).
func VerifyTreeAgainstLedger(ctx context.Context, log store.AuditLog, cp TreeCheckpoint) error {
	if cp.Size < 1 {
		return errors.New("audit: a checkpoint commits to at least one event")
	}
	var acc treeAccumulator
	err := log.Walk(ctx, 1, func(ev model.AuditEvent) error {
		acc.add(ev.Hash)
		if acc.size == cp.Size {
			return errWalkDone
		}
		return nil
	})
	if err != nil && !errors.Is(err, errWalkDone) {
		return err
	}
	if acc.size < cp.Size {
		return fmt.Errorf("audit: the ledger has %d events but the checkpoint commits to %d: events were removed", acc.size, cp.Size)
	}
	if root := acc.root(); !bytes.Equal(root[:], cp.Root) {
		return fmt.Errorf("audit: the first %d events hash to %x, not the checkpoint's %x: history was rewritten", cp.Size, root[:], cp.Root)
	}
	return nil
}

// treeAccumulator is the RFC 6962 hash computed by streaming leaves in order,
// holding only the roots of the perfect subtrees to the left of the frontier.
type treeAccumulator struct {
	size  int64
	stack []treeSubtree
}

type treeSubtree struct {
	hash tlog.Hash
	n    int64
}

func (a *treeAccumulator) add(data []byte) {
	a.size++
	a.stack = append(a.stack, treeSubtree{hash: tlog.RecordHash(data), n: 1})
	for l := len(a.stack); l >= 2 && a.stack[l-1].n == a.stack[l-2].n; l = len(a.stack) {
		left, right := a.stack[l-2], a.stack[l-1]
		a.stack = append(a.stack[:l-2], treeSubtree{hash: tlog.NodeHash(left.hash, right.hash), n: left.n * 2})
	}
}

func (a *treeAccumulator) root() tlog.Hash {
	h := a.stack[len(a.stack)-1].hash
	for i := len(a.stack) - 2; i >= 0; i-- {
		h = tlog.NodeHash(a.stack[i].hash, h)
	}
	return h
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

// ProveInclusion builds the TreeInclusion of event seq in the tree of the given
// size (the size of a checkpoint the operator holds). ok=false means no such
// event is in that tree.
func ProveInclusion(ctx context.Context, log store.AuditLog, seq, size int64) (TreeInclusion, bool, error) {
	tr, can := log.(store.AuditTreeReader)
	if !can {
		return TreeInclusion{}, false, errors.New("audit: this store keeps no Merkle tree")
	}
	inc, found, err := tr.AuditInclusionProof(ctx, seq, size)
	if err != nil || !found {
		return TreeInclusion{}, false, err
	}
	var eventHash []byte
	err = log.Walk(ctx, seq, func(ev model.AuditEvent) error {
		if ev.Seq == seq {
			eventHash = ev.Hash
		}
		return errWalkDone
	})
	if err != nil && !errors.Is(err, errWalkDone) {
		return TreeInclusion{}, false, err
	}
	if eventHash == nil {
		return TreeInclusion{}, false, fmt.Errorf("audit: the tree holds event seq %d but the ledger does not: the event was removed", seq)
	}
	return TreeInclusion{Seq: seq, EventHash: eventHash, LeafIndex: inc.LeafIndex, TreeSize: size, Path: inc.Proof}, true, nil
}

// VerifyInclusion checks, offline, that p proves its event is a leaf of cp.
func VerifyInclusion(p TreeInclusion, cp TreeCheckpoint) error {
	if p.TreeSize != cp.Size {
		return fmt.Errorf("audit: the proof is for a tree of %d events, the checkpoint commits to %d", p.TreeSize, cp.Size)
	}
	proof := make(tlog.RecordProof, len(p.Path))
	for i, h := range p.Path {
		if len(h) != tlog.HashSize {
			return fmt.Errorf("audit: proof hash %d is not %d bytes", i, tlog.HashSize)
		}
		copy(proof[i][:], h)
	}
	var root tlog.Hash
	copy(root[:], cp.Root)
	if err := tlog.CheckRecord(proof, cp.Size, root, p.LeafIndex, tlog.RecordHash(p.EventHash)); err != nil {
		return fmt.Errorf("audit: event seq %d is not in the checkpoint's tree: %w", p.Seq, err)
	}
	return nil
}
