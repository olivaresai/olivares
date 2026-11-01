// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Cursor CLI. The vendor installs with `curl https://cursor.com/install | bash`: the script
// downloads downloads.cursor.com/lab/<version>/<os>/<arch>/agent-cli-package.tar.gz and
// unpacks it with --strip-components=1 (measured on 2026.10.22-27dcf5f). It publishes no
// checksum and no signature, so this installer never runs the script. It reads the version
// from the script's own download line, builds the package address from the pinned origin,
// and records the digest it observed in the receipt: origin-only, not a publisher
// signature. Redirects are refused.
const (
	CursorScriptURL   = "https://cursor.com/install"
	CursorPackageBase = "https://downloads.cursor.com/lab"

	LayoutIDCursorPackageV1 = "cursor-package-v1"

	// The vendor tree lives in lib/<program> so the confined session's shared lib grant
	// (confine.programLibrary: lib/<program> beside bin) covers it; any other name is unreadable.
	cursorLibDir     = "lib/agent"
	cursorLauncher   = cursorLibDir + "/cursor-agent"
	cursorWrapper    = "bin/agent"
	cursorMaxArchive = int64(384 << 20)
	cursorScriptCap  = 64 << 10
)

// cursorWrapperScript is the one file this installer writes: the vendor launcher locates
// its node and bundle beside itself, so the entry point only has to reach it.
const cursorWrapperScript = "#!/bin/sh\nexec \"$(dirname \"$0\")/../" + cursorLauncher + "\" \"$@\"\n"

var (
	// The version is a plain token between the pinned prefix and the OS/ARCH placeholders;
	// a path, query or another host does not match.
	cursorDownloadRe = regexp.MustCompile(`(?m)^[ \t]*DOWNLOAD_URL="https://downloads\.cursor\.com/lab/([0-9A-Za-z][0-9A-Za-z._-]{0,63})/\$\{OS\}/\$\{ARCH\}/agent-cli-package\.tar\.gz"[ \t]*$`)
	cursorVersionRe  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-[0-9a-f]{7,40}$`)
)

// CursorOptions configures the adapter. Zero values mean the official script and package
// origin, a default HTTP client and the default probe budget.
type CursorOptions struct {
	ScriptURL, PackageBase string
	Client                 *http.Client
	ProbeBudget            time.Duration
	ProbeGrace             time.Duration
}

// Cursor installs the Cursor CLI from the vendor's own origin into an Olivares-owned
// versioned directory. It does not vendor the CLI.
type Cursor struct {
	script, pkgBase string
	fetch           fetcher
	probeBudget     time.Duration
	probeGrace      time.Duration
}

func NewCursor(opts CursorOptions) *Cursor {
	c := &Cursor{script: CursorScriptURL, pkgBase: CursorPackageBase, probeBudget: opts.ProbeBudget, probeGrace: opts.ProbeGrace}
	if opts.ScriptURL != "" {
		c.script = opts.ScriptURL
	}
	if opts.PackageBase != "" {
		c.pkgBase = strings.TrimRight(opts.PackageBase, "/")
	}
	client := opts.Client
	if client == nil {
		client = NewHTTPClient()
	}
	c.fetch = fetcher{client: client}
	if c.probeBudget <= 0 {
		c.probeBudget = 20 * time.Second
	}
	if c.probeGrace <= 0 {
		c.probeGrace = 3 * time.Second
	}
	return c
}

func (c *Cursor) Key() string               { return DriverCursor }
func (c *Cursor) VerificationLevel() string { return VerificationOriginTarball }

// cursorVendorPath is the OS/arch directory of the vendor's package address.
func cursorVendorPath(vendor string) string { return strings.Replace(vendor, "-", "/", 1) }

func cursorPackageURL(base, version, vendor string) string {
	return base + "/" + version + "/" + cursorVendorPath(vendor) + "/agent-cli-package.tar.gz"
}

func cursorLayout() ExpectedLayout {
	return ExpectedLayout{ID: LayoutIDCursorPackageV1, Variant: DriverCursor, EntryPoint: cursorWrapper, Members: []ExpectedMember{
		{Path: "bin", Kind: MemberKindDirectory, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec},
		{Path: cursorWrapper, Kind: MemberKindRegular, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec},
		{Path: "lib", Kind: MemberKindDirectory, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec},
		{Path: cursorLibDir, Kind: MemberKindDirectory, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec},
		{Path: cursorLauncher, Kind: MemberKindRegular, Role: MemberRoleArchiveOnly, FinalMode: v2ModeExec},
	}, Limits: ExtractionLimits{MaxCompressedBytes: cursorMaxArchive, MaxExpandedBytes: 1536 << 20, MaxMembers: 4096, MaxMemberBytes: 512 << 20}}
}

func validateCursorLayout(l ExpectedLayout) error {
	if !reflect.DeepEqual(l, cursorLayout()) {
		return refuse(KindInvalidRequest, "cursor layout must match its fixed bounded policy")
	}
	return nil
}

// cursorMember is the confinement rule for every installed path: the wrapper and the
// vendor tree under lib/agent, nothing else.
func cursorMember(name string) bool {
	return name == "bin" || name == cursorWrapper || name == "lib" || name == cursorLibDir || strings.HasPrefix(name, cursorLibDir+"/")
}

// validateCursorSelection is the policy of a Cursor plan: an official plan names exactly
// the pinned script and package address, every plan names a package address of the plan's
// own version and platform, and nothing in it claims a checksum, signature or redirect.
func (s SelectionV2) validateCursorSelection() error {
	plat, vendor, err := PlatformV2For(DriverCursor, Platform{OS: s.Platform.OS, Arch: s.Platform.Arch})
	if err != nil {
		return err
	}
	if s.Platform != plat || s.VendorPlatform != vendor {
		return refuse(KindInvalidRequest, "cursor platform does not match its fixed vendor mapping")
	}
	if s.PackagePolicyID != PackagePolicyOriginTarballV1 || s.Verification.Kind != VerificationOriginTarball || s.Verification.Cosign != nil {
		return refuse(KindInvalidRequest, "cursor requires package_policy_id %q and verification.kind %q", PackagePolicyOriginTarballV1, VerificationOriginTarball)
	}
	if s.FetchedObject != (FetchedObjectExpectation{DigestState: DigestStateUnknown, SizeState: SizeStateBounded, MaxSize: cursorMaxArchive}) {
		return refuse(KindInvalidRequest, "cursor fetched_object must be an unknown digest of bounded size")
	}
	if !reflect.DeepEqual(s.Layout, cursorLayout()) {
		return refuse(KindInvalidRequest, "cursor layout must match its fixed bounded policy")
	}
	src := s.Source
	if src.Pointer.State != URLStatePresent || src.Package.State != URLStatePresent || src.Checksums.State != URLStateAbsent || len(src.Proofs) != 0 || len(s.RequiredSubjects) != 0 {
		return refuse(KindInvalidRequest, "cursor names a script and a package and no checksum, proof or signed subject")
	}
	if src.Redirects.MaxHops != 0 {
		return refuse(KindInvalidRequest, "cursor refuses redirects")
	}
	suffix := "/" + s.Version + "/" + cursorVendorPath(vendor) + "/agent-cli-package.tar.gz"
	if !strings.HasSuffix(src.Package.URL, suffix) {
		return refuse(KindInvalidRequest, "cursor package address does not name the plan's version and platform")
	}
	if src.Kind == SourceOfficial && (src.Pointer.URL != CursorScriptURL || src.Package.URL != cursorPackageURL(CursorPackageBase, s.Version, vendor)) {
		return refuse(KindInvalidRequest, "an official cursor plan names the vendor's own script and package address")
	}
	return nil
}

func (c *Cursor) ResolveV2(ctx context.Context, req RequestV2) (*PlanV2, *ResolvedMaterialV2, error) {
	if req.Driver != DriverCursor {
		return nil, nil, refuse(KindInvalidRequest, "cursor adapter received driver %q", req.Driver)
	}
	plat, vendor, err := PlatformV2For(DriverCursor, Platform{OS: req.Platform.OS, Arch: req.Platform.Arch})
	if err != nil {
		return nil, nil, err
	}
	if req.Platform.Libc != "" {
		return nil, nil, refuse(KindUnsupportedPlatform, "cursor platform libc must be empty, got %q", req.Platform.Libc)
	}
	if req.Source != "" {
		return nil, nil, refuse(KindUnsupportedSource, "cursor installs only from the vendor's own origin")
	}
	channel, version := ChannelExact, strings.TrimSpace(req.Version)
	pointer := URLRef{State: URLStatePresent, URL: c.script}
	var pointerBytes []byte
	switch version {
	case ChannelLatest, ChannelStable:
		channel = version
		scriptOrigin, err := canonicalOrigin(c.script)
		if err != nil {
			return nil, nil, err
		}
		// The script is read the way the package is: no redirect to anywhere else.
		body, status, err := c.fetch.confine(RedirectPolicyV2{AllowedOrigins: []string{}}, []string{scriptOrigin}).small(ctx, c.script, cursorScriptCap)
		if err != nil {
			return nil, nil, err
		}
		if err := statusOrTransport(status, c.script, "version"); err != nil {
			return nil, nil, err
		}
		lines := cursorDownloadRe.FindAllSubmatch(body, -1)
		if len(lines) != 1 || !ValidVersion(string(lines[0][1])) {
			return nil, nil, refuse(KindManifestInvalid, "%s names %d package downloads, want exactly one this installer recognizes", c.script, len(lines))
		}
		version, pointerBytes = string(lines[0][1]), body
	default:
		if !ValidVersion(version) {
			return nil, nil, refuse(KindInvalidRequest, "version %q is neither an exact version nor one of latest, stable", req.Version)
		}
	}
	pkgURL := cursorPackageURL(c.pkgBase, version, vendor)
	origins := []string{}
	for _, u := range []string{c.script, pkgURL} {
		origin, err := canonicalOrigin(u)
		if err != nil {
			return nil, nil, err
		}
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	slices.Sort(origins)
	kind := SourceMirror
	if c.script == CursorScriptURL && c.pkgBase == CursorPackageBase {
		kind = SourceOfficial
	}
	sel := SelectionV2{
		Driver: DriverCursor, Channel: channel, RequestedVersion: req.Version, Version: version,
		Platform: plat, VendorPlatform: vendor,
		Source: SourcePolicyV2{
			Kind: kind, Pointer: pointer, Checksums: URLRef{State: URLStateAbsent},
			Package: URLRef{State: URLStatePresent, URL: pkgURL}, Proofs: []ProofLocator{},
			AllowedOrigins: origins, Redirects: RedirectPolicyV2{MaxHops: 0, AllowedOrigins: []string{}},
		},
		FetchedObject:    FetchedObjectExpectation{DigestState: DigestStateUnknown, SizeState: SizeStateBounded, MaxSize: cursorMaxArchive},
		Layout:           cursorLayout(),
		RequiredSubjects: []SubjectExpectation{},
		PackagePolicyID:  PackagePolicyOriginTarballV1,
		Verification:     VerificationProfileV2{Kind: VerificationOriginTarball},
		Destination:      destinationFor(req.DestRoot, DriverCursor, version, vendor, "agent"),
	}
	plan := &PlanV2{Schema: PlanSchemaV2, Selection: sel}
	if err := plan.validate(); err != nil {
		return nil, nil, err
	}
	plan.Digest = ComputeDigestV2(plan)
	return plan, &ResolvedMaterialV2{
		Pointer: resolvedFromURL(pointer, pointerBytes), Checksums: ResolvedObjectV2{State: URLStateAbsent}, Proofs: []ResolvedProofV2{},
	}, nil
}

// routePointer: every route to a Cursor release records the same script as its pointer, so
// an install planned as latest is the same release as one planned by its exact version.
func (c *Cursor) routePointer(sel SelectionV2, _ string) string { return sel.Source.Pointer.URL }

func (c *Cursor) FetchV2(ctx context.Context, plan *PlanV2, w io.Writer) (FetchedObjectObserved, error) {
	if err := plan.validate(); err != nil {
		return FetchedObjectObserved{}, err
	}
	f := c.fetch.confine(plan.Selection.Source.Redirects, plan.Selection.Source.AllowedOrigins)
	return f.bounded(ctx, plan.Selection.Source.Package.URL, plan.Selection.FetchedObject.MaxSize, w)
}

func (c *Cursor) PlaceV2(ctx context.Context, plan *PlanV2, root *os.Root, staging, fetched string) (*ObservedPayloadInventory, error) {
	if err := plan.validate(); err != nil {
		return nil, err
	}
	return extractCursorPackage(ctx, root, staging, fetched, plan.Selection.Layout)
}

func (c *Cursor) VerifyPayload(ctx context.Context, access PayloadAccess, inv *ObservedPayloadInventory, policy PackagePolicyV2) error {
	if policy.ID != PackagePolicyOriginTarballV1 || policy.Verification.Kind != VerificationOriginTarball || policy.Layout.ID != LayoutIDCursorPackageV1 || len(policy.RequiredSubjects) != 0 {
		return refuse(KindInvalidRequest, "cursor verification policy mismatch")
	}
	return verifyReleaseArchiveInventory(ctx, access, inv, DriverCursor, policy.Layout.Limits)
}

// verifyCursorReceipt: with no published checksum the receipt's inventory is the only
// record of what was placed; the tree on disk must still be exactly that.
func verifyCursorReceipt(ctx context.Context, root *os.Root, prefix string, rec *ReceiptV2) error {
	if rec.PackagePolicyID != PackagePolicyOriginTarballV1 || rec.VerificationKind != VerificationOriginTarball {
		return refuse(KindDamaged, "cursor receipt has a different verification policy")
	}
	inv, err := inventoryReleaseArchive(ctx, root, prefix, DriverCursor, cursorLayout().Limits)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(inv.Members, rec.Payload.Members) {
		return refuse(KindDamaged, "cursor files do not match the receipt inventory")
	}
	return nil
}

// extractCursorPackage unpacks the vendor tarball under lib/agent, the way the
// vendor's script does (--strip-components=1), and writes the one wrapper. It accepts
// regular files and directories under one top directory; a link, special file, repeated
// path or path outside that directory refuses the whole package. A file is 0755 when the
// archive marks it executable, else 0644.
func extractCursorPackage(ctx context.Context, root *os.Root, prefix, fetched string, layout ExpectedLayout) (*ObservedPayloadInventory, error) {
	file, err := root.Open(fetched)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(archiveContextReader{ctx: ctx, reader: file})
	if err != nil {
		return nil, refuse(KindManifestInvalid, "invalid gzip archive: %v", err)
	}
	defer gz.Close()
	decoded := &io.LimitedReader{R: gz, N: layout.Limits.MaxExpandedBytes + 1}
	tr := tar.NewReader(archiveContextReader{ctx: ctx, reader: decoded})
	lib := filepath.Join(prefix, filepath.FromSlash(cursorLibDir))
	if err := ensureDirAll(root, lib, 0o755); err != nil {
		return nil, err
	}
	type placed struct {
		relative string
		mode     os.FileMode
	}
	var top string
	var files []placed
	seen := map[string]bool{}
	var expanded int64
	members := 0
	for {
		hdr, err := tr.Next()
		if decoded.N == 0 {
			return nil, refuse(KindResponseTooLarge, "decoded archive exceeds policy")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, refuse(KindManifestInvalid, "read archive: %v", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if members++; members > layout.Limits.MaxMembers {
			return nil, refuse(KindResponseTooLarge, "archive member count exceeds policy")
		}
		name, err := releaseArchivePath(hdr.Name)
		if err != nil {
			return nil, refuse(KindManifestInvalid, "%v", err)
		}
		if name == "" {
			return nil, refuse(KindManifestInvalid, "invalid root archive member")
		}
		first, rest, _ := strings.Cut(name, "/")
		if top == "" {
			top = first
		}
		if first != top {
			return nil, refuse(KindManifestInvalid, "archive member %q is outside the package root %q", name, top)
		}
		if rest == "" {
			if hdr.Typeflag != tar.TypeDir {
				return nil, refuse(KindManifestInvalid, "the package root is not a directory")
			}
			continue
		}
		if seen[rest] {
			return nil, refuse(KindManifestInvalid, "duplicate archive member %q", rest)
		}
		seen[rest] = true
		relative := filepath.Join(lib, filepath.FromSlash(rest))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := ensureDirAll(root, relative, 0o755); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 || hdr.Size > layout.Limits.MaxMemberBytes || expanded > layout.Limits.MaxExpandedBytes-hdr.Size {
				return nil, refuse(KindResponseTooLarge, "archive member or expanded size exceeds policy")
			}
			expanded += hdr.Size
			if err := ensureDirAll(root, filepath.Dir(relative), 0o755); err != nil {
				return nil, err
			}
			target, err := root.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if errors.Is(err, fs.ErrExist) {
				return nil, refuse(KindManifestInvalid, "archive member %q conflicts with another", rest)
			}
			if err != nil {
				return nil, refuse(KindDestinationNotWritable, "create %q: %w", rest, err)
			}
			var writeErr error
			n, copyErr := io.Copy(&cursorWriter{w: target, err: &writeErr}, io.LimitReader(tr, hdr.Size+1))
			syncErr := target.Sync()
			closeErr := target.Close()
			if writeErr != nil {
				return nil, refuse(KindDestinationNotWritable, "write %q: %w", rest, writeErr)
			}
			if copyErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				return nil, refuse(KindManifestInvalid, "read archive member %q: %w", rest, copyErr)
			}
			if n != hdr.Size {
				return nil, refuse(KindSizeMismatch, "archive member %q was incomplete", rest)
			}
			if syncErr != nil {
				return nil, syncErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			files = append(files, placed{relative, mode})
		default:
			return nil, refuse(KindManifestInvalid, "archive member %q is a link or special file", name)
		}
	}
	// Reading on to the end checks the gzip trailer's length and checksum: the tar reader stops
	// at its end marker without it.
	if _, err := io.Copy(io.Discard, decoded); err != nil {
		return nil, refuse(KindManifestInvalid, "read archive: %w", err)
	}
	if decoded.N == 0 {
		return nil, refuse(KindResponseTooLarge, "decoded archive exceeds policy")
	}
	if !seen["cursor-agent"] {
		return nil, refuse(KindManifestInvalid, "the package has no cursor-agent launcher")
	}
	wrapper := filepath.Join(prefix, filepath.FromSlash(cursorWrapper))
	if err := ensureDirAll(root, filepath.Dir(wrapper), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileExcl(root, wrapper, []byte(cursorWrapperScript), 0o755); err != nil {
		return nil, err
	}
	files = append(files, placed{wrapper, 0o755})
	for _, f := range files {
		if err := root.Chmod(f.relative, f.mode); err != nil {
			return nil, err
		}
	}
	inv, err := inventoryReleaseArchive(ctx, root, prefix, DriverCursor, layout.Limits)
	if err != nil {
		return nil, err
	}
	return requireArchiveMembers(inv, layout)
}

// cursorWriter remembers a failed write, so a full disk is not reported as a short archive.
type cursorWriter struct {
	w   io.Writer
	err *error
}

func (c *cursorWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		*c.err = err
	}
	return n, err
}

func (c *Cursor) Probe(ctx context.Context, exe, scratch, want string) (ProbeReport, error) {
	home := filepath.Join(scratch, "home")
	tmp := filepath.Join(scratch, "tmp")
	for _, dir := range []string{home, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return ProbeReport{}, refuse(KindProbeFailed, "prepare probe home: %v", err)
		}
	}
	// The confined probe reads the release's lib/agent through the shared lib/<program> grant.
	report, err := runProbe(ctx, probeSpec{
		Exe: exe, Args: []string{"--version"}, Dir: home, Write: []string{scratch}, Budget: c.probeBudget, Grace: c.probeGrace,
		Env: []string{"HOME=" + home, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin", "TERM=dumb", "LANG=C.UTF-8"},
	})
	if err != nil {
		return report, err
	}
	// `agent` is a common program name, and Cursor's --version prints only its
	// <year>.<month>.<day>-<build> version: anything else is not this tool.
	got := firstLine(report.Output)
	if !cursorVersionRe.MatchString(got) {
		return report, refuse(KindProbeMismatch, "%s --version printed %q, which is not a Cursor CLI version", exe, got)
	}
	if want != "" && got != want {
		return report, refuse(KindProbeMismatch, "%s --version reports %q; the plan says %s", exe, got, want)
	}
	return report, nil
}

// DefaultPaths are the vendor's own link and the usual local prefix; an executable found
// there is only observed, never run, unless it is registered or named.
func (c *Cursor) DefaultPaths(home string) []string {
	paths := []string{"/usr/local/bin/agent"}
	if filepath.IsAbs(home) {
		paths = append(paths, filepath.Join(home, ".local", "bin", "agent"))
	}
	return paths
}

var _ PackageProviderV2 = (*Cursor)(nil)
