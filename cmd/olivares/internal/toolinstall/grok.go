// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GrokOfficialBaseURL is xAI's CLI origin as recorded in the 2026-09-04
// industry installation survey. This installer never runs the vendor install
// script. Verification class is origin-only: HTTPS from this origin plus a
// bounded probe. It is not a publisher signature.
const GrokOfficialBaseURL = "https://x.ai/cli"

const (
	grokProbeBudget    = 20 * time.Second
	grokProbeGrace     = 3 * time.Second
	grokProbeFixedPath = "/usr/bin:/bin"
	grokPointerCap     = 256
	grokDefaultMax     = 512 << 20
)

// GrokOptions configures the adapter. Zero values mean the official base URL,
// a default HTTP client and the default probe budget.
type GrokOptions struct {
	BaseURL     string
	Client      *http.Client
	ProbeBudget time.Duration
	ProbeGrace  time.Duration
}

// Grok installs Grok Build from the official origin into an Olivares-owned
// versioned directory. It does not vendor the CLI.
type Grok struct {
	base        string
	fetch       fetcher
	probeBudget time.Duration
	probeGrace  time.Duration
}

func NewGrok(opts GrokOptions) *Grok {
	g := &Grok{base: GrokOfficialBaseURL, probeBudget: opts.ProbeBudget, probeGrace: opts.ProbeGrace}
	if opts.BaseURL != "" {
		g.base = strings.TrimRight(opts.BaseURL, "/")
	}
	client := opts.Client
	if client == nil {
		client = NewHTTPClient()
	}
	g.fetch = fetcher{client: client}
	if g.probeBudget <= 0 {
		g.probeBudget = grokProbeBudget
	}
	if g.probeGrace <= 0 {
		g.probeGrace = grokProbeGrace
	}
	return g
}

func (g *Grok) Key() string { return DriverGrok }

func (g *Grok) ResolveV2(ctx context.Context, req RequestV2) (*PlanV2, *ResolvedMaterialV2, error) {
	if req.Driver != DriverGrok {
		return nil, nil, refuse(KindInvalidRequest, "grok adapter received driver %q", req.Driver)
	}
	plat, vendor, err := PlatformV2For(DriverGrok, Platform{OS: req.Platform.OS, Arch: req.Platform.Arch})
	if err != nil {
		return nil, nil, err
	}
	if req.Platform.Libc != "" && req.Platform.Libc != plat.Libc {
		return nil, nil, refuse(KindUnsupportedPlatform, "grok platform libc must be empty, got %q", req.Platform.Libc)
	}
	base, kind := g.base, SourceOfficial
	if req.Source != "" {
		if base, err = baseURL(req.Source); err != nil {
			return nil, nil, err
		}
		if base != g.base {
			kind = SourceMirror
		}
	}
	origin, err := originOfBase(base)
	if err != nil {
		return nil, nil, err
	}
	channel, version := ChannelExact, strings.TrimSpace(req.Version)
	pointer := URLRef{State: URLStatePresent, URL: joinURL(base, version)}
	var pointerBytes []byte
	switch version {
	case ChannelLatest, ChannelStable:
		channel = version
		pointer.URL = joinURL(base, version)
		body, status, err := g.fetch.small(ctx, pointer.URL, grokPointerCap)
		if err != nil {
			return nil, nil, err
		}
		if err := statusOrTransport(status, pointer.URL, "version"); err != nil {
			return nil, nil, err
		}
		version = strings.TrimSpace(string(body))
		if !ValidVersion(version) {
			return nil, nil, refuse(KindVersionUnknown, "the %s pointer at %s returned %q, which is not a version", channel, pointer.URL, version)
		}
		pointerBytes = body
	default:
		if !ValidVersion(version) {
			return nil, nil, refuse(KindInvalidRequest, "version %q is neither an exact version (X.Y.Z) nor one of latest, stable", req.Version)
		}
		pointer.URL = joinURL(base, version)
	}
	pkgURL := joinURL(base, "grok-"+version+"-"+vendor)
	if err := requirePresentOrigin(pkgURL, "source.package", origin); err != nil {
		return nil, nil, err
	}
	if err := requirePresentOrigin(pointer.URL, "source.pointer", origin); err != nil {
		return nil, nil, err
	}
	layout := ExpectedLayout{
		ID:         LayoutIDGrokBinV1,
		Variant:    LayoutVariantGrok,
		EntryPoint: "bin/grok",
		Members:    expectedMembersFromSpec(grokLayoutMembers()),
		Limits: ExtractionLimits{
			MaxCompressedBytes: grokDefaultMax,
			MaxExpandedBytes:   grokDefaultMax,
			MaxMembers:         len(grokLayoutMembers()),
			MaxMemberBytes:     grokDefaultMax,
		},
	}
	sel := SelectionV2{
		Driver:           DriverGrok,
		Channel:          channel,
		RequestedVersion: req.Version,
		Version:          version,
		Platform:         plat,
		VendorPlatform:   vendor,
		Source: SourcePolicyV2{
			Kind:           kind,
			Pointer:        pointer,
			Checksums:      URLRef{State: URLStateAbsent},
			Package:        URLRef{State: URLStatePresent, URL: pkgURL},
			Proofs:         []ProofLocator{},
			AllowedOrigins: []string{origin},
			Redirects:      RedirectPolicyV2{MaxHops: 0, AllowedOrigins: []string{}},
		},
		FetchedObject: FetchedObjectExpectation{
			DigestState: DigestStateUnknown,
			SizeState:   SizeStateBounded,
			MaxSize:     grokDefaultMax,
		},
		Layout:           layout,
		RequiredSubjects: []SubjectExpectation{},
		PackagePolicyID:  PackagePolicyOriginOnlyV1,
		Verification:     VerificationProfileV2{Kind: VerificationNoneOriginOnly},
		Destination:      destinationFor(req.DestRoot, DriverGrok, version, vendor, DriverGrok),
	}
	plan := &PlanV2{Schema: PlanSchemaV2, Selection: sel}
	if err := plan.validate(); err != nil {
		return nil, nil, err
	}
	plan.Digest = ComputeDigestV2(plan)
	material := &ResolvedMaterialV2{
		Pointer:   resolvedFromURL(pointer, pointerBytes),
		Checksums: ResolvedObjectV2{State: URLStateAbsent},
		Proofs:    []ResolvedProofV2{},
	}
	return plan, material, nil
}

func resolvedFromURL(u URLRef, body []byte) ResolvedObjectV2 {
	out := ResolvedObjectV2{State: u.State, URL: u.URL}
	if len(body) > 0 {
		out.Bytes = body
		out.SHA256 = sha256Hex(body)
		out.Size = int64(len(body))
	}
	return out
}

func (g *Grok) FetchV2(ctx context.Context, plan *PlanV2, w io.Writer) (FetchedObjectObserved, error) {
	if plan == nil {
		return FetchedObjectObserved{}, refuse(KindInvalidRequest, "v2 plan is missing")
	}
	f := g.fetch.confine(plan.Selection.Source.Redirects, plan.Selection.Source.AllowedOrigins)
	return f.bounded(ctx, plan.Selection.Source.Package.URL, plan.Selection.FetchedObject.MaxSize, w)
}

func (g *Grok) PlaceV2(_ context.Context, plan *PlanV2, root *os.Root, stagingRel, fetchedRel string) (*ObservedPayloadInventory, error) {
	if plan == nil {
		return nil, refuse(KindInvalidRequest, "v2 plan is missing")
	}
	if err := ensureDir(root, filepath.Join(stagingRel, "bin"), 0o755); err != nil {
		return nil, err
	}
	dst := filepath.Join(stagingRel, filepath.FromSlash(plan.Selection.Layout.EntryPoint))
	if err := copyPlacedFile(root, fetchedRel, dst, 0o600); err != nil {
		return nil, err
	}
	return inventoryLayout(root, stagingRel, plan.Selection.Layout)
}

func (g *Grok) VerifyPayload(_ context.Context, _ PayloadAccess, inv *ObservedPayloadInventory, policy PackagePolicyV2) error {
	if policy.ID != PackagePolicyOriginOnlyV1 {
		return refuse(KindInvalidRequest, "grok package_policy_id must be %s", PackagePolicyOriginOnlyV1)
	}
	if policy.Verification.Kind != VerificationNoneOriginOnly {
		return refuse(KindInvalidRequest, "grok verification.kind must be %s; origin-only is not a publisher signature", VerificationNoneOriginOnly)
	}
	if len(policy.RequiredSubjects) != 0 {
		return refuse(KindInvalidRequest, "grok required_subjects must be empty")
	}
	want := grokLayoutMembers()
	if inv == nil || len(inv.Members) != len(want) {
		return refuse(KindDamaged, "placed grok inventory has %d members, want %d", countMembers(inv), len(want))
	}
	for i, m := range inv.Members {
		w := want[i]
		if m.Path != w.Path || m.Kind != w.Kind {
			return refuse(KindDamaged, "placed grok member %d is %s %s, want %s %s", i, m.Path, m.Kind, w.Path, w.Kind)
		}
		if m.Kind == MemberKindRegular && !isHex64(m.SHA256) {
			return refuse(KindDamaged, "placed grok member %s has no digest", m.Path)
		}
	}
	return nil
}

func countMembers(inv *ObservedPayloadInventory) int {
	if inv == nil {
		return 0
	}
	return len(inv.Members)
}

func (g *Grok) Probe(ctx context.Context, exe, scratch, wantVersion string) (ProbeReport, error) {
	home := filepath.Join(scratch, "home")
	tmp := filepath.Join(scratch, "tmp")
	for _, d := range []string{home, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return ProbeReport{}, refuse(KindProbeFailed, "prepare probe home: %v", err)
		}
	}
	spec := probeSpec{
		Exe: exe, Args: []string{"--version"}, Dir: home, Budget: g.probeBudget, Grace: g.probeGrace,
		Env: []string{
			"HOME=" + home,
			"TMPDIR=" + tmp,
			"PATH=" + grokProbeFixedPath,
			"TERM=dumb",
			"LANG=C.UTF-8",
		},
	}
	rep, err := runProbe(ctx, spec)
	if err != nil {
		return rep, err
	}
	line := firstLine(rep.Output)
	if line == "" || !strings.Contains(strings.ToLower(line), "grok") {
		return rep, refuse(KindProbeMismatch, "%s --version printed %q, which does not identify Grok", exe, line)
	}
	gotVersion := firstVersionToken(line)
	if wantVersion != "" && gotVersion != wantVersion {
		return rep, refuse(KindProbeMismatch, "%s --version reports %q; the plan says %s", exe, gotVersion, wantVersion)
	}
	return rep, nil
}

func (g *Grok) DefaultPaths(home string) []string {
	paths := []string{"/usr/bin/grok", "/usr/local/bin/grok"}
	if filepath.IsAbs(home) {
		paths = append(paths,
			filepath.Join(home, ".local", "bin", "grok"),
			filepath.Join(home, ".grok", "bin", "grok"),
		)
	}
	return paths
}

func (g *Grok) ObserveAuth(home string) AuthObservation {
	return observeLoginFile(grokAuthPath(home))
}

var _ PackageProviderV2 = (*Grok)(nil)
