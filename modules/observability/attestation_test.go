// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"net/http"
	"runtime"
	"strings"
	"testing"
)

const attestationPath = "/v1/m/observability/attestation"

// TestAttestationDefaults: with nothing plumbed, the binary block reports the
// unstamped ldflags defaults verbatim, the measured runtime facts, a real
// self-hash, and the honest release/pipeline blocks.
func TestAttestationDefaults(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", attestationPath, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("attestation = %d %s", r.code, r.raw)
	}
	bin := mapOf(r.body["binary"])
	if strOf(bin["version"]) != "dev" || strOf(bin["commit"]) != "none" || strOf(bin["build_date"]) != "unknown" {
		t.Fatalf("unstamped defaults = %q/%q/%q, want dev/none/unknown",
			strOf(bin["version"]), strOf(bin["commit"]), strOf(bin["build_date"]))
	}
	if strOf(bin["status"]) != "measured" {
		t.Fatalf("binary.status = %q", strOf(bin["status"]))
	}
	if strOf(bin["go_version"]) == "" || strOf(bin["os"]) != runtime.GOOS || strOf(bin["arch"]) != runtime.GOARCH {
		t.Fatalf("runtime facts = %q/%q/%q", strOf(bin["go_version"]), strOf(bin["os"]), strOf(bin["arch"]))
	}
	// The self-hash is measured from the running executable (the test binary
	// here) — 64 lowercase hex, or an explanatory note instead, never both absent.
	hash, note := strOf(bin["self_sha256"]), strOf(bin["self_hash_note"])
	if hash == "" && note == "" {
		t.Fatal("self_sha256 and self_hash_note both absent")
	}
	if hash != "" && (len(hash) != 64 || !isLowerHexLoose(hash)) {
		t.Fatalf("self_sha256 = %q, want 64 lowercase hex", hash)
	}
	if mm := mapOf(bin["main_module"]); strOf(mm["path"]) == "" {
		t.Fatalf("main_module = %v", bin["main_module"])
	}
	if sums := mapOf(bin["module_sums"]); strOf(sums["note"]) == "" {
		t.Fatalf("module_sums must carry the go.work note: %v", bin["module_sums"])
	}
	if vcs := mapOf(bin["vcs_stamp"]); strOf(vcs["reason"]) == "" {
		t.Fatalf("vcs_stamp must carry a reason: %v", bin["vcs_stamp"])
	}
	if _, present := mapOf(bin["fips140"])["enabled"]; !present {
		t.Fatalf("fips140 block missing: %v", bin["fips140"])
	}

	rel := mapOf(r.body["release"])
	if boolOf(rel["published"]) || strOf(rel["status"]) != "not_published" {
		t.Fatalf("release = %v, want measured absence", rel)
	}
	if strOf(rel["signature_status"]) != "not_verified" || !boolOf(rel["verifier_available"]) {
		t.Fatalf("signature posture = %v", rel)
	}
	if tlog := mapOf(rel["transparency_log"]); boolOf(tlog["verified"]) || strOf(tlog["note"]) == "" {
		t.Fatalf("transparency_log = %v", rel["transparency_log"])
	}

	pipe := mapOf(r.body["pipeline"])
	if strOf(pipe["status"]) != "declared" {
		t.Fatalf("pipeline.status = %q, want declared", strOf(pipe["status"]))
	}
	// The declared block may not answer a question it declares unobservable. It used
	// to end with "it has never been fired", a repository-history claim rendered
	// unconditionally beside the live release badge — the second of the two
	// contradictions the 2026-08-14 contrast found on one screen.
	if strings.Contains(strOf(pipe["note"]), "never been fired") {
		t.Fatalf("pipeline.note asserts repository history the same sentence calls unobservable: %q", strOf(pipe["note"]))
	}
	if wf, _ := pipe["workflows"].([]any); len(wf) != 5 {
		t.Fatalf("workflows = %v, want the 5 release/posture workflows", pipe["workflows"])
	}
	if strOf(r.body["captured_at"]) == "" {
		t.Fatal("captured_at missing")
	}
}

// TestAttestationBuildInfoOverride: the composition root plumbs the ldflags
// values via WithBuildInfo; they are reported verbatim — including a -dirty
// suffix, which stays a string, never a boolean claim.
func TestAttestationBuildInfoOverride(t *testing.T) {
	h := newHarness(t, WithBuildInfo(BuildInfo{Version: "abc1234-dirty", Commit: "abc1234", Date: "2026-06-12T08:00:00Z"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", attestationPath, admin, nil, tenantHdr(tenant))
	bin := mapOf(r.body["binary"])
	if strOf(bin["version"]) != "abc1234-dirty" || strOf(bin["commit"]) != "abc1234" || strOf(bin["build_date"]) != "2026-06-12T08:00:00Z" {
		t.Fatalf("plumbed build info = %q/%q/%q", strOf(bin["version"]), strOf(bin["commit"]), strOf(bin["build_date"]))
	}
}

// withReleaseAnchor swaps the releaseKeyOrigin seam for the duration of one test.
// artifactVerifyKeyB64 is unexported in core/release, so this is the only way to
// measure the anchor guard on its TRUE side; without it every assertion about a
// published release would be an assertion about a build that has no anchor, i.e.
// about the wrong subject. Safe as a package-level swap because no test in this
// package calls t.Parallel (checked: zero occurrences across the five _test files).
func withReleaseAnchor(t *testing.T, origin string) {
	t.Helper()
	saved := releaseKeyOrigin
	t.Cleanup(func() { releaseKeyOrigin = saved })
	releaseKeyOrigin = func() string { return origin }
}

// TestAttestationPublishedRelease is the witness for the DEFECT: a binary carrying
// both facts the release ceremony injects — an orderable main.version stamp and the
// embedded OTA anchor — must report itself PUBLISHED on its own attestation surface.
// Before the fix measuredRelease() took no arguments and returned published=false
// unconditionally, so this test fails on the old code at the very first assertion:
// a signed, tagged release called itself beta to its own customer.
//
// It runs over HTTP, not against measuredRelease directly, because the defect is as
// much about the handler never PASSING the facts as about the function never reading
// them: the reason echoes the plumbed stamp, which a constant could not produce.
func TestAttestationPublishedRelease(t *testing.T) {
	withReleaseAnchor(t, "release")
	h := newHarness(t, WithBuildInfo(BuildInfo{Version: "26.8.0", Commit: "abc1234", Date: "2026-08-13T08:00:00Z"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", attestationPath, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("attestation = %d %s", r.code, r.raw)
	}
	rel := mapOf(r.body["release"])
	if !boolOf(rel["published"]) || strOf(rel["status"]) != "published" {
		t.Fatalf("a stamped release with the OTA anchor embedded reports %v; want published/published", rel)
	}
	// The handler must forward THIS process's stamp, not a literal: the reason names it.
	if !strings.Contains(strOf(rel["reason"]), "26.8.0") {
		t.Fatalf("reason does not name the plumbed version stamp: %q", strOf(rel["reason"]))
	}
	// The signature verdict must NOT ride along with the release verdict — the
	// process still cannot verify its own detached signature. Only the reason moves.
	if strOf(rel["signature_status"]) != "not_verified" {
		t.Fatalf("signature_status = %q; a published binary still cannot verify itself in-process", strOf(rel["signature_status"]))
	}
	if strOf(rel["signature_reason"]) == releaseSigReason {
		t.Fatalf("signature_reason still denies that any release artifacts exist, on a published release: %q", strOf(rel["signature_reason"]))
	}
	if tlog := mapOf(rel["transparency_log"]); boolOf(tlog["verified"]) {
		t.Fatalf("transparency_log.verified went true on a publish verdict: %v", rel["transparency_log"])
	}
	// THE 2026-08-14 HALF — the surface must not affirm more than it can prove.
	// A binary forged with two -ldflags values (no `-tags release`, no signature, no
	// tag, no publication) reaches this exact response: it was compiled and run
	// against this tree. So the positive state has to arrive already qualified, on
	// the wire, without a reader having to know that.
	prov := mapOf(rel["provenance"])
	if strOf(prov["kind"]) != releaseProvenanceSelfDeclared || boolOf(prov["attested"]) {
		t.Fatalf("provenance = %v; the positive verdict rests on two link-time values and nothing attested them", rel["provenance"])
	}
	if !strings.Contains(strOf(prov["note"]), "not an attestation") {
		t.Fatalf("provenance.note does not name the class of the claim: %q", strOf(prov["note"]))
	}
	if strings.Contains(strOf(rel["reason"]), "published release") {
		t.Fatalf("the reason still asserts publication: %q", strOf(rel["reason"]))
	}
	if !strings.Contains(strOf(rel["reason"]), "self-declared") {
		t.Fatalf("the reason does not name itself self-declared: %q", strOf(rel["reason"]))
	}
}

// TestAttestationUnstampedStaysNotPublished is the CONTRAFACTUAL that forbids
// "fixing" the defect by returning true. The OTA anchor is forced present, so the
// unstamped version stamp is the ONLY fact left that can refuse — exactly the guard
// under test. A build from source must keep saying not_published, with the original
// beta reason, because for it that answer was never wrong.
//
// IT IS DIFFERENTIAL, and that is the whole point of the 2026-08-14 rewrite. As
// written before, it asserted only the NEGATIVE half — so it passed unchanged against
// the pre-#730 body (`measuredRelease` ignoring its inputs and returning
// false/not_published), which is the very code it was published to refute. A
// contrafactual that the defect also satisfies measures nothing about the defect. So
// it now pins BOTH sides of the same guard in one run, over HTTP: the unstamped build
// must land negative with the original reason, AND a stamped one on the same wiring
// must land somewhere else. A constant — true OR false — cannot satisfy both.
func TestAttestationUnstampedStaysNotPublished(t *testing.T) {
	withReleaseAnchor(t, "release")

	unstamped := func() map[string]any {
		h := newHarness(t) // no WithBuildInfo: the "dev" ldflags default
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		return mapOf(h.do("GET", attestationPath, admin, nil, tenantHdr(tenant)).body["release"])
	}()
	stamped := func() map[string]any {
		h := newHarness(t, WithBuildInfo(BuildInfo{Version: "26.8.0", Commit: "abc1234", Date: "2026-08-13T08:00:00Z"}))
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		return mapOf(h.do("GET", attestationPath, admin, nil, tenantHdr(tenant)).body["release"])
	}()

	// The guard under test: no version stamp, so not a release, whatever the anchor says.
	if boolOf(unstamped["published"]) || strOf(unstamped["status"]) != "not_published" {
		t.Fatalf("an UNSTAMPED build reports %v; a build with no version stamp is not a release", unstamped)
	}
	if strOf(unstamped["reason"]) != releaseReason {
		t.Fatalf("reason = %q, want the unchanged beta reason %q", strOf(unstamped["reason"]), releaseReason)
	}
	if strOf(unstamped["signature_reason"]) != releaseSigReason {
		t.Fatalf("signature_reason = %q, want the unchanged %q", strOf(unstamped["signature_reason"]), releaseSigReason)
	}
	// The discrimination: the SAME endpoint, the SAME anchor, one fact changed, and
	// the answer must move. If it does not, this endpoint is not reading the binary
	// it claims to describe and the negative above is a default, not a verdict.
	if boolOf(stamped["published"]) == boolOf(unstamped["published"]) {
		t.Fatalf("published is %v for BOTH an unstamped and a release-stamped build: the verdict is not derived from the version stamp at all",
			boolOf(unstamped["published"]))
	}
	if strOf(stamped["reason"]) == strOf(unstamped["reason"]) {
		t.Fatalf("both builds are given the identical reason %q: the reason is boilerplate, not a named cause", strOf(unstamped["reason"]))
	}
	// Provenance does NOT move with the verdict — it is the class of the evidence,
	// not the evidence — and it must be present on the negative side too, so no
	// consumer learns about self-declaration only when the answer turns positive.
	for name, rel := range map[string]map[string]any{"unstamped": unstamped, "stamped": stamped} {
		prov := mapOf(rel["provenance"])
		if strOf(prov["kind"]) != releaseProvenanceSelfDeclared || boolOf(prov["attested"]) {
			t.Fatalf("%s provenance = %v, want kind=%s / attested=false in BOTH polarities", name, rel["provenance"], releaseProvenanceSelfDeclared)
		}
	}
}

// TestAttestationAnchorlessBuildIsNotPublished is the second contrafactual, and the
// one that keeps the fix from replacing a false negative with a false POSITIVE. A
// version stamp is a label anyone can set with -ldflags, and `task build:bin` sets a
// real-looking one from `git describe` on any tagged tree (Taskfile.yml:111). Without
// the OTA anchor that build is not a release, and must not claim to be one.
func TestAttestationAnchorlessBuildIsNotPublished(t *testing.T) {
	withReleaseAnchor(t, "none")
	h := newHarness(t, WithBuildInfo(BuildInfo{Version: "26.8.0", Commit: "abc1234", Date: "2026-08-13T08:00:00Z"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", attestationPath, admin, nil, tenantHdr(tenant))
	rel := mapOf(r.body["release"])
	if boolOf(rel["published"]) || strOf(rel["status"]) != "not_published" {
		t.Fatalf("a locally stamped build with NO OTA anchor reports %v; want not_published", rel)
	}
	// The reason must name the missing fact, otherwise the operator cannot tell this
	// apart from the beta case and will look for a tag that is not the problem.
	if !strings.Contains(strOf(rel["reason"]), "ota-key=none") {
		t.Fatalf("reason does not name the absent anchor: %q", strOf(rel["reason"]))
	}
}

// TestReleaseIdentityShapes pins every shape the verdict distinguishes, in both
// polarities, at the decision function itself. wantPublished is asserted first; the
// reason substring is asserted second so a mutant that reaches the right verdict for
// the wrong reason (a guard deleted and covered by the next one down) is still named.
func TestReleaseIdentityShapes(t *testing.T) {
	cases := []struct {
		name          string
		version       string
		keyOrigin     string
		wantPublished bool
		wantReason    string // substring
	}{
		// (1) unstamped — the original, correct not_published.
		{"dev default", "dev", "release", false, releaseReason},
		{"empty stamp", "", "release", false, releaseReason},
		{"v-prefixed dev", "vdev", "release", false, releaseReason},
		// (2) a stamp that is not a version.
		{"bare object name", "abc1234", "release", false, "is not a semantic version"},
		{"bare object dirty", "abc1234-dirty", "release", false, "is not a semantic version"},
		{"two-component", "26.8", "release", false, "is not a semantic version"},
		// (3) a local build wearing a version: both git-describe markers parse.
		{"describe distance", "v26.8.0-3-gabc1234", "release", false, "git-describe marker"},
		{"describe dirty", "26.8.0-dirty", "release", false, "git-describe marker"},
		{"describe on a prerelease tag", "26.8.0-rc.1-12-gdeadbee", "release", false, "git-describe marker"},
		// (4) orderable and clean, but no ceremony.
		{"no anchor", "26.8.0", "none", false, "ota-key=none"},
		{"broken anchor", "26.8.0", "misconfigured", false, "ota-key=misconfigured"},
		{"unknown origin", "26.8.0", "", false, "no usable OTA verification anchor"},
		// (5) both facts: a release. Prereleases the ceremony really produces are
		// releases too — an rc IS published, and refusing it would be the same
		// defect in the other direction.
		{"released", "26.8.0", "release", true, "release-stamped 26.8.0 (self-declared)"},
		{"released with v", "v26.8.0", "release", true, "release-stamped v26.8.0 (self-declared)"},
		{"released rc", "26.8.0-rc.1", "release", true, "release-stamped 26.8.0-rc.1 (self-declared)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := releaseIdentity(c.version, c.keyOrigin)
			if got != c.wantPublished {
				t.Fatalf("releaseIdentity(%q, %q) published = %v, want %v (reason: %s)",
					c.version, c.keyOrigin, got, c.wantPublished, reason)
			}
			if !strings.Contains(reason, c.wantReason) {
				t.Fatalf("releaseIdentity(%q, %q) reason = %q, want it to contain %q",
					c.version, c.keyOrigin, reason, c.wantReason)
			}
			// The DTO must carry the verdict through unchanged, and status must stay
			// in lockstep with the boolean — two fields saying different things is
			// how a console renders a badge the payload contradicts.
			rel := measuredRelease(c.version, c.keyOrigin)
			if rel.Published != c.wantPublished {
				t.Fatalf("measuredRelease(%q, %q).Published = %v, want %v", c.version, c.keyOrigin, rel.Published, c.wantPublished)
			}
			wantStatus := releaseStatusNotPublished
			if c.wantPublished {
				wantStatus = releaseStatusPublished
			}
			if rel.Status != wantStatus {
				t.Fatalf("measuredRelease(%q, %q).Status = %q, want %q", c.version, c.keyOrigin, rel.Status, wantStatus)
			}
			// not_verified is invariant across BOTH verdicts: the process never
			// verifies its own detached signature.
			if rel.SignatureStatus != "not_verified" || !rel.VerifierAvailable || rel.TransparencyLog.Verified {
				t.Fatalf("integrity posture moved with the release verdict: %+v", rel)
			}
			// Neither does the provenance CLASS. attested must never ride along with
			// published: the pair of link-time facts is exactly as self-declared on a
			// release-stamped binary as on a source build — that is the whole finding.
			if rel.Provenance.Kind != releaseProvenanceSelfDeclared || rel.Provenance.Attested || rel.Provenance.Note == "" {
				t.Fatalf("provenance = %+v, want a self-declared, unattested class with a note, in BOTH polarities", rel.Provenance)
			}
			// And the positive verdict must not call itself published in prose. A
			// forged binary reaches this exact string with two -ldflags values.
			if c.wantPublished && strings.Contains(rel.Reason, "published release") {
				t.Fatalf("the positive reason claims %q; the link-time pair is release-SHAPED, and publication is not observable from here", rel.Reason)
			}
		})
	}
}

// TestDescribeSuffixBoundaries pins the edges of the git-describe marker so the guard
// cannot quietly widen into rejecting real release prereleases (which WOULD reach the
// positive state) nor narrow into accepting local builds.
//
// EVERY BOUNDARY IS ASSERTED THROUGH measuredRelease, not only on the helper, and that
// is the 2026-08-14 rewrite. Asserting describeSuffix alone made this file's most
// detailed test pass unchanged against the pre-#730 body — the helper can be perfectly
// calibrated and completely dead while the endpoint answers from a constant. A
// boundary that never reaches the DTO is not a boundary of the product.
func TestDescribeSuffixBoundaries(t *testing.T) {
	local := []string{
		"26.8.0-3-gabc1234",        // the plain describe form
		"v26.8.0-1-g0123456",       // one commit past, v-prefixed
		"26.8.0-127-gdeadbeefcafe", // long abbreviation (core.abbrev grows)
		"26.8.0-3-gabc1234-dirty",  // both markers at once; -dirty wins on the tail
		"26.8.0-rc.1-3-gabc1234",   // appended to a prerelease tag
		"26.8.0-dirty",             // clean tag, dirty tree
	}
	for _, v := range local {
		if describeSuffix(v) == "" {
			t.Errorf("describeSuffix(%q) = \"\"; that stamp names a LOCAL build, not a tag", v)
		}
		// …and the marker must actually decide the endpoint's answer. With the anchor
		// forced present, this stamp is the only fact left that can refuse.
		if rel := measuredRelease(v, "release"); rel.Published || rel.Status != releaseStatusNotPublished {
			t.Errorf("measuredRelease(%q, release) = %v/%q; a git-describe marker names a LOCAL build and must not reach the positive state", v, rel.Published, rel.Status)
		}
	}
	releases := []string{
		"26.8.0",             // no prerelease at all
		"v26.8.0",            //
		"26.8.0-rc.1",        // a real release candidate
		"26.8.0-beta.2",      //
		"26.8.0-3",           // a numeric identifier with no object name after it
		"26.8.0-gabc1234",    // an object-shaped field with no commit distance before it
		"26.8.0-03-gabc1234", // git never emits a zero-padded distance
		"26.8.0-3-gxyz",      // not hex, and too short
		"26.8.0-3-abc1234",   // no "g" prefix
		"26.8.0-3-g",         // "g" alone
	}
	for _, v := range releases {
		if s := describeSuffix(v); s != "" {
			t.Errorf("describeSuffix(%q) = %q; that is not a git-describe marker and the stamp must stay eligible", v, s)
		}
		// "eligible" has to mean something: with the anchor present these stamps must
		// reach the POSITIVE state. This is the half a constant-returning
		// measuredRelease cannot satisfy, and the reason this test now discriminates.
		if rel := measuredRelease(v, "release"); !rel.Published || rel.Status != releaseStatusPublished {
			t.Errorf("measuredRelease(%q, release) = %v/%q; that stamp carries no describe marker and must stay eligible for the positive state", v, rel.Published, rel.Status)
		}
	}
}

// TestAttestationRBAC: read-tier; unauthenticated rejected.
func TestAttestationRBAC(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	if r := h.do("GET", attestationPath, "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", r.code)
	}
	if r := h.do("GET", attestationPath, viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer = %d, want 200", r.code)
	}
}
