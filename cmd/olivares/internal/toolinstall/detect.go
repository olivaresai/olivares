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
	"sort"
	"strings"
	"time"
)

// Installed is one release directory under the root and how it reads today.
// Provenance is present only when the retained manifest signature was
// re-verified under the pinned key in this run; the receipt's own provenance
// claim is never republished unverified.
type Installed struct {
	Driver           string      `json:"driver"`
	Version          string      `json:"version"`
	VendorPlatform   string      `json:"vendor_platform"`
	Platform         Platform    `json:"platform"`
	ReleaseDir       string      `json:"release_dir"`
	Executable       string      `json:"executable"`
	IsExecutable     bool        `json:"is_executable"`
	State            string      `json:"state"`
	Reason           string      `json:"reason,omitempty"`
	SHA256           string      `json:"sha256,omitempty"`
	Size             int64       `json:"size,omitempty"`
	InstalledAt      time.Time   `json:"installed_at,omitempty"`
	InstallerVersion string      `json:"installer_version,omitempty"`
	PlanDigest       string      `json:"plan_digest,omitempty"`
	ReceiptPath      string      `json:"receipt_path,omitempty"`
	Provenance       *Provenance `json:"provenance,omitempty"`
}

// Release states. installed means receipt, bytes AND the retained signed
// manifest agree, re-verified now under the pin. unverified means bytes and
// receipt agree but the retained proof could not be re-checked (no verifier);
// it is reported distinctly and never treated as registered. damaged means a
// proven contradiction.
const (
	StateInstalled  = "installed"
	StateDamaged    = "damaged"
	StateUnverified = "unverified"
)

// Leftover is a staging directory some earlier operation did not finish. It is
// reported with whatever its marker says and is never removed by list.
type Leftover struct {
	Path   string          `json:"path"`
	Marker json.RawMessage `json:"marker,omitempty"`
	Note   string          `json:"note"`
}

// Inventory is what list reports for one root.
type Inventory struct {
	Root       string      `json:"root"`
	RootExists bool        `json:"root_exists"`
	Installed  []Installed `json:"installed"`
	Leftovers  []Leftover  `json:"leftovers"`
	Unexpected []string    `json:"unexpected"`
}

const stagingMarkerCap = 4096

// List reads every release directory under root, re-hashes its executable
// against its receipt and re-verifies its retained signed manifest under the
// pinned key, exactly as install's no-op path does. It writes nothing.
func (e *Engine) List(ctx context.Context, rootPath string) (*Inventory, error) {
	inv := &Inventory{Root: rootPath, Installed: []Installed{}, Leftovers: []Leftover{}, Unexpected: []string{}}
	if !filepath.IsAbs(rootPath) {
		return nil, refuse(KindInvalidRequest, "root %q must be absolute", rootPath)
	}
	if _, err := os.Lstat(rootPath); errors.Is(err, fs.ErrNotExist) {
		return inv, nil
	}
	root, err := openRoot(rootPath, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	inv.RootExists = true
	drivers, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rootPath, err)
	}
	for _, d := range drivers {
		if d.Name() == LockFile {
			continue
		}
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			inv.Unexpected = append(inv.Unexpected, filepath.Join(rootPath, d.Name()))
			continue
		}
		releases, err := fs.ReadDir(root.FS(), d.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Join(rootPath, d.Name()), err)
		}
		for _, r := range releases {
			rel := filepath.Join(d.Name(), r.Name())
			abs := filepath.Join(rootPath, rel)
			switch {
			case strings.HasPrefix(r.Name(), stagingPrefix) && r.IsDir():
				lo := Leftover{Path: abs, Note: "unfinished staging directory from an interrupted install; not removed automatically, inspect and remove it by hand"}
				if b, err := readSmallFile(root, filepath.Join(rel, stagingMarker), stagingMarkerCap); err == nil && json.Valid(b) {
					lo.Marker = json.RawMessage(b)
				}
				inv.Leftovers = append(inv.Leftovers, lo)
			case r.IsDir() && !strings.HasPrefix(r.Name(), "."):
				inv.Installed = append(inv.Installed, e.inspectRelease(ctx, root, rel, abs, d.Name()))
			default:
				inv.Unexpected = append(inv.Unexpected, abs)
			}
		}
	}
	sort.Slice(inv.Installed, func(i, j int) bool { return inv.Installed[i].ReleaseDir < inv.Installed[j].ReleaseDir })
	return inv, nil
}

// smallFileFS is the I/O surface readSmallContents needs. Production wraps
// *os.Root so containment stays with os.Root; tests supply a reader that can
// disagree with Lstat without racing a writer or swapping a global hook.
type smallFileFS interface {
	Lstat(name string) (os.FileInfo, error)
	Open(name string) (io.ReadCloser, error)
}

type osSmallFiles struct{ root *os.Root }

func (o osSmallFiles) Lstat(name string) (os.FileInfo, error) { return o.root.Lstat(name) }

func (o osSmallFiles) Open(name string) (io.ReadCloser, error) { return o.root.Open(name) }

// readSmallFile reads a regular, non-symlink file of at most limit bytes. The
// bound is applied by the reader itself, not by a length check after a whole
// read, so a grown file costs at most limit+1 bytes.
func readSmallFile(root *os.Root, rel string, limit int64) ([]byte, error) {
	return readSmallContents(osSmallFiles{root}, rel, limit)
}

func readSmallContents(files smallFileFS, rel string, limit int64) ([]byte, error) {
	fi, err := files.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", rel, limit)
	}
	f, err := files.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s grew past %d bytes while being read", rel, limit)
	}
	return b, nil
}

func (e *Engine) inspectRelease(ctx context.Context, root *os.Root, rel, abs, driver string) Installed {
	in := Installed{Driver: driver, ReleaseDir: abs, State: StateDamaged}
	rec, err := readReceipt(root, filepath.Join(rel, ReceiptFile), abs)
	if err != nil {
		in.Reason = "receipt: " + err.Error()
		return in
	}
	in.Version, in.VendorPlatform, in.Platform = rec.Version, rec.VendorPlatform, rec.Platform
	in.Executable, in.InstalledAt = rec.Destination.Executable, rec.InstalledAt
	in.InstallerVersion, in.PlanDigest, in.ReceiptPath = rec.InstallerVersion, rec.PlanDigest, filepath.Join(abs, ReceiptFile)
	if rec.Driver != driver {
		in.Reason = fmt.Sprintf("receipt records driver %q under the %q directory", rec.Driver, driver)
		return in
	}
	exeRel := filepath.Join(rel, "bin", rec.Artifact.Name)
	fi, err := root.Lstat(exeRel)
	if err != nil {
		in.Reason = "executable: " + err.Error()
		return in
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		in.Reason = "executable is not a regular file"
		return in
	}
	in.IsExecutable = fi.Mode().Perm()&0o111 != 0
	sum, size, err := fileSHA256Root(root, exeRel)
	if err != nil {
		in.Reason = "executable: " + err.Error()
		return in
	}
	in.SHA256, in.Size = sum, size
	if sum != rec.Artifact.SHA256 || size != rec.Artifact.Size {
		in.Reason = fmt.Sprintf("executable holds sha256 %s (%d bytes); the receipt records %s (%d bytes)", sum, size, rec.Artifact.SHA256, rec.Artifact.Size)
		return in
	}
	for _, name := range []string{rec.Retained.Manifest, rec.Retained.Signature, rec.Retained.Key} {
		if name == "" || name != filepath.Base(name) {
			in.Reason = fmt.Sprintf("receipt names an invalid retained file %q", name)
			return in
		}
		if rfi, err := root.Lstat(filepath.Join(rel, name)); err != nil || rfi.Mode()&os.ModeSymlink != 0 || !rfi.Mode().IsRegular() {
			in.Reason = fmt.Sprintf("retained %s is missing or not a regular file", name)
			return in
		}
	}
	manifest, err := readSmallFile(root, filepath.Join(rel, rec.Retained.Manifest), claudeManifestCap)
	if err != nil {
		in.Reason = "retained manifest: " + err.Error()
		return in
	}
	signature, err := readSmallFile(root, filepath.Join(rel, rec.Retained.Signature), claudeSignatureCap)
	if err != nil {
		in.Reason = "retained signature: " + err.Error()
		return in
	}
	p, err := e.catalog.Lookup(driver)
	if err != nil {
		in.State = StateUnverified
		in.Reason = fmt.Sprintf("no installer is registered for %q, so its retained signature was not re-checked", driver)
		return in
	}
	vm, err := p.VerifyMaterial(ctx, &Material{Manifest: manifest, Signature: signature})
	if err != nil {
		if KindOf(err) == KindVerificationUnavailable {
			in.State = StateUnverified
			in.Reason = "retained signature not re-checked: " + err.Error()
			return in
		}
		in.Reason = "retained manifest does not verify under the pinned key: " + err.Error()
		return in
	}
	art, ok := vm.Artifacts[rec.VendorPlatform]
	if vm.Version != rec.Version || !ok || art.SHA256 != sum || art.Size != size {
		in.Reason = "retained signed manifest names different material than the installed bytes"
		return in
	}
	prov := vm.Provenance
	in.Provenance = &prov
	in.State = StateInstalled
	return in
}

// DetectOptions scopes detection to the caller's own paths. Nothing outside
// Root, Home's vendor default locations, the system prefixes, PathEnv and the
// explicitly named ProbePaths is inspected. Nothing is executed unless asked:
// Probe runs only registered or manifest-corroborated candidates; an
// unregistered path runs only when its exact path is named in ProbePaths. A
// damaged candidate is never executed, named or not.
type DetectOptions struct {
	Driver string
	Root   string
	// Home is the current account's home; empty skips vendor default paths.
	Home string
	// PathEnv is a PATH-style list to search; empty skips it. Relative entries
	// are ignored so nothing resolves against the working directory.
	PathEnv string
	Probe   bool
	// ProbePaths are exact absolute paths the operator selected for execution.
	ProbePaths []string
	// Material, when set, is verified under the pinned key and used to mark a
	// candidate whose bytes the signed manifest names as manifest-corroborated.
	Material *Material
}

const (
	MatchRegistered           = "registered"
	MatchManifestCorroborated = "manifest-corroborated"
	MatchUnregisteredObserved = "unregistered-observed"
	MatchUnverified           = "unverified"
	MatchDamaged              = "damaged"
)

// Candidate is one executable detection found and what could be said about it.
type Candidate struct {
	Driver    string `json:"driver"`
	Path      string `json:"path"`
	Resolved  string `json:"resolved"`
	IsSymlink bool   `json:"is_symlink"`
	// Aliases are other paths (symlinks) that resolve to the same file.
	Aliases        []string     `json:"aliases,omitempty"`
	Origin         string       `json:"origin"`
	Match          string       `json:"match"`
	Version        string       `json:"version,omitempty"`
	VendorPlatform string       `json:"vendor_platform,omitempty"`
	SHA256         string       `json:"sha256,omitempty"`
	Size           int64        `json:"size,omitempty"`
	Executable     bool         `json:"executable"`
	Probe          *ProbeReport `json:"probe,omitempty"`
	ProbeError     string       `json:"probe_error,omitempty"`
	// ProbeSkipped says why a candidate was not executed in this run.
	ProbeSkipped string `json:"probe_skipped,omitempty"`
	Note         string `json:"note,omitempty"`
}

// Detect enumerates candidate executables for the driver. Observed facts (path,
// bytes) are always reported; a version is reported only from a receipt, from a
// verified manifest, or from an explicit probe. When a probe that was requested
// (by Probe eligibility or by name) was refused or failed, the candidates are
// still returned together with a probe_failed refusal, so the caller can show
// them and still exit non-zero.
func (e *Engine) Detect(ctx context.Context, opts DetectOptions) ([]Candidate, error) {
	p, err := e.catalog.Lookup(opts.Driver)
	if err != nil {
		return nil, err
	}
	var vm *VerifiedManifest
	if opts.Material != nil {
		if vm, err = p.VerifyMaterial(ctx, opts.Material); err != nil {
			return nil, err
		}
	}
	named := map[string]bool{}
	for _, raw := range opts.ProbePaths {
		path := filepath.Clean(strings.TrimSpace(raw))
		if !filepath.IsAbs(path) {
			return nil, refuse(KindInvalidRequest, "probe path %q must be absolute", raw)
		}
		named[path] = true
	}
	seen := map[string]int{}
	var out []Candidate
	// One entry per resolved file: the real path is the candidate and every
	// symlink pointing at it is an alias, whichever was found first.
	add := func(c Candidate) {
		if c.Resolved == "" {
			out = append(out, c)
			return
		}
		if i, dup := seen[c.Resolved]; dup {
			prev := &out[i]
			if prev.IsSymlink && !c.IsSymlink {
				prev.Aliases = appendAlias(prev.Aliases, prev.Path, c.Path)
				prev.Path, prev.IsSymlink, prev.Origin = c.Path, false, c.Origin
			} else {
				prev.Aliases = appendAlias(prev.Aliases, c.Path, prev.Path)
			}
			return
		}
		seen[c.Resolved] = len(out)
		out = append(out, c)
	}
	if opts.Root != "" {
		inv, err := e.List(ctx, opts.Root)
		if err != nil {
			return nil, err
		}
		for _, in := range inv.Installed {
			if in.Driver != opts.Driver {
				continue
			}
			c := Candidate{Driver: opts.Driver, Path: in.Executable, Resolved: in.Executable, Origin: "managed",
				Version: in.Version, VendorPlatform: in.VendorPlatform, SHA256: in.SHA256, Size: in.Size, Executable: in.IsExecutable}
			switch in.State {
			case StateInstalled:
				c.Match = MatchRegistered
			case StateUnverified:
				c.Match, c.Note = MatchUnverified, in.Reason
			default:
				c.Match, c.Note = MatchDamaged, in.Reason
			}
			add(c)
		}
	}
	for _, path := range p.DefaultPaths(opts.Home) {
		if c, ok := observe(opts.Driver, path, "vendor-default"); ok {
			add(c)
		}
	}
	if opts.PathEnv != "" {
		for _, dir := range filepath.SplitList(opts.PathEnv) {
			if !filepath.IsAbs(dir) {
				continue
			}
			if c, ok := observe(opts.Driver, filepath.Join(dir, opts.Driver), "path"); ok {
				add(c)
			}
		}
	}
	isNamed := func(c *Candidate) bool {
		if named[c.Path] || named[c.Resolved] {
			return true
		}
		for _, a := range c.Aliases {
			if named[a] {
				return true
			}
		}
		return false
	}
	for path := range named {
		known := false
		for i := range out {
			if isNamed(&out[i]) && (out[i].Path == path || out[i].Resolved == path || containsString(out[i].Aliases, path)) {
				known = true
				break
			}
		}
		if known {
			continue
		}
		c, ok := observe(opts.Driver, path, "named")
		if !ok {
			c = Candidate{Driver: opts.Driver, Path: path, Origin: "named", Match: MatchUnregisteredObserved, Note: "no such file"}
		}
		add(c)
	}
	failures := 0
	for i := range out {
		c := &out[i]
		if c.Match == MatchUnregisteredObserved && vm != nil {
			for key, art := range vm.Artifacts {
				if c.SHA256 != "" && art.SHA256 == c.SHA256 && art.Size == c.Size {
					c.Match, c.Version, c.VendorPlatform = MatchManifestCorroborated, vm.Version, key
					c.Note = "bytes match the signed manifest verified under the pinned key"
				}
			}
		}
		requested := isNamed(c)
		eligible := opts.Probe && (c.Match == MatchRegistered || c.Match == MatchManifestCorroborated)
		switch {
		case c.Match == MatchDamaged:
			// Proven-wrong bytes are never executed, whatever selected them.
			c.ProbeSkipped = "never executed: this release is damaged (its bytes disagree with its receipt or its retained signed manifest)"
			if requested {
				failures++
			}
			continue
		case !requested && !eligible:
			if opts.Probe || len(named) > 0 {
				c.ProbeSkipped = "not executed: only registered or manifest-corroborated candidates run under --probe; name this exact path with --probe-path to run it"
			}
			continue
		}
		if c.Resolved == "" || c.SHA256 == "" {
			c.ProbeSkipped = "refused: " + firstNonEmpty(c.Note, "the path could not be read")
			failures++
			continue
		}
		if !c.Executable {
			c.ProbeSkipped = "refused: the file is not executable"
			failures++
			continue
		}
		if err := probeSafety(c.Resolved); err != nil {
			c.ProbeSkipped = "refused: " + err.Error()
			failures++
			continue
		}
		scratch, err := os.MkdirTemp("", "olivares-tool-probe-")
		if err != nil {
			c.ProbeError = err.Error()
			failures++
			continue
		}
		rep, perr := p.Probe(ctx, c.Resolved, scratch, "")
		_ = os.RemoveAll(scratch)
		c.Probe = &rep
		if perr != nil {
			c.ProbeError = perr.Error()
			failures++
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if failures > 0 {
		return out, refuse(KindProbeFailed, "%d requested probe(s) were refused or failed; each candidate carries its probe_error or probe_skipped", failures)
	}
	return out, nil
}

// observe records a path without executing it. Symlinks are followed only to
// name the target; the reported bytes are the target's.
func observe(driver, path, origin string) (Candidate, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Candidate{}, false
	}
	c := Candidate{Driver: driver, Path: path, Resolved: path, Origin: origin, Match: MatchUnregisteredObserved}
	if fi.Mode()&os.ModeSymlink != 0 {
		c.IsSymlink = true
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			c.Resolved = ""
			c.Note = "dangling symlink: " + err.Error()
			return c, true
		}
		c.Resolved = target
		if fi, err = os.Stat(target); err != nil {
			c.Note = err.Error()
			return c, true
		}
	}
	if !fi.Mode().IsRegular() {
		c.Note = "not a regular file"
		return c, true
	}
	c.Size = fi.Size()
	c.Executable = fi.Mode().Perm()&0o111 != 0
	if sum, err := fileSHA256(c.Resolved); err == nil {
		c.SHA256 = sum
	} else {
		c.Note = "unreadable: " + err.Error()
	}
	return c, true
}

// appendAlias adds alias unless it is the candidate's own path or already listed:
// the same symlink is commonly reached twice, as a vendor default and via PATH.
func appendAlias(aliases []string, alias, path string) []string {
	if alias == path || containsString(aliases, alias) {
		return aliases
	}
	aliases = append(aliases, alias)
	sort.Strings(aliases)
	return aliases
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
