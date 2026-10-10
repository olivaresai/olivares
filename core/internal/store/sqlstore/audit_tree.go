// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// The audit Merkle tree is the RFC 6962 tree (x/mod/sumdb/tlog) over the tenant's
// ledger: one leaf per event, in sequence order, whose data is the event's chain
// hash. tlog's own storage layout is kept as is: every record stores its leaf hash
// and the interior hashes it completes, at the indexes tlog.StoredHashIndex
// assigns, so a head and any proof read O(log n) rows. Rows are append-only.
//
// An audit.gap marker is one leaf and the declared hole is none, so a leaf's
// position is not seq-1. Each row therefore records the record number (leaf) and
// the audit sequence (seq) of the append that wrote it.

// auditTreeReader reads stored hashes for tlog inside the audit transaction, so
// hashes written earlier in the same append are visible to the next one.
func (a *auditLog) auditTreeReader(ctx context.Context) tlog.HashReader {
	return tlog.HashReaderFunc(func(indexes []int64) ([]tlog.Hash, error) {
		if len(indexes) == 0 {
			return nil, nil
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(indexes)), ",")
		q := a.dia.Rebind("SELECT idx, hash FROM " + a.relation(dialect.AuditTreeTable) +
			" WHERE tenant_id = ? AND idx IN (" + marks + ")")
		args := make([]any, 0, len(indexes)+1)
		args = append(args, a.tenant.String())
		for _, i := range indexes {
			args = append(args, i)
		}
		rows, err := a.tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, wrapUnavailableErr(err)
		}
		defer rows.Close()
		byIndex := make(map[int64]tlog.Hash, len(indexes))
		for rows.Next() {
			var idx int64
			var raw []byte
			if err := rows.Scan(&idx, &raw); err != nil {
				return nil, wrapUnavailableErr(err)
			}
			if len(raw) != tlog.HashSize {
				return nil, fmt.Errorf("sqlstore: audit tree hash %d has %d bytes, want %d", idx, len(raw), tlog.HashSize)
			}
			var h tlog.Hash
			copy(h[:], raw)
			byIndex[idx] = h
		}
		if err := rows.Err(); err != nil {
			return nil, wrapUnavailableErr(err)
		}
		out := make([]tlog.Hash, len(indexes))
		for i, idx := range indexes {
			h, ok := byIndex[idx]
			if !ok {
				return nil, fmt.Errorf("sqlstore: audit tree is missing stored hash %d", idx)
			}
			out[i] = h
		}
		return out, nil
	})
}

// treeTail returns the number of leaves in the tenant's tree and the audit
// sequence of the last one (0 for an empty tree).
func (a *auditLog) treeTail(ctx context.Context) (size, lastSeq int64, err error) {
	q := a.dia.Rebind("SELECT leaf, seq FROM " + a.relation(dialect.AuditTreeTable) +
		" WHERE tenant_id = ? ORDER BY idx DESC LIMIT 1")
	var leaf int64
	err = a.tx.QueryRowContext(ctx, q, a.tenant.String()).Scan(&leaf, &lastSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, wrapUnavailableErr(err)
	}
	return leaf + 1, lastSeq, nil
}

// auditTreeCatchUpPerAppend bounds how many leaves one Append adds, so the first
// write after an upgrade cannot turn a large pre-v24 ledger into one long request
// transaction holding the tenant lock. A healthy ledger adds exactly one leaf per
// event and never reaches it. A variable only so a test can lower it.
var auditTreeCatchUpPerAppend = 2048

// extendTree adds a leaf for every ledger event above the tree's last one, up to
// and including upToSeq, at most limit of them (limit <= 0 means all). Append calls
// it right after each insert, when that is exactly the one new event. A ledger
// sealed before core v24 has events and no tree: Append completes it
// auditTreeCatchUpPerAppend leaves at a time, and AuditTreeHead in a writable scope
// (the checkpoint ceremony) completes it at once. Leaves are always added in
// sequence order, so a tree that is behind is a prefix, never a tree with holes.
func (a *auditLog) extendTree(ctx context.Context, upToSeq int64, limit int) error {
	size, lastSeq, err := a.treeTail(ctx)
	if err != nil {
		return err
	}
	q := "SELECT seq, hash FROM " + a.relation(auditTable) +
		" WHERE tenant_id = ? AND seq > ? AND seq <= ? ORDER BY seq ASC"
	args := []any{a.tenant.String(), lastSeq, upToSeq}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := a.tx.QueryContext(ctx, a.dia.Rebind(q), args...)
	if err != nil {
		return wrapUnavailableErr(err)
	}
	type leaf struct {
		seq  int64
		hash []byte
	}
	var pending []leaf
	for rows.Next() {
		var l leaf
		if err := rows.Scan(&l.seq, &l.hash); err != nil {
			_ = rows.Close()
			return wrapUnavailableErr(err)
		}
		pending = append(pending, l)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return wrapUnavailableErr(err)
	}
	if err := rows.Close(); err != nil {
		return wrapUnavailableErr(err)
	}

	reader := a.auditTreeReader(ctx)
	ins := a.dia.Rebind("INSERT INTO " + a.relation(dialect.AuditTreeTable) +
		" (tenant_id, idx, leaf, seq, hash) VALUES (?, ?, ?, ?, ?)")
	for _, l := range pending {
		if len(l.hash) != tlog.HashSize {
			return fmt.Errorf("sqlstore: audit event seq %d has a %d-byte chain hash, want %d", l.seq, len(l.hash), tlog.HashSize)
		}
		stored, err := tlog.StoredHashes(size, l.hash, reader)
		if err != nil {
			return fmt.Errorf("sqlstore: audit tree record %d (seq %d): %w", size, l.seq, err)
		}
		first := tlog.StoredHashIndex(0, size)
		for i, h := range stored {
			if _, err := a.tx.ExecContext(ctx, ins, a.tenant.String(), first+int64(i), size, l.seq, h[:]); err != nil {
				return mapWriteErr(err)
			}
		}
		size++
	}
	return nil
}

// AuditTreeHead implements store.AuditTreeReader. In a writable scope it first
// completes a tree that predates core v24 (see extendTree); a read-only scope
// reports store.ErrAuditTreeIncomplete when the tree is behind the ledger.
func (a *auditLog) AuditTreeHead(ctx context.Context) (store.AuditTreeHead, error) {
	if !a.readOnly {
		if err := a.noteWrite(); err != nil {
			return store.AuditTreeHead{}, err
		}
		// Same audit-first notice Append and LockAppends give before the tenant lock,
		// so the directory writer tracker can refuse the inverse order.
		if a.directoryWriter != nil {
			a.directoryWriter.noteAudit()
		}
		if err := a.lockTenant(ctx); err != nil {
			return store.AuditTreeHead{}, err
		}
		seq, _, err := a.tail(ctx)
		if err != nil {
			return store.AuditTreeHead{}, err
		}
		if err := a.extendTree(ctx, seq, 0); err != nil {
			return store.AuditTreeHead{}, err
		}
	}
	size, treeSeq, err := a.treeTail(ctx)
	if err != nil {
		return store.AuditTreeHead{}, err
	}
	if a.readOnly {
		ledgerSeq, _, err := a.tail(ctx)
		if err != nil {
			return store.AuditTreeHead{}, err
		}
		if treeSeq != ledgerSeq {
			return store.AuditTreeHead{}, store.ErrAuditTreeIncomplete
		}
	}
	if size == 0 {
		return store.AuditTreeHead{}, nil
	}
	root, err := tlog.TreeHash(size, a.auditTreeReader(ctx))
	if err != nil {
		return store.AuditTreeHead{}, fmt.Errorf("sqlstore: audit tree head at size %d: %w", size, err)
	}
	return store.AuditTreeHead{Size: size, Root: root[:]}, nil
}

// AuditInclusionProof implements store.AuditTreeReader.
func (a *auditLog) AuditInclusionProof(ctx context.Context, seq, size int64) (store.AuditInclusion, bool, error) {
	have, _, err := a.treeTail(ctx)
	if err != nil {
		return store.AuditInclusion{}, false, err
	}
	if size < 1 {
		return store.AuditInclusion{}, false, nil
	}
	if size > have {
		return store.AuditInclusion{}, false, fmt.Errorf("sqlstore: the audit tree has %d leaves, fewer than the %d asked for", have, size)
	}
	q := a.dia.Rebind("SELECT leaf FROM " + a.relation(dialect.AuditTreeTable) +
		" WHERE tenant_id = ? AND seq = ? LIMIT 1")
	var leaf int64
	err = a.tx.QueryRowContext(ctx, q, a.tenant.String(), seq).Scan(&leaf)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && leaf >= size) {
		return store.AuditInclusion{}, false, nil
	}
	if err != nil {
		return store.AuditInclusion{}, false, wrapUnavailableErr(err)
	}
	proof, err := tlog.ProveRecord(size, leaf, a.auditTreeReader(ctx))
	if err != nil {
		return store.AuditInclusion{}, false, fmt.Errorf("sqlstore: audit inclusion proof for seq %d at size %d: %w", seq, size, err)
	}
	return store.AuditInclusion{LeafIndex: leaf, Proof: hashesToBytes(proof)}, true, nil
}

func hashesToBytes(hs []tlog.Hash) [][]byte {
	out := make([][]byte, len(hs))
	for i := range hs {
		out[i] = hs[i][:]
	}
	return out
}
