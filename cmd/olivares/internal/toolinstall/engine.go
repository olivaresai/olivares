// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ActionConflict is the plan action when the destination is occupied by
// something that is not a valid installed release. Install refuses it; nothing
// repairs it automatically.
const ActionConflict = "conflict"

// stagingDirMode keeps a release private while it is being filled; nothing
// under it is verified yet. releaseDirMode is set explicitly just before the
// finished, verified release is renamed into place, so a deliberately shared
// root can serve the binary to other accounts. The root the operator chose and
// existing releases are never chmod-ed.
const (
	stagingDirMode os.FileMode = 0o700
	releaseDirMode os.FileMode = 0o755
)

// Engine orchestrates providers against a destination root.
type Engine struct {
	catalog          *Catalog
	installerVersion string
	now              func() time.Time
	// beforePlace runs after the receipt is written and before the staging
	// directory is renamed into place. Tests use it to interrupt an install.
	beforePlace func() error
}

type EngineOptions struct {
	InstallerVersion string
	Now              func() time.Time
}

func NewEngine(c *Catalog, o EngineOptions) *Engine {
	e := &Engine{catalog: c, installerVersion: o.InstallerVersion, now: o.Now}
	if e.now == nil {
		e.now = func() time.Time { return time.Now().UTC() }
	}
	if e.installerVersion == "" {
		e.installerVersion = "dev"
	}
	return e
}

// Catalog exposes the registered providers.
func (e *Engine) Catalog() *Catalog { return e.catalog }

func validateRequest(req Request) error {
	if req.Driver == "" {
		return refuse(KindInvalidRequest, "a driver is required")
	}
	if !filepath.IsAbs(req.DestRoot) || filepath.Clean(req.DestRoot) != req.DestRoot || req.DestRoot == string(filepath.Separator) {
		return refuse(KindInvalidRequest, "destination root %q must be an absolute, clean, non-root path", req.DestRoot)
	}
	return nil
}

// Plan resolves the request and reports what install would do. It creates
// nothing, downloads no artifact and executes nothing; the only disk reads are
// of the destination, to report what is already there.
func (e *Engine) Plan(ctx context.Context, req Request) (*Plan, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	p, err := e.catalog.Lookup(req.Driver)
	if err != nil {
		return nil, err
	}
	plan, _, err := p.Resolve(ctx, req)
	if err != nil {
		return nil, err
	}
	e.observe(plan)
	plan.Digest = ComputeDigest(plan)
	return plan, nil
}

// observe fills Action and Existing from what sits at the destination.
func (e *Engine) observe(plan *Plan) {
	plan.Action = ActionInstall
	fi, err := os.Lstat(plan.Destination.ReleaseDir)
	if err != nil {
		return
	}
	plan.Observed = append(plan.Observed, "existing_release_dir")
	obs := &Observed{ReleaseDir: plan.Destination.ReleaseDir}
	plan.Existing = obs
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		obs.Note = "the release path exists and is not a directory; install refuses it and changes nothing"
		plan.Action = ActionConflict
		return
	}
	if rfi, err := os.Lstat(filepath.Join(plan.Destination.ReleaseDir, ReceiptFile)); err == nil && rfi.Mode().IsRegular() {
		obs.ReceiptPresent = true
	}
	if efi, err := os.Lstat(plan.Destination.Executable); err == nil && efi.Mode().IsRegular() {
		obs.ExecutablePresent = true
		obs.ExecutableSize = efi.Size()
		if sum, err := fileSHA256(plan.Destination.Executable); err == nil {
			obs.ExecutableSHA256 = sum
			plan.Observed = append(plan.Observed, "existing_executable_sha256")
		}
	}
	switch {
	case obs.ReceiptPresent && obs.ExecutablePresent:
		plan.Action = ActionNoop
		obs.Note = "a release is already recorded here; install revalidates its receipt, bytes and retained signed manifest before crediting it"
	default:
		plan.Action = ActionConflict
		obs.Note = "the release directory exists without a complete receipt and executable; install refuses it (damaged) and does not repair or remove it"
	}
}

// Install performs the plan. When approved is non-nil the fresh resolution must
// carry the same digest, otherwise the install is refused as plan_changed. The
// returned plan is the one actually executed.
func (e *Engine) Install(ctx context.Context, req Request, approved *Plan, progress io.Writer) (*Receipt, *Plan, error) {
	if progress == nil {
		progress = io.Discard
	}
	if err := validateRequest(req); err != nil {
		return nil, nil, err
	}
	p, err := e.catalog.Lookup(req.Driver)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(progress, "resolving %s %s for %s from %s\n", req.Driver, req.Version, req.Platform, orOfficial(req.Source))
	plan, material, err := p.Resolve(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	plan.Digest = ComputeDigest(plan)
	fmt.Fprintf(progress, "manifest signature verified: primary key %s, signing key %s, made %s\n", plan.Provenance.KeyFingerprint, plan.Provenance.SigningKeyFingerprint, plan.Provenance.SignatureCreated.Format(time.RFC3339))
	fmt.Fprintf(progress, "selected %s %s %s: %d bytes, sha256 %s\n", plan.Driver, plan.Version, plan.VendorPlatform, plan.Artifact.Size, plan.Artifact.SHA256)
	if approved != nil && approved.Digest != plan.Digest {
		return nil, plan, refuse(KindPlanChanged, "the approved plan (digest %s) no longer matches what %s resolves to (digest %s); the selection changed, so a new plan must be reviewed", short(approved.Digest), orOfficial(req.Source), short(plan.Digest))
	}
	root, err := openRoot(req.DestRoot, true)
	if err != nil {
		return nil, plan, err
	}
	defer func() { _ = root.Close() }()
	release, err := lockRoot(root, req.DestRoot)
	if err != nil {
		return nil, plan, err
	}
	defer release()

	if err := ensureDir(root, plan.Driver, 0o755); err != nil {
		return nil, plan, err
	}
	relRelease := filepath.Join(plan.Driver, plan.Version+"-"+plan.VendorPlatform)
	if fi, err := root.Lstat(relRelease); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			plan.Action = ActionConflict
			return nil, plan, refuse(KindConflict, "%s exists and is not a directory; it is left untouched", plan.Destination.ReleaseDir)
		}
		rec, err := e.revalidate(ctx, root, p, plan, relRelease)
		if err != nil {
			plan.Action = ActionConflict
			return nil, plan, err
		}
		plan.Action = ActionNoop
		fmt.Fprintf(progress, "already installed and revalidated: %s\n", plan.Destination.Executable)
		return rec, plan, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, plan, refuse(KindDestinationUnsafe, "inspect %s: %v", plan.Destination.ReleaseDir, err)
	}
	plan.Action = ActionInstall

	staging := filepath.Join(plan.Driver, stagingPrefix+plan.Version+"-"+plan.VendorPlatform+"."+randHex(8))
	if err := root.Mkdir(staging, stagingDirMode); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "create staging directory under %s: %v", plan.Destination.Root, err)
	}
	placed := false
	defer func() {
		if !placed {
			// Only the directory THIS operation created is removed. Other
			// .staging.* directories belong to other operations and are reported
			// by list, never removed here.
			_ = root.RemoveAll(staging)
		}
	}()
	marker, _ := json.Marshal(stagingMarkerDoc{
		Schema: "olivares.ai/tool-install/staging/v1", PID: os.Getpid(), StartedAt: e.now(),
		PlanDigest: plan.Digest, Driver: plan.Driver, Version: plan.Version, VendorPlatform: plan.VendorPlatform,
		InstallerVersion: e.installerVersion,
	})
	if err := writeFileExcl(root, filepath.Join(staging, stagingMarker), marker, 0o600); err != nil {
		return nil, plan, err
	}
	for _, f := range []struct {
		name string
		b    []byte
	}{{RetainedManifest, material.Manifest}, {RetainedSignature, material.Signature}, {RetainedKey, material.Key}} {
		if err := writeFileExcl(root, filepath.Join(staging, f.name), f.b, 0o644); err != nil {
			return nil, plan, err
		}
	}
	if err := root.Mkdir(filepath.Join(staging, "bin"), 0o755); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "create %s/bin: %v", staging, err)
	}
	part := filepath.Join(staging, "bin", plan.Artifact.Name+".part")
	final := filepath.Join(staging, "bin", plan.Artifact.Name)
	fmt.Fprintf(progress, "downloading %d bytes from %s\n", plan.Artifact.Size, plan.Source.ArtifactURL)
	f, err := root.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "create %s: %v", part, err)
	}
	got, ferr := p.FetchArtifact(ctx, plan, f)
	if ferr == nil {
		ferr = f.Sync()
		if ferr != nil {
			ferr = refuse(KindDestinationNotWritable, "fsync artifact: %v", ferr)
		}
	}
	if cerr := f.Close(); ferr == nil && cerr != nil {
		ferr = refuse(KindDestinationNotWritable, "close artifact: %v", cerr)
	}
	if ferr != nil {
		return nil, plan, ferr
	}
	fmt.Fprintf(progress, "artifact matches the verified manifest: %d bytes, sha256 %s\n", got.Size, got.SHA256)
	// Executable only now: size and hash were enforced by the fetch.
	if err := root.Chmod(part, 0o755); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "chmod artifact: %v", err)
	}
	if err := root.Rename(part, final); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "name artifact: %v", err)
	}
	scratch := filepath.Join(staging, ".probe")
	if err := root.Mkdir(scratch, 0o700); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "create probe scratch: %v", err)
	}
	exeAbs := filepath.Join(req.DestRoot, final)
	fmt.Fprintf(progress, "probing the staged executable: %s --version\n", exeAbs)
	probe, err := p.Probe(ctx, exeAbs, filepath.Join(req.DestRoot, scratch), plan.Version)
	if err != nil {
		return nil, plan, err
	}
	fmt.Fprintf(progress, "probe reported %q in %d ms\n", firstLine(probe.Output), probe.DurationMS)
	if err := root.RemoveAll(scratch); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "remove probe scratch: %v", err)
	}
	// The probe ran the staged file; make sure it is still the file that was
	// verified before it is recorded and placed.
	if sum, size, err := fileSHA256Root(root, final); err != nil || sum != plan.Artifact.SHA256 || size != plan.Artifact.Size {
		return nil, plan, refuse(KindDigestMismatch, "the staged executable changed between verification and placement (%v)", err)
	}
	rec := &Receipt{
		Schema: ReceiptSchema, Driver: plan.Driver, Version: plan.Version, Platform: plan.Platform, VendorPlatform: plan.VendorPlatform,
		Source: plan.Source, Artifact: got, Provenance: plan.Provenance, Destination: plan.Destination, Probe: probe,
		PlanDigest: plan.Digest, InstalledAt: e.now(), InstallerVersion: e.installerVersion,
		Retained: Retained{Manifest: RetainedManifest, Signature: RetainedSignature, Key: RetainedKey},
	}
	recJSON, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, plan, err
	}
	if err := writeFileExcl(root, filepath.Join(staging, ReceiptFile), recJSON, 0o644); err != nil {
		return nil, plan, err
	}
	if err := root.Remove(filepath.Join(staging, stagingMarker)); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "finish staging: %v", err)
	}
	if err := syncDir(root, staging); err != nil {
		return nil, plan, err
	}
	if e.beforePlace != nil {
		if err := e.beforePlace(); err != nil {
			// The interruption hook models a crash: staging stays for list to
			// report, still private, and nothing else has been touched.
			placed = true
			return nil, plan, err
		}
	}
	// Everything inside is verified and recorded: open the finished release to
	// the mode a shared root expects, then publish it in one rename.
	if err := root.Chmod(staging, releaseDirMode); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "set release directory mode: %v", err)
	}
	if err := root.Rename(staging, relRelease); err != nil {
		if _, lerr := root.Lstat(relRelease); lerr == nil {
			return nil, plan, refuse(KindConflict, "%s appeared while this install was staging; the staged copy is discarded and the existing directory is left as found", plan.Destination.ReleaseDir)
		}
		return nil, plan, refuse(KindDestinationNotWritable, "place release at %s: %v", plan.Destination.ReleaseDir, err)
	}
	placed = true
	if err := syncDir(root, plan.Driver); err != nil {
		return rec, plan, err
	}
	if sum, size, err := fileSHA256Root(root, filepath.Join(relRelease, "bin", plan.Artifact.Name)); err != nil || sum != plan.Artifact.SHA256 || size != plan.Artifact.Size {
		return rec, plan, refuse(KindDamaged, "%s was placed but does not re-read as the verified bytes (%v)", plan.Destination.Executable, err)
	}
	fmt.Fprintf(progress, "placed %s\n", plan.Destination.Executable)
	return rec, plan, nil
}

// revalidate credits an existing release only when its receipt, bytes and
// retained signed manifest all agree with the fresh plan.
func (e *Engine) revalidate(ctx context.Context, root *os.Root, p Provider, plan *Plan, relRelease string) (*Receipt, error) {
	rec, err := readReceipt(root, filepath.Join(relRelease, ReceiptFile), plan.Destination.ReleaseDir)
	if err != nil {
		return nil, refuse(KindDamaged, "%s exists but its receipt is not usable (%v); it is left untouched and no repair is attempted", plan.Destination.ReleaseDir, err)
	}
	if rec.Driver != plan.Driver || rec.Version != plan.Version || rec.VendorPlatform != plan.VendorPlatform {
		return nil, refuse(KindDamaged, "%s records %s %s %s, not %s %s %s", plan.Destination.ReleaseDir, rec.Driver, rec.Version, rec.VendorPlatform, plan.Driver, plan.Version, plan.VendorPlatform)
	}
	sum, size, err := fileSHA256Root(root, filepath.Join(relRelease, "bin", plan.Artifact.Name))
	if err != nil {
		return nil, refuse(KindDamaged, "%s: executable cannot be read (%v)", plan.Destination.ReleaseDir, err)
	}
	if sum != plan.Artifact.SHA256 || size != plan.Artifact.Size {
		return nil, refuse(KindDamaged, "%s holds sha256 %s (%d bytes); the verified manifest binds %s (%d bytes); the existing bytes are left untouched", plan.Destination.Executable, sum, size, plan.Artifact.SHA256, plan.Artifact.Size)
	}
	if rec.Artifact.SHA256 != sum {
		return nil, refuse(KindDamaged, "%s receipt records sha256 %s but the file holds %s", plan.Destination.ReleaseDir, rec.Artifact.SHA256, sum)
	}
	manifest, err := root.ReadFile(filepath.Join(relRelease, RetainedManifest))
	if err != nil {
		return nil, refuse(KindDamaged, "%s: retained manifest cannot be read (%v)", plan.Destination.ReleaseDir, err)
	}
	signature, err := root.ReadFile(filepath.Join(relRelease, RetainedSignature))
	if err != nil {
		return nil, refuse(KindDamaged, "%s: retained signature cannot be read (%v)", plan.Destination.ReleaseDir, err)
	}
	if int64(len(manifest)) > claudeManifestCap || int64(len(signature)) > claudeSignatureCap {
		return nil, refuse(KindDamaged, "%s: retained metadata exceeds its size cap", plan.Destination.ReleaseDir)
	}
	vm, err := p.VerifyMaterial(ctx, &Material{Manifest: manifest, Signature: signature})
	if err != nil {
		return nil, refuse(KindDamaged, "%s: retained manifest does not verify (%v)", plan.Destination.ReleaseDir, err)
	}
	art, ok := vm.Artifacts[plan.VendorPlatform]
	if vm.Version != plan.Version || !ok || art.SHA256 != sum || art.Size != size {
		return nil, refuse(KindDamaged, "%s: retained signed manifest names different material than the installed bytes", plan.Destination.ReleaseDir)
	}
	return rec, nil
}

type stagingMarkerDoc struct {
	Schema           string    `json:"schema"`
	PID              int       `json:"pid"`
	StartedAt        time.Time `json:"started_at"`
	PlanDigest       string    `json:"plan_digest"`
	Driver           string    `json:"driver"`
	Version          string    `json:"version"`
	VendorPlatform   string    `json:"vendor_platform"`
	InstallerVersion string    `json:"installer_version"`
}

// openRoot validates the destination root and opens it for traversal-safe
// operations. With create, a missing root is created (0755) for the caller.
func openRoot(rootPath string, create bool) (*os.Root, error) {
	fi, err := os.Lstat(rootPath)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := os.MkdirAll(rootPath, 0o755); err != nil {
			return nil, refuse(KindDestinationNotWritable, "create destination root %s: %v", rootPath, err)
		}
		fi, err = os.Lstat(rootPath)
	}
	if err != nil {
		return nil, refuse(KindDestinationNotWritable, "destination root %s: %v", rootPath, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, refuse(KindDestinationUnsafe, "destination root %s is a symlink; name the real directory", rootPath)
	}
	if !fi.IsDir() {
		return nil, refuse(KindDestinationUnsafe, "destination root %s is not a directory", rootPath)
	}
	if err := ownedByCaller(fi); err != nil {
		return nil, refuse(KindDestinationUnsafe, "destination root %s: %v", rootPath, err)
	}
	if fi.Mode().Perm()&0o002 != 0 {
		return nil, refuse(KindDestinationUnsafe, "destination root %s is world-writable (%s); another account could plant directories under it", rootPath, fi.Mode().Perm())
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, refuse(KindDestinationNotWritable, "open destination root %s: %v", rootPath, err)
	}
	return root, nil
}

// lockRoot takes the per-root single-agent lock.
func lockRoot(root *os.Root, rootPath string) (func(), error) {
	f, err := root.OpenFile(LockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, refuse(KindDestinationNotWritable, "open %s: %v", filepath.Join(rootPath, LockFile), err)
	}
	release, err := lockFile(f, filepath.Join(rootPath, LockFile))
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return release, nil
}

// LockRoot takes the install lock of root for callers outside an install (a
// future remove, or a test that models a concurrent installer).
func LockRoot(rootPath string) (func(), error) {
	root, err := openRoot(rootPath, true)
	if err != nil {
		return nil, err
	}
	release, err := lockRoot(root, rootPath)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return func() { release(); _ = root.Close() }, nil
}

func ensureDir(root *os.Root, rel string, perm os.FileMode) error {
	fi, err := root.Lstat(rel)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return refuse(KindDestinationUnsafe, "%s exists under the destination root and is not a directory", rel)
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if err := root.Mkdir(rel, perm); err != nil {
			return refuse(KindDestinationNotWritable, "create %s: %v", rel, err)
		}
		return nil
	default:
		return refuse(KindDestinationUnsafe, "inspect %s: %v", rel, err)
	}
}

func writeFileExcl(root *os.Root, rel string, b []byte, perm os.FileMode) error {
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return refuse(KindDestinationNotWritable, "create %s: %v", rel, err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return refuse(KindDestinationNotWritable, "write %s: %v", rel, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return refuse(KindDestinationNotWritable, "fsync %s: %v", rel, err)
	}
	if err := f.Close(); err != nil {
		return refuse(KindDestinationNotWritable, "close %s: %v", rel, err)
	}
	return nil
}

func syncDir(root *os.Root, rel string) error {
	d, err := root.Open(rel)
	if err != nil {
		return refuse(KindDestinationNotWritable, "open %s for fsync: %v", rel, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return refuse(KindDestinationNotWritable, "fsync %s: %v", rel, err)
	}
	return nil
}

func fileSHA256Root(root *os.Root, rel string) (string, int64, error) {
	fi, err := root.Lstat(rel)
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", rel)
	}
	f, err := root.Open(rel)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	return hashReader(f)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- read-only fingerprint of a path the plan derived from the destination root
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sum, _, err := hashReader(f)
	return sum, err
}

func hashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("toolinstall: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func orOfficial(source string) string {
	if strings.TrimSpace(source) == "" {
		return "the official release origin"
	}
	return source
}
