// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// PlanSchemaV2 is the Codex/Grok approval document. PlanSchema (v1) is unchanged
// and remains Claude's document. The two never share a decoder or omitempty bridge.
const PlanSchemaV2 = "olivares.ai/tool-install/plan/v2"

const (
	DriverCodex = "codex"
	DriverGrok  = "grok"

	DigestStateExact   = "exact"
	DigestStateUnknown = "unknown"
	SizeStateExact     = "exact"
	SizeStateBounded   = "bounded"

	URLStatePresent = "present"
	URLStateAbsent  = "absent"

	MemberKindDirectory = "directory"
	MemberKindRegular   = "regular"

	MemberRoleSignedSubject = "signed-subject"
	MemberRoleArchiveOnly   = "archive-only"
	MemberRoleMetadata      = "metadata"

	VerificationSigstoreCosign = "sigstore-cosign"
	VerificationNoneOriginOnly = "none-origin-only"

	CosignModeOffline = "offline"
	CosignModeOnline  = "online"

	RefreshPolicyNone = "none"
	RefreshPolicyTUF  = "tuf"
	EgressPolicyNone  = "none"
	EgressPolicyTUF   = "tuf-repo-cdn"

	BundleFormatCosignLegacy = "cosign-legacy-simple-signing"

	PackagePolicyCodexMixedV1 = "codex-mixed-v1"
	PackagePolicyOriginOnlyV1 = "origin-only-v1"

	LayoutIDCodexPackageV1 = "codex-package-layout-v1"
	LayoutIDGrokBinV1      = "grok-bin-v1"
	LayoutVariantCodex     = "codex"
	LayoutVariantGrok      = "grok"
)

const (
	maxV2TokenBytes           = 512
	maxV2URLBytes             = 2048
	maxV2PathBytes            = 256
	maxV2IdentityBytes        = 512
	maxV2ListLen              = 32
	maxV2Proofs               = 8
	maxV2Subjects             = 8
	maxV2Origins              = 16
	maxV2RedirectHops         = 5
	maxV2FetchedBytes         = 1 << 30
	maxV2ExpandedBytes        = 2 << 30
	maxV2RootIteration        = 10_000
	v2ModeExec         uint32 = 0o755
	v2ModeFile         uint32 = 0o644
)

// PlanV2 is the v2 approval document: a schema, a digest-bound selection, and
// the digest of that selection. Action, observed, existing and proof reports
// are not fields of this type.
type PlanV2 struct {
	Schema    string      `json:"schema"`
	Selection SelectionV2 `json:"selection"`
	Digest    string      `json:"digest"`
}

// SelectionV2 is the operator-approved selection. Every field is represented;
// absence uses an explicit state, never omitempty.
type SelectionV2 struct {
	Driver           string                   `json:"driver"`
	Channel          string                   `json:"channel"`
	RequestedVersion string                   `json:"requested_version"`
	Version          string                   `json:"version"`
	Platform         PlatformV2               `json:"platform"`
	VendorPlatform   string                   `json:"vendor_platform"`
	Source           SourcePolicyV2           `json:"source"`
	FetchedObject    FetchedObjectExpectation `json:"fetched_object"`
	Layout           ExpectedLayout           `json:"layout"`
	RequiredSubjects []SubjectExpectation     `json:"required_subjects"`
	PackagePolicyID  string                   `json:"package_policy_id"`
	Verification     VerificationProfileV2    `json:"verification"`
	Destination      Destination              `json:"destination"`
}

// PlatformV2 is the target triple without omitempty: libc is "" when not used.
type PlatformV2 struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Libc string `json:"libc"`
}

// SourcePolicyV2 binds every fetch URL, the allowed origins and the redirect policy.
type SourcePolicyV2 struct {
	Kind           string           `json:"kind"`
	Pointer        URLRef           `json:"pointer"`
	Checksums      URLRef           `json:"checksums"`
	Package        URLRef           `json:"package"`
	Proofs         []ProofLocator   `json:"proofs"`
	AllowedOrigins []string         `json:"allowed_origins"`
	Redirects      RedirectPolicyV2 `json:"redirects"`
}

// URLRef is a URL with an explicit present/absent state.
type URLRef struct {
	State string `json:"state"`
	URL   string `json:"url"`
}

// ProofLocator names one retained proof URL by role (subject identity, not bytes).
type ProofLocator struct {
	Role string `json:"role"`
	URL  string `json:"url"`
}

// RedirectPolicyV2 bounds hops and the exact origins a redirect may land on.
type RedirectPolicyV2 struct {
	MaxHops        int      `json:"max_hops"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// FetchedObjectExpectation is the selected download. Individual member hashes
// that only exist inside an archive are not invented here.
type FetchedObjectExpectation struct {
	DigestState string `json:"digest_state"`
	SHA256      string `json:"sha256"`
	SizeState   string `json:"size_state"`
	Size        int64  `json:"size"`
	MaxSize     int64  `json:"max_size"`
}

// ExpectedLayout is the closed payload layout bound into the plan.
type ExpectedLayout struct {
	ID           string           `json:"id"`
	Variant      string           `json:"variant"`
	EntryPoint   string           `json:"entry_point"`
	ResourcesDir string           `json:"resources_dir"`
	PathDir      string           `json:"path_dir"`
	Members      []ExpectedMember `json:"members"`
	Limits       ExtractionLimits `json:"limits"`
}

// ExpectedMember is one path in the layout. It has no content digest: signed
// subjects carry hashes as pending claims on SubjectExpectation; remainder
// members are bound only through the fetched object.
type ExpectedMember struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Role      string `json:"role"`
	FinalMode uint32 `json:"final_mode"`
}

// ExtractionLimits bounds extract before any executable bit is applied.
type ExtractionLimits struct {
	MaxCompressedBytes int64 `json:"max_compressed_bytes"`
	MaxExpandedBytes   int64 `json:"max_expanded_bytes"`
	MaxMembers         int   `json:"max_members"`
	MaxMemberBytes     int64 `json:"max_member_bytes"`
}

// SubjectExpectation is a pending publisher-signed claim for one extracted path.
type SubjectExpectation struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Identity       string `json:"identity"`
	Issuer         string `json:"issuer"`
	ProofRole      string `json:"proof_role"`
}

// VerificationProfileV2 selects the verifier. Cosign is a JSON object or null.
type VerificationProfileV2 struct {
	Kind   string           `json:"kind"`
	Cosign *CosignProfileV2 `json:"cosign"`
}

// CosignProfileV2 is bound into the digest: binary, root, mode, egress and checks.
// Bundle bytes, Rekor index and verifier text are receipt proof, not selection.
type CosignProfileV2 struct {
	ID                   string `json:"id"`
	Version              string `json:"version"`
	ExecutableSHA256     string `json:"executable_sha256"`
	TrustedRootIteration int    `json:"trusted_root_iteration"`
	TrustedRootSHA256    string `json:"trusted_root_sha256"`
	Mode                 string `json:"mode"`
	RefreshPolicy        string `json:"refresh_policy"`
	EgressPolicy         string `json:"egress_policy"`
	BundleFormat         string `json:"bundle_format"`
	RequireSignature     bool   `json:"require_signature"`
	RequireRekor         bool   `json:"require_rekor"`
	RequireSCT           bool   `json:"require_sct"`
}

func (p *PlanV2) validate() error {
	if p == nil {
		return refuse(KindInvalidRequest, "v2 plan is missing")
	}
	if p.Schema != PlanSchemaV2 {
		return refuse(KindInvalidRequest, "plan file declares schema %q, want %q", p.Schema, PlanSchemaV2)
	}
	return p.Selection.validate()
}

func (s *SelectionV2) validate() error {
	if s == nil {
		return refuse(KindInvalidRequest, "v2 selection is missing")
	}
	if err := closedToken(s.Driver, "driver", DriverCodex, DriverGrok); err != nil {
		return err
	}
	if err := closedToken(s.Channel, "channel", ChannelExact, ChannelLatest, ChannelStable); err != nil {
		return err
	}
	if err := boundedToken(s.RequestedVersion, "requested_version", 64, false); err != nil {
		return err
	}
	if !ValidVersion(s.Version) {
		return refuse(KindInvalidRequest, "version %q is not a bounded semantic version", s.Version)
	}
	if s.Channel == ChannelExact && s.RequestedVersion != s.Version {
		return refuse(KindInvalidRequest, "exact channel requested_version %q must equal version %q", s.RequestedVersion, s.Version)
	}
	if s.Channel != ChannelExact && s.RequestedVersion != ChannelLatest && s.RequestedVersion != ChannelStable {
		return refuse(KindInvalidRequest, "channel %q requested_version %q is not a pointer name", s.Channel, s.RequestedVersion)
	}
	if err := s.Platform.validate(s.Driver); err != nil {
		return err
	}
	if err := boundedToken(s.VendorPlatform, "vendor_platform", 64, false); err != nil {
		return err
	}
	if !vendorPlatformV2Re.MatchString(s.VendorPlatform) {
		return refuse(KindInvalidRequest, "vendor_platform %q is not a bounded vendor key", s.VendorPlatform)
	}
	if err := closedToken(s.PackagePolicyID, "package_policy_id", PackagePolicyCodexMixedV1, PackagePolicyOriginOnlyV1); err != nil {
		return err
	}
	if err := s.Source.validate(); err != nil {
		return err
	}
	if err := s.FetchedObject.validate(); err != nil {
		return err
	}
	if err := s.Layout.validate(); err != nil {
		return err
	}
	if s.RequiredSubjects == nil {
		return refuse(KindInvalidRequest, "required_subjects must be an array")
	}
	if len(s.RequiredSubjects) > maxV2Subjects {
		return refuse(KindInvalidRequest, "required_subjects has %d entries, want at most %d", len(s.RequiredSubjects), maxV2Subjects)
	}
	if err := requireCanonicalBy(len(s.RequiredSubjects), func(i int) string { return s.RequiredSubjects[i].Path }, "required_subjects"); err != nil {
		return err
	}
	for i := range s.RequiredSubjects {
		if err := s.RequiredSubjects[i].validate(); err != nil {
			return err
		}
	}
	if err := s.Verification.validate(); err != nil {
		return err
	}
	if err := validateDestinationV2(s.Destination); err != nil {
		return err
	}
	if err := s.validateClosedCombination(); err != nil {
		return err
	}
	if err := s.bindSubjectsToLayoutAndProofs(); err != nil {
		return err
	}
	return s.Source.originsCover(s.presentFetchURLs())
}

func (p PlatformV2) validate(driver string) error {
	if p.OS != "linux" {
		return refuse(KindInvalidRequest, "platform os %q is outside this increment (linux only)", p.OS)
	}
	if p.Arch != "amd64" && p.Arch != "arm64" {
		return refuse(KindInvalidRequest, "platform arch %q is outside this increment", p.Arch)
	}
	switch driver {
	case DriverCodex:
		if p.Libc != "musl" {
			return refuse(KindInvalidRequest, "codex platform libc must be musl, got %q", p.Libc)
		}
	case DriverGrok:
		if p.Libc != "" {
			return refuse(KindInvalidRequest, "grok platform libc must be empty, got %q", p.Libc)
		}
	}
	return nil
}

func (s SourcePolicyV2) validate() error {
	if err := closedToken(s.Kind, "source.kind", SourceOfficial, SourceMirror); err != nil {
		return err
	}
	if err := s.Pointer.validate("source.pointer"); err != nil {
		return err
	}
	if err := s.Checksums.validate("source.checksums"); err != nil {
		return err
	}
	if err := s.Package.validate("source.package"); err != nil {
		return err
	}
	if s.Proofs == nil {
		return refuse(KindInvalidRequest, "source.proofs must be an array")
	}
	if len(s.Proofs) > maxV2Proofs {
		return refuse(KindInvalidRequest, "source.proofs has %d entries, want at most %d", len(s.Proofs), maxV2Proofs)
	}
	if err := requireCanonicalBy(len(s.Proofs), func(i int) string { return s.Proofs[i].Role }, "source.proofs"); err != nil {
		return err
	}
	for i, p := range s.Proofs {
		if err := boundedToken(p.Role, fmt.Sprintf("source.proofs[%d].role", i), 64, false); err != nil {
			return err
		}
		if err := requireHTTPSURL(p.URL, fmt.Sprintf("source.proofs[%d].url", i)); err != nil {
			return err
		}
	}
	if s.AllowedOrigins == nil {
		return refuse(KindInvalidRequest, "source.allowed_origins must be an array")
	}
	if err := validateOriginList(s.AllowedOrigins, "source.allowed_origins"); err != nil {
		return err
	}
	return s.Redirects.validate()
}

func (r RedirectPolicyV2) validate() error {
	if r.MaxHops < 0 || r.MaxHops > maxV2RedirectHops {
		return refuse(KindInvalidRequest, "source.redirects.max_hops %d is outside 0..%d", r.MaxHops, maxV2RedirectHops)
	}
	if r.AllowedOrigins == nil {
		return refuse(KindInvalidRequest, "source.redirects.allowed_origins must be an array")
	}
	if r.MaxHops == 0 && len(r.AllowedOrigins) != 0 {
		return refuse(KindInvalidRequest, "source.redirects.allowed_origins must be empty when max_hops is 0")
	}
	if r.MaxHops > 0 && len(r.AllowedOrigins) == 0 {
		return refuse(KindInvalidRequest, "source.redirects.allowed_origins is required when max_hops is %d", r.MaxHops)
	}
	if r.MaxHops == 0 {
		return nil
	}
	return validateOriginList(r.AllowedOrigins, "source.redirects.allowed_origins")
}

func (u URLRef) validate(name string) error {
	if err := closedToken(u.State, name+".state", URLStatePresent, URLStateAbsent); err != nil {
		return err
	}
	switch u.State {
	case URLStateAbsent:
		if u.URL != "" {
			return refuse(KindInvalidRequest, "%s.url must be empty when state is absent", name)
		}
	case URLStatePresent:
		if err := requireHTTPSURL(u.URL, name+".url"); err != nil {
			return err
		}
	}
	return nil
}

func (f FetchedObjectExpectation) validate() error {
	if err := closedToken(f.DigestState, "fetched_object.digest_state", DigestStateExact, DigestStateUnknown); err != nil {
		return err
	}
	if err := closedToken(f.SizeState, "fetched_object.size_state", SizeStateExact, SizeStateBounded); err != nil {
		return err
	}
	if f.MaxSize <= 0 || f.MaxSize > maxV2FetchedBytes {
		return refuse(KindInvalidRequest, "fetched_object.max_size %d is outside 1..%d", f.MaxSize, maxV2FetchedBytes)
	}
	switch f.DigestState {
	case DigestStateExact:
		if !isHex64(f.SHA256) {
			return refuse(KindInvalidRequest, "fetched_object.sha256 must be lowercase hex-64 when digest_state is exact")
		}
		if f.SizeState != SizeStateExact {
			return refuse(KindInvalidRequest, "fetched_object.size_state must be exact when digest_state is exact")
		}
	case DigestStateUnknown:
		if f.SHA256 != "" {
			return refuse(KindInvalidRequest, "fetched_object.sha256 must be empty when digest_state is unknown")
		}
	}
	switch f.SizeState {
	case SizeStateExact:
		if f.Size <= 0 {
			return refuse(KindInvalidRequest, "fetched_object.size must be >0 when size_state is exact")
		}
		if f.Size > f.MaxSize {
			return refuse(KindInvalidRequest, "fetched_object.size %d exceeds max_size %d", f.Size, f.MaxSize)
		}
	case SizeStateBounded:
		if f.Size != 0 {
			return refuse(KindInvalidRequest, "fetched_object.size must be 0 when size_state is bounded")
		}
	}
	return nil
}

func (l ExpectedLayout) validate() error {
	if err := closedToken(l.ID, "layout.id", LayoutIDCodexPackageV1, LayoutIDGrokBinV1); err != nil {
		return err
	}
	if err := closedToken(l.Variant, "layout.variant", LayoutVariantCodex, LayoutVariantGrok); err != nil {
		return err
	}
	if err := layoutPath(l.EntryPoint, "layout.entry_point", false); err != nil {
		return err
	}
	if l.Members == nil {
		return refuse(KindInvalidRequest, "layout.members must be an array")
	}
	if len(l.Members) == 0 || len(l.Members) > maxV2ListLen {
		return refuse(KindInvalidRequest, "layout.members has %d entries, want 1..%d", len(l.Members), maxV2ListLen)
	}
	if err := requireCanonicalBy(len(l.Members), func(i int) string { return l.Members[i].Path }, "layout.members"); err != nil {
		return err
	}
	for i := range l.Members {
		if err := l.Members[i].validate(); err != nil {
			return err
		}
	}
	if err := l.Limits.validate(len(l.Members)); err != nil {
		return err
	}
	switch l.ID {
	case LayoutIDCodexPackageV1:
		if l.Variant != LayoutVariantCodex {
			return refuse(KindInvalidRequest, "layout id %q requires variant %q", l.ID, LayoutVariantCodex)
		}
		if err := layoutPath(l.ResourcesDir, "layout.resources_dir", false); err != nil {
			return err
		}
		if err := layoutPath(l.PathDir, "layout.path_dir", false); err != nil {
			return err
		}
		if l.EntryPoint != "bin/codex" || l.ResourcesDir != "codex-resources" || l.PathDir != "codex-path" {
			return refuse(KindInvalidRequest, "codex layout entry_point/resources_dir/path_dir do not match the closed layout")
		}
		if err := membersMatch(l.Members, codexLayoutMembers()); err != nil {
			return err
		}
	case LayoutIDGrokBinV1:
		if l.Variant != LayoutVariantGrok {
			return refuse(KindInvalidRequest, "layout id %q requires variant %q", l.ID, LayoutVariantGrok)
		}
		if l.ResourcesDir != "" || l.PathDir != "" {
			return refuse(KindInvalidRequest, "grok layout resources_dir and path_dir must be empty")
		}
		if l.EntryPoint != "bin/grok" {
			return refuse(KindInvalidRequest, "grok layout entry_point must be bin/grok")
		}
		if err := membersMatch(l.Members, grokLayoutMembers()); err != nil {
			return err
		}
	}
	return nil
}

func (m ExpectedMember) validate() error {
	if err := layoutPath(m.Path, "layout.members.path", false); err != nil {
		return err
	}
	if err := closedToken(m.Kind, "layout.members.kind", MemberKindDirectory, MemberKindRegular); err != nil {
		return err
	}
	if err := closedToken(m.Role, "layout.members.role", MemberRoleSignedSubject, MemberRoleArchiveOnly, MemberRoleMetadata); err != nil {
		return err
	}
	switch m.Kind {
	case MemberKindDirectory:
		if m.Role != MemberRoleArchiveOnly {
			return refuse(KindInvalidRequest, "directory %q must have role archive-only", m.Path)
		}
		if m.FinalMode != v2ModeExec {
			return refuse(KindInvalidRequest, "directory %q final_mode must be 0755", m.Path)
		}
	case MemberKindRegular:
		switch m.Role {
		case MemberRoleSignedSubject:
			if m.FinalMode != v2ModeExec {
				return refuse(KindInvalidRequest, "signed-subject %q final_mode must be 0755", m.Path)
			}
		case MemberRoleMetadata:
			if m.FinalMode != v2ModeFile {
				return refuse(KindInvalidRequest, "metadata %q final_mode must be 0644", m.Path)
			}
		case MemberRoleArchiveOnly:
			if m.FinalMode != v2ModeExec && m.FinalMode != v2ModeFile {
				return refuse(KindInvalidRequest, "archive-only regular %q final_mode must be 0755 or 0644", m.Path)
			}
		}
	}
	return nil
}

func (l ExtractionLimits) validate(memberCount int) error {
	if l.MaxCompressedBytes <= 0 || l.MaxCompressedBytes > maxV2FetchedBytes {
		return refuse(KindInvalidRequest, "layout.limits.max_compressed_bytes %d is outside 1..%d", l.MaxCompressedBytes, maxV2FetchedBytes)
	}
	if l.MaxExpandedBytes < l.MaxCompressedBytes || l.MaxExpandedBytes > maxV2ExpandedBytes {
		return refuse(KindInvalidRequest, "layout.limits.max_expanded_bytes %d is outside compressed..%d", l.MaxExpandedBytes, maxV2ExpandedBytes)
	}
	if l.MaxMembers != memberCount {
		return refuse(KindInvalidRequest, "layout.limits.max_members %d must equal the closed member count %d", l.MaxMembers, memberCount)
	}
	if l.MaxMemberBytes <= 0 || l.MaxMemberBytes > l.MaxExpandedBytes {
		return refuse(KindInvalidRequest, "layout.limits.max_member_bytes %d is outside 1..max_expanded_bytes", l.MaxMemberBytes)
	}
	return nil
}

func (s SubjectExpectation) validate() error {
	if err := layoutPath(s.Path, "required_subjects.path", false); err != nil {
		return err
	}
	if !isHex64(s.ExpectedSHA256) {
		return refuse(KindInvalidRequest, "required_subjects %q expected_sha256 must be lowercase hex-64", s.Path)
	}
	if err := boundedToken(s.Identity, "required_subjects.identity", maxV2IdentityBytes, false); err != nil {
		return err
	}
	if err := boundedToken(s.Issuer, "required_subjects.issuer", maxV2IdentityBytes, false); err != nil {
		return err
	}
	if err := requireHTTPSURL(s.Identity, "required_subjects.identity"); err != nil {
		return err
	}
	if err := requireHTTPSURL(s.Issuer, "required_subjects.issuer"); err != nil {
		return err
	}
	return boundedToken(s.ProofRole, "required_subjects.proof_role", 64, false)
}

func (v VerificationProfileV2) validate() error {
	if err := closedToken(v.Kind, "verification.kind", VerificationSigstoreCosign, VerificationNoneOriginOnly); err != nil {
		return err
	}
	switch v.Kind {
	case VerificationSigstoreCosign:
		if v.Cosign == nil {
			return refuse(KindInvalidRequest, "verification.cosign is required for %s", v.Kind)
		}
		return v.Cosign.validate()
	case VerificationNoneOriginOnly:
		if v.Cosign != nil {
			return refuse(KindInvalidRequest, "verification.cosign must be null for %s", v.Kind)
		}
	}
	return nil
}

func (c *CosignProfileV2) validate() error {
	if err := boundedToken(c.ID, "verification.cosign.id", 64, false); err != nil {
		return err
	}
	if err := boundedToken(c.Version, "verification.cosign.version", 64, false); err != nil {
		return err
	}
	if !isHex64(c.ExecutableSHA256) {
		return refuse(KindInvalidRequest, "verification.cosign.executable_sha256 must be lowercase hex-64")
	}
	if c.TrustedRootIteration <= 0 || c.TrustedRootIteration > maxV2RootIteration {
		return refuse(KindInvalidRequest, "verification.cosign.trusted_root_iteration %d is outside 1..%d", c.TrustedRootIteration, maxV2RootIteration)
	}
	if !isHex64(c.TrustedRootSHA256) {
		return refuse(KindInvalidRequest, "verification.cosign.trusted_root_sha256 must be lowercase hex-64")
	}
	if err := closedToken(c.Mode, "verification.cosign.mode", CosignModeOffline, CosignModeOnline); err != nil {
		return err
	}
	if err := closedToken(c.BundleFormat, "verification.cosign.bundle_format", BundleFormatCosignLegacy); err != nil {
		return err
	}
	if !c.RequireSignature || !c.RequireRekor || !c.RequireSCT {
		return refuse(KindInvalidRequest, "verification.cosign must require signature, rekor and sct")
	}
	switch c.Mode {
	case CosignModeOffline:
		if c.RefreshPolicy != RefreshPolicyNone || c.EgressPolicy != EgressPolicyNone {
			return refuse(KindInvalidRequest, "offline cosign refresh_policy and egress_policy must be none")
		}
	case CosignModeOnline:
		if err := closedToken(c.RefreshPolicy, "verification.cosign.refresh_policy", RefreshPolicyTUF); err != nil {
			return err
		}
		if err := closedToken(c.EgressPolicy, "verification.cosign.egress_policy", EgressPolicyTUF); err != nil {
			return err
		}
	}
	return nil
}

func (s SelectionV2) validateClosedCombination() error {
	switch s.Driver {
	case DriverCodex:
		if s.PackagePolicyID != PackagePolicyCodexMixedV1 {
			return refuse(KindInvalidRequest, "driver %q requires package_policy_id %q", s.Driver, PackagePolicyCodexMixedV1)
		}
		if s.Layout.ID != LayoutIDCodexPackageV1 {
			return refuse(KindInvalidRequest, "driver %q requires layout.id %q", s.Driver, LayoutIDCodexPackageV1)
		}
		if s.Verification.Kind != VerificationSigstoreCosign {
			return refuse(KindInvalidRequest, "driver %q requires verification.kind %q", s.Driver, VerificationSigstoreCosign)
		}
		if s.FetchedObject.DigestState != DigestStateExact {
			return refuse(KindInvalidRequest, "codex fetched_object.digest_state must be exact")
		}
		if s.Source.Pointer.State != URLStatePresent || s.Source.Checksums.State != URLStatePresent || s.Source.Package.State != URLStatePresent {
			return refuse(KindInvalidRequest, "codex source pointer, checksums and package URLs must be present")
		}
		if len(s.Source.Proofs) == 0 {
			return refuse(KindInvalidRequest, "codex source.proofs must name each signed subject")
		}
	case DriverGrok:
		if s.PackagePolicyID != PackagePolicyOriginOnlyV1 {
			return refuse(KindInvalidRequest, "driver %q requires package_policy_id %q", s.Driver, PackagePolicyOriginOnlyV1)
		}
		if s.Layout.ID != LayoutIDGrokBinV1 {
			return refuse(KindInvalidRequest, "driver %q requires layout.id %q", s.Driver, LayoutIDGrokBinV1)
		}
		if s.Verification.Kind != VerificationNoneOriginOnly {
			return refuse(KindInvalidRequest, "driver %q requires verification.kind %q", s.Driver, VerificationNoneOriginOnly)
		}
		if s.FetchedObject.DigestState != DigestStateUnknown {
			return refuse(KindInvalidRequest, "grok fetched_object.digest_state must be unknown")
		}
		if s.Source.Pointer.State != URLStatePresent || s.Source.Package.State != URLStatePresent {
			return refuse(KindInvalidRequest, "grok source pointer and package URLs must be present")
		}
		if s.Source.Checksums.State != URLStateAbsent {
			return refuse(KindInvalidRequest, "grok source.checksums must be absent")
		}
		if len(s.Source.Proofs) != 0 {
			return refuse(KindInvalidRequest, "grok source.proofs must be empty")
		}
		if len(s.RequiredSubjects) != 0 {
			return refuse(KindInvalidRequest, "grok required_subjects must be empty")
		}
	}
	return nil
}

func (s SelectionV2) bindSubjectsToLayoutAndProofs() error {
	var signed []string
	for _, m := range s.Layout.Members {
		if m.Role == MemberRoleSignedSubject {
			signed = append(signed, m.Path)
		}
	}
	if len(signed) != len(s.RequiredSubjects) {
		return refuse(KindInvalidRequest, "required_subjects has %d entries, layout has %d signed-subject members", len(s.RequiredSubjects), len(signed))
	}
	proofByRole := map[string]struct{}{}
	for _, p := range s.Source.Proofs {
		proofByRole[p.Role] = struct{}{}
	}
	consumed := map[string]struct{}{}
	for i, sub := range s.RequiredSubjects {
		if sub.Path != signed[i] {
			return refuse(KindInvalidRequest, "required_subjects[%d].path %q does not match signed-subject member %q", i, sub.Path, signed[i])
		}
		if _, ok := proofByRole[sub.ProofRole]; !ok {
			return refuse(KindInvalidRequest, "required_subjects %q proof_role %q has no source.proofs URL", sub.Path, sub.ProofRole)
		}
		if _, dup := consumed[sub.ProofRole]; dup {
			return refuse(KindInvalidRequest, "required_subjects proof_role %q is consumed more than once", sub.ProofRole)
		}
		consumed[sub.ProofRole] = struct{}{}
	}
	if len(consumed) != len(proofByRole) {
		return refuse(KindInvalidRequest, "source.proofs roles must be exactly the required subject proof_roles")
	}
	return nil
}

func (s SelectionV2) presentFetchURLs() []string {
	var out []string
	for _, u := range []URLRef{s.Source.Pointer, s.Source.Checksums, s.Source.Package} {
		if u.State == URLStatePresent {
			out = append(out, u.URL)
		}
	}
	for _, p := range s.Source.Proofs {
		out = append(out, p.URL)
	}
	return out
}

func (s SourcePolicyV2) originsCover(urls []string) error {
	allowed := map[string]struct{}{}
	for _, o := range s.AllowedOrigins {
		allowed[o] = struct{}{}
	}
	seenURL := map[string]struct{}{}
	for _, raw := range urls {
		if _, dup := seenURL[raw]; dup {
			return refuse(KindInvalidRequest, "source URLs contain duplicate %q", raw)
		}
		seenURL[raw] = struct{}{}
		origin, err := canonicalOrigin(raw)
		if err != nil {
			return err
		}
		if _, ok := allowed[origin]; !ok {
			return refuse(KindInvalidRequest, "source URL origin %q is not in allowed_origins", origin)
		}
	}
	for _, o := range s.Redirects.AllowedOrigins {
		if _, ok := allowed[o]; !ok {
			return refuse(KindInvalidRequest, "redirect origin %q is not in source.allowed_origins", o)
		}
	}
	return nil
}

func validateDestinationV2(d Destination) error {
	for _, item := range []struct {
		name, val string
	}{
		{"destination.root", d.Root},
		{"destination.release_dir", d.ReleaseDir},
		{"destination.executable", d.Executable},
	} {
		if err := boundedToken(item.val, item.name, maxV2PathBytes*4, false); err != nil {
			return err
		}
		if !filepath.IsAbs(item.val) || filepath.Clean(item.val) != item.val || item.val == string(filepath.Separator) {
			return refuse(KindInvalidRequest, "%s %q must be an absolute, clean, non-root path", item.name, item.val)
		}
		if strings.ContainsRune(item.val, 0) {
			return refuse(KindInvalidRequest, "%s contains a NUL", item.name)
		}
	}
	if d.ReleaseDir != d.Root && !strings.HasPrefix(d.ReleaseDir, d.Root+string(filepath.Separator)) {
		return refuse(KindInvalidRequest, "destination.release_dir is not under destination.root")
	}
	if !strings.HasPrefix(d.Executable, d.ReleaseDir+string(filepath.Separator)) {
		return refuse(KindInvalidRequest, "destination.executable is not under destination.release_dir")
	}
	return nil
}

type memberSpec struct {
	Path, Kind, Role string
	Mode             uint32
}

func codexLayoutMembers() []memberSpec {
	return []memberSpec{
		{"bin", MemberKindDirectory, MemberRoleArchiveOnly, v2ModeExec},
		{"bin/codex", MemberKindRegular, MemberRoleSignedSubject, v2ModeExec},
		{"bin/codex-code-mode-host", MemberKindRegular, MemberRoleSignedSubject, v2ModeExec},
		{"codex-package.json", MemberKindRegular, MemberRoleMetadata, v2ModeFile},
		{"codex-path", MemberKindDirectory, MemberRoleArchiveOnly, v2ModeExec},
		{"codex-path/rg", MemberKindRegular, MemberRoleArchiveOnly, v2ModeExec},
		{"codex-resources", MemberKindDirectory, MemberRoleArchiveOnly, v2ModeExec},
		{"codex-resources/bwrap", MemberKindRegular, MemberRoleSignedSubject, v2ModeExec},
		{"codex-resources/zsh", MemberKindDirectory, MemberRoleArchiveOnly, v2ModeExec},
		{"codex-resources/zsh/bin", MemberKindDirectory, MemberRoleArchiveOnly, v2ModeExec},
		{"codex-resources/zsh/bin/zsh", MemberKindRegular, MemberRoleArchiveOnly, v2ModeExec},
	}
}

func grokLayoutMembers() []memberSpec {
	return []memberSpec{
		{"bin", MemberKindDirectory, MemberRoleArchiveOnly, v2ModeExec},
		{"bin/grok", MemberKindRegular, MemberRoleArchiveOnly, v2ModeExec},
	}
}

func expectedMembersFromSpec(spec []memberSpec) []ExpectedMember {
	out := make([]ExpectedMember, len(spec))
	for i, s := range spec {
		out[i] = ExpectedMember{Path: s.Path, Kind: s.Kind, Role: s.Role, FinalMode: s.Mode}
	}
	return out
}

func membersMatch(got []ExpectedMember, want []memberSpec) error {
	if len(got) != len(want) {
		return refuse(KindInvalidRequest, "layout.members has %d entries, want the closed set of %d", len(got), len(want))
	}
	for i := range want {
		g := got[i]
		w := want[i]
		if g.Path != w.Path || g.Kind != w.Kind || g.Role != w.Role || g.FinalMode != w.Mode {
			return refuse(KindInvalidRequest, "layout.members[%d] is %s %s %s mode %d, want %s %s %s mode %d",
				i, g.Path, g.Kind, g.Role, g.FinalMode, w.Path, w.Kind, w.Role, w.Mode)
		}
	}
	return nil
}

var vendorPlatformV2Re = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func closedToken(got, name string, allowed ...string) error {
	if err := boundedToken(got, name, 64, false); err != nil {
		return err
	}
	for _, a := range allowed {
		if got == a {
			return nil
		}
	}
	return refuse(KindInvalidRequest, "%s %q is not a closed value", name, got)
}

func boundedToken(s, name string, max int, allowEmpty bool) error {
	if s == "" {
		if allowEmpty {
			return nil
		}
		return refuse(KindInvalidRequest, "%s is required", name)
	}
	if !utf8.ValidString(s) {
		return refuse(KindInvalidRequest, "%s is not valid UTF-8", name)
	}
	if len(s) > max {
		return refuse(KindInvalidRequest, "%s is longer than %d bytes", name, max)
	}
	if strings.ContainsRune(s, 0) {
		return refuse(KindInvalidRequest, "%s contains a NUL", name)
	}
	if strings.TrimSpace(s) != s {
		return refuse(KindInvalidRequest, "%s has leading or trailing space", name)
	}
	return nil
}

func requireCanonicalBy(n int, key func(int) string, what string) error {
	if n < 0 {
		return refuse(KindInvalidRequest, "%s is invalid", what)
	}
	for i := 0; i < n; i++ {
		k := key(i)
		if k == "" {
			return refuse(KindInvalidRequest, "%s[%d] has an empty canonical key", what, i)
		}
		if i > 0 && key(i-1) >= k {
			return refuse(KindInvalidRequest, "%s is not in canonical unique order (offending key %q)", what, k)
		}
	}
	return nil
}

func layoutPath(p, name string, allowEmpty bool) error {
	if p == "" {
		if allowEmpty {
			return nil
		}
		return refuse(KindInvalidRequest, "%s is required", name)
	}
	if err := boundedToken(p, name, maxV2PathBytes, false); err != nil {
		return err
	}
	if strings.Contains(p, "\\") || strings.HasPrefix(p, "/") || strings.Contains(p, "//") {
		return refuse(KindInvalidRequest, "%s %q is not a relative slash-separated path", name, p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return refuse(KindInvalidRequest, "%s %q has an empty, dot or parent component", name, p)
		}
	}
	return nil
}

func requireHTTPSURL(raw, name string) error {
	if err := boundedToken(raw, name, maxV2URLBytes, false); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return refuse(KindInvalidRequest, "%s %q is not an absolute https URL", name, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return refuse(KindInvalidRequest, "%s %q must not carry a query or fragment", name, raw)
	}
	if u.Host != strings.ToLower(u.Host) {
		return refuse(KindInvalidRequest, "%s host %q is not canonical lowercase", name, u.Host)
	}
	if raw != u.String() {
		return refuse(KindInvalidRequest, "%s %q is not in canonical URL form", name, raw)
	}
	return nil
}

func validateOriginList(origins []string, what string) error {
	if len(origins) == 0 {
		return refuse(KindInvalidRequest, "%s must not be empty", what)
	}
	if len(origins) > maxV2Origins {
		return refuse(KindInvalidRequest, "%s has %d entries, want at most %d", what, len(origins), maxV2Origins)
	}
	if err := requireCanonicalBy(len(origins), func(i int) string { return origins[i] }, what); err != nil {
		return err
	}
	for i, o := range origins {
		origin, err := parseOrigin(o)
		if err != nil {
			return refuse(KindInvalidRequest, "%s[%d] %q is not a canonical https origin", what, i, o)
		}
		if origin != o {
			return refuse(KindInvalidRequest, "%s[%d] %q is not canonical (want %q)", what, i, o, origin)
		}
	}
	return nil
}

func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", refuse(KindInvalidRequest, "URL %q has no https origin", raw)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func parseOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", fmt.Errorf("not an origin")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("origin has a path")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("origin has query")
	}
	if raw != u.Scheme+"://"+u.Host {
		return "", fmt.Errorf("not canonical")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}
