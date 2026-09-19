// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const fetchedObjectName = ".fetched"

// PlanV2 resolves a v2 request and reports the digest-bound selection. It
// creates nothing and does not fetch the package.
func (e *Engine) PlanV2(ctx context.Context, req RequestV2) (*PlanV2, error) {
	if err := validateRequestV2(req); err != nil {
		return nil, err
	}
	p, err := e.v2Provider(req.Driver)
	if err != nil {
		return nil, err
	}
	plan, _, err := p.ResolveV2(ctx, req)
	if err != nil {
		return nil, err
	}
	plan.Digest = ComputeDigestV2(plan)
	return plan, nil
}

// InstallV2 performs the v2 plan: private staging, bounded fetch, confined
// place, inventory, required verification, executable modes, probe, receipt,
// atomic publication. Error removes only this operation's staging.
func (e *Engine) InstallV2(ctx context.Context, req RequestV2, approved *PlanV2, progress io.Writer) (*ReceiptV2, *PlanV2, error) {
	if progress == nil {
		progress = io.Discard
	}
	if err := validateRequestV2(req); err != nil {
		return nil, nil, err
	}
	p, err := e.v2Provider(req.Driver)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(progress, "resolving %s %s from %s\n", req.Driver, req.Version, orOfficial(req.Source))
	plan, material, err := p.ResolveV2(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	plan.Digest = ComputeDigestV2(plan)
	fmt.Fprintf(progress, "selected %s %s %s verification %s\n", plan.Selection.Driver, plan.Selection.Version, plan.Selection.VendorPlatform, plan.Selection.Verification.Kind)
	if approved != nil && approved.Digest != plan.Digest {
		return nil, plan, refuse(KindPlanChanged, "the approved plan (digest %s) no longer matches what resolves (digest %s); the selection changed, so a new plan must be reviewed", short(approved.Digest), short(plan.Digest))
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

	if err := ensureDir(root, plan.Selection.Driver, 0o755); err != nil {
		return nil, plan, err
	}
	relRelease := filepath.Join(plan.Selection.Driver, plan.Selection.Version+"-"+plan.Selection.VendorPlatform)
	if fi, err := root.Lstat(relRelease); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return nil, plan, refuse(KindConflict, "%s exists and is not a directory; it is left untouched", plan.Selection.Destination.ReleaseDir)
		}
		rec, err := e.revalidateV2(ctx, root, p, plan, relRelease)
		if err != nil {
			return nil, plan, err
		}
		fmt.Fprintf(progress, "already installed and revalidated: %s\n", plan.Selection.Destination.Executable)
		return rec, plan, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, plan, refuse(KindDestinationUnsafe, "inspect %s: %v", plan.Selection.Destination.ReleaseDir, err)
	}

	staging := filepath.Join(plan.Selection.Driver, stagingPrefix+plan.Selection.Version+"-"+plan.Selection.VendorPlatform+"."+randHex(8))
	if err := root.Mkdir(staging, stagingDirMode); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "create staging directory under %s: %v", plan.Selection.Destination.Root, err)
	}
	placed := false
	defer func() {
		if !placed {
			_ = root.RemoveAll(staging)
		}
	}()
	marker, _ := json.Marshal(stagingMarkerDoc{
		Schema: "olivares.ai/tool-install/staging/v1", PID: os.Getpid(), StartedAt: e.now(),
		PlanDigest: plan.Digest, Driver: plan.Selection.Driver, Version: plan.Selection.Version, VendorPlatform: plan.Selection.VendorPlatform,
		InstallerVersion: e.installerVersion,
	})
	if err := writeFileExcl(root, filepath.Join(staging, stagingMarker), marker, 0o600); err != nil {
		return nil, plan, err
	}
	fetchedRel := filepath.Join(staging, fetchedObjectName)
	fmt.Fprintf(progress, "downloading from %s\n", plan.Selection.Source.Package.URL)
	f, err := root.OpenFile(fetchedRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "create fetched object: %v", err)
	}
	// The stream is OBSERVED, not intercepted: the file is still fsynced and
	// closed through its own handle below, and the provider still computes the
	// digest from the bytes it writes. Grok declares only a MAXIMUM size
	// (SizeStateBounded), so a percentage is not always available and the counter
	// says bytes in that case rather than inventing a denominator.
	total := int64(0)
	if plan.Selection.FetchedObject.SizeState == SizeStateExact {
		total = plan.Selection.FetchedObject.Size
	}
	counted := newFetchProgress(f, progress, total)
	got, ferr := p.FetchV2(ctx, plan, counted)
	if ferr == nil {
		counted.done()
	}
	if ferr == nil {
		ferr = f.Sync()
		if ferr != nil {
			ferr = refuse(KindDestinationNotWritable, "fsync fetched object: %v", ferr)
		}
	}
	if cerr := f.Close(); ferr == nil && cerr != nil {
		ferr = refuse(KindDestinationNotWritable, "close fetched object: %v", cerr)
	}
	if ferr != nil {
		return nil, plan, ferr
	}
	fmt.Fprintf(progress, "fetched %d bytes sha256 %s\n", got.Size, got.SHA256)
	inv, err := p.PlaceV2(ctx, plan, root, staging, fetchedRel)
	if err != nil {
		return nil, plan, err
	}
	if err := root.Remove(fetchedRel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, plan, refuse(KindDestinationNotWritable, "remove fetched object: %v", err)
	}
	if material != nil && len(material.Proofs) > 0 {
		if err := ensureDir(root, filepath.Join(staging, ".proofs"), 0o700); err != nil {
			return nil, plan, err
		}
		for _, pr := range material.Proofs {
			if err := writeFileExcl(root, filepath.Join(staging, ".proofs", pr.Role), pr.Bytes, 0o600); err != nil {
				return nil, plan, err
			}
		}
	}
	policy := PackagePolicyV2{
		ID:               plan.Selection.PackagePolicyID,
		RequiredSubjects: plan.Selection.RequiredSubjects,
		Layout:           plan.Selection.Layout,
		Verification:     plan.Selection.Verification,
	}
	access := stagingAccess{root: root, prefix: staging}
	if err := p.VerifyPayload(ctx, access, inv, policy); err != nil {
		return nil, plan, err
	}
	if err := applyFinalModes(root, staging, plan.Selection.Layout); err != nil {
		return nil, plan, err
	}
	scratch := filepath.Join(staging, ".probe")
	if err := root.Mkdir(scratch, 0o700); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "create probe scratch: %v", err)
	}
	exeAbs := filepath.Join(req.DestRoot, staging, filepath.FromSlash(plan.Selection.Layout.EntryPoint))
	fmt.Fprintf(progress, "probing the staged executable: %s --version\n", exeAbs)
	probe, err := p.Probe(ctx, exeAbs, filepath.Join(req.DestRoot, scratch), plan.Selection.Version)
	if err != nil {
		return nil, plan, err
	}
	fmt.Fprintf(progress, "probe reported %q in %d ms\n", firstLine(probe.Output), probe.DurationMS)
	if err := root.RemoveAll(scratch); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "remove probe scratch: %v", err)
	}
	if err := root.RemoveAll(filepath.Join(staging, ".proofs")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, plan, refuse(KindDestinationNotWritable, "remove proofs: %v", err)
	}
	rec := &ReceiptV2{
		Schema:           ReceiptSchemaV2,
		Driver:           plan.Selection.Driver,
		Version:          plan.Selection.Version,
		VendorPlatform:   plan.Selection.VendorPlatform,
		Platform:         plan.Selection.Platform,
		Destination:      plan.Selection.Destination,
		PlanDigest:       plan.Digest,
		PackagePolicyID:  plan.Selection.PackagePolicyID,
		VerificationKind: plan.Selection.Verification.Kind,
		FetchedObject:    got,
		Payload:          *inv,
		Probe:            probe,
		AuthObservation:  AuthObservation{State: AuthStateAbsent, Kind: AuthKindLoginFilePresence, Note: authNote()},
		InstalledAt:      e.now(),
		InstallerVersion: e.installerVersion,
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
			placed = true
			return nil, plan, err
		}
	}
	if err := root.Chmod(staging, releaseDirMode); err != nil {
		return nil, plan, refuse(KindDestinationNotWritable, "set release directory mode: %v", err)
	}
	if err := root.Rename(staging, relRelease); err != nil {
		if _, lerr := root.Lstat(relRelease); lerr == nil {
			return nil, plan, refuse(KindConflict, "%s appeared while this install was staging; the staged copy is discarded and the existing directory is left as found", plan.Selection.Destination.ReleaseDir)
		}
		return nil, plan, refuse(KindDestinationNotWritable, "place release at %s: %v", plan.Selection.Destination.ReleaseDir, err)
	}
	placed = true
	if err := syncDir(root, plan.Selection.Driver); err != nil {
		return rec, plan, err
	}
	fmt.Fprintf(progress, "placed %s\n", plan.Selection.Destination.Executable)
	return rec, plan, nil
}

func (e *Engine) revalidateV2(ctx context.Context, root *os.Root, p PackageProviderV2, plan *PlanV2, relRelease string) (*ReceiptV2, error) {
	rec, err := readReceiptV2(root, filepath.Join(relRelease, ReceiptFile), plan.Selection.Destination.ReleaseDir)
	if err != nil {
		return nil, refuse(KindDamaged, "%s exists but its receipt is not usable (%v); it is left untouched and no repair is attempted", plan.Selection.Destination.ReleaseDir, err)
	}
	if rec.Driver != plan.Selection.Driver || rec.Version != plan.Selection.Version || rec.VendorPlatform != plan.Selection.VendorPlatform {
		return nil, refuse(KindDamaged, "%s records a different driver, version or platform than the plan", plan.Selection.Destination.ReleaseDir)
	}
	if rec.PlanDigest != plan.Digest {
		return nil, refuse(KindDamaged, "%s receipt plan digest %s does not match the current plan %s", plan.Selection.Destination.ReleaseDir, short(rec.PlanDigest), short(plan.Digest))
	}
	entry := filepath.Join(relRelease, filepath.FromSlash(plan.Selection.Layout.EntryPoint))
	sum, size, err := fileSHA256Root(root, entry)
	if err != nil {
		return nil, refuse(KindDamaged, "%s: executable cannot be read (%v)", plan.Selection.Destination.ReleaseDir, err)
	}
	want, _, herr := memberHash(&rec.Payload, plan.Selection.Layout.EntryPoint)
	if herr != nil || want != sum {
		return nil, refuse(KindDamaged, "%s executable sha256 %s does not match the receipt inventory", plan.Selection.Destination.Executable, sum)
	}
	if rec.VerificationKind == VerificationNoneOriginOnly && rec.FetchedObject.SHA256 != sum {
		return nil, refuse(KindDamaged, "%s origin-only receipt fetched-object digest %s does not match the executable %s", plan.Selection.Destination.ReleaseDir, rec.FetchedObject.SHA256, sum)
	}
	_ = size
	_ = p
	_ = ctx
	return rec, nil
}

type stagingAccess struct {
	root   *os.Root
	prefix string
}

func (s stagingAccess) Lstat(rel string) (fs.FileInfo, error) {
	return s.root.Lstat(filepath.Join(s.prefix, filepath.FromSlash(rel)))
}

func (s stagingAccess) OpenFile(rel string) (*os.File, error) {
	return s.root.Open(filepath.Join(s.prefix, filepath.FromSlash(rel)))
}

// LatestInstalled returns the newest StateInstalled release for driver under
// root. It is used to pin a session runtime to a managed install. It does not
// execute anything.
func (e *Engine) LatestInstalled(ctx context.Context, root, driver string) (Installed, bool, error) {
	inv, err := e.List(ctx, root)
	if err != nil {
		return Installed{}, false, err
	}
	var best *Installed
	for i := range inv.Installed {
		in := &inv.Installed[i]
		if in.Driver != driver || in.State != StateInstalled {
			continue
		}
		if best == nil || in.InstalledAt.After(best.InstalledAt) || (in.InstalledAt.Equal(best.InstalledAt) && in.Version > best.Version) {
			best = in
		}
	}
	if best == nil {
		return Installed{}, false, nil
	}
	return *best, true, nil
}
