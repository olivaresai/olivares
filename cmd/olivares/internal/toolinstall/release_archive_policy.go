// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package toolinstall

import (
	"fmt"
	"path"
	"reflect"
	"strings"
)

const (
	DriverOpenCode                  = "opencode"
	DriverOllama                    = "ollama"
	VerificationGitHubReleaseSHA256 = "github-release-sha256"
	PackagePolicyReleaseArchiveV1   = "github-release-archive-v1"
	LayoutIDOpenCodeArchiveV1       = "opencode-archive-v1"
	LayoutIDOllamaArchiveV1         = "ollama-runtime-archive-v1"
	maxReleaseArchiveBytes          = int64(2 << 30)
	maxOllamaExpandedBytes          = int64(8 << 30)
)

func releaseRepository(driver string) string {
	switch driver {
	case DriverOpenCode:
		return "anomalyco/opencode"
	case DriverOllama:
		return "ollama/ollama"
	}
	return ""
}
func releaseAssetName(driver, vendor string) string {
	if driver == DriverOpenCode {
		return "opencode-" + vendor + ".tar.gz"
	}
	return "ollama-" + vendor + ".tar.zst"
}
func releaseArchiveLayout(driver string) ExpectedLayout {
	layout := ExpectedLayout{ID: LayoutIDOpenCodeArchiveV1, Variant: driver, EntryPoint: "bin/" + driver, Members: []ExpectedMember{
		{Path: "bin", Kind: MemberKindDirectory, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec},
		{Path: "bin/" + driver, Kind: MemberKindRegular, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec}}, Limits: ExtractionLimits{MaxCompressedBytes: 128 << 20, MaxExpandedBytes: 512 << 20, MaxMembers: 2, MaxMemberBytes: 512 << 20}}
	if driver == DriverOllama {
		layout.ID = LayoutIDOllamaArchiveV1
		layout.Members = append(layout.Members, ExpectedMember{Path: "lib", Kind: MemberKindDirectory, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec}, ExpectedMember{Path: "lib/ollama", Kind: MemberKindDirectory, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec})
		layout.Limits = ExtractionLimits{MaxCompressedBytes: maxReleaseArchiveBytes, MaxExpandedBytes: maxOllamaExpandedBytes, MaxMembers: 512, MaxMemberBytes: 2 << 30}
	}
	return layout
}
func releaseArchiveLayoutID(id string) bool {
	return id == LayoutIDOpenCodeArchiveV1 || id == LayoutIDOllamaArchiveV1
}
func validateReleaseArchiveLayout(layout ExpectedLayout) error {
	driver := DriverOpenCode
	if layout.ID == LayoutIDOllamaArchiveV1 {
		driver = DriverOllama
	}
	if !reflect.DeepEqual(layout, releaseArchiveLayout(driver)) {
		return refuse(KindInvalidRequest, "release archive layout must match its fixed bounded policy")
	}
	return nil
}
func validateOllamaFetched(f FetchedObjectExpectation) error {
	if f.DigestState != DigestStateExact || !isHex64(f.SHA256) || f.SizeState != SizeStateExact || f.Size <= 0 || f.Size > f.MaxSize || f.MaxSize != maxReleaseArchiveBytes {
		return refuse(KindInvalidRequest, "Ollama release requires an exact SHA-256 and size within its 2 GiB archive cap")
	}
	return nil
}
func (s SelectionV2) validateReleaseArchiveSelection() error {
	repo := releaseRepository(s.Driver)
	plat, vendor, err := PlatformV2For(s.Driver, Platform{OS: s.Platform.OS, Arch: s.Platform.Arch})
	if err != nil {
		return err
	}
	if s.Platform != plat || s.VendorPlatform != vendor {
		return refuse(KindInvalidRequest, "release archive platform does not match its fixed vendor mapping")
	}
	if s.PackagePolicyID != PackagePolicyReleaseArchiveV1 || s.Verification.Kind != VerificationGitHubReleaseSHA256 || s.Verification.Cosign != nil || len(s.RequiredSubjects) != 0 || len(s.Source.Proofs) != 0 || s.Source.Kind != SourceOfficial {
		return refuse(KindInvalidRequest, "release archive requires official metadata checksum policy without a publisher signature claim")
	}
	if s.FetchedObject.DigestState != DigestStateExact || s.FetchedObject.SizeState != SizeStateExact || s.FetchedObject.MaxSize != s.Layout.Limits.MaxCompressedBytes {
		return refuse(KindInvalidRequest, "release archive checksum and size must be exact and bounded")
	}
	if !reflect.DeepEqual(s.Layout, releaseArchiveLayout(s.Driver)) {
		return refuse(KindInvalidRequest, "release archive layout does not match its driver")
	}
	api := "https://api.github.com/repos/" + repo + "/releases/"
	metadata := api + "tags/v" + s.Version
	if s.Source.Checksums != (URLRef{State: URLStatePresent, URL: metadata}) {
		return refuse(KindInvalidRequest, "checksum metadata must use the pinned official repository API")
	}
	if s.Source.Pointer.State != URLStatePresent || (s.Source.Pointer.URL != api+"latest" && s.Source.Pointer.URL != metadata) {
		return refuse(KindInvalidRequest, "release pointer is outside the pinned official repository API")
	}
	pkg := "https://github.com/" + repo + "/releases/download/v" + s.Version + "/" + releaseAssetName(s.Driver, vendor)
	if s.Source.Package != (URLRef{State: URLStatePresent, URL: pkg}) {
		return refuse(KindInvalidRequest, "archive URL is outside the selected official release")
	}
	if !reflect.DeepEqual(s.Source.AllowedOrigins, []string{"https://api.github.com", "https://github.com", "https://release-assets.githubusercontent.com"}) || !reflect.DeepEqual(s.Source.Redirects, RedirectPolicyV2{MaxHops: 3, AllowedOrigins: []string{"https://release-assets.githubusercontent.com"}}) {
		return refuse(KindInvalidRequest, "release archive origins and redirects must use the pinned GitHub policy")
	}
	return nil
}

// Archive names may have a conventional ./ prefix, but never a path alias,
// absolute path, backslash, parent component, control character or empty segment.
func releaseArchivePath(raw string) (string, error) {
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.TrimSuffix(raw, "/")
	if raw == "." || raw == "" {
		return "", nil
	}
	if len(raw) > maxV2PathBytes || strings.ContainsAny(raw, "\\\x00\r\n") || path.IsAbs(raw) || path.Clean(raw) != raw {
		return "", fmt.Errorf("unsafe archive path %q", raw)
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." || part == "." || part == "" {
			return "", fmt.Errorf("unsafe archive path %q", raw)
		}
	}
	return raw, nil
}
func releaseArchiveMember(driver, name string) bool {
	if driver == DriverOpenCode {
		return name == "bin" || name == "bin/opencode"
	}
	return name == "bin" || name == "bin/ollama" || name == "lib" || name == "lib/ollama" || strings.HasPrefix(name, "lib/ollama/")
}
