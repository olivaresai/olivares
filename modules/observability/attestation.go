// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"crypto/fips140"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/secure/modelsign"
)

// Compile-time proof for releaseDTO.VerifierAvailable: the native in-toto/DSSE
// attestation verifier (core/secure/modelsign) is linked into this
// binary, so the "a verifier is available" claim is proven by the linker, not
// asserted. There is simply nothing to verify yet: no release artifacts or
// attestation bundles exist for any built binary (beta; verified).
var _ = modelsign.VerifyAttestation

// Constant texts of the measured release and declared blocks. The pipeline
// facts were verified against the tree at (workflows + triggers in
// .github/workflows/, draft-only goreleaser config) — the running process
// cannot observe repository or CI state, so the pipeline block never claims
// more than "declared".
const (
	// releaseReason is scoped to THIS binary — the running process cannot
	// observe repository history (whether a tag was ever pushed is a repo fact
	// that belongs to the declared pipeline block, not to measured absence).
	// It is the reason for the UNSTAMPED shape only; the other not-published
	// shapes name themselves (see releaseIdentity).
	releaseReason    = "beta: no tag, signature or attestation accompanies this binary"
	releaseSigReason = "no release artifacts or attestation bundles exist for this binary"
	releaseTlogNote  = "the native verifier never claims Rekor inclusion (core/secure/modelsign)"
	// pipelineNote used to end with "it has never been fired (beta)" — a claim about
	// REPOSITORY HISTORY that the very next sentence declares unobservable, and which
	// the console rendered unconditionally beside the live release badge. Two
	// propositions on one screen, one of them not measurable from here: removed. What
	// is left is the build-time fact (the workflows are in the tree, the trigger is a
	// v* tag) plus the explicit refusal to answer the historical question.
	pipelineNote        = "release pipeline exists in the source tree and runs only on a pushed v* tag. The running process cannot observe repository or CI state, so it cannot say whether that has ever happened."
	vcsAbsentReason     = "go.work workspace build: Go stamps no vcs.* settings"
	moduleSumsDevelNote = "deps without module sums are (devel) path/workspace members; external deps are counted by non-empty module sum"
	buildInfoAbsentNote = "debug.ReadBuildInfo unavailable for this binary; module identity and sums cannot be measured"
	selfHashFailNote    = "self-hash unavailable: the running executable could not be read"

	// releasePublishedSigReason replaces releaseSigReason on a PUBLISHED binary,
	// where "no release artifacts exist" would itself be false. The verdict does
	// not change — the process still says not_verified, because the detached
	// signature over checksums.txt is not inside the binary and cannot be checked
	// in-process — but the reason stops denying the artifacts' existence.
	releasePublishedSigReason = "this binary embeds the OTA verification anchor, but the detached signature over checksums.txt is not carried inside it: release artifacts are verified out-of-band (docs/RELEASE-VERIFICATION.md)"

	// releaseProvenanceSelfDeclared is the ONLY provenance kind a running process can
	// report about its own release identity, and releaseProvenanceNote is the sentence
	// that says so on the surface. See the provenanceDTO doc in dto.go for why this is
	// a constant and why that is not the defect PR #730 fixed.
	releaseProvenanceSelfDeclared = "self_declared"
	releaseProvenanceNote         = "SELF-DECLARED build provenance, not an attestation: the version stamp and the OTA anchor are both link-time values chosen by whoever linked this binary, and this process holds no trust anchor that was not also chosen then. A build carrying both facts is release-SHAPED; whether an official release was published is a repository/distribution fact this process cannot observe. `olivares version` reports the same anchors under the same caveat (cmd/olivares/main.go)."
)

// releaseStatusPublished and releaseStatusNotPublished are the two values of
// releaseDTO.Status. not_published is NOT going away: it is the correct answer
// for every build that is not a release, which is every build until a v* tag
// fires the pipeline. What changed is that it is now REACHED by measurement
// instead of being the only value the function could produce.
const (
	releaseStatusPublished    = "published"
	releaseStatusNotPublished = "not_published"
)

// releasePipelineWorkflows are the release/posture workflows present in the
// source tree (.github/workflows/) — a build-time fact, declared, will go
// stale only if the tree changes.
var releasePipelineWorkflows = []string{
	"release.yml", "release-chart.yml", "release-provider.yml", "scorecard.yml", "patch-velocity.yml",
}

// handleAttestation returns the measured truth about the RUNNING binary.
// Everything in the binary block is measured in-process at request time
// (except the self-hash, computed once); the release block is MEASURED from
// this binary's own link-time facts; the pipeline block is declared. No store
// access.
func (m *Module) handleAttestation(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	out := attestationDTO{
		Binary:     m.measureBinary(),
		Release:    measuredRelease(m.build.Version, releaseKeyOrigin()),
		Pipeline:   declaredPipeline(),
		CapturedAt: rfc3339(m.now()),
	}
	writeJSON(w, http.StatusOK, out)
}

// measureBinary collects the measured block: ldflags metadata (plumbed via
// WithBuildInfo — main.{commit,date} never leave package main otherwise),
// debug.ReadBuildInfo, runtime constants, FIPS 140-3 mode and the self-hash.
func (m *Module) measureBinary() binaryDTO {
	b := binaryDTO{
		Version:   m.build.Version,
		Commit:    m.build.Commit,
		BuildDate: m.build.Date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		// FIPS 140-3 MODE, self-verified in-process: enabled reports the
		// GODEBUG=fips140 runtime toggle, version the module GOFIPS140 linked at
		// build time. Mode, never a validation claim (cmd/olivares/main.go:59-70).
		FIPS140: fipsDTO{Enabled: fips140.Enabled(), Version: fips140.Version()},
		Status:  "measured",
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.GoVersion != "" {
			b.GoVersion = bi.GoVersion
		}
		b.MainModule = mainModDTO{Path: bi.Main.Path, Version: bi.Main.Version}
		external, unsummed := 0, 0
		for _, dep := range bi.Deps {
			if dep.Sum != "" {
				external++
			} else {
				unsummed++
			}
		}
		// The note is EVIDENCE-derived, not asserted: the (devel) explanation is
		// attached only when sum-less deps are actually present (this repo's
		// go.work builds; a future non-workspace build reports full coverage).
		note := "all linked dependencies carry module sums"
		if unsummed > 0 {
			note = moduleSumsDevelNote
		}
		b.ModuleSums = sumsDTO{ExternalDeps: external, SumsPresent: external > 0, Note: note}
		// vcs.* stamping: empirically ABSENT under this repo's go.work workspace
		// mode (verified even with -buildvcs=true) — the check stays so a
		// future non-workspace build reports the stamp the moment it exists.
		b.VCSStamp = vcsDTO{Available: false, Reason: vcsAbsentReason}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				b.VCSStamp = vcsDTO{Available: true, Reason: "vcs.revision " + clampStr(s.Value, 64) + " stamped by the Go toolchain"}
				break
			}
		}
	} else {
		// No build info at all (not a module build): report the absence, never
		// fabricated module identity.
		b.ModuleSums = sumsDTO{Note: buildInfoAbsentNote}
		b.VCSStamp = vcsDTO{Available: false, Reason: buildInfoAbsentNote}
	}

	b.SelfSHA256, b.SelfHashNote = m.selfSHA256()
	return b
}

// selfSHA256 stream-hashes the running executable, once per process (the file
// cannot change underneath the running binary; pattern:
// cmd/olivares/externalplugins.go's plugin-hash gate). On error the hash
// is omitted and a note explains — the error text itself is not echoed (it may
// embed a host path).
func (m *Module) selfSHA256() (hash, note string) {
	m.selfHashOnce.Do(func() {
		path, err := os.Executable()
		if err != nil {
			m.selfHashNote = selfHashFailNote
			return
		}
		f, err := os.Open(path)
		if err != nil {
			m.selfHashNote = selfHashFailNote
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			m.selfHashNote = selfHashFailNote
			return
		}
		m.selfHash = hex.EncodeToString(h.Sum(nil))
	})
	return m.selfHash, m.selfHashNote
}

// --- the measured release block -------------------------------------------------
//
// UNTIL 2026-08-13 measuredRelease() TOOK NO ARGUMENTS AND READ NOTHING. It returned
// published=false / "not_published" for every binary that has ever run, so the one
// build the block exists to describe — a signed, tagged, published release — reported
// itself unpublished on its own attestation surface, and the console rendered the
// beta badge over it (attestation-view.tsx:99, components.tsx:768). The absence was
// never measured; it was asserted, and an assertion cannot become false when the fact
// changes. That is the defect: not "not_published is wrong" (it is right for every
// build made so far) but "not_published is the only answer reachable".
//
// A running process cannot see tags, artifacts or CI — that is why the pipeline block
// stays DECLARED. It CAN see the two facts the release ceremony injects at LINK time,
// and which no other build path sets together:
//
//  1. THE VERSION STAMP — goreleaser's `-X main.version={{ .Version }}`
//     (.goreleaser.yaml:102 and :151), plumbed in here by WithBuildInfo because
//     main.version never leaves package main (wire.go:953).
//  2. THE OTA TRUST ANCHOR — `-X core/release.artifactVerifyKeyB64=…`, injected by the
//     release build (.goreleaser.yaml:109 and :156; Taskfile.yml:104), and classified
//     by the same core/release.KeyOrigin() that `olivares version` already prints as
//     this binary's provenance (cmd/olivares/main.go:265).
//
// BOTH ARE REQUIRED, and the conjunction is the calibration that keeps a plain
// `task build:bin` out of the positive state: it stamps main.version from
// `git describe --tags --always` (Taskfile.yml:111), which on a tagged tree yields a
// perfectly parseable "v26.8.0", but injects no anchor.
//
// WHAT THE CONJUNCTION IS NOT — AND THE SENTENCE THAT USED TO BE HERE.
// This comment used to say the anchor is injected "ONLY by a `-tags release` build",
// and the positive verdict called itself a "published release". Both were measured
// false on 2026-08-14 (Codex sol max contrast of PR #730, reproduced here):
//
//	go build -trimpath -ldflags '-X main.version=26.8.0 \
//	  -X …/core/release.artifactVerifyKeyB64=AAAA…AAA=' -o probe ./cmd/olivares
//	→ ota-key=release/66687aad, license-key=dev/54091177
//
// No `-tags release`, no signature, no tag, no publication — both facts obtained, from
// 32 arbitrary bytes that merely decode as an Ed25519 public key (core/release
// KeyOrigin checks SHAPE, never identity). Adding `-tags release` to the forgery does
// not help either: it is the builder's own flag, and the same probe with it produces
// license-key=release too (one placeholder file under firstparty/bins is the whole
// cost). And there is nothing stronger available in-process: the detached signature
// over checksums.txt is not carried inside the binary (releasePublishedSigReason), and
// the official OTA key exists only as a build-time repo variable
// (.goreleaser.yaml:108, vars.OLIVARES_OTA_PUBKEY) — no official value is pinned in
// this tree for a running binary to compare itself against, and scripts/
// check-release-pubkey.sh validates shape and mutual difference, never identity.
//
// So EVERY fact reachable from here is chosen by whoever linked the binary. The pair
// is therefore reported for what it is — SELF-DECLARED build provenance, named as such
// on the surface (releaseProvenanceNote, provenanceDTO) — exactly as `olivares version`
// already qualifies the same two anchors. The verdict keeps both polarities, because
// "an unreleased build says not_published" is still right and a release-stamped build
// saying not_published was the defect; what it stops doing is presenting itself as
// proof that a release was published.

// releaseKeyOrigin indirects core/release.KeyOrigin so BOTH polarities of the anchor
// guard can be exercised. artifactVerifyKeyB64 is an UNEXPORTED link-time var of
// another module (core/release/verify.go:51), so no test outside that package can set
// it, and a guard that can only ever be measured on its false side is a guard nobody
// has checked. Production reads the real function; only tests in this package swap it.
// Same seam core/license takes from inside its own package
// (core/license/embedded_internal_test.go:19).
var releaseKeyOrigin = release.KeyOrigin

// measuredRelease answers "does the binary serving this request carry the release
// build's link-time identity?" from version — the ldflags stamp this process was built
// with — and keyOrigin, the classification of its embedded OTA anchor ("release" |
// "none" | "misconfigured"). Only "release" + an orderable, non-local version stamp
// reaches the positive state; everything else keeps status not_published and says
// WHICH fact was missing.
//
// It is NOT an answer to "was this binary published", and it never was — that question
// needs a fact from outside the binary. The Provenance block travels with the verdict
// in both polarities so the distinction is on the wire, not only in this comment.
func measuredRelease(version, keyOrigin string) releaseDTO {
	rel := releaseDTO{
		Published:       false,
		Status:          releaseStatusNotPublished,
		SignatureStatus: "not_verified",
		SignatureReason: releaseSigReason,
		// The epistemic class of the verdict below, on EVERY response and in BOTH
		// polarities: nothing this process can read about its own release identity
		// was authenticated by anyone but the linker that wrote it.
		Provenance:        provenanceDTO{Kind: releaseProvenanceSelfDeclared, Attested: false, Note: releaseProvenanceNote},
		VerifierAvailable: true, // compile-time-proven: see the modelsign reference above
		TransparencyLog:   tlogDTO{Verified: false, Note: releaseTlogNote},
	}
	published, reason := releaseIdentity(version, keyOrigin)
	rel.Reason = reason
	if published {
		rel.Published = true
		rel.Status = releaseStatusPublished
		// The signature verdict does NOT flip with the release verdict: the process
		// still cannot verify itself. Only the reason changes, because claiming "no
		// release artifacts exist" about a published release is the same class of
		// untruth this whole block was fixed for.
		rel.SignatureReason = releasePublishedSigReason
	}
	return rel
}

// releaseIdentity is the verdict and its named cause. The four not-published shapes
// keep ONE status — a build that is not a release is not a release, whichever fact is
// missing — and differ only in the reason, so no consumer has to learn a new status
// value to keep working while an operator is told exactly what is absent.
func releaseIdentity(version, keyOrigin string) (published bool, reason string) {
	switch {
	// (1) NO STAMP AT ALL. release.IsUnstamped is the canonical predicate for "this
	// build has no position in the release ordering" (core/release/version.go:58) —
	// the same one `security check` (cmd_security.go:214), the upgrade path
	// (cmd_upgrade.go:457) and the advisory engine (secadvisory/advisory.go:236)
	// ask, and it is asked FIRST here for the reason version.go:36 gives: an
	// unstamped build is UNKNOWN, not "very old", so nothing may be compared against
	// it. This is the ORIGINAL answer, unchanged, and still what every plain
	// `go build ./cmd/olivares` and every test binary gets.
	case release.IsUnstamped(version):
		return false, releaseReason

	// (2) A STAMP THAT IS NOT A VERSION. `git describe --tags --always` falls back to
	// a bare abbreviated object name when no tag is reachable (Taskfile.yml:111), so
	// "abc1234" or "abc1234-dirty" reaches us as a stamp with no position in the
	// ordering. cmd_security.go:210-217 already names this as the second shape of the
	// same defect; it is not a release identity either.
	case !parsesAsVersion(version):
		return false, fmt.Sprintf(
			"not a release: this binary's version stamp %q is not a semantic version (a source build stamps `git describe --tags --always`, which yields a bare commit object when no tag is reachable)",
			clampStr(version, 64))

	// (3) A LOCAL BUILD WEARING A VERSION. Both git-describe markers PARSE as
	// semantic-version prereleases, so (2) waves them through — see describeSuffix.
	case describeSuffix(version) != "":
		return false, fmt.Sprintf(
			"not a release: the version stamp %q carries a git-describe marker (%s), so it names a local build near a tag, not the tag itself",
			clampStr(version, 64), describeSuffix(version))

	// (4) A VERSION WITHOUT THE CEREMONY. The stamp is orderable and clean, but the
	// binary embeds no usable OTA anchor, so it was not produced by a `-tags release`
	// build. Deny-closed: a version string is a label anyone can set with -ldflags;
	// the anchor is the half of the pair the release ceremony holds.
	case keyOrigin != "release":
		return false, fmt.Sprintf(
			"not a release: the version stamp %q is orderable, but this binary embeds no usable OTA verification anchor (ota-key=%s), so the release ceremony's second link-time fact is absent",
			clampStr(version, 64), clampStr(keyOrigin, 32))

	// (5) BOTH FACTS PRESENT: release-shaped. NOT "published" — see the block comment
	// above on what the pair does and does not establish. The word the reason uses is
	// the strongest one the evidence supports, and it names its own class in the same
	// breath, so no reader has to find the provenance block to be told.
	default:
		return true, fmt.Sprintf(
			"release-stamped %s (self-declared): this binary carries an orderable release version stamp AND an OTA verification anchor (ota-key=%s) — the pair the release build injects. Both are link-time values chosen by whoever linked it, so this is build provenance, not proof that an official release was published",
			clampStr(version, 64), clampStr(keyOrigin, 32))
	}
}

// parsesAsVersion reports whether s has a position in the release ordering. It is the
// second half of the pair core/release exposes: IsUnstamped says "no stamp", this says
// "a stamp that is not a version". Callers must ask IsUnstamped first — "dev" parses
// to the ZERO version rather than an error (core/release/version.go:64-72), so on its
// own this predicate would call an unstamped build orderable.
func parsesAsVersion(s string) bool {
	_, err := release.ParseVersion(s)
	return err == nil
}

// describeSuffix names the git-describe marker that proves a stamp was produced by a
// LOCAL build rather than by a tag, or "" when there is none.
//
// Both shapes come from this repo's own build lines, not from guesswork:
// `git describe --tags --always` (Taskfile.yml:111) appends "-<N>-g<object>" once HEAD
// has moved past the tag, and `--dirty` (Taskfile.yml:2041) appends "-dirty" when the
// tree had uncommitted changes. Both PARSE as ordinary semantic-version prereleases,
// so ParseVersion alone accepts them as release identities. A RELEASE prerelease is
// "rc.1" or "beta.2"; it is never a commit distance followed by an abbreviated object
// name, and it is never "dirty". Only the TAIL is inspected, because git APPENDS these
// markers: "26.8.0-rc.1-3-gabc1234" is a local build near an rc tag.
//
// IT WORKS ON THE RAW PRERELEASE TEXT, NOT ON release.Version.Pre, and that is not a
// style choice — the first version of this guard used Pre and let the commit-distance
// form straight through, caught by TestDescribeSuffixBoundaries. SemVer splits the
// prerelease on DOTS while git joins these markers with HYPHENS, so "26.8.0-3-gabc1234"
// parses to the SINGLE identifier "3-gabc1234": at the dot grain the two fields the
// guard is looking for do not exist.
func describeSuffix(version string) string {
	if _, err := release.ParseVersion(version); err != nil {
		return "" // not a version at all; releaseIdentity case (2) already refused it
	}
	t := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if i := strings.IndexByte(t, '+'); i >= 0 {
		t = t[:i] // build metadata is not part of the identity and carries no marker
	}
	i := strings.IndexByte(t, '-')
	if i < 0 {
		return "" // no prerelease at all: a bare tag
	}
	seg := strings.Split(t[i+1:], "-")
	n := len(seg)
	if seg[n-1] == "dirty" {
		return "-dirty: the working tree had uncommitted changes"
	}
	if n >= 2 && isCommitDistance(seg[n-2]) && isAbbrevObject(seg[n-1]) {
		return "-" + seg[n-2] + "-" + seg[n-1] + ": HEAD is " + seg[n-2] + " commit(s) past the nearest tag"
	}
	return ""
}

// isCommitDistance reports whether s is the non-negative decimal commit count git
// describe puts between the tag and the object name. A leading zero is not one: git
// never emits "07", so accepting it would widen the shape past what git produces.
func isCommitDistance(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isAbbrevObject reports whether s is git describe's "g"+abbreviated-object-name
// field. The abbreviation length is not fixed (it grows with the repository and
// core.abbrev moves it), so the length is bounded, not pinned: git's own floor is 4.
func isAbbrevObject(s string) bool {
	return len(s) >= 5 && s[0] == 'g' && isLowerHexLoose(s[1:])
}

// declaredPipeline is the declared block (see the constants above).
func declaredPipeline() pipelineDTO {
	return pipelineDTO{
		Workflows: append([]string(nil), releasePipelineWorkflows...),
		Status:    "declared",
		Note:      pipelineNote,
	}
}
