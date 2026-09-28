// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"index/suffixarray"
	"math"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// AliasKind says how an alias names an account in counted content.
type AliasKind uint8

const (
	// AliasAccountID is the account's own id.
	AliasAccountID AliasKind = iota + 1
	// AliasCredential is the id of one of the account's sessions or tokens.
	AliasCredential
	// AliasEmail is the account's normalized email address.
	AliasEmail
	// AliasExternal is the account's directory external id.
	AliasExternal
)

// Alias is one value counted content may name an account by.
type Alias struct {
	Kind  AliasKind
	Value string
}

// Document is one piece of counted content in every representation the matcher
// reads: the stored bytes, and the strings the format's representation owner
// decoded from them.
type Document struct {
	// Raw is the stored bytes. They are always matched.
	Raw string
	// Decoded are the strings the representation owner decoded, in source order.
	Decoded []string
}

// MatchLimits bounds one match over one document. Every limit is passed
// explicitly, and a zero limit allows nothing.
type MatchLimits struct {
	MaxContentBytes   int
	MaxDecodedStrings int
	MaxDecodedBytes   int
	MaxCanonicalIDs   int
	MaxOccurrences    int
}

// ErrAliasCapacity marks a match that exceeded one of its limits. It is not
// retryable: the content or the limits must change first.
var ErrAliasCapacity = errors.New("auth: alias matching exceeded a capacity limit")

// CapacityError names the limit a match exceeded, as one of the Capacity
// dimensions below.
type CapacityError struct {
	Dimension string
}

// The dimensions of a CapacityError, one per limit of MatchLimits. Each is a
// wire code, alias_capacity:<dimension>, and none is retryable.
const (
	CapacityContentBytes   = "content_bytes"
	CapacityDecodedStrings = "decoded_strings"
	CapacityDecodedBytes   = "decoded_bytes"
	CapacityCanonicalIDs   = "canonical_ids"
	CapacityOccurrences    = "occurrences"
)

func (e CapacityError) Error() string {
	return "auth: alias matching exceeded its " + e.Dimension + " limit"
}

// Unwrap makes errors.Is(err, ErrAliasCapacity) hold.
func (e CapacityError) Unwrap() error { return ErrAliasCapacity }

const (
	// countedContentBytes is the content cap every counted writer applies.
	countedContentBytes = 262_144
	// canonicalIDWidth is the length of an id's canonical text form.
	canonicalIDWidth = 36
	// canonicalIDStride is the smallest distance between the starts of two
	// canonical windows: a closer window would put its first hyphen on a hex
	// position of the other.
	canonicalIDStride = 28
)

// CountedContentLimits returns the limits of a match over counted content. The
// three byte and string limits are the writers' content cap: an admitted
// document is valid UTF-8, so no decoded string is longer than its source span.
// MaxCanonicalIDs admits the densest capped document: one window every 28 bytes
// in the raw bytes and as many again in the decoded strings. MaxOccurrences is a
// finite bound that has not been measured.
func CountedContentLimits() MatchLimits {
	windows := (countedContentBytes-canonicalIDWidth)/canonicalIDStride + 1
	return MatchLimits{
		MaxContentBytes:   countedContentBytes,
		MaxDecodedStrings: countedContentBytes,
		MaxDecodedBytes:   countedContentBytes,
		MaxCanonicalIDs:   2 * windows,
		MaxOccurrences:    8 << 20,
	}
}

// MatchAliases reports, for each alias, whether doc names it in its raw bytes or
// in one of its decoded strings. An account or credential id matches as one of
// the canonical 36-byte windows, in any letter case, wherever it appears. An
// email address matches as a whole-token occurrence, in any letter case. An
// external id matches as a whole-token occurrence of its exact bytes. A
// whole-token occurrence has no identifier character ([A-Za-z0-9._-]) just
// before or just after it inside its representation.
//
// The representations are indexed once per call, in two forms: as stored, for
// external ids, and lowercased as strings.ToLower lowers them, for emails and
// ids. Each form lays the representations end to end and keeps their
// boundaries, so an occurrence counts only inside one representation. Each
// alias is then one query of one index, whatever the number of
// representations. Every candidate occurrence a query returns counts against
// MaxOccurrences, including those after a hit.
func MatchAliases(doc Document, aliases []Alias, lim MatchLimits) ([]bool, error) {
	return matchAliases(doc, aliases, lim, newSuffixIndex)
}

// corpusIndex is the part of an index a match queries: the positions where a
// needle occurs, at most n of them.
type corpusIndex interface {
	Lookup(s []byte, n int) []int
}

// newSuffixIndex is the production index of a corpus: a standard-library suffix
// array over its bytes.
func newSuffixIndex(data []byte) corpusIndex { return suffixarray.New(data) }

// matchAliases is MatchAliases with the index constructor as a parameter.
// Every corpus is indexed by build and queried only through the index build
// returns, so a test can observe each index a match builds and each query it
// makes without the matcher reporting on itself.
func matchAliases(doc Document, aliases []Alias, lim MatchLimits, build func([]byte) corpusIndex) ([]bool, error) {
	if err := checkDocument(doc, lim); err != nil {
		return nil, err
	}
	var stored, lowered *corpus
	form := func(lower bool) *corpus {
		c := &stored
		fold := func(s string) string { return s }
		if lower {
			c, fold = &lowered, strings.ToLower
		}
		if *c == nil {
			*c = newCorpus(doc, fold, build)
		}
		return *c
	}
	candidates := 0
	hits := make([]bool, len(aliases))
	for i, a := range aliases {
		if a.Value == "" {
			continue
		}
		var c *corpus
		var needle string
		whole := true
		switch a.Kind {
		case AliasAccountID, AliasCredential:
			needle = strings.ToLower(a.Value)
			if !isCanonicalIDWindow(needle) {
				continue
			}
			c, whole = form(true), false
		case AliasEmail:
			needle, c = strings.ToLower(a.Value), form(true)
		case AliasExternal:
			needle, c = a.Value, form(false)
		default:
			continue
		}
		limit := lim.MaxOccurrences - candidates
		if limit < math.MaxInt {
			limit++
		}
		positions := c.index.Lookup([]byte(needle), limit)
		candidates += len(positions)
		if candidates > lim.MaxOccurrences {
			return nil, CapacityError{Dimension: CapacityOccurrences}
		}
		for _, p := range positions {
			if c.holds(p, len(needle), whole) {
				hits[i] = true
				break
			}
		}
	}
	return hits, nil
}

// corpus is one form of a document's representations laid end to end, the raw
// bytes first and then each decoded string in order, with one index over them.
// starts[k] is where representation k begins, and its last entry is the end of
// the corpus.
type corpus struct {
	data   []byte
	index  corpusIndex
	starts []int
}

// newCorpus lays out doc's representations, each passed through fold, and
// indexes them once with build.
func newCorpus(doc Document, fold func(string) string, build func([]byte) corpusIndex) *corpus {
	size := len(doc.Raw)
	for _, s := range doc.Decoded {
		size += len(s)
	}
	data := make([]byte, 0, size)
	starts := make([]int, 0, len(doc.Decoded)+2)
	add := func(s string) {
		starts = append(starts, len(data))
		data = append(data, fold(s)...)
	}
	add(doc.Raw)
	for _, s := range doc.Decoded {
		add(s)
	}
	starts = append(starts, len(data))
	return &corpus{data: data, index: build(data), starts: starts}
}

// holds reports whether an n-byte occurrence at p lies inside one
// representation and, when whole is set, has no identifier byte just before or
// just after it in that representation.
func (c *corpus) holds(p, n int, whole bool) bool {
	k := sort.SearchInts(c.starts, p+1) - 1
	start, end := c.starts[k], c.starts[k+1]
	if p+n > end {
		return false
	}
	if !whole {
		return true
	}
	return (p == start || !identByte(c.data[p-1])) && (p+n == end || !identByte(c.data[p+n]))
}

// CanonicalIDs returns every distinct canonical id doc carries, in its raw bytes
// or in its decoded strings, lowercased and sorted. More than MaxCanonicalIDs
// distinct ids is a capacity refusal.
func CanonicalIDs(doc Document, lim MatchLimits) ([]model.ID, error) {
	if err := checkDocument(doc, lim); err != nil {
		return nil, err
	}
	seen := make(map[model.ID]struct{})
	var out []model.ID
	scan := func(s string) error {
		for i := 0; i+canonicalIDWidth <= len(s); i++ {
			if s[i+8] != '-' {
				continue
			}
			window := s[i : i+canonicalIDWidth]
			if !isCanonicalIDWindow(window) {
				continue
			}
			id := model.ID(strings.ToLower(window))
			if id.IsZero() {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			if len(out) >= lim.MaxCanonicalIDs {
				return CapacityError{Dimension: CapacityCanonicalIDs}
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
		return nil
	}
	if err := scan(doc.Raw); err != nil {
		return nil, err
	}
	for _, s := range doc.Decoded {
		if err := scan(s); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// errNegativeMatchLimit refuses limits a caller got wrong: a limit below zero
// has no meaning. A zero limit is valid and admits nothing of its kind.
var errNegativeMatchLimit = errors.New("auth: a match limit is negative")

// checkDocument refuses a negative limit, then applies the document limits,
// before any index is built.
func checkDocument(doc Document, lim MatchLimits) error {
	if lim.MaxContentBytes < 0 || lim.MaxDecodedStrings < 0 || lim.MaxDecodedBytes < 0 ||
		lim.MaxCanonicalIDs < 0 || lim.MaxOccurrences < 0 {
		return errNegativeMatchLimit
	}
	if len(doc.Raw) > lim.MaxContentBytes {
		return CapacityError{Dimension: CapacityContentBytes}
	}
	if len(doc.Decoded) > lim.MaxDecodedStrings {
		return CapacityError{Dimension: CapacityDecodedStrings}
	}
	total := 0
	for _, s := range doc.Decoded {
		total += len(s)
		if total > lim.MaxDecodedBytes {
			return CapacityError{Dimension: CapacityDecodedBytes}
		}
	}
	return nil
}

// isCanonicalIDWindow reports whether s is an id in its canonical 8-4-4-4-12
// text form, in either letter case.
func isCanonicalIDWindow(s string) bool {
	if len(s) != canonicalIDWidth {
		return false
	}
	for i := 0; i < canonicalIDWidth; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHexDigit(c) {
				return false
			}
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

// identByte reports whether c would run together with an alias:
// [A-Za-z0-9._-].
func identByte(c byte) bool {
	return c == '_' || c == '-' || c == '.' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}
