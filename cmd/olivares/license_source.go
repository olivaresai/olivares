// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// licenseFileName is the canonical at-rest license inside the data dir — the file
// `olivares license install` writes and the engine reads by default. It sits beside
// the other data-dir artifacts the engine owns (olivares.db, tls.crt/tls.key) and
// follows the same convention; it holds the signed license blob
// (base64url(payload).base64url(sig)), never a cryptographic key (the customer never
// holds one — the verification public key is embedded in the binary, the signing
// private key exists only in the narrowly scoped license-issuance Worker, LICENSING.md).
const licenseFileName = "license.key"

// License source kinds, in PRECEDENCE order (highest first). They are the honest
// provenance reported by `license status`, server-info and the console edition
// panel so the effective source is never a silent mystery (docs/SECURITY-HARDENING.md).
const (
	licenseSourceNone      = "none"       // no license configured anywhere
	licenseSourceFlag      = "flag"       // an explicit --license <path>
	licenseSourceEnvPath   = "env-path"   // OLIVARES_LICENSE_PATH=<path>
	licenseSourceEnvInline = "env-inline" // OLIVARES_LICENSE=<blob>
	licenseSourceDataDir   = "data-dir"   // the canonical <data-dir>/license.key
)

// licenseSource is a resolved license: the blob, where it came from and (for a
// file source) its path. A zero Blob with Kind==none means no license is configured.
type licenseSource struct {
	Blob string
	Kind string
	Path string // set for flag / env-path / data-dir; empty for env-inline / none
}

// External reports whether the license comes from a source that OUTRANKS the
// data-dir canonical file (an explicit flag or an env override). The console/CLI
// install path writes the data-dir file, so when an external source is active the
// install would be SHADOWED on the next resolve — the service refuses it rather
// than silently write a file that never takes effect.
func (s licenseSource) External() bool {
	switch s.Kind {
	case licenseSourceFlag, licenseSourceEnvPath, licenseSourceEnvInline:
		return true
	default:
		return false
	}
}

// licenseDataDirPath is the canonical data-dir license path.
func licenseDataDirPath(dataDir string) string {
	return filepath.Join(dataDir, licenseFileName)
}

// resolveLicense determines the active license and its provenance, in the order
// explicit (--license / cfg.LicenseFile) > OLIVARES_LICENSE_PATH > OLIVARES_LICENSE
// > the data-dir default file (§3 point 2, the GitLab/Grafana/Elastic multi-surface
// install). A file source that is SET but unreadable returns an error (an explicit
// operator intent must fail loudly, never silently fall through to a different
// license); the data-dir default being ABSENT is not an error (it is the optional
// default — the engine simply runs license-less, which the open product does anyway).
//
// It only READS the blob; it never verifies it (verification is the holder's job,
// and per LICENSING.md a bad blob is "no commercial license", never a boot failure).
func resolveLicense(explicitPath, dataDir string, getenv func(string) string) (licenseSource, error) {
	if explicitPath != "" {
		b, err := os.ReadFile(explicitPath)
		if err != nil {
			return licenseSource{}, fmt.Errorf("read --license %q: %w", explicitPath, err)
		}
		return licenseSource{Blob: strings.TrimSpace(string(b)), Kind: licenseSourceFlag, Path: explicitPath}, nil
	}
	if p := strings.TrimSpace(getenv("OLIVARES_LICENSE_PATH")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return licenseSource{}, fmt.Errorf("read OLIVARES_LICENSE_PATH %q: %w", p, err)
		}
		return licenseSource{Blob: strings.TrimSpace(string(b)), Kind: licenseSourceEnvPath, Path: p}, nil
	}
	if v := strings.TrimSpace(getenv("OLIVARES_LICENSE")); v != "" {
		return licenseSource{Blob: v, Kind: licenseSourceEnvInline}, nil
	}
	// Data-dir default: present → use it; absent → none (not an error).
	path := licenseDataDirPath(dataDir)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return licenseSource{Kind: licenseSourceNone}, nil
		}
		return licenseSource{}, fmt.Errorf("read %q: %w", path, err)
	}
	return licenseSource{Blob: strings.TrimSpace(string(b)), Kind: licenseSourceDataDir, Path: path}, nil
}

// licenseOverridePresent reports whether a license source OUTRANKING the data-dir
// file is configured, regardless of whether it is currently readable. The console/
// CLI install path consults it to refuse writing a data-dir file that a higher-
// precedence override would shadow — pointing the operator at the real source
// instead of silently producing a no-op file.
func licenseOverridePresent(explicitPath string, getenv func(string) string) (kind, detail string, present bool) {
	if explicitPath != "" {
		return licenseSourceFlag, explicitPath, true
	}
	if p := strings.TrimSpace(getenv("OLIVARES_LICENSE_PATH")); p != "" {
		return licenseSourceEnvPath, p, true
	}
	if strings.TrimSpace(getenv("OLIVARES_LICENSE")) != "" {
		return licenseSourceEnvInline, "OLIVARES_LICENSE", true
	}
	return "", "", false
}
