// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A bundle carries the files the database does not hold: the tools' sign-ins and
// homes under the data directory, and the registered folders. Each directory is one
// "tree": a tar of it, sealed under the KEK in the chunked format of the store
// snapshot (snapshotenc.go), stored as files/<kind>-<name>.tar.enc. The manifest
// lists every regular file of every tree by path, size and SHA-256, and the keyed
// inventory binds the sealed member, so an altered, dropped or swapped tree is
// refused before a restore writes anything.
const (
	// TreeKindData is a subtree of the data directory; it returns to the data
	// directory of the restore under the same name.
	TreeKindData = "data"
	// TreeKindFolder is a registered folder; it returns to its registered path, or
	// under --files-root on another host.
	TreeKindFolder = "folder"

	treeMemberDir = "files/"
	treeMemberExt = ".tar.enc"
)

var treeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// TreeSource names a directory to carry. Name is a token: the subtree's directory
// name for TreeKindData, the folder's registry reference for TreeKindFolder.
type TreeSource struct {
	Kind string
	Name string
	Path string
}

// NotIncluded records a directory the backup left out and why, so a person reading
// the manifest never has to guess whether a folder was forgotten.
type NotIncluded struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Tree describes one sealed tree of a bundle.
type Tree struct {
	// ID is Kind + "-" + Name.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Path is where the directory lived on the source host.
	Path string `json:"path"`
	// Member is the sealed tar member in the bundle.
	Member string `json:"member"`
	// SizeBytes and SHA256 describe the plaintext tar the member opens to.
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	// Files lists every regular file of the tree.
	Files []TreeFile `json:"files"`
}

// TreeFile is one regular file of a tree.
type TreeFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Mode      uint32 `json:"mode"`
	// Changed marks a file that was written to while it was being copied; the
	// bytes in the bundle are what the copy read, and a later backup with the
	// writers paused is the clean one.
	Changed bool `json:"changed_during_backup,omitempty"`
}

// ChangedFiles counts the files copied while they were being written to.
func (t Tree) ChangedFiles() int {
	n := 0
	for _, f := range t.Files {
		if f.Changed {
			n++
		}
	}
	return n
}

func treeID(kind, name string) string { return kind + "-" + name }

// validate checks the shape a reader trusts before it joins any path.
func (t Tree) validate() error {
	if t.Kind != TreeKindData && t.Kind != TreeKindFolder {
		return fmt.Errorf("dr: tree %q has unknown kind %q", t.ID, t.Kind)
	}
	if !treeNamePattern.MatchString(t.Name) || t.ID != treeID(t.Kind, t.Name) {
		return fmt.Errorf("dr: tree %q has an unsafe name", t.ID)
	}
	if t.Member != treeMemberDir+t.ID+treeMemberExt {
		return fmt.Errorf("dr: tree %q names the member %q", t.ID, t.Member)
	}
	if !filepath.IsAbs(t.Path) || filepath.Clean(t.Path) != t.Path {
		return fmt.Errorf("dr: tree %q has a path that is not absolute and clean: %q", t.ID, t.Path)
	}
	return nil
}

// validateTrees checks every tree of a manifest and that no two share an ID.
func validateTrees(trees []Tree) error {
	seen := map[string]bool{}
	for _, t := range trees {
		if err := t.validate(); err != nil {
			return err
		}
		if seen[t.ID] {
			return fmt.Errorf("dr: duplicate tree %q", t.ID)
		}
		seen[t.ID] = true
	}
	return nil
}

// PackedTree is a tree sealed on disk, ready for WriteAuthenticatedBundle. Only
// PackTree makes one, so a bundle cannot carry a tree in the clear.
type PackedTree struct {
	Tree   Tree
	sealed string
}

// Remove deletes the sealed copy.
func (p *PackedTree) Remove() {
	if p != nil && p.sealed != "" {
		_ = os.Remove(p.sealed)
	}
}

// PackTree walks src, seals its tar under c into a new file in scratch and returns
// it. The source is only read. The plaintext never touches the disk: the tar flows
// through a pipe into the sealer.
func PackTree(c *KeyCipher, src TreeSource, scratch string) (*PackedTree, error) {
	if c == nil {
		return nil, errors.New("dr: pack tree requires a KEK")
	}
	t := Tree{
		ID: treeID(src.Kind, src.Name), Kind: src.Kind, Name: src.Name, Path: src.Path,
		Member: treeMemberDir + treeID(src.Kind, src.Name) + treeMemberExt,
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(src.Path)
	if err != nil {
		return nil, fmt.Errorf("dr: pack %s: %w", src.Path, err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("dr: pack %s: not a directory", src.Path)
	}
	out, err := os.CreateTemp(scratch, "tree-*.sealed")
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	type walked struct {
		files []TreeFile
		err   error
	}
	done := make(chan walked, 1)
	go func() {
		files, err := writeTree(pw, root)
		_ = pw.CloseWithError(err)
		done <- walked{files, err}
	}()
	h := sha256.New()
	counted := &countingReader{r: io.TeeReader(pr, h)}
	sealErr := c.sealStream(out, counted, snapshotChunkSize)
	if sealErr != nil {
		_ = pr.CloseWithError(sealErr)
	}
	w := <-done
	closeErr := out.Close()
	for _, err := range []error{w.err, sealErr, closeErr} {
		if err != nil {
			_ = os.Remove(out.Name())
			return nil, fmt.Errorf("dr: pack %s: %w", src.Path, err)
		}
	}
	t.Files, t.SizeBytes, t.SHA256 = w.files, counted.n, hex.EncodeToString(h.Sum(nil))
	return &PackedTree{Tree: t, sealed: out.Name()}, nil
}

// CollectTrees packs every source. A data subtree the host does not have is
// skipped; a registered folder that is gone is listed as not included. Any other
// failure aborts and removes what was packed: a backup that cannot read a file
// says so instead of certifying a copy without it.
func CollectTrees(c *KeyCipher, sources []TreeSource, scratch string) ([]*PackedTree, []NotIncluded, error) {
	var packed []*PackedTree
	var left []NotIncluded
	fail := func(err error) ([]*PackedTree, []NotIncluded, error) {
		for _, p := range packed {
			p.Remove()
		}
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, src := range sources {
		id := treeID(src.Kind, src.Name)
		if seen[id] {
			return fail(fmt.Errorf("dr: duplicate tree source %q", id))
		}
		seen[id] = true
		if _, err := os.Stat(src.Path); errors.Is(err, fs.ErrNotExist) {
			if src.Kind == TreeKindFolder {
				left = append(left, NotIncluded{Kind: src.Kind, Name: src.Name, Path: src.Path, Reason: "the folder does not exist on this host"})
			}
			continue
		}
		p, err := PackTree(c, src, scratch)
		if err != nil {
			return fail(err)
		}
		packed = append(packed, p)
	}
	return packed, left, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// writeTree writes root as a tar to w: directories, regular files and symlinks,
// each with its mode, owner, times and POSIX ACLs. Sockets, devices and pipes hold
// nothing to restore. Everything is opened for reading only.
func writeTree(w io.Writer, root string) ([]TreeFile, error) {
	tw := tar.NewWriter(w)
	var files []TreeFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) && p != root {
				return nil // removed while the walk was running
			}
			return walkErr
		}
		name := "."
		if rel, err := filepath.Rel(root, p); err != nil {
			return err
		} else if rel != "." {
			name = filepath.ToSlash(rel)
		}
		switch {
		case d.IsDir():
			info, err := d.Info()
			if errors.Is(err, fs.ErrNotExist) && p != root {
				return nil
			} else if err != nil {
				return err
			}
			return writeTreeHeader(tw, info, p, name+"/", "", 0)
		case d.Type().IsRegular():
			f, err := packTreeFile(tw, p, name)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
			files = append(files, f)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return writeTreeHeader(tw, info, p, name, link, 0)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return files, nil
}

func writeTreeHeader(tw *tar.Writer, info fs.FileInfo, p, name, link string, size int64) error {
	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	hdr.Name, hdr.Size = name, size
	hdr.Uname, hdr.Gname = "", ""
	hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
	hdr.Format = tar.FormatPAX
	if info.Mode().IsDir() || info.Mode().IsRegular() {
		for k, v := range readACLs(p) {
			if hdr.PAXRecords == nil {
				hdr.PAXRecords = map[string]string{}
			}
			hdr.PAXRecords[k] = v
		}
	}
	return tw.WriteHeader(hdr)
}

// packTreeFile copies one regular file into the tar. The header carries the size
// seen when the file was opened; a file that shrinks while it is read is padded
// with zeros to that size and marked Changed, one that grows is cut at it.
func packTreeFile(tw *tar.Writer, p, name string) (TreeFile, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|openNoFollow, 0)
	if err != nil {
		return TreeFile{}, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return TreeFile{}, err
	}
	size := st.Size()
	if err := writeTreeHeader(tw, st, p, name, "", size); err != nil {
		return TreeFile{}, err
	}
	h := sha256.New()
	dst := io.MultiWriter(tw, h)
	n, err := io.CopyN(dst, f, size)
	changed := false
	if errors.Is(err, io.EOF) {
		changed = true
		_, err = io.CopyN(dst, zeroReader{}, size-n)
	}
	if err != nil {
		return TreeFile{}, fmt.Errorf("read %s: %w", p, err)
	}
	if after, err := f.Stat(); err == nil && (after.Size() != size || !after.ModTime().Equal(st.ModTime())) {
		changed = true
	}
	return TreeFile{
		Path: name, SizeBytes: size, SHA256: hex.EncodeToString(h.Sum(nil)),
		Mode: uint32(st.Mode().Perm()), Changed: changed,
	}, nil
}

// verifyTreeMember authenticates every chunk of a sealed tree and checks the
// plaintext against the digest the keyed manifest vouches for.
func verifyTreeMember(c *KeyCipher, work string, t Tree) error {
	p, err := safeJoin(work, t.Member)
	if err != nil {
		return err
	}
	in, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	h, n := sha256.New(), &countingWriter{}
	if err := c.openStream(io.MultiWriter(h, n), in); err != nil {
		return fmt.Errorf("DR payload %s: %w", t.Member, err)
	}
	if n.n != t.SizeBytes || hex.EncodeToString(h.Sum(nil)) != t.SHA256 {
		return fmt.Errorf("DR payload digest mismatch after opening %s (corrupt or altered export)", t.Member)
	}
	return nil
}

// TreeTarget is where one tree will be restored.
type TreeTarget struct {
	Tree Tree
	Dest string
}

// PlanTreeRestore decides where every tree of m goes and refuses a destination
// that already holds files, before a restore writes anything. A data tree goes to
// dataDir/<name>. A folder goes to its registered path, or to filesRoot/<id> when
// filesRoot is set (another host). force allows writing into a non-empty place;
// files of the same name are replaced and nothing else is removed. replaceData
// lifts the check for data trees only: the caller moves the live ones aside first
// (an in-place restore).
func PlanTreeRestore(m *Manifest, dataDir, filesRoot string, force, replaceData bool) ([]TreeTarget, error) {
	var out []TreeTarget
	for _, t := range m.Trees {
		if err := t.validate(); err != nil {
			return nil, err
		}
		dest := filepath.Join(dataDir, t.Name)
		if t.Kind == TreeKindFolder {
			dest = t.Path
			if filesRoot != "" {
				dest = filepath.Join(filesRoot, t.ID)
			}
		}
		if !(replaceData && t.Kind == TreeKindData) {
			if err := checkTreeDestination(dest, force); err != nil {
				return nil, fmt.Errorf("%s: %w", t.ID, err)
			}
		}
		out = append(out, TreeTarget{Tree: t, Dest: dest})
	}
	return out, nil
}

func checkTreeDestination(dest string, force bool) error {
	entries, err := os.ReadDir(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("%s is not a readable directory: %w", dest, err)
	case len(entries) > 0 && !force:
		return fmt.Errorf("%s already holds files; restore them elsewhere with --files-root, or pass --force to replace files of the same name", dest)
	}
	return nil
}

// PreserveDataTrees renames every existing data-tree destination to dest+suffix, so
// a restore that replaces the live state keeps the old one beside it, as it does the
// store and the keys.
func PreserveDataTrees(targets []TreeTarget, suffix string) error {
	for _, tg := range targets {
		if tg.Tree.Kind != TreeKindData {
			continue
		}
		if _, err := os.Lstat(tg.Dest); err != nil {
			continue
		}
		if err := os.Rename(tg.Dest, tg.Dest+suffix); err != nil {
			return fmt.Errorf("preserve %s before restoring its files: %w", tg.Dest, err)
		}
	}
	return nil
}

// TreeRestoreReport counts what RestoreTrees wrote.
type TreeRestoreReport struct {
	Files int
	Bytes int64
	// OwnerSkipped and ACLSkipped count entries whose owner or ACL could not be
	// restored (the restoring user is not root, or the filesystem has no ACLs).
	OwnerSkipped int
	ACLSkipped   int
}

// RestoreTrees opens every sealed tree in work and writes it under its target. It
// re-checks each file against the manifest's digest as it lands and fails on a
// file the manifest does not list or one it lists and the tar lacks.
func RestoreTrees(c *KeyCipher, work string, targets []TreeTarget) (TreeRestoreReport, error) {
	var rep TreeRestoreReport
	for _, tg := range targets {
		if err := restoreTree(c, work, tg, &rep); err != nil {
			return rep, fmt.Errorf("restore %s into %s: %w", tg.Tree.ID, tg.Dest, err)
		}
	}
	return rep, nil
}

type dirMeta struct {
	path string
	hdr  *tar.Header
}

func restoreTree(c *KeyCipher, work string, tg TreeTarget, rep *TreeRestoreReport) error {
	member, err := safeJoin(work, tg.Tree.Member)
	if err != nil {
		return err
	}
	in, err := os.Open(member)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = in.Close() }()
		_ = pw.CloseWithError(c.openStream(pw, in))
	}()
	defer func() { _ = pr.Close() }()

	h, n := sha256.New(), &countingWriter{}
	stream := io.TeeReader(pr, io.MultiWriter(h, n))
	tr := tar.NewReader(stream)
	want := make(map[string]TreeFile, len(tg.Tree.Files))
	for _, f := range tg.Tree.Files {
		want[f.Path] = f
	}
	if err := os.MkdirAll(tg.Dest, 0o700); err != nil {
		return err
	}
	var dirs []dirMeta
	seen := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		target := tg.Dest
		if name != "." {
			if target, err = safeJoin(tg.Dest, name); err != nil {
				return err
			}
			if err := ensureNoSymlinkParents(tg.Dest, target); err != nil {
				return err
			}
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirMeta{target, hdr})
		case tar.TypeReg:
			f, ok := want[name]
			if !ok {
				return fmt.Errorf("the tar holds %q, which the manifest does not list", name)
			}
			if err := restoreTreeFile(tr, target, hdr, f, rep); err != nil {
				return err
			}
			seen++
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if _, err := os.Lstat(target); err == nil {
				if err := os.Remove(target); err != nil {
					return err
				}
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			applyTreeMeta(target, hdr, true, rep)
		default:
			return fmt.Errorf("entry %q has unsupported type %q", hdr.Name, string(hdr.Typeflag))
		}
	}
	if _, err := io.Copy(io.Discard, stream); err != nil {
		return err
	}
	if seen != len(want) {
		return fmt.Errorf("the manifest lists %d files and the tar holds %d", len(want), seen)
	}
	if n.n != tg.Tree.SizeBytes || hex.EncodeToString(h.Sum(nil)) != tg.Tree.SHA256 {
		return errors.New("the opened tree does not match its manifest digest")
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		applyTreeMeta(dirs[i].path, dirs[i].hdr, false, rep)
	}
	return nil
}

func restoreTreeFile(tr io.Reader, target string, hdr *tar.Header, want TreeFile, rep *TreeRestoreReport) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory and the backup holds a file there", target)
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), tr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != want.SizeBytes || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%s does not match its manifest digest", want.Path)
	}
	applyTreeMeta(target, hdr, false, rep)
	rep.Files++
	rep.Bytes += n
	return nil
}

// applyTreeMeta sets owner, mode, ACLs and times. Setuid and setgid bits are never
// restored. An owner or ACL the restoring user cannot set is counted, not fatal.
func applyTreeMeta(target string, hdr *tar.Header, link bool, rep *TreeRestoreReport) {
	if os.Geteuid() == 0 {
		if err := os.Lchown(target, hdr.Uid, hdr.Gid); err != nil {
			rep.OwnerSkipped++
		}
	} else if hdr.Uid != os.Geteuid() || hdr.Gid != os.Getegid() {
		rep.OwnerSkipped++
	}
	if link {
		return
	}
	_ = os.Chmod(target, fs.FileMode(hdr.Mode).Perm())
	for k, v := range hdr.PAXRecords {
		if attr, ok := strings.CutPrefix(k, aclRecordPrefix); ok {
			if err := writeACL(target, attr, []byte(v)); err != nil {
				rep.ACLSkipped++
			}
		}
	}
	_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
}

// ensureNoSymlinkParents refuses to write through a symlink that an earlier entry
// planted inside dest.
func ensureNoSymlinkParents(dest, target string) error {
	rel, err := filepath.Rel(dest, filepath.Dir(target))
	if err != nil || rel == "." {
		return err
	}
	cur := dest
	for _, part := range strings.Split(filepath.ToSlash(path.Clean(filepath.ToSlash(rel))), "/") {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a plain directory; the entry would be written through it", cur)
		}
	}
	return nil
}
