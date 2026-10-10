// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import "embed"

// qualifiedReleases is the official release metadata of the version this build was
// qualified with, for tools whose releases attach no checksum file, trimmed
// to its installable archives and the fields ReleaseArchive reads. When GitHub's API
// limit for the network is used up, that release installs from it instead of failing;
// the archive still comes from github.com and must match the SHA-256 recorded here. The
// file is, for the qualified tag:
//
//	curl -s https://api.github.com/repos/<repo>/releases/tags/<tag> | jq -S '{tag_name,draft,prerelease,
//	  assets:[.assets[]|select(.name|test("^opencode-[a-z0-9-]+\\.tar\\.gz$"))|{name,size,digest,browser_download_url}]}'
//
//go:embed qualified/*.json
var qualifiedReleases embed.FS

// qualifiedRelease is the recorded metadata for driver, if this build has one.
func qualifiedRelease(driver string) ([]byte, bool) {
	body, err := qualifiedReleases.ReadFile("qualified/" + driver + ".json")
	return body, err == nil
}
