// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	// PlanSchema and ReceiptSchema name the typed documents this package emits.
	PlanSchema    = "olivares.ai/tool-install/plan/v1"
	ReceiptSchema = "olivares.ai/tool-install/receipt/v1"

	// ProvenancePublisherSigned is the only provenance class this release installs:
	// a detached signature by a key pinned out of band. A checksum served by the
	// artifact's own origin, or TLS alone, would be a different class and is not
	// offered here.
	ProvenancePublisherSigned = "publisher-signed"

	ActionInstall = "install"
	ActionNoop    = "noop"

	ChannelExact  = "exact"
	ChannelLatest = "latest"
	ChannelStable = "stable"

	SourceOfficial = "official"
	SourceMirror   = "mirror"

	// ReceiptFile and the retained metadata names inside a release directory.
	ReceiptFile       = "receipt.json"
	RetainedManifest  = "manifest.json"
	RetainedSignature = "manifest.json.sig"
	RetainedKey       = "signing-key.asc"
	// stagingPrefix marks a directory the engine is still filling. It is never a
	// cleanup selector: the engine removes only the staging directory it created.
	stagingPrefix = ".staging."
	stagingMarker = "staging.json"
	// LockFile is the per-root install lock. The file is never removed: flock is a
	// property of the inode, and unlinking it would let a waiter and a newcomer
	// both believe they hold the lock.
	LockFile = ".tool-install.lock"
)

// Platform is the target of an install in Go's vocabulary; each provider maps it
// to its own artifact key (Claude: linux-x64, linux-arm64-musl, ...).
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// Libc is glibc or musl on Linux and empty elsewhere.
	Libc string `json:"libc,omitempty"`
}

func (p Platform) String() string {
	if p.Libc == "" {
		return p.OS + "/" + p.Arch
	}
	return p.OS + "/" + p.Arch + "/" + p.Libc
}

// HostPlatform reports the platform this process runs on. On Linux the libc is
// decided by the presence of a musl dynamic loader, which needs no execution.
func HostPlatform() Platform {
	p := Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if p.OS == "linux" {
		p.Libc = "glibc"
		if muslLoaderPresent() {
			p.Libc = "musl"
		}
	}
	return p
}

// ParsePlatform accepts the vendor-style keys operators see in release listings:
// linux-x64, linux-arm64, linux-x64-musl, linux-arm64-musl, darwin-x64,
// darwin-arm64, win32-x64, win32-arm64.
func ParsePlatform(key string) (Platform, error) {
	parts := strings.Split(strings.TrimSpace(key), "-")
	if len(parts) < 2 || len(parts) > 3 {
		return Platform{}, fmt.Errorf("platform %q: want <os>-<arch>[-musl]", key)
	}
	var p Platform
	switch parts[0] {
	case "linux":
		p.OS, p.Libc = "linux", "glibc"
	case "darwin":
		p.OS = "darwin"
	case "win32":
		p.OS = "windows"
	default:
		return Platform{}, fmt.Errorf("platform %q: unknown OS %q", key, parts[0])
	}
	switch parts[1] {
	case "x64":
		p.Arch = "amd64"
	case "arm64":
		p.Arch = "arm64"
	default:
		return Platform{}, fmt.Errorf("platform %q: unknown architecture %q", key, parts[1])
	}
	if len(parts) == 3 {
		if parts[2] != "musl" || p.OS != "linux" {
			return Platform{}, fmt.Errorf("platform %q: only linux accepts a -musl suffix", key)
		}
		p.Libc = "musl"
	}
	return p, nil
}

// Request is what an operator asks for. Version is an exact version or one of the
// provider's pointers (latest, stable). DestRoot must be absolute. Source is
// empty for the official origin or an http(s) base URL of a mirror that carries
// the same layout; a mirror never changes which key is trusted.
type Request struct {
	Driver   string   `json:"driver"`
	Version  string   `json:"version"`
	Platform Platform `json:"platform"`
	DestRoot string   `json:"dest_root"`
	Source   string   `json:"source,omitempty"`
}

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// ValidVersion accepts a semantic-version-shaped string that is safe to use as a
// path element: no separators, no dots-only components, bounded length.
func ValidVersion(v string) bool {
	if len(v) == 0 || len(v) > 64 || strings.Contains(v, "..") {
		return false
	}
	return versionRe.MatchString(v)
}

var vendorKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Source records where the material came from, URL by URL.
type Source struct {
	Kind         string `json:"kind"`
	BaseURL      string `json:"base_url"`
	PointerURL   string `json:"pointer_url,omitempty"`
	ManifestURL  string `json:"manifest_url"`
	SignatureURL string `json:"signature_url"`
	ArtifactURL  string `json:"artifact_url"`
}

// Artifact is the exact material the verified manifest names for the platform.
type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Provenance records what was verified and with which key.
type Provenance struct {
	Class                 string    `json:"class"`
	Verifier              string    `json:"verifier"`
	KeyFingerprint        string    `json:"key_fingerprint"`
	SigningKeyFingerprint string    `json:"signing_key_fingerprint"`
	SignatureCreated      time.Time `json:"signature_created"`
	ManifestSHA256        string    `json:"manifest_sha256"`
	SignatureSHA256       string    `json:"signature_sha256"`
	KeySHA256             string    `json:"key_sha256"`
}

// Destination is where the release lands. ReleaseDir and Executable are absolute.
type Destination struct {
	Root       string `json:"root"`
	ReleaseDir string `json:"release_dir"`
	Executable string `json:"executable"`
}

// Observed describes what already sits at the destination when the plan is made.
// It is OBSERVED, not verified: install revalidates receipt, bytes and manifest
// before crediting an existing release.
type Observed struct {
	ReleaseDir        string `json:"release_dir"`
	ReceiptPresent    bool   `json:"receipt_present"`
	ExecutablePresent bool   `json:"executable_present"`
	ExecutableSHA256  string `json:"executable_sha256,omitempty"`
	ExecutableSize    int64  `json:"executable_size,omitempty"`
	Note              string `json:"note,omitempty"`
}

// Plan is the typed selection an operator approves. Digest binds the selection
// (driver, version, platform, source, artifact, destination, manifest content
// and pinned key) and the fixed Verified list. Action, Observed and Existing
// are observations re-derived at install time: they appear in rendered output
// but never in an approval file. MarshalPlan / ReadPlan use a dedicated
// approval representation that omits those fields, so encoding/json cannot
// fold a human claim onto them.
type Plan struct {
	Schema           string      `json:"schema"`
	Driver           string      `json:"driver"`
	Channel          string      `json:"channel"`
	RequestedVersion string      `json:"requested_version"`
	Version          string      `json:"version"`
	Platform         Platform    `json:"platform"`
	VendorPlatform   string      `json:"vendor_platform"`
	Source           Source      `json:"source"`
	Artifact         Artifact    `json:"artifact"`
	Provenance       Provenance  `json:"provenance"`
	Destination      Destination `json:"destination"`
	// Verified lists the facts established while planning; Observed lists the
	// facts merely read from disk. Anything not in Verified is unverified.
	Verified []string  `json:"verified"`
	Observed []string  `json:"observed,omitempty"`
	Action   string    `json:"action,omitempty"`
	Existing *Observed `json:"existing,omitempty"`
	Digest   string    `json:"digest"`
}

// ProbeReport is the outcome of executing a staged or detected executable.
type ProbeReport struct {
	Argv       []string `json:"argv"`
	Output     string   `json:"output"`
	DurationMS int64    `json:"duration_ms"`
	// EnvNames lists the variable names the probe ran with; values are not
	// recorded because they are temporary paths.
	EnvNames []string `json:"env_names"`
}

// Receipt is written into the release directory after the probe passed and
// before the directory is renamed into place.
type Receipt struct {
	Schema           string      `json:"schema"`
	Driver           string      `json:"driver"`
	Version          string      `json:"version"`
	Platform         Platform    `json:"platform"`
	VendorPlatform   string      `json:"vendor_platform"`
	Source           Source      `json:"source"`
	Artifact         Artifact    `json:"artifact"`
	Provenance       Provenance  `json:"provenance"`
	Destination      Destination `json:"destination"`
	Probe            ProbeReport `json:"probe"`
	PlanDigest       string      `json:"plan_digest"`
	InstalledAt      time.Time   `json:"installed_at"`
	InstallerVersion string      `json:"installer_version"`
	Retained         Retained    `json:"retained"`
}

// Retained names the verified metadata kept beside the executable so a later
// no-op or detection can re-verify against the same bytes.
type Retained struct {
	Manifest  string `json:"manifest"`
	Signature string `json:"signature"`
	Key       string `json:"key"`
}

// Material is the verified metadata a provider hands the engine for retention.
type Material struct {
	Manifest  []byte
	Signature []byte
	Key       []byte
}
