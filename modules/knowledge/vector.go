// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// ---- classification clearance (the ordered governance label set) ---------------

// Classification labels, in increasing sensitivity. Retrieval admits a chunk only
// when its classification rank is <= the requesting identity's clearance rank.
const (
	classPublic       = "public"
	classInternal     = "internal"
	classConfidential = "confidential"
	classSecret       = "secret"
)

// classificationRank orders the labels. An unknown label is absent from the map,
// so the comma-ok lookup makes any unrecognized classification fail CLOSED (never
// a silent "treated as public"). "restricted" is an alias for the top rank.
var classificationRank = map[string]int{
	classPublic: 0, classInternal: 1, classConfidential: 2, classSecret: 3, "restricted": 3,
}

// normClass normalizes a classification label; empty means public (the default a
// document/chunk without a source label inherits).
func normClass(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return classPublic
	}
	return s
}

// classificationAllowed reports whether an identity with the given clearance may
// see a chunk of the given classification. It fails CLOSED on either side: an
// unknown chunk label or an unknown clearance denies (a garbage label must never
// behave like "public").
func classificationAllowed(chunkClass, clearance string) bool {
	cr, ok := classificationRank[normClass(chunkClass)]
	if !ok {
		return false
	}
	lr, ok := classificationRank[normClass(clearance)]
	if !ok {
		return false
	}
	return cr <= lr
}

// aclAllows reports whether an identity in the given groups may see a chunk with
// the given (already effective) ACL. An empty ACL or one containing "anyone" is
// unrestricted; otherwise the identity must share at least one group reference.
func aclAllows(chunkACL, groups []string) bool {
	if len(chunkACL) == 0 {
		return true
	}
	have := make(map[string]bool, len(groups))
	for _, g := range groups {
		have[g] = true
	}
	for _, a := range chunkACL {
		if a == "anyone" || have[a] {
			return true
		}
	}
	return false
}

// ---- embedding serialization (M1: a magic+version+dim prefix) -------------------

// embedMagic tags a stored embedding blob with a format+version marker so a
// dimension or byte-order mismatch is caught at decode, not as silent cosine
// corruption (the design review's M1). Layout: magic[4] | dim:uint32 LE | dim ×
// float32 LE.
var embedMagic = [4]byte{'O', 'E', 'V', '1'}

// errBadEmbedding is returned when a stored embedding blob is malformed.
var errBadEmbedding = errors.New("knowledge: malformed embedding blob")

// encodeEmbedding serializes a vector to the magic-prefixed little-endian form.
func encodeEmbedding(v []float32) []byte {
	out := make([]byte, 8+4*len(v))
	copy(out[0:4], embedMagic[:])
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(v)))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[8+4*i:], math.Float32bits(f))
	}
	return out
}

// decodeEmbedding parses a magic-prefixed blob back into a vector, validating the
// magic and the declared dimension against the byte length.
func decodeEmbedding(b []byte) ([]float32, error) {
	if len(b) < 8 || b[0] != embedMagic[0] || b[1] != embedMagic[1] || b[2] != embedMagic[2] || b[3] != embedMagic[3] {
		return nil, errBadEmbedding
	}
	dim := int(binary.LittleEndian.Uint32(b[4:8]))
	if dim < 0 || len(b) != 8+4*dim {
		return nil, errBadEmbedding
	}
	v := make([]float32, dim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[8+4*i:]))
	}
	return v, nil
}

// cosine returns the cosine similarity of two equal-length vectors, or 0 for a
// length mismatch or a zero vector (never NaN).
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ---- cosineIndex: the default, in-process exact ranker --------------------------

// cosineIndex ranks governance-filtered candidates by exact cosine similarity in
// process. It is the self-host/air-gap default behind the VectorIndex seam: zero
// extra services, exact (not approximate) results. Its honest limit is a linear
// scan per query — fine to ~10^5 chunks/tenant; an external ANN backend (pgvector
// at scale) plugs in behind VectorIndex for larger corpora.
type cosineIndex struct{}

func (cosineIndex) Rank(_ context.Context, query []float32, candidates []VectorCandidate, topK int) ([]ScoredChunk, error) {
	scored := make([]ScoredChunk, 0, len(candidates))
	for _, c := range candidates {
		scored = append(scored, ScoredChunk{ChunkID: c.ChunkID, Score: cosine(query, c.Vector)})
	}
	// Deterministic order: score desc, then chunk id asc as a stable tiebreaker.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].ChunkID < scored[j].ChunkID
	})
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}

// ---- LocalHashEmbedder: the zero-egress default embedder ------------------------

// localEmbedDim is the fixed dimension of the local feature-hashing embedder.
const localEmbedDim = 256

// LocalHashModelRef is the model reference recorded for the local embedder so a
// reader (API/UI/lineage) always knows the vectors are non-semantic.
const LocalHashModelRef = "local-hash"

// LocalHashEmbedder is the default embedder: a deterministic, dependency-free
// feature-hashing bag-of-words embedder (token → FNV bucket → L2-normalized
// vector). It is a REAL, deterministic embedding, but NON-SEMANTIC — it matches
// lexical tokens, not meaning. It exists so a knowledge base WORKS out of the box
// with ZERO EGRESS (the air-gap/self-host guarantee: the data never leaves to an
// embedding provider). For semantic retrieval the composition root wires a
// model-backed Embedder; the KB records embed_model="local-hash" so the
// non-semantic fallback is never silently mistaken for semantic quality.
type LocalHashEmbedder struct{}

// Embed returns one L2-normalized feature-hash vector per text. It never errors
// and never egresses.
func (LocalHashEmbedder) Embed(_ context.Context, _ model.TenantID, texts []string) ([][]float32, string, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = featureHash(t, localEmbedDim)
	}
	return out, LocalHashModelRef, nil
}

// Dim returns the local embedder's fixed dimension.
func (LocalHashEmbedder) Dim() int { return localEmbedDim }

// AllowsEgress is false: the local embedder never sends text out of the perimeter.
func (LocalHashEmbedder) AllowsEgress() bool { return false }

// ModelRef returns the local embedder's stable reference.
func (LocalHashEmbedder) ModelRef() string { return LocalHashModelRef }

// featureHash builds an L2-normalized term-frequency vector over hashed token
// buckets. Deterministic for a given text and dim.
func featureHash(text string, dim int) []float32 {
	v := make([]float32, dim)
	for _, tok := range tokenize(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		v[h.Sum32()%uint32(dim)]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// tokenize lowercases and splits text into alphanumeric tokens.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// ---- chunking -------------------------------------------------------------------

// maxChunkRunes bounds one chunk's size; chunkText splits on paragraph boundaries
// and accumulates up to this many runes so a chunk is a coherent unit, not a
// fixed-width cut through a word.
const maxChunkRunes = 1200

// chunkText splits a (already-redacted) body into chunks. It splits on blank lines
// into paragraphs and packs paragraphs into chunks up to maxChunkRunes. A single
// oversized paragraph is hard-split. It never returns an empty chunk.
func chunkText(body string) []string {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	paras := splitParagraphs(body)
	var chunks []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			chunks = append(chunks, strings.TrimSpace(cur.String()))
			cur.Reset()
			curLen = 0
		}
	}
	for _, p := range paras {
		pr := []rune(p)
		if len(pr) > maxChunkRunes {
			flush()
			for start := 0; start < len(pr); start += maxChunkRunes {
				end := start + maxChunkRunes
				if end > len(pr) {
					end = len(pr)
				}
				chunks = append(chunks, strings.TrimSpace(string(pr[start:end])))
			}
			continue
		}
		if curLen+len(pr) > maxChunkRunes {
			flush()
		}
		if curLen > 0 {
			cur.WriteString("\n\n")
			curLen += 2
		}
		cur.WriteString(p)
		curLen += len(pr)
	}
	flush()
	return chunks
}

// splitParagraphs splits on blank lines, dropping empties.
func splitParagraphs(body string) []string {
	raw := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return []string{strings.TrimSpace(body)}
	}
	return out
}

// tokenCount is an approximate token count (whitespace-delimited words) recorded
// per chunk for context/compaction accounting.
func tokenCount(s string) int64 {
	return int64(len(strings.Fields(s)))
}
