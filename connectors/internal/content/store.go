// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package content

import (
	"sort"
	"strconv"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

// DefaultPageSize is the List page size a data connector serves when the caller
// does not constrain it. The knowledge module pages until the cursor is empty.
const DefaultPageSize = 100

// Store is the in-memory document set a data connector builds once at Open (by
// parsing its source's native export) and serves through List/Fetch. Embedding it
// gives every content connector identical, correct pagination and lookup so each
// connector only implements its native-format parser.
//
// It holds parsed Documents in RAM. For the bounded export-file model this is
// fine; a streaming/live-API connector at scale is a documented follow-up.
type Store struct {
	docs []contentsource.Document
	idx  map[string]int
}

// SetDocs replaces the store's documents, sorted by DocID for stable pagination,
// and rebuilds the id index. A later document with a duplicate DocID wins (the
// connector parsed it last); the index points at the surviving row.
func (s *Store) SetDocs(docs []contentsource.Document) {
	sorted := make([]contentsource.Document, len(docs))
	copy(sorted, docs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].DocID < sorted[j].DocID })
	s.docs = sorted
	s.idx = make(map[string]int, len(sorted))
	for i, d := range sorted {
		s.idx[d.DocID] = i
	}
}

// Len reports how many documents the store holds.
func (s *Store) Len() int { return len(s.docs) }

// List returns one page of DocRefs from offset cursor (a decimal index; "" = 0)
// and the next cursor ("" when exhausted). It is deterministic (DocID order).
func (s *Store) List(cursor string) ([]contentsource.DocRef, string, error) {
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return nil, "", errBadCursor
		}
		start = n
	}
	if start >= len(s.docs) {
		return nil, "", nil
	}
	end := start + DefaultPageSize
	if end > len(s.docs) {
		end = len(s.docs)
	}
	refs := make([]contentsource.DocRef, 0, end-start)
	for _, d := range s.docs[start:end] {
		refs = append(refs, contentsource.DocRef{
			DocID: d.DocID, Title: d.Title, ContentType: d.ContentType, ModifiedAt: d.ModifiedAt,
		})
	}
	next := ""
	if end < len(s.docs) {
		next = strconv.Itoa(end)
	}
	return refs, next, nil
}

// Fetch returns the document with the given id, or ErrNotFound.
func (s *Store) Fetch(docID string) (contentsource.Document, error) {
	if i, ok := s.idx[docID]; ok {
		return s.docs[i], nil
	}
	return contentsource.Document{}, ErrNotFound
}

// ErrNotFound is returned by Fetch when no document has the requested id.
var ErrNotFound = &lookupError{"content: document not found"}

// errBadCursor is returned by List for a malformed continuation cursor.
var errBadCursor = &lookupError{"content: invalid cursor"}

type lookupError struct{ msg string }

func (e *lookupError) Error() string { return e.msg }
