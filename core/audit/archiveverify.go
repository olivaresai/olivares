// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/store"
)

// ArchiveVerifyOptions select which signature checks the archive verifier runs
// on top of the always-on structural checks (canon re-derivation, linkage,
// declared-gap validation, manifest digests, cross-segment continuity).
type ArchiveVerifyOptions struct {
	// EventKeys are the epoch-fenced candidate Ed25519 keys for per-event
	// signatures (current + prior generations, the VerifyEventsFenced model).
	// Each key carries the per-tenant sequence range it is trusted to have
	// signed: an unbounded key (LastSeq==0) is the current generation, a bounded
	// key (LastSeq==L) is a RETIRED generation valid only through its epoch — so a
	// single unbounded key reproduces the pre single-generation behavior
	// exactly, while a rotated archive is fenced (a retired key can NEVER validate
	// a current-epoch archived event, closing F-07). Empty skips the per-event
	// signature check; non-empty makes a missing signature on a non-checkpoint
	// event a failure, exactly like VerifyEventsFenced. Non-empty EventKeys
	// WITHOUT Checkpoints makes every checkpoint line a failure
	// ("checkpoint-unverifiable"): checkpoint lines are exempt from the
	// per-event check, so an attacker who re-derived a whole forged chain could
	// otherwise dress every event as a checkpoint and dodge the only signature
	// check in force — pin both key sets, or neither (the advisory
	// chain-structure-only mode). The boundaries come from the operator's
	// `--event-pubkey key@last_seq` pins (the attacker-resistant flow, identical
	// to the live path's external pin); an archive's own keys.json lists the
	// generations UNFENCED, so an advisory verify against it stays honestly
	// advisory — a keys file that rode with the archive proves nothing (docs/SECURITY-HARDENING.md).
	EventKeys []FencedKey
	// Checkpoints verifies each checkpoint event's signature over its attested
	// head (the event's own PrevHash — O(1) in the stream, unlike
	// VerifyCheckpointsWith's O(chain) seq→hash map). nil/empty skips it.
	Checkpoints *CheckpointVerifier
}

// ArchiveVerifyReport is the outcome of verifying an archive directory. Its
// Reason vocabulary extends store.VerifyReport's ("hash-mismatch",
// "prev-mismatch", "seq-gap", "gap-mismatch") and the signature reports'
// ("event-sig-invalid", "event-sig-missing", "checkpoint-sig-invalid",
// "checkpoint-unverifiable", "recovery-sig-invalid",
// "recovery-position-invalid", "recovery-unverifiable", "keyrotation-sig-invalid",
// "keyrotation-position-invalid", "keyrotation-unverifiable") with archive-specific reasons:
// "manifest-unreadable", "bad-format", "key-mismatch", "events-missing",
// "manifest-missing", "bad-line", "line-not-canonical", "count-mismatch",
// "first-hash-mismatch", "last-hash-mismatch", "events-sha256-mismatch",
// "segment-gap", "segment-link-mismatch", "no-events".
type ArchiveVerifyReport struct {
	// OK is true only when at least one archived event was found and every
	// segment of every tenant verified.
	OK bool
	// Tenants/Segments/Events/Checkpoints count what was checked.
	Tenants     int
	Segments    int
	Events      int64
	Checkpoints int
	// DeclaredGaps is the number of sanctioned, marker-declared holes crossed.
	// They do not fail verification; authenticity rides on each marker's normal
	// per-event signature when EventKeys are pinned, so structure-only checking
	// remains advisory just as it is for every other event.
	DeclaredGaps int64
	// Ranges maps each tenant to the contiguous sequence range its verified
	// segments cover. An auditor MUST read it: a green OK attests exactly this
	// range and nothing outside it (a removed prefix or tail is offline-
	// undetectable; see VerifyArchiveDir).
	Ranges map[string]ArchiveTenantRange
	// BreakTenant/BreakSegment/BreakAt locate the first failure (the tenant id,
	// the failing segment's events key, and the event sequence when the failure
	// is event-level, else 0).
	BreakTenant  string
	BreakSegment string
	BreakAt      int64
	// Reason describes the first failure, or "".
	Reason string
}

func (r *ArchiveVerifyReport) fail(tenant, segment string, seq int64, reason string) {
	if r.Reason == "" {
		r.BreakTenant = tenant
		r.BreakSegment = segment
		r.BreakAt = seq
		r.Reason = reason
	}
}

// ArchiveTenantRange is the contiguous sequence range one tenant's verified
// segments cover.
type ArchiveTenantRange struct {
	FromSeq int64
	ToSeq   int64
	// StartsMidChain is true when FromSeq > 1: the archive does not reach back
	// to genesis, so everything before FromSeq is simply NOT attested (an
	// offline verifier cannot tell a legitimate partial export from a removed
	// prefix). It is deliberately not a failure — partial exports are
	// legitimate — but the flag must reach the auditor.
	StartsMidChain bool
}

// archiveSeg pairs one manifest with its on-disk file paths during a verify.
type archiveSeg struct {
	manifest     SegmentManifest
	eventsPath   string
	manifestPath string
}

// maxArchiveLine bounds one JSONL line during verify (audit meta is minimal
// data, so real lines are far smaller; the bound only prevents a hostile file
// from ballooning memory — the verifier is constant-memory by design).
const maxArchiveLine = 4 << 20

// VerifyArchiveDir verifies an archive directory tree written by
// ExportSegments: every "*.jsonl.manifest.json" under dir is
// loaded, grouped by tenant and ordered by from_seq; each segment is then
// streamed event-by-event in constant memory. Per event it re-derives the
// canon chain hash from the archived fields (the stored canonical meta string
// is the MetaDigest input), requires the line to be the canonical writer bytes
// (a re-marshal of the parsed line must reproduce the on-disk bytes — the
// manifest is unsigned, so events_sha256 alone is attacker-fixable and cannot
// stop unknown-field/duplicate-key smuggling), checks prev linkage and
// gap-freedom, and — with keys in opts — the per-event and checkpoint
// signatures under their own domains. Per segment it checks first/last hash,
// count and the events file's SHA-256 against the manifest; across segments it
// checks contiguity (from = prev.to+1) and the prev_segment_last_hash link, so
// an INTERIOR removed or reordered segment cannot hide. A removed PREFIX or
// TAIL is out of scope offline: nothing in the directory says where the chain
// began or ended, so the report's per-tenant Ranges (with StartsMidChain) state
// exactly what was attested — the in-chain anchor events and signed checkpoints
// of the live chain are what cover the tail. It is offline by construction: no
// store, no network. Like store.AuditLog.Verify it reports the FIRST
// inconsistency rather than erroring; an error is environmental (unreadable
// directory).
func VerifyArchiveDir(ctx context.Context, dir string, opts ArchiveVerifyOptions) (ArchiveVerifyReport, error) {
	rep := ArchiveVerifyReport{}
	if err := validateFencedKeys(opts.EventKeys); err != nil {
		return ArchiveVerifyReport{}, err
	}
	// One epoch partition for the whole archive: the pinned key set is constant
	// across every tenant and segment, so the fence is built once and shared,
	// exactly the selector the live VerifyEventsFenced uses.
	fence := newEpochFence(opts.EventKeys)

	var manifestPaths, eventsPaths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(p, ".jsonl.manifest.json"):
			manifestPaths = append(manifestPaths, p)
		case strings.HasSuffix(p, ".jsonl"):
			eventsPaths = append(eventsPaths, p)
		}
		return nil
	})
	if err != nil {
		return ArchiveVerifyReport{}, fmt.Errorf("audit: archive verify: %w", err)
	}

	// Pair manifests with their events files and group by tenant. A stray events
	// file (no manifest) or a manifest whose events file is gone is a failure:
	// the archive's unit of evidence is the pair.
	manifestFor := make(map[string]bool, len(manifestPaths))
	byTenant := map[string][]archiveSeg{}
	for _, mp := range manifestPaths {
		evPath := strings.TrimSuffix(mp, ".manifest.json")
		manifestFor[evPath] = true
		b, rerr := os.ReadFile(mp)
		if rerr != nil {
			return ArchiveVerifyReport{}, fmt.Errorf("audit: archive verify: %w", rerr)
		}
		var m SegmentManifest
		if jerr := json.Unmarshal(b, &m); jerr != nil {
			rep.fail("", relKey(dir, evPath), 0, "manifest-unreadable")
			return rep, nil
		}
		if !slices.Contains(ArchiveFormats, m.Format) {
			rep.fail(m.Tenant, relKey(dir, evPath), 0, "bad-format")
			return rep, nil
		}
		// The file name commits to the range (SegmentKey): a renamed segment
		// cannot impersonate another range even before the hash links are walked.
		if filepath.Base(evPath) != fmt.Sprintf("seg-%012d-%012d.jsonl", m.FromSeq, m.ToSeq) {
			rep.fail(m.Tenant, relKey(dir, evPath), 0, "key-mismatch")
			return rep, nil
		}
		if _, serr := os.Stat(evPath); serr != nil {
			rep.fail(m.Tenant, relKey(dir, evPath), 0, "events-missing")
			return rep, nil
		}
		byTenant[m.Tenant] = append(byTenant[m.Tenant], archiveSeg{manifest: m, eventsPath: evPath, manifestPath: mp})
	}
	for _, ep := range eventsPaths {
		if !manifestFor[ep] {
			rep.fail("", relKey(dir, ep), 0, "manifest-missing")
			return rep, nil
		}
	}

	tenants := make([]string, 0, len(byTenant))
	for t := range byTenant {
		tenants = append(tenants, t)
	}
	sort.Strings(tenants)
	rep.Ranges = make(map[string]ArchiveTenantRange, len(tenants))
	for _, tenant := range tenants {
		segs := byTenant[tenant]
		sort.Slice(segs, func(i, j int) bool { return segs[i].manifest.FromSeq < segs[j].manifest.FromSeq })
		rep.Tenants++
		var prev *SegmentManifest
		for i := range segs {
			if err := ctx.Err(); err != nil {
				return ArchiveVerifyReport{}, err
			}
			m := segs[i].manifest
			key := relKey(dir, segs[i].eventsPath)
			if prev != nil {
				// Cross-segment continuity needs no external state: contiguous
				// ranges plus the prev_segment_last_hash link.
				if m.FromSeq != prev.ToSeq+1 {
					rep.fail(tenant, key, 0, "segment-gap")
					return rep, nil
				}
				if m.PrevSegmentLastHash != prev.LastHash {
					rep.fail(tenant, key, 0, "segment-link-mismatch")
					return rep, nil
				}
			}
			if !verifySegmentStream(&rep, tenant, key, segs[i].eventsPath, m, opts, fence) {
				return rep, nil
			}
			rep.Segments++
			prev = &segs[i].manifest
			// Ranges record only VERIFIED segments: a green OK attests exactly
			// this contiguous range, nothing before or after it.
			r := rep.Ranges[tenant]
			if r.ToSeq == 0 {
				r.FromSeq = m.FromSeq
				r.StartsMidChain = m.FromSeq > 1
			}
			r.ToSeq = m.ToSeq
			rep.Ranges[tenant] = r
		}
	}
	if rep.Events == 0 {
		rep.Reason = "no-events"
	} else if rep.Reason == "" {
		rep.OK = true
	}
	return rep, nil
}

// verifySegmentStream streams one events file against its manifest, recording
// the first failure in rep. It returns false to stop the whole verify (a
// failure was recorded) and true when the segment is intact. Constant memory:
// one line plus running digests.
func verifySegmentStream(rep *ArchiveVerifyReport, tenant, key, eventsPath string, m SegmentManifest, opts ArchiveVerifyOptions, fence epochFence) bool {
	f, err := os.Open(eventsPath)
	if err != nil {
		rep.fail(tenant, key, 0, "events-missing")
		return false
	}
	defer f.Close()

	// The scanner consumes the file THROUGH the digest, so reaching EOF means
	// the running SHA-256 covers exactly the bytes on disk.
	fileSum := sha256.New()
	sc := bufio.NewScanner(io.TeeReader(f, fileSum))
	sc.Buffer(make([]byte, 64*1024), maxArchiveLine)

	expectedSeq := m.FromSeq
	expectedPrev, err := hex.DecodeString(m.PrevSegmentLastHash)
	if err != nil {
		rep.fail(tenant, key, 0, "manifest-unreadable")
		return false
	}
	var count int64
	// sawBlind records whether any line in THIS segment carried a blind, so the
	// declared format can be bound to the segment's own content below.
	var sawBlind bool
	var firstHash, lastHash string
	checkEventSigs := len(opts.EventKeys) > 0
	checkCpSigs := opts.Checkpoints != nil && !opts.Checkpoints.Empty()
	for sc.Scan() {
		raw := sc.Bytes()
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var line archiveLine
		if jerr := dec.Decode(&line); jerr != nil {
			// Valid JSON that smuggles unknown fields is a canonicality failure
			// (data riding outside the hash); bytes that are not JSON at all are
			// a plain bad line.
			if json.Unmarshal(raw, &line) == nil {
				rep.fail(tenant, key, line.Seq, "line-not-canonical")
			} else {
				rep.fail(tenant, key, expectedSeq, "bad-line")
			}
			return false
		}
		// The verifier binds the BYTES, not just the parsed values: BuildSegment
		// writes exactly json.Marshal(archiveLine{...}), so re-marshaling the
		// parsed line (the same struct — equality is honest by construction) must
		// reproduce the on-disk line. The manifest is unsigned, so events_sha256
		// alone is attacker-fixable: without this check duplicate keys, reordered
		// fields or trailing bytes could ride inside a "verified" archive without
		// touching any hash.
		if remarshaled, merr := json.Marshal(line); merr != nil || !bytes.Equal(remarshaled, raw) {
			rep.fail(tenant, key, line.Seq, "line-not-canonical")
			return false
		}
		prevHash, perr := hex.DecodeString(line.PrevHash)
		hash, herr := hex.DecodeString(line.Hash)
		payloadHash, pherr := hex.DecodeString(line.PayloadHash)
		sig, serr := base64.StdEncoding.DecodeString(line.Sig)
		// An absent meta_blind is a v1 line, whose rows were sealed under the
		// unblinded rule; a PRESENT but unreadable one is a corrupt line, and
		// tolerating it would silently fall back to a rule this row was not sealed
		// under and report a hash mismatch that looks like tampering.
		//
		// The nil stays nil deliberately: hex.DecodeString("") returns an EMPTY BUT
		// NON-NIL slice, and the discriminator distinguishes absent (nil, legacy
		// rule) from present (BlindLen bytes) — an empty non-nil slice is neither
		// and is refused. Decoding unconditionally would turn every v1 line into a
		// malformed one.
		var blind []byte
		var berr error
		if line.MetaBlind != "" {
			blind, berr = hex.DecodeString(line.MetaBlind)
		}
		if perr != nil || herr != nil || pherr != nil || serr != nil || berr != nil {
			rep.fail(tenant, key, line.Seq, "bad-line")
			return false
		}
		rep.Events++
		count++
		// meta "" is normalized to the canonical empty form "{}" (the store
		// never writes ""; this only tolerates a normalizing writer).
		meta := line.Meta
		if meta == "" {
			meta = "{}"
		}
		if line.Seq != expectedSeq {
			if line.Action != ActionGap {
				rep.fail(tenant, key, expectedSeq, "seq-gap")
				return false
			}
			if !store.DeclaresGap(meta, expectedSeq, line.Seq) {
				rep.fail(tenant, key, line.Seq, "gap-mismatch")
				return false
			}
			rep.DeclaredGaps++
			expectedSeq = line.Seq
		} else if line.Action == ActionGap {
			rep.fail(tenant, key, line.Seq, "gap-mismatch")
			return false
		}
		if !bytesEq(prevHash, expectedPrev) {
			rep.fail(tenant, key, line.Seq, "prev-mismatch")
			return false
		}
		// Re-derive the chain hash from the archived fields alone — the check an
		// external copy could not perform before the canonical meta was carried
		//.
		commitment, cerr := canon.MetaCommitmentFor(blind, meta)
		if cerr != nil {
			rep.fail(tenant, key, line.Seq, "meta-blind-malformed")
			return false
		}
		if blind != nil {
			sawBlind = true
		}
		want, herr2 := canon.EventHash(canon.Event{
			TenantID:       tenant,
			Seq:            line.Seq,
			OccurredAt:     line.OccurredAt,
			Actor:          line.Actor,
			ActorKind:      line.ActorKind,
			Action:         line.Action,
			TargetKind:     line.TargetKind,
			TargetID:       line.TargetID,
			MetaCommitment: commitment,
			PayloadHash:    payloadHash,
			PrevHash:       prevHash,
		})
		if herr2 != nil {
			rep.fail(tenant, key, line.Seq, "malformed-record")
			return false
		}
		if !bytesEq(want, hash) {
			rep.fail(tenant, key, line.Seq, "hash-mismatch")
			return false
		}
		if line.Action == ActionCheckpoint {
			rep.Checkpoints++
			// A checkpoint's PrevHash IS the attested head (seq-1), so the
			// signature check is O(1) in the stream — no seq→hash map.
			switch {
			case checkCpSigs:
				if len(sig) == 0 || !opts.Checkpoints.verify(checkpointPreimage(tenant, line.Seq-1, prevHash), sig) {
					rep.fail(tenant, key, line.Seq, "checkpoint-sig-invalid")
					return false
				}
			case checkEventSigs:
				// Checkpoint lines are exempt from the per-event signature check,
				// so with ONLY event keys pinned an attacker who re-derived a
				// forged chain (canon needs no key) could dress every event as a
				// checkpoint and dodge the one signature check in force.
				// Deny-closed: no checkpoint key makes a checkpoint line
				// UNVERIFIABLE, never skippable.
				rep.fail(tenant, key, line.Seq, "checkpoint-unverifiable")
				return false
			}
		} else if line.Action == store.ActionAuditRecover {
			// Recovery markers use their own off-box signature domain and carry the
			// signed preimage fields in canonical Meta. They are never accepted as a
			// normal on-box event signature. In advisory structure-only mode their
			// signature remains unchecked, exactly like an unchecked checkpoint.
			switch {
			case checkCpSigs:
				evidence, derr := decodeRecoveryEvidence([]byte(meta))
				if derr != nil || validateRecoveryEvidence(evidence) != nil || evidence.Tenant != tenant ||
					len(sig) == 0 || !opts.Checkpoints.verify(recoverPreimage(evidence), sig) {
					rep.fail(tenant, key, line.Seq, "recovery-sig-invalid")
					return false
				}
				if line.Seq < 1 || evidence.QuarantinedTo != line.Seq-1 {
					rep.fail(tenant, key, line.Seq, "recovery-position-invalid")
					return false
				}
			case checkEventSigs:
				rep.fail(tenant, key, line.Seq, "recovery-unverifiable")
				return false
			}
		} else if line.Action == store.ActionAuditKeyRotation {
			// Key-rotation boundaries (F-07) are off-box signed like recovery
			// markers and are never a normal event signature. When the off-box key is
			// pinned, verify the boundary's signature and its position; with only event
			// keys pinned they are deny-closed unverifiable, exactly like recovery.
			switch {
			case checkCpSigs:
				evidence, derr := decodeKeyRotationEvidence([]byte(meta))
				if derr != nil || validateKeyRotationEvidence(evidence) != nil || evidence.Tenant != tenant ||
					len(sig) == 0 || !opts.Checkpoints.verify(keyRotationPreimage(evidence), sig) {
					rep.fail(tenant, key, line.Seq, "keyrotation-sig-invalid")
					return false
				}
				if line.Seq < 1 || evidence.PriorLastSeq != line.Seq-1 {
					rep.fail(tenant, key, line.Seq, "keyrotation-position-invalid")
					return false
				}
			case checkEventSigs:
				rep.fail(tenant, key, line.Seq, "keyrotation-unverifiable")
				return false
			}
		} else if checkEventSigs {
			if len(sig) == 0 {
				rep.fail(tenant, key, line.Seq, "event-sig-missing")
				return false
			}
			// Epoch-FENCED per-event check (F-07): only the key whose epoch owns
			// this sequence may validate it. A retired generation re-signing an event
			// of the current epoch fails here even though its raw Ed25519 signature is
			// valid — the archive fences exactly like the live VerifyEventsFenced.
			preimage := eventPreimage(tenant, line.Seq, hash)
			if !fence.verify(line.Seq, preimage, sig) {
				rep.fail(tenant, key, line.Seq, "event-sig-invalid")
				return false
			}
		}
		if count == 1 {
			firstHash = line.Hash
		}
		lastHash = line.Hash
		expectedSeq = line.Seq + 1
		expectedPrev = hash
	}
	if serr := sc.Err(); serr != nil {
		rep.fail(tenant, key, expectedSeq, "bad-line")
		return false
	}
	switch {
	case count != m.Count:
		rep.fail(tenant, key, 0, "count-mismatch")
	case firstHash != m.FirstHash:
		rep.fail(tenant, key, m.FromSeq, "first-hash-mismatch")
	case lastHash != m.LastHash:
		rep.fail(tenant, key, m.ToSeq, "last-hash-mismatch")
	case hex.EncodeToString(fileSum.Sum(nil)) != m.EventsSHA256:
		rep.fail(tenant, key, 0, "events-sha256-mismatch")

	// Bind the DECLARED version to the segment's own CONTENT, both ways. Accepting
	// the tag by set membership alone would let a segment claim either version
	// regardless of the lines it holds, and the version is what an out-of-tree or
	// older verifier uses to decide whether it can read the file at all.
	//
	// Honest scope: this is a SHAPE guarantee, not the thing that stops rule
	// substitution. A line's rule is already bound by its own hash — the blind
	// enters the preimage through the commitment, so stripping it from a line or
	// adding one to another changes the derived commitment and fails hash-mismatch
	// above. What this adds is that a segment is exactly what BuildSegment would
	// have produced for its content, which is what the byte-identical re-put of a
	// retried export depends on, and it removes the ambiguity of a file whose tag
	// and body disagree.
	case sawBlind && m.Format == ArchiveFormatV1:
		rep.fail(tenant, key, 0, "format-content-mismatch")
	case !sawBlind && m.Format == ArchiveFormat:
		rep.fail(tenant, key, 0, "format-content-mismatch")
	default:
		return true
	}
	return false
}

// relKey renders a path relative to the verify root (the object key as
// exported), falling back to the absolute path.
func relKey(dir, p string) string {
	if rel, err := filepath.Rel(dir, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return p
}

// LoadArchiveKeys reads the advisory keys.json at the archive root, written by
// the export (WriteArchiveKeys). ok=false when absent. Verifier-supplied pins
// REPLACE these keys (cmd_audit precedent): the file rode with the archive, so
// it proves nothing against an attacker who wrote the archive.
func LoadArchiveKeys(dir string) (ArchiveKeys, bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, ArchiveKeysName))
	if os.IsNotExist(err) {
		return ArchiveKeys{}, false, nil
	}
	if err != nil {
		return ArchiveKeys{}, false, fmt.Errorf("audit: archive keys: %w", err)
	}
	var k ArchiveKeys
	if err := json.Unmarshal(b, &k); err != nil {
		return ArchiveKeys{}, false, fmt.Errorf("audit: archive keys: %w", err)
	}
	if k.Format != ArchiveKeysFormat {
		return ArchiveKeys{}, false, fmt.Errorf("audit: archive keys: unknown format %q", k.Format)
	}
	return k, true, nil
}
