//go:build contract && linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package protocolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
)

type installed struct {
	Version       string `json:"version"`
	Executable    string `json:"-"`
	Source        string `json:"source"`
	SHA256        string `json:"executable_sha256"`
	PackageSHA256 string `json:"package_sha256"`
	Verification  string `json:"verification"`
}

func installTool(tool string) (installed, error) {
	version := os.Getenv("OLIVARES_CONTRACT_VERSION_" + strings.ToUpper(tool))
	if version == "" {
		version = "latest"
		if tool == "grok" {
			version = "stable"
		}
	}
	cache, err := filepath.Abs(filepath.Join(".cache", "protocolcontract", tool+"-"+version))
	if err != nil {
		return installed{}, err
	}
	return installToolAt(tool, version, cache)
}

func installToolAt(tool, version, cache string) (installed, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if version != "latest" && version != "stable" && !toolinstall.ValidVersion(version) {
		return installed{}, fmt.Errorf("invalid requested tool version")
	}
	claude := toolinstall.NewClaude(toolinstall.ClaudeOptions{Verifier: toolinstall.NativeOpenPGPVerifier{}})
	catalog, err := toolinstall.NewCapabilityCatalog(toolinstall.NewCatalog(claude),
		toolinstall.NewCodexRelease(toolinstall.ReleaseArchiveOptions{}), toolinstall.NewGrok(toolinstall.GrokOptions{}),
		toolinstall.NewOpenCode(toolinstall.ReleaseArchiveOptions{}))
	if err != nil {
		return installed{}, err
	}
	engine := toolinstall.NewEngineWithCapabilities(catalog, toolinstall.EngineOptions{InstallerVersion: "protocol-contract"})
	var result installed
	if tool == "claude" {
		rec, plan, err := engine.Install(ctx, toolinstall.Request{Driver: tool, Version: version, DestRoot: cache,
			Platform: toolinstall.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH, Libc: "glibc"}}, nil, io.Discard)
		if err != nil {
			return result, err
		}
		result = installed{Version: rec.Version, Executable: rec.Destination.Executable, Source: plan.Source.ArtifactURL,
			PackageSHA256: rec.Artifact.SHA256, Verification: rec.Provenance.Class}
	} else {
		platform, _, err := toolinstall.PlatformV2For(tool, toolinstall.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH})
		if err != nil {
			return result, err
		}
		rec, plan, err := engine.InstallV2(ctx, toolinstall.RequestV2{Driver: tool, Version: version, DestRoot: cache, Platform: platform}, nil, io.Discard)
		if err != nil {
			return result, err
		}
		result = installed{Version: rec.Version, Executable: rec.Destination.Executable, Source: plan.Selection.Source.Package.URL,
			PackageSHA256: rec.FetchedObject.SHA256, Verification: rec.VerificationKind}
	}
	f, err := os.Open(result.Executable)
	if err != nil {
		return result, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return result, err
	}
	result.SHA256 = hex.EncodeToString(h.Sum(nil))
	if result.Version == "" || result.PackageSHA256 == "" {
		return result, fmt.Errorf("installer returned incomplete identity")
	}
	return result, nil
}
