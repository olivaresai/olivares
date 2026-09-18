// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CodexOfficialBaseURL is the origin this installer speaks. The closed metadata
// layout is a version pointer, SHA256SUMS, the musl package tarball, and one
// Sigstore bundle per signed subject. A host that does not publish that layout
// is refused rather than guessed. This installer never runs the vendor script.
const CodexOfficialBaseURL = "https://chatgpt.com/codex"

const (
	codexProbeBudget    = 20 * time.Second
	codexProbeGrace     = 3 * time.Second
	codexProbeFixedPath = "/usr/bin:/bin"
	codexPointerCap     = 1 << 20
	codexChecksumCap    = 1 << 20
	codexProofCap       = 1 << 20
	codexDefaultMax     = 1 << 30
)

// CodexOptions configures the adapter.
type CodexOptions struct {
	BaseURL     string
	Client      *http.Client
	Verifier    SubjectProofVerifier
	ProbeBudget time.Duration
	ProbeGrace  time.Duration
}

// Codex installs the official Codex musl package under the mixed-assurance
// policy: exact package digest, closed layout, and publisher-signed subjects
// when a SubjectProofVerifier is configured. Without a verifier, origin install
// is refused. Detection and probe of an already-present binary do not need it.
type Codex struct {
	base        string
	fetch       fetcher
	verifier    SubjectProofVerifier
	probeBudget time.Duration
	probeGrace  time.Duration
}

func NewCodex(opts CodexOptions) *Codex {
	c := &Codex{
		base: CodexOfficialBaseURL, verifier: opts.Verifier,
		probeBudget: opts.ProbeBudget, probeGrace: opts.ProbeGrace,
	}
	if opts.BaseURL != "" {
		c.base = strings.TrimRight(opts.BaseURL, "/")
	}
	client := opts.Client
	if client == nil {
		client = NewHTTPClient()
	}
	c.fetch = fetcher{client: client}
	if c.verifier == nil {
		c.verifier = UnavailableSubjectVerifier{}
	}
	if c.probeBudget <= 0 {
		c.probeBudget = codexProbeBudget
	}
	if c.probeGrace <= 0 {
		c.probeGrace = codexProbeGrace
	}
	return c
}

func (c *Codex) Key() string { return DriverCodex }

type codexPointerDoc struct {
	Version string `json:"version"`
	Package struct {
		File   string `json:"file"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"package"`
	Subjects []struct {
		Path      string `json:"path"`
		SHA256    string `json:"sha256"`
		Identity  string `json:"identity"`
		Issuer    string `json:"issuer"`
		ProofRole string `json:"proof_role"`
		ProofFile string `json:"proof_file"`
	} `json:"subjects"`
}

func (c *Codex) ResolveV2(ctx context.Context, req RequestV2) (*PlanV2, *ResolvedMaterialV2, error) {
	if req.Driver != DriverCodex {
		return nil, nil, refuse(KindInvalidRequest, "codex adapter received driver %q", req.Driver)
	}
	plat, vendor, err := PlatformV2For(DriverCodex, Platform{OS: req.Platform.OS, Arch: req.Platform.Arch})
	if err != nil {
		return nil, nil, err
	}
	base, kind := c.base, SourceOfficial
	if req.Source != "" {
		if base, err = baseURL(req.Source); err != nil {
			return nil, nil, err
		}
		if base != c.base {
			kind = SourceMirror
		}
	}
	origin, err := originOfBase(base)
	if err != nil {
		return nil, nil, err
	}
	channel, version := ChannelExact, strings.TrimSpace(req.Version)
	pointerURL := joinURL(base, version+"/pointer.json")
	switch version {
	case ChannelLatest, ChannelStable:
		channel = version
		pointerURL = joinURL(base, version)
	default:
		if !ValidVersion(version) {
			return nil, nil, refuse(KindInvalidRequest, "version %q is neither an exact version (X.Y.Z) nor one of latest, stable", req.Version)
		}
	}
	body, status, err := c.fetch.small(ctx, pointerURL, codexPointerCap)
	if err != nil {
		return nil, nil, err
	}
	if err := statusOrTransport(status, pointerURL, "version"); err != nil {
		return nil, nil, err
	}
	var doc codexPointerDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, nil, refuse(KindManifestInvalid, "codex pointer at %s is not the JSON document this installer understands: %v", pointerURL, err)
	}
	if !ValidVersion(doc.Version) {
		return nil, nil, refuse(KindManifestInvalid, "codex pointer declares version %q, which is not a version", doc.Version)
	}
	if channel == ChannelExact && doc.Version != version {
		return nil, nil, refuse(KindManifestInvalid, "codex pointer at %s declares version %s, not the requested %s", pointerURL, doc.Version, version)
	}
	version = doc.Version
	if doc.Package.File == "" || filepath.Base(doc.Package.File) != doc.Package.File || strings.ContainsAny(doc.Package.File, `/\`) {
		return nil, nil, refuse(KindManifestInvalid, "codex pointer names package file %q, which is not a plain file name", doc.Package.File)
	}
	if !isHex64(doc.Package.SHA256) || doc.Package.Size <= 0 || doc.Package.Size > codexDefaultMax {
		return nil, nil, refuse(KindManifestInvalid, "codex pointer package digest or size is unusable")
	}
	checksumsURL := joinURL(base, version+"/SHA256SUMS")
	sums, status, err := c.fetch.small(ctx, checksumsURL, codexChecksumCap)
	if err != nil {
		return nil, nil, err
	}
	if err := statusOrTransport(status, checksumsURL, "source"); err != nil {
		return nil, nil, err
	}
	gotSHA, gotSize, err := parseSHA256SUMS(sums, doc.Package.File)
	if err != nil {
		return nil, nil, err
	}
	if gotSHA != doc.Package.SHA256 || (gotSize != 0 && gotSize != doc.Package.Size) {
		return nil, nil, refuse(KindDigestMismatch, "codex pointer package digest does not match SHA256SUMS")
	}
	pkgURL := joinURL(base, version+"/"+doc.Package.File)
	subjects, proofs, proofObjs, err := c.bindSubjects(ctx, base, origin, doc)
	if err != nil {
		return nil, nil, err
	}
	expanded := doc.Package.Size * 4
	if expanded < doc.Package.Size {
		expanded = maxV2ExpandedBytes
	}
	if expanded > maxV2ExpandedBytes {
		expanded = maxV2ExpandedBytes
	}
	if expanded < doc.Package.Size {
		expanded = doc.Package.Size
	}
	layout := ExpectedLayout{
		ID:           LayoutIDCodexPackageV1,
		Variant:      LayoutVariantCodex,
		EntryPoint:   "bin/codex",
		ResourcesDir: "codex-resources",
		PathDir:      "codex-path",
		Members:      expectedMembersFromSpec(codexLayoutMembers()),
		Limits: ExtractionLimits{
			MaxCompressedBytes: doc.Package.Size,
			MaxExpandedBytes:   expanded,
			MaxMembers:         len(codexLayoutMembers()),
			MaxMemberBytes:     expanded,
		},
	}
	sel := SelectionV2{
		Driver:           DriverCodex,
		Channel:          channel,
		RequestedVersion: req.Version,
		Version:          version,
		Platform:         plat,
		VendorPlatform:   vendor,
		Source: SourcePolicyV2{
			Kind:           kind,
			Pointer:        URLRef{State: URLStatePresent, URL: pointerURL},
			Checksums:      URLRef{State: URLStatePresent, URL: checksumsURL},
			Package:        URLRef{State: URLStatePresent, URL: pkgURL},
			Proofs:         proofs,
			AllowedOrigins: []string{origin},
			Redirects:      RedirectPolicyV2{MaxHops: 0, AllowedOrigins: []string{}},
		},
		FetchedObject: FetchedObjectExpectation{
			DigestState: DigestStateExact,
			SHA256:      doc.Package.SHA256,
			SizeState:   SizeStateExact,
			Size:        doc.Package.Size,
			MaxSize:     max(doc.Package.Size, 1),
		},
		Layout:           layout,
		RequiredSubjects: subjects,
		PackagePolicyID:  PackagePolicyCodexMixedV1,
		Verification: VerificationProfileV2{
			Kind: VerificationSigstoreCosign,
			Cosign: &CosignProfileV2{
				ID:                   "cosign",
				Version:              "unwired",
				ExecutableSHA256:     strings.Repeat("00", 32),
				TrustedRootIteration: 1,
				TrustedRootSHA256:    strings.Repeat("00", 32),
				Mode:                 CosignModeOffline,
				RefreshPolicy:        RefreshPolicyNone,
				EgressPolicy:         EgressPolicyNone,
				BundleFormat:         BundleFormatCosignLegacy,
				RequireSignature:     true,
				RequireRekor:         true,
				RequireSCT:           true,
			},
		},
		Destination: destinationFor(req.DestRoot, DriverCodex, version, vendor, DriverCodex),
	}
	// Cosign profile fields are a closed schema bound into the digest. They do
	// not claim that Cosign ran. VerifyPayload still refuses without a verifier.
	plan := &PlanV2{Schema: PlanSchemaV2, Selection: sel}
	if err := plan.validate(); err != nil {
		return nil, nil, err
	}
	plan.Digest = ComputeDigestV2(plan)
	material := &ResolvedMaterialV2{
		Pointer:   resolvedFromURL(sel.Source.Pointer, body),
		Checksums: resolvedFromURL(sel.Source.Checksums, sums),
		Proofs:    proofObjs,
	}
	return plan, material, nil
}

func (c *Codex) bindSubjects(ctx context.Context, base, origin string, doc codexPointerDoc) ([]SubjectExpectation, []ProofLocator, []ResolvedProofV2, error) {
	if len(doc.Subjects) != 3 {
		return nil, nil, nil, refuse(KindManifestInvalid, "codex pointer must name exactly 3 signed subjects, got %d", len(doc.Subjects))
	}
	wantPaths := []string{"bin/codex", "bin/codex-code-mode-host", "codex-resources/bwrap"}
	var subjects []SubjectExpectation
	var proofs []ProofLocator
	var objs []ResolvedProofV2
	for i, s := range doc.Subjects {
		if s.Path != wantPaths[i] {
			return nil, nil, nil, refuse(KindManifestInvalid, "codex pointer subject[%d].path %q, want %q", i, s.Path, wantPaths[i])
		}
		if !isHex64(s.SHA256) {
			return nil, nil, nil, refuse(KindManifestInvalid, "codex pointer subject %s sha256 is not hex-64", s.Path)
		}
		role := s.ProofRole
		if role == "" {
			role = filepath.Base(s.Path)
		}
		file := s.ProofFile
		if file == "" {
			file = role + ".sigstore"
		}
		if filepath.Base(file) != file || strings.ContainsAny(file, `/\`) {
			return nil, nil, nil, refuse(KindManifestInvalid, "codex proof file %q is not a plain file name", file)
		}
		u := joinURL(base, doc.Version+"/"+file)
		if err := requirePresentOrigin(u, "source.proofs", origin); err != nil {
			return nil, nil, nil, err
		}
		bundle, status, err := c.fetch.small(ctx, u, codexProofCap)
		if err != nil {
			return nil, nil, nil, err
		}
		if err := statusOrTransport(status, u, "source"); err != nil {
			return nil, nil, nil, err
		}
		subjects = append(subjects, SubjectExpectation{
			Path: s.Path, ExpectedSHA256: s.SHA256, Identity: s.Identity, Issuer: s.Issuer, ProofRole: role,
		})
		proofs = append(proofs, ProofLocator{Role: role, URL: u})
		objs = append(objs, ResolvedProofV2{Role: role, URL: u, Bytes: bundle, SHA256: sha256Hex(bundle), Size: int64(len(bundle))})
	}
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].Role < proofs[j].Role })
	sort.Slice(objs, func(i, j int) bool { return objs[i].Role < objs[j].Role })
	return subjects, proofs, objs, nil
}

func parseSHA256SUMS(raw []byte, filename string) (sha string, size int64, err error) {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name != filename {
			continue
		}
		if !isHex64(fields[0]) {
			return "", 0, refuse(KindManifestInvalid, "SHA256SUMS digest for %s is not hex-64", filename)
		}
		if len(fields) >= 3 {
			n, perr := strconv.ParseInt(fields[1], 10, 64)
			if perr == nil {
				size = n
			}
		}
		return fields[0], size, nil
	}
	return "", 0, refuse(KindManifestInvalid, "SHA256SUMS does not name %s", filename)
}

func (c *Codex) FetchV2(ctx context.Context, plan *PlanV2, w io.Writer) (FetchedObjectObserved, error) {
	if plan == nil {
		return FetchedObjectObserved{}, refuse(KindInvalidRequest, "v2 plan is missing")
	}
	f := c.fetch.confine(plan.Selection.Source.Redirects, plan.Selection.Source.AllowedOrigins)
	fo := plan.Selection.FetchedObject
	return f.exactSizeHash(ctx, plan.Selection.Source.Package.URL, fo.SHA256, fo.Size, fo.MaxSize, w)
}

func (c *Codex) PlaceV2(ctx context.Context, plan *PlanV2, root *os.Root, stagingRel, fetchedRel string) (*ObservedPayloadInventory, error) {
	if plan == nil {
		return nil, refuse(KindInvalidRequest, "v2 plan is missing")
	}
	if err := extractTarGz(ctx, root, stagingRel, fetchedRel, plan.Selection.Layout); err != nil {
		return nil, err
	}
	return inventoryLayout(root, stagingRel, plan.Selection.Layout)
}

func (c *Codex) VerifyPayload(ctx context.Context, access PayloadAccess, inv *ObservedPayloadInventory, policy PackagePolicyV2) error {
	if policy.ID != PackagePolicyCodexMixedV1 {
		return refuse(KindInvalidRequest, "codex package_policy_id must be %s", PackagePolicyCodexMixedV1)
	}
	if policy.Verification.Kind != VerificationSigstoreCosign {
		return refuse(KindInvalidRequest, "codex verification.kind must be %s", VerificationSigstoreCosign)
	}
	if c.verifier == nil {
		return UnavailableSubjectVerifier{}.VerifySubject(ctx, SubjectExpectation{}, "", nil)
	}
	want := codexLayoutMembers()
	if inv == nil || len(inv.Members) != len(want) {
		return refuse(KindDamaged, "placed codex inventory has %d members, want %d", countMembers(inv), len(want))
	}
	for i, m := range inv.Members {
		w := want[i]
		if m.Path != w.Path || m.Kind != w.Kind {
			return refuse(KindDamaged, "placed codex member %d is %s %s, want %s %s", i, m.Path, m.Kind, w.Path, w.Kind)
		}
	}
	for _, sub := range policy.RequiredSubjects {
		got, _, err := memberHash(inv, sub.Path)
		if err != nil {
			return refuse(KindDamaged, "%v", err)
		}
		if got != sub.ExpectedSHA256 {
			return refuse(KindDigestMismatch, "placed subject %s sha256 %s does not match the plan claim %s", sub.Path, got, sub.ExpectedSHA256)
		}
		bundle, err := readProof(access, sub.ProofRole)
		if err != nil {
			return err
		}
		if err := c.verifier.VerifySubject(ctx, sub, got, bundle); err != nil {
			return err
		}
	}
	return nil
}

func readProof(access PayloadAccess, role string) ([]byte, error) {
	if access == nil {
		return nil, refuse(KindSignatureInvalid, "no payload access to read proof %s", role)
	}
	rel := filepath.ToSlash(filepath.Join(".proofs", role))
	f, err := access.OpenFile(rel)
	if err != nil {
		return nil, refuse(KindSignatureInvalid, "open proof %s: %v", role, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, codexProofCap+1))
	if err != nil {
		return nil, refuse(KindSignatureInvalid, "read proof %s: %v", role, err)
	}
	if int64(len(b)) > codexProofCap {
		return nil, refuse(KindResponseTooLarge, "proof %s is larger than %d bytes", role, codexProofCap)
	}
	return b, nil
}

func (c *Codex) Probe(ctx context.Context, exe, scratch, wantVersion string) (ProbeReport, error) {
	home := filepath.Join(scratch, "home")
	config := filepath.Join(scratch, "config")
	tmp := filepath.Join(scratch, "tmp")
	for _, d := range []string{home, config, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return ProbeReport{}, refuse(KindProbeFailed, "prepare probe home: %v", err)
		}
	}
	spec := probeSpec{
		Exe: exe, Args: []string{"--version"}, Dir: home, Budget: c.probeBudget, Grace: c.probeGrace,
		Env: []string{
			"HOME=" + home,
			"CODEX_HOME=" + config,
			"TMPDIR=" + tmp,
			"PATH=" + codexProbeFixedPath,
			"TERM=dumb",
			"LANG=C.UTF-8",
		},
	}
	rep, err := runProbe(ctx, spec)
	if err != nil {
		return rep, err
	}
	line := firstLine(rep.Output)
	if line == "" || !strings.Contains(strings.ToLower(line), "codex") {
		return rep, refuse(KindProbeMismatch, "%s --version printed %q, which does not identify Codex", exe, line)
	}
	gotVersion := firstVersionToken(line)
	if wantVersion != "" && gotVersion != wantVersion {
		return rep, refuse(KindProbeMismatch, "%s --version reports %q; the plan says %s", exe, gotVersion, wantVersion)
	}
	return rep, nil
}

func (c *Codex) DefaultPaths(home string) []string {
	paths := []string{"/usr/bin/codex", "/usr/local/bin/codex"}
	if filepath.IsAbs(home) {
		paths = append(paths, filepath.Join(home, ".local", "bin", "codex"))
	}
	return paths
}

func (c *Codex) ObserveAuth(home string) AuthObservation {
	return observeLoginFile(codexAuthPath(home))
}

var _ PackageProviderV2 = (*Codex)(nil)
