// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import "path/filepath"

// PlatformV2For maps a host or operator platform onto the closed v2 platform
// for driver. Grok does not use a libc field. Codex is the musl package on
// Linux amd64/arm64 even when the host libc is glibc: the package is static.
func PlatformV2For(driver string, host Platform) (PlatformV2, string, error) {
	if host.OS != "linux" {
		return PlatformV2{}, "", refuse(KindUnsupportedPlatform, "%s is published by some vendors but this installer verifies and installs Linux amd64/arm64 in this increment; macOS and Windows remain release work", host.OS)
	}
	switch driver {
	case DriverGrok:
		p := PlatformV2{OS: "linux", Arch: host.Arch, Libc: ""}
		switch host.Arch {
		case "amd64":
			return p, "linux-x86_64", nil
		case "arm64":
			return p, "linux-aarch64", nil
		default:
			return PlatformV2{}, "", refuse(KindUnsupportedPlatform, "architecture %q has no Grok Build release in this increment", host.Arch)
		}
	case DriverCodex:
		p := PlatformV2{OS: "linux", Arch: host.Arch, Libc: "musl"}
		switch host.Arch {
		case "amd64":
			return p, "x86_64-unknown-linux-musl", nil
		case "arm64":
			return p, "aarch64-unknown-linux-musl", nil
		default:
			return PlatformV2{}, "", refuse(KindUnsupportedPlatform, "architecture %q has no Codex package in this increment", host.Arch)
		}
	default:
		return PlatformV2{}, "", refuse(KindUnsupportedProvider, "driver %q has no v2 platform map", driver)
	}
}

func validateRequestV2(req RequestV2) error {
	if req.Driver == "" {
		return refuse(KindInvalidRequest, "a driver is required")
	}
	if req.Driver != DriverCodex && req.Driver != DriverGrok {
		return refuse(KindUnsupportedProvider, "driver %q is not a v2 installer", req.Driver)
	}
	if !filepath.IsAbs(req.DestRoot) || filepath.Clean(req.DestRoot) != req.DestRoot || req.DestRoot == string(filepath.Separator) {
		return refuse(KindInvalidRequest, "destination root %q must be an absolute, clean, non-root path", req.DestRoot)
	}
	return nil
}
