// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package toolinstall

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/modules/catalog"
)

const (
	VerificationCatalogSHA256 = "catalog-sha256"
	PackagePolicyDeclared     = "catalog-archive-v1"
	LayoutIDDeclared          = "catalog-archive-v1"
	maxDeclaredArchiveBytes   = int64(64 << 20)
)

// declaredProvider extends the existing release-archive provider. Its download
// stays bounded in memory until its admitted digest passes, before root writes.
type declaredProvider struct {
	archive     *ReleaseArchive
	declaration catalog.ToolDeclaration
	bytes       bytes.Buffer
	observed    FetchedObjectObserved
}

func (e *Engine) requestV2Provider(req RequestV2) (PackageProviderV2, error) {
	if req.Declaration == nil {
		return e.v2Provider(req.Driver)
	}
	client := e.httpClient
	if client == nil {
		client = NewHTTPClient()
	}
	return &declaredProvider{archive: &ReleaseArchive{driver: req.Driver, fetch: fetcher{client: client}}, declaration: *req.Declaration}, nil
}

func declaredSelection(req RequestV2) (SelectionV2, error) {
	d := req.Declaration
	if d == nil {
		return SelectionV2{}, refuse(KindUnadmitted, "a catalog-admitted declaration is required")
	}
	if err := d.Validate(); err != nil {
		return SelectionV2{}, refuse(KindInvalidRequest, "%v", err)
	}
	if req.Driver != d.Driver() || req.Version != d.Version || req.Source != "" {
		return SelectionV2{}, refuse(KindInvalidRequest, "request differs from the admitted declaration")
	}
	if err := req.Platform.validate(req.Driver); err != nil {
		return SelectionV2{}, err
	}
	if req.Platform.Libc != "" || !ValidVersion(d.Version) {
		return SelectionV2{}, refuse(KindInvalidRequest, "invalid declared platform or version")
	}
	if !filepath.IsAbs(req.DestRoot) || filepath.Clean(req.DestRoot) != req.DestRoot || req.DestRoot == "/" {
		return SelectionV2{}, refuse(KindInvalidRequest, "destination must be an absolute, clean, non-root path")
	}
	artifact := d.ArtifactURL()
	origin, err := originOfBase(artifact)
	if err != nil {
		return SelectionV2{}, err
	}
	origins := []string{origin}
	if origin == "https://github.com" {
		origins = append(origins, "https://release-assets.githubusercontent.com", "https://objects.githubusercontent.com")
	}
	sort.Strings(origins)
	vendor := req.Platform.OS + "-" + req.Platform.Arch
	release := filepath.Join(req.DestRoot, req.Driver, d.Version+"-"+vendor)
	tool := *d
	layout := declaredLayout(d.Program)
	sel := SelectionV2{Tool: &tool, Driver: req.Driver, Channel: ChannelExact, RequestedVersion: d.Version, Version: d.Version, Platform: req.Platform, VendorPlatform: vendor,
		Source:        SourcePolicyV2{Kind: SourceMirror, Pointer: URLRef{State: URLStateAbsent}, Checksums: URLRef{State: URLStateAbsent}, Package: URLRef{State: URLStatePresent, URL: artifact}, Proofs: []ProofLocator{}, AllowedOrigins: origins, Redirects: RedirectPolicyV2{MaxHops: 3, AllowedOrigins: origins}},
		FetchedObject: FetchedObjectExpectation{DigestState: DigestStateExact, SHA256: d.SHA256, SizeState: SizeStateBounded, MaxSize: maxDeclaredArchiveBytes},
		Layout:        layout, RequiredSubjects: []SubjectExpectation{}, PackagePolicyID: PackagePolicyDeclared, Verification: VerificationProfileV2{Kind: VerificationCatalogSHA256},
		Destination: Destination{Root: req.DestRoot, ReleaseDir: release, Executable: filepath.Join(release, filepath.FromSlash(layout.EntryPoint))}}
	if err := sel.Source.validate(); err != nil {
		return SelectionV2{}, err
	}
	if err := validateDestinationV2(sel.Destination); err != nil {
		return SelectionV2{}, err
	}
	return sel, nil
}

func declaredLayout(program string) ExpectedLayout {
	l := releaseArchiveLayout(DriverOpenCode)
	l.ID = LayoutIDDeclared
	l.Variant = "declared-" + program
	l.EntryPoint = "bin/" + program
	l.Members[1].Path = l.EntryPoint
	l.Limits = ExtractionLimits{MaxCompressedBytes: maxDeclaredArchiveBytes, MaxExpandedBytes: 256 << 20, MaxMembers: 4096, MaxMemberBytes: 64 << 20}
	return l
}

func validateDeclaredSelection(s *SelectionV2) error {
	expected, err := declaredSelection(RequestV2{Driver: s.Driver, Version: s.Version, Platform: s.Platform, DestRoot: s.Destination.Root, Declaration: s.Tool})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*s, expected) {
		return refuse(KindInvalidRequest, "declared selection differs from its pinned catalog policy")
	}
	return nil
}

func (p *declaredProvider) ResolveV2(_ context.Context, req RequestV2) (*PlanV2, *ResolvedMaterialV2, error) {
	selection, err := declaredSelection(req)
	if err != nil {
		return nil, nil, err
	}
	return &PlanV2{Schema: PlanSchemaV2, Selection: selection}, nil, nil
}

func (p *declaredProvider) prepare(ctx context.Context, plan *PlanV2) error {
	got, err := p.archive.fetch.confine(plan.Selection.Source.Redirects, plan.Selection.Source.AllowedOrigins).bounded(ctx, plan.Selection.Source.Package.URL, maxDeclaredArchiveBytes, &p.bytes)
	if err != nil {
		return err
	}
	if got.SHA256 != p.declaration.SHA256 {
		return refuse(KindDigestMismatch, "download differs from the catalog-admitted SHA-256 digest")
	}
	p.observed = got
	return nil
}

func (p *declaredProvider) FetchV2(_ context.Context, _ *PlanV2, w io.Writer) (FetchedObjectObserved, error) {
	if p.observed.Size <= 0 || p.observed.SHA256 != p.declaration.SHA256 {
		return FetchedObjectObserved{}, refuse(KindDigestMismatch, "declared archive has not passed preflight verification")
	}
	_, err := io.Copy(w, bytes.NewReader(p.bytes.Bytes()))
	return p.observed, err
}

func (p *declaredProvider) PlaceV2(ctx context.Context, plan *PlanV2, root *os.Root, staging, fetched string) (*ObservedPayloadInventory, error) {
	digest, _, err := fileSHA256Root(root, fetched)
	if err != nil {
		return nil, err
	}
	if digest != p.declaration.SHA256 {
		return nil, refuse(KindDigestMismatch, "staged archive differs from the admitted digest")
	}
	return extractReleaseArchive(ctx, p.archive.driver, root, staging, fetched, plan.Selection.Layout, p.declaration.Prefix())
}

func (p *declaredProvider) VerifyPayload(ctx context.Context, access PayloadAccess, inv *ObservedPayloadInventory, policy PackagePolicyV2) error {
	if policy.ID != PackagePolicyDeclared || policy.Verification.Kind != VerificationCatalogSHA256 || !reflect.DeepEqual(policy.Layout, declaredLayout(p.declaration.Program)) || len(policy.RequiredSubjects) != 0 {
		return refuse(KindInvalidRequest, "declared payload verification policy mismatch")
	}
	return verifyReleaseArchiveInventory(ctx, access, inv, p.archive.driver, policy.Layout.Limits)
}

func verifyDeclaredReceipt(ctx context.Context, root *os.Root, prefix string, rec *ReceiptV2) error {
	if rec.Tool == nil || rec.Tool.Validate() != nil || rec.Driver != rec.Tool.Driver() || rec.Version != rec.Tool.Version || rec.FetchedObject.SHA256 != rec.Tool.SHA256 || rec.PackagePolicyID != PackagePolicyDeclared || rec.VerificationKind != VerificationCatalogSHA256 {
		return refuse(KindDamaged, "declared receipt differs from its catalog pin")
	}
	selection, err := declaredSelection(RequestV2{Driver: rec.Driver, Version: rec.Version, Platform: rec.Platform, DestRoot: rec.Destination.Root, Declaration: rec.Tool})
	if err != nil || rec.PlanDigest != ComputeDigestV2(&PlanV2{Schema: PlanSchemaV2, Selection: selection}) || rec.Destination != selection.Destination || rec.VendorPlatform != selection.VendorPlatform {
		return refuse(KindDamaged, "declared receipt metadata differs from its approved plan")
	}
	inv, err := inventoryReleaseArchive(ctx, root, prefix, rec.Driver, declaredLayout(rec.Tool.Program).Limits)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(inv.Members, rec.Payload.Members) {
		return refuse(KindDamaged, "declared files differ from the receipt inventory")
	}
	return nil
}

func declaredProgram(driver string) string { return strings.TrimPrefix(driver, "declared-") }

func (p *declaredProvider) Key() string { return p.archive.Key() }

func (p *declaredProvider) Probe(ctx context.Context, exe, scratch, want string) (ProbeReport, error) {
	return p.archive.Probe(ctx, exe, scratch, want)
}
func (p *declaredProvider) DefaultPaths(home string) []string { return p.archive.DefaultPaths(home) }
