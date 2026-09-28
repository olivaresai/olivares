// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"errors"
	"fmt"
	"index/suffixarray"
	"strings"
	"testing"
)

// densestWindows returns length bytes that hold one distinct canonical window
// every 28 bytes and no other window. Unit k is the 24 hex digits of base+k in
// the 8-4-4-4-4 shape; window k is unit k and the first 8 digits of unit k+1.
func densestWindows(base uint64, length int) string {
	var b strings.Builder
	for k := uint64(0); b.Len() < length; k++ {
		h := fmt.Sprintf("%024x", base+k)
		b.WriteString(h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:24])
	}
	return b.String()[:length]
}

func TestTheDensestCappedDocumentIsAdmitted(t *testing.T) {
	t.Parallel()

	lim := CountedContentLimits()
	derived := 2 * ((262_144-36)/28 + 1)
	if derived != 18_724 || lim.MaxCanonicalIDs != derived {
		t.Fatalf("MaxCanonicalIDs = %d, want the derived %d (18,724)", lim.MaxCanonicalIDs, derived)
	}
	doc := Document{
		Raw:     densestWindows(0x111111111111, 262_144),
		Decoded: []string{densestWindows(0x222222222222, 262_144)},
	}
	ids, err := CanonicalIDs(doc, lim)
	if err != nil {
		t.Fatalf("CanonicalIDs of the densest capped document: %v", err)
	}
	if len(ids) != 18_724 {
		t.Fatalf("CanonicalIDs found %d ids, want 18,724 (9,362 raw and 9,362 decoded)", len(ids))
	}
	hits, err := MatchAliases(doc, []Alias{{Kind: AliasAccountID, Value: strings.ToUpper(string(ids[0]))}}, lim)
	if err != nil || len(hits) != 1 || !hits[0] {
		t.Fatalf("MatchAliases on the densest document = %v, %v; want one hit", hits, err)
	}

	onePast := lim
	onePast.MaxCanonicalIDs = derived - 1
	_, err = CanonicalIDs(doc, onePast)
	var capacity CapacityError
	if !errors.As(err, &capacity) || capacity.Dimension != "canonical_ids" || !errors.Is(err, ErrAliasCapacity) {
		t.Fatalf("one id past the limit: error = %v, want a canonical_ids capacity refusal", err)
	}
}

func TestMatchAliasesPerKindPredicate(t *testing.T) {
	t.Parallel()

	const id = "a1b2c3d4-0000-4000-8000-00000000000a"
	upper := strings.ToUpper(id)
	cases := []struct {
		name  string
		doc   Document
		alias Alias
		want  bool
	}{
		{"account id in another case inside a longer run", Document{Raw: "x" + upper + "y"}, Alias{AliasAccountID, id}, true},
		{"credential id in a decoded string only", Document{Raw: `"escaped"`, Decoded: []string{"cred " + id}}, Alias{AliasCredential, id}, true},
		{"an account id that is not canonical never matches", Document{Raw: "not-an-id"}, Alias{AliasAccountID, "not-an-id"}, false},
		{"email as a whole token in another case", Document{Raw: "Contact JDoe@X.test now"}, Alias{AliasEmail, "jdoe@x.test"}, true},
		{"email run together with an identifier character", Document{Raw: "xjdoe@x.test"}, Alias{AliasEmail, "jdoe@x.test"}, false},
		{"escaped email counts through its decoded string", Document{Raw: `{"u":"jdoe\u0040x.test"}`, Decoded: []string{"u", "jdoe@x.test"}}, Alias{AliasEmail, "jdoe@x.test"}, true},
		{"escaped email is not in the raw bytes alone", Document{Raw: `{"u":"jdoe\u0040x.test"}`}, Alias{AliasEmail, "jdoe@x.test"}, false},
		{"plain external id", Document{Raw: `["00u1abcd"]`}, Alias{AliasExternal, "00u1abcd"}, true},
		{"external id with a delimiter", Document{Raw: `{"n":"Smith, John"}`}, Alias{AliasExternal, "Smith, John"}, true},
		{"external id with its exact bytes and an at sign", Document{Raw: `"JDoe@X.test"`}, Alias{AliasExternal, "JDoe@X.test"}, true},
		{"L1: a UUID-shaped external id in another case", Document{Raw: `"` + id + `"`}, Alias{AliasExternal, upper}, false},
		{"L2: a UUID-shaped external id inside a longer run", Document{Raw: `"x-` + id + `"`}, Alias{AliasExternal, id}, false},
		{"L3: an external id with an at sign in another case", Document{Raw: `{"u":"JDoe@X.test"}`}, Alias{AliasExternal, "jdoe@x.test"}, false},
		{"an empty alias never matches", Document{Raw: "anything"}, Alias{AliasExternal, ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits, err := MatchAliases(c.doc, []Alias{c.alias}, CountedContentLimits())
			if err != nil {
				t.Fatalf("MatchAliases: %v", err)
			}
			if len(hits) != 1 || hits[0] != c.want {
				t.Fatalf("hits = %v, want [%v]", hits, c.want)
			}
		})
	}

	t.Run("every examined occurrence counts against the budget", func(t *testing.T) {
		lim := CountedContentLimits()
		lim.MaxOccurrences = 2
		_, err := MatchAliases(Document{Raw: "xab xab xab ab"}, []Alias{{AliasExternal, "ab"}}, lim)
		var capacity CapacityError
		if !errors.As(err, &capacity) || capacity.Dimension != "occurrences" {
			t.Fatalf("error = %v, want an occurrences capacity refusal", err)
		}
	})

	t.Run("content above the limit is refused before matching", func(t *testing.T) {
		lim := CountedContentLimits()
		lim.MaxContentBytes = 4
		_, err := MatchAliases(Document{Raw: "12345"}, []Alias{{AliasExternal, "1"}}, lim)
		var capacity CapacityError
		if !errors.As(err, &capacity) || capacity.Dimension != "content_bytes" {
			t.Fatalf("error = %v, want a content_bytes capacity refusal", err)
		}
	})
}

func TestQualifiedSubjectKeyRefusesPaddingAndControl(t *testing.T) {
	t.Parallel()

	key, err := QualifiedSubjectKey("https://idp.example", "sub-1")
	if err != nil {
		t.Fatalf("QualifiedSubjectKey of canonical parts: %v", err)
	}
	if key != QualifiedKey("https://idp.example\x1fsub-1") {
		t.Fatalf("key = %q, want issuer, U+001F, subject", key)
	}
	stored := FederatedIdentity{Issuer: "https://idp.example", Subject: "sub-1"}.QualifiedSubject()
	if string(key) != stored {
		t.Fatalf("key = %q, want the stored form %q for canonical parts", key, stored)
	}

	refusals := []struct {
		issuer, subject, reason string
	}{
		{"", "sub-1", "empty"},
		{"https://idp.example", "", "empty"},
		{" https://idp.example", "sub-1", "whitespace"},
		{"https://idp.example", "sub-1 ", "whitespace"},
		{"https://idp.example", "sub-1\u00a0", "whitespace"},
		{"https://idp.example", "sub\x1f1", "control"},
		{"https://idp.example\x1fx", "sub-1", "control"},
		{"https://idp.example", "s\x7fub", "control"},
		{"https://idp.example", "sub-1\n", "control"},
	}
	for _, c := range refusals {
		key, err := QualifiedSubjectKey(c.issuer, c.subject)
		var unkeyable OwnerUnkeyableError
		if !errors.As(err, &unkeyable) || unkeyable.Reason != c.reason || !errors.Is(err, ErrOwnerUnkeyable) {
			t.Errorf("QualifiedSubjectKey(%q, %q) = %q, %v; want the %s refusal", c.issuer, c.subject, key, err, c.reason)
		}
		if key != "" {
			t.Errorf("QualifiedSubjectKey(%q, %q) returned key %q with a refusal", c.issuer, c.subject, key)
		}
	}
}

// indexSeam is a test-owned index constructor. It builds the production suffix
// array for every corpus a match asks for and records what the match asked of
// it, so the test, not the matcher, counts the index work.
type indexSeam struct {
	builds       int
	indexedBytes int
	lookups      int
	candidates   int
	limits       []int
}

// build is the constructor matchAliases receives.
func (s *indexSeam) build(data []byte) corpusIndex {
	s.builds++
	s.indexedBytes += len(data)
	return seamIndex{index: suffixarray.New(data), seam: s}
}

// seamIndex forwards each query to the production suffix array and records it.
type seamIndex struct {
	index *suffixarray.Index
	seam  *indexSeam
}

// Lookup implements corpusIndex.
func (i seamIndex) Lookup(s []byte, n int) []int {
	i.seam.lookups++
	i.seam.limits = append(i.seam.limits, n)
	positions := i.index.Lookup(s, n)
	i.seam.candidates += len(positions)
	return positions
}

func TestMatchAliasesIndexesEachFormOnce(t *testing.T) {
	t.Parallel()

	// Many short decoded strings and thousands of aliases the document does not
	// name: one index per form and one query per alias, whatever the number of
	// representations. The seam counts, not the matcher: a match that bypassed
	// the index would show no query, whatever it recorded itself.
	decoded := make([]string, 4096)
	for i := range decoded {
		decoded[i] = fmt.Sprintf("s%04x", i)
	}
	doc := Document{Raw: `{"owner":"Named@X.test"}`, Decoded: decoded}
	var aliases []Alias
	for i := 0; i < 3000; i++ {
		aliases = append(aliases,
			Alias{Kind: AliasExternal, Value: fmt.Sprintf("absent-ext-%d", i)},
			Alias{Kind: AliasEmail, Value: fmt.Sprintf("absent-%d@x.test", i)})
	}
	aliases = append(aliases,
		Alias{Kind: AliasEmail, Value: "named@x.test"},
		Alias{Kind: AliasExternal, Value: ""},
		Alias{Kind: AliasAccountID, Value: "not-an-id"})
	var seam indexSeam
	hits, err := matchAliases(doc, aliases, CountedContentLimits(), seam.build)
	if err != nil {
		t.Fatalf("matchAliases: %v", err)
	}
	for i, hit := range hits {
		if hit != (i == 6000) {
			t.Fatalf("alias %d (%q) hit = %t; want only the named email to hit", i, aliases[i].Value, hit)
		}
	}
	form := len(doc.Raw) + 5*len(decoded)
	if seam.builds != 2 || seam.indexedBytes != 2*form || seam.lookups != 6001 || seam.candidates != 1 {
		t.Fatalf("the index was built %d times over %d bytes and answered %d queries with %d candidates; want 2, %d, 6001 and 1: one index per form and one query per non-empty alias",
			seam.builds, seam.indexedBytes, seam.lookups, seam.candidates, 2*form)
	}
}

func TestMatchAliasesIndexesWithTheSuffixArray(t *testing.T) {
	t.Parallel()

	// The seam tests count the work of whatever constructor they pass. This pins
	// the production constructor, which MatchAliases passes: it indexes a corpus
	// as a standard-library suffix array, not as any other index with the same
	// Lookup contract.
	index := newSuffixIndex([]byte("ab xab ab ab"))
	if _, ok := index.(*suffixarray.Index); !ok {
		t.Fatalf("newSuffixIndex built a %T, want a *suffixarray.Index", index)
	}
}

func TestMatchAliasesNeverMatchesAcrossRepresentations(t *testing.T) {
	t.Parallel()

	const id = "a1b2c3d4-0000-4000-8000-00000000000a"
	cases := []struct {
		name  string
		doc   Document
		alias Alias
		want  bool
	}{
		{"an external id split across the raw bytes and a decoded string", Document{Raw: "ab", Decoded: []string{"cd"}}, Alias{AliasExternal, "bc"}, false},
		{"an external id split across two decoded strings", Document{Decoded: []string{"x-1", "2-y"}}, Alias{AliasExternal, "12"}, false},
		{"arbitrary external-id bytes across a boundary", Document{Raw: "a\x00", Decoded: []string{"\xffb"}}, Alias{AliasExternal, "\x00\xff"}, false},
		{"an email split across two decoded strings", Document{Decoded: []string{"jdoe@", "x.test"}}, Alias{AliasEmail, "jdoe@x.test"}, false},
		{"an id split across two decoded strings", Document{Decoded: []string{id[:10], id[10:]}}, Alias{AliasAccountID, id}, false},
		{"a whole token ends where its representation ends", Document{Raw: "jdoe@x.test", Decoded: []string{"z"}}, Alias{AliasEmail, "jdoe@x.test"}, true},
		{"a whole token starts where its representation starts", Document{Raw: "a", Decoded: []string{"00u1abcd"}}, Alias{AliasExternal, "00u1abcd"}, true},
		{"a representation that shrinks when lowered keeps its boundary", Document{Raw: "\u0130", Decoded: []string{"x@y.z"}}, Alias{AliasEmail, "ix@y.z"}, false},
		{"a representation that shrinks when lowered still matches inside", Document{Decoded: []string{"\u0130x@y.z"}}, Alias{AliasEmail, "ix@y.z"}, true},
		{"the Kelvin sign lowers as strings.ToLower lowers it", Document{Raw: "\u212adoe@x.test"}, Alias{AliasEmail, "kdoe@x.test"}, true},
		{"an empty decoded string between two others", Document{Decoded: []string{"ab", "", "cd"}}, Alias{AliasExternal, "cd"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits, err := MatchAliases(c.doc, []Alias{c.alias}, CountedContentLimits())
			if err != nil {
				t.Fatalf("MatchAliases: %v", err)
			}
			if len(hits) != 1 || hits[0] != c.want {
				t.Fatalf("hits = %v, want [%v]", hits, c.want)
			}
		})
	}
}

func TestMatchAliasesCountsEveryCandidateItsIndexReturns(t *testing.T) {
	t.Parallel()

	// "ab" occurs four times; the first occurrence is a whole token. Every
	// candidate a query returns counts, including those after the hit, so the
	// budget bounds the work of the query itself.
	doc := Document{Raw: "ab xab ab ab"}
	aliases := []Alias{{Kind: AliasExternal, Value: "ab"}}
	lim := CountedContentLimits()

	lim.MaxOccurrences = 4
	var seam indexSeam
	hits, err := matchAliases(doc, aliases, lim, seam.build)
	if err != nil || len(hits) != 1 || !hits[0] || seam.candidates != 4 {
		t.Fatalf("at four candidates: hits %v, %d candidates, error %v; want [true], 4, nil", hits, seam.candidates, err)
	}

	lim.MaxOccurrences = 3
	_, err = MatchAliases(doc, aliases, lim)
	var capacity CapacityError
	if !errors.As(err, &capacity) || capacity.Dimension != "occurrences" {
		t.Fatalf("at three: error %v, want an occurrences capacity refusal", err)
	}
}

func TestMatchAliasesRefusesANegativeLimitBeforeIndexing(t *testing.T) {
	t.Parallel()

	doc := Document{Raw: "ab xab ab ab"}
	aliases := []Alias{{Kind: AliasExternal, Value: "ab"}, {Kind: AliasExternal, Value: "absent"}}
	for _, c := range []struct {
		name string
		set  func(*MatchLimits)
	}{
		{"MaxContentBytes", func(l *MatchLimits) { l.MaxContentBytes = -1 }},
		{"MaxDecodedStrings", func(l *MatchLimits) { l.MaxDecodedStrings = -1 }},
		{"MaxDecodedBytes", func(l *MatchLimits) { l.MaxDecodedBytes = -1 }},
		{"MaxCanonicalIDs", func(l *MatchLimits) { l.MaxCanonicalIDs = -1 }},
		{"MaxOccurrences", func(l *MatchLimits) { l.MaxOccurrences = -2 }},
		// -1 is the boundary case: were it admitted, the first query would ask
		// the index for no position at all.
		{"MaxOccurrences at minus one", func(l *MatchLimits) { l.MaxOccurrences = -1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			lim := CountedContentLimits()
			c.set(&lim)
			var seam indexSeam
			hits, err := matchAliases(doc, aliases, lim, seam.build)
			if !errors.Is(err, errNegativeMatchLimit) || hits != nil || seam.builds != 0 || seam.lookups != 0 {
				t.Fatalf("a negative %s: hits %v, error %v, %d builds, %d queries; want the invalid-limit refusal before any index is built or queried",
					c.name, hits, err, seam.builds, seam.lookups)
			}
			if _, err := CanonicalIDs(doc, lim); !errors.Is(err, errNegativeMatchLimit) {
				t.Fatalf("a negative %s: CanonicalIDs error %v, want the invalid-limit refusal", c.name, err)
			}
		})
	}

	t.Run("a zero occurrence limit admits no candidate and asks for at most one", func(t *testing.T) {
		lim := CountedContentLimits()
		lim.MaxOccurrences = 0
		var seam indexSeam
		hits, err := matchAliases(doc, []Alias{{Kind: AliasExternal, Value: "absent"}}, lim, seam.build)
		if err != nil || len(hits) != 1 || hits[0] || len(seam.limits) != 1 || seam.limits[0] != 1 {
			t.Fatalf("an absent alias at zero: hits %v, error %v, query limits %v; want [false], nil, [1]", hits, err, seam.limits)
		}
		_, err = matchAliases(doc, []Alias{{Kind: AliasExternal, Value: "ab"}}, lim, (&indexSeam{}).build)
		var capacity CapacityError
		if !errors.As(err, &capacity) || capacity.Dimension != "occurrences" {
			t.Fatalf("a present alias at zero: error %v, want an occurrences capacity refusal", err)
		}
	})

	// Every other limit is valid at zero too: it admits a document with none of
	// its unit, and the first unit refuses as that limit's capacity.
	const id = "a1b2c3d4-0000-4000-8000-00000000000a"
	for _, c := range []struct {
		name      string
		set       func(*MatchLimits)
		none, one Document
		dimension string
	}{
		{"MaxContentBytes", func(l *MatchLimits) { l.MaxContentBytes = 0 },
			Document{Decoded: []string{"ab"}}, Document{Raw: "a"}, CapacityContentBytes},
		{"MaxDecodedStrings", func(l *MatchLimits) { l.MaxDecodedStrings = 0 },
			Document{Raw: "ab"}, Document{Raw: "ab", Decoded: []string{""}}, CapacityDecodedStrings},
		{"MaxDecodedBytes", func(l *MatchLimits) { l.MaxDecodedBytes = 0 },
			Document{Raw: "ab", Decoded: []string{""}}, Document{Raw: "ab", Decoded: []string{"a"}}, CapacityDecodedBytes},
		{"MaxCanonicalIDs", func(l *MatchLimits) { l.MaxCanonicalIDs = 0 },
			Document{Raw: "ab"}, Document{Raw: id}, CapacityCanonicalIDs},
	} {
		t.Run("a zero "+c.name, func(t *testing.T) {
			lim := CountedContentLimits()
			c.set(&lim)
			var seam indexSeam
			hits, err := matchAliases(c.none, []Alias{{Kind: AliasExternal, Value: "ab"}}, lim, seam.build)
			if err != nil || len(hits) != 1 || !hits[0] {
				t.Fatalf("a zero %s over a document with none of its unit: hits %v, error %v; want [true], nil",
					c.name, hits, err)
			}
			if ids, err := CanonicalIDs(c.none, lim); err != nil || len(ids) != 0 {
				t.Fatalf("a zero %s over a document with none of its unit: CanonicalIDs %v, error %v; want none, nil",
					c.name, ids, err)
			}
			_, err = CanonicalIDs(c.one, lim)
			var capacity CapacityError
			if !errors.As(err, &capacity) || capacity.Dimension != c.dimension {
				t.Fatalf("a zero %s over a document with one unit: error %v, want a %s capacity refusal",
					c.name, err, c.dimension)
			}
		})
	}
}
