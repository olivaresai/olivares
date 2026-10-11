// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
)

// THE COMMITTED TEXT OF A FILE, read without running anything. A folder the agent writes
// can carry repository config that runs commands (fsmonitor, filters, textconv), and the
// engine reads it outside the session's confinement (run_changes.go), so the committed
// side of a diff is read the way git stores it: HEAD, its commit, the trees down to the
// path, and the blob, from loose objects and packs, through os.Root. The reader only
// opens files of the folder's own .git directory; a link that leaves the folder is
// refused by os.Root, and nothing on this path reads the repository's config.
//
// ponytail: SHA-1 repositories whose objects and refs are all in the folder's own .git
// directory. A linked worktree (.git is a file), alternates, a reference clone and a
// SHA-256 repository answer errNoRepository, so the console offers no diff there instead of
// a wrong one. Upgrade trigger: a session worktree users review in the console; then
// resolve the git directory from the workspace the row records, never from the file.

// headRev is the one revision a read can ask for, the value of ?rev=.
const headRev = "HEAD"

const (
	// headMaxObject bounds what one object, or one delta result, may inflate to.
	headMaxObject = 8 << 20
	// headMaxChain bounds a delta chain; git's own default depth is 50.
	headMaxChain = 100
	// headBudget bounds everything one request may inflate or rebuild, all objects and
	// delta levels together. The folder is the agent's, and an object file need not hash to
	// its name, so a tree can list itself: a long path would otherwise walk it, 8 MiB at a
	// time, for as long as the request line allows.
	headBudget = 256 << 20
	// headMaxDepth bounds the components of a path; headMaxPackDir the entries of
	// objects/pack that one lookup reads.
	headMaxDepth   = 64
	headMaxPackDir = 1024
	headRefLimit   = 4096
)

// headRepo is the folder's .git directory and what is left of one request's budget.
type headRepo struct {
	*os.Root
	left int64
}

// spend takes n bytes out of the budget, before they are allocated.
func (r *headRepo) spend(n int64) error {
	if r.left -= n; r.left < 0 {
		return errHeadTooLarge
	}
	return nil
}

var (
	// errNoRepository: the folder has no git history this node can read.
	errNoRepository = errors.New("no git history can be read in this folder")
	// errNotInHead: the history is readable and HEAD has no file at the path.
	errNotInHead = errors.New("the path is not in HEAD")
	// errHeadTooLarge: the committed file is past what is read.
	errHeadTooLarge = errors.New("the committed file is too large to read")
)

const (
	gitCommit   = 1
	gitTree     = 2
	gitBlob     = 3
	gitOfsDelta = 6
	gitRefDelta = 7
)

// headBlob returns the content of rel (slash-separated, inside the folder) at HEAD of the
// folder's git repository.
func headBlob(root *os.Root, rel string) ([]byte, error) {
	clean, err := cleanFolderPath(rel)
	if err != nil {
		return nil, err
	}
	g, err := root.OpenRoot(".git")
	if err != nil {
		return nil, errNoRepository
	}
	defer g.Close()
	head, err := headCommit(g)
	if err != nil {
		return nil, err
	}
	names := strings.Split(clean, "/")
	if len(names) > headMaxDepth { // deeper than any tree this reader walks
		return nil, errNotInHead
	}
	repo := &headRepo{Root: g, left: headBudget}
	kind, body, err := gitObject(repo, head, 0)
	if err != nil {
		return nil, err
	}
	treeID, ok := strings.CutPrefix(string(firstLine(body)), "tree ")
	if kind != gitCommit || !ok {
		return nil, errNoRepository
	}
	if treeID, err = objectID(treeID); err != nil { // an id read from the folder is checked like any other
		return nil, err
	}
	var mode string
	id := treeID
	for _, name := range names {
		if id == "" || (mode != "" && mode != "40000") {
			return nil, errNotInHead
		}
		kind, body, err = gitObject(repo, id, 0)
		if err != nil {
			return nil, err
		}
		if kind != gitTree {
			return nil, errNoRepository
		}
		if mode, id, err = treeEntry(body, name); err != nil {
			return nil, err
		}
	}
	if id == "" || mode == "40000" || mode == "160000" { // absent, a directory, a submodule
		return nil, errNotInHead
	}
	if kind, body, err = gitObject(repo, id, 0); err != nil || kind != gitBlob {
		if err == nil {
			err = errNoRepository
		}
		return nil, err
	}
	return body, nil
}

func firstLine(b []byte) []byte {
	line, _, _ := bytes.Cut(b, []byte{'\n'})
	return line
}

// treeEntry finds name in a tree object: its mode and object id, or "" when absent.
func treeEntry(tree []byte, name string) (mode, id string, err error) {
	for len(tree) > 0 {
		sp := bytes.IndexByte(tree, ' ')
		nul := bytes.IndexByte(tree, 0)
		if sp < 0 || nul < sp || len(tree) < nul+21 {
			return "", "", errNoRepository
		}
		if string(tree[sp+1:nul]) == name {
			return string(tree[:sp]), hex.EncodeToString(tree[nul+1 : nul+21]), nil
		}
		tree = tree[nul+21:]
	}
	return "", "", nil
}

// headCommit resolves HEAD to a commit id. An unborn branch is "not in HEAD".
func headCommit(g *os.Root) (string, error) {
	line, err := readSmall(g, "HEAD")
	if err != nil {
		return "", errNoRepository
	}
	ref, symbolic := strings.CutPrefix(line, "ref: ")
	if !symbolic {
		return objectID(line)
	}
	if !strings.HasPrefix(ref, "refs/") {
		return "", errNoRepository
	}
	if id, err := readSmall(g, ref); err == nil {
		return objectID(id)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", errNoRepository
	}
	packed, err := openRegular(g, "packed-refs")
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNotInHead // an unborn branch has no packed ref either
	}
	if err != nil {
		return "", errNoRepository
	}
	defer packed.Close()
	if fi, err := packed.Stat(); err != nil || fi.Size() > headMaxObject {
		return "", errNoRepository // a ref past the cut would read as "not in HEAD"
	}
	sc := bufio.NewScanner(packed)
	for sc.Scan() {
		if id, name, ok := strings.Cut(sc.Text(), " "); ok && name == ref {
			return objectID(id)
		}
	}
	if sc.Err() != nil {
		return "", errNoRepository
	}
	return "", errNotInHead
}

// objectID checks an object id read from the folder: 40 hex digits (so also a refusal of
// a SHA-256 repository), the only shape the lookups below may index.
func objectID(s string) (string, error) {
	if _, err := hex.DecodeString(s); err != nil || len(s) != 40 {
		return "", errNoRepository
	}
	return s, nil
}

// openRegular opens a file of the folder's .git directory for reading. The folder is
// written by the agent, so a FIFO or device planted where git keeps a file must not block
// the engine: the open does not wait, and anything but a regular file is refused. A file
// that is not there is fs.ErrNotExist.
func openRegular(g *os.Root, name string) (*os.File, error) {
	f, err := g.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, errNoRepository
	}
	return f, nil
}

func readSmall(g *os.Root, name string) (string, error) {
	f, err := openRegular(g, name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, headRefLimit))
	return strings.TrimSpace(string(b)), err
}

// gitObject returns the type and body of an object, loose first, then in the packs.
// A missing object means the repository is not whole in this folder (alternates, a
// partial clone): errNoRepository, never "not in HEAD".
func gitObject(g *headRepo, id string, chain int) (int, []byte, error) {
	if chain > headMaxChain {
		return 0, nil, errNoRepository
	}
	if kind, body, err := looseObject(g, id); !errors.Is(err, fs.ErrNotExist) {
		return kind, body, err
	}
	indexes, err := packIndexes(g.Root)
	if err != nil {
		return 0, nil, errNoRepository
	}
	raw, _ := hex.DecodeString(id)
	for _, idx := range indexes {
		offset, found, err := packOffset(g.Root, "objects/pack/"+idx, raw)
		if err != nil {
			return 0, nil, errNoRepository
		}
		if found {
			return packObject(g, "objects/pack/"+strings.TrimSuffix(idx, ".idx")+".pack", offset, chain)
		}
	}
	return 0, nil, errNoRepository
}

// packIndexes names the *.idx files of objects/pack. The directory is the agent's: it is
// opened without waiting (a FIFO there must not block), must be a directory, and more than
// headMaxPackDir entries in it is refused rather than read.
func packIndexes(g *os.Root) ([]string, error) {
	d, err := g.OpenFile("objects/pack", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if fi, err := d.Stat(); err != nil || !fi.IsDir() {
		return nil, errNoRepository
	}
	entries, err := d.ReadDir(headMaxPackDir + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > headMaxPackDir {
		return nil, errNoRepository
	}
	var indexes []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".idx") {
			indexes = append(indexes, e.Name())
		}
	}
	return indexes, nil
}

func looseObject(g *headRepo, id string) (int, []byte, error) {
	f, err := openRegular(g.Root, "objects/"+id[:2]+"/"+id[2:])
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil, err // the caller tries the packs
	}
	if err != nil {
		return 0, nil, errNoRepository
	}
	defer f.Close()
	zr, err := zlib.NewReader(f)
	if err != nil {
		return 0, nil, errNoRepository
	}
	br := bufio.NewReader(zr)
	header, err := br.ReadSlice(0)
	if err != nil {
		return 0, nil, errNoRepository
	}
	typeName, sizeText, _ := strings.Cut(string(header[:len(header)-1]), " ")
	size, err := strconv.ParseInt(sizeText, 10, 64)
	kind := map[string]int{"commit": gitCommit, "tree": gitTree, "blob": gitBlob}[typeName]
	if err != nil || size < 0 || kind == 0 {
		return 0, nil, errNoRepository
	}
	if size > headMaxObject {
		return 0, nil, errHeadTooLarge
	}
	if err := g.spend(size); err != nil {
		return 0, nil, err
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(br, body); err != nil {
		return 0, nil, errNoRepository
	}
	return kind, body, nil
}

// packOffset looks id up in one pack index (version 2): the fanout narrows the range, a
// binary search over the sorted ids finds it, and the offset table gives its place.
func packOffset(g *os.Root, idxName string, id []byte) (int64, bool, error) {
	f, err := openRegular(g, idxName)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	var head [8 + 256*4]byte
	if _, err := f.ReadAt(head[:], 0); err != nil || !bytes.Equal(head[:8], []byte{0xff, 't', 'O', 'c', 0, 0, 0, 2}) {
		return 0, false, errNoRepository
	}
	fan := func(i int) int64 { return int64(binary.BigEndian.Uint32(head[8+4*i:])) }
	lo, hi := int64(0), fan(int(id[0]))
	if id[0] > 0 {
		lo = fan(int(id[0]) - 1)
	}
	total := fan(255)
	shas := int64(len(head))
	var cur [20]byte
	for lo < hi {
		mid := (lo + hi) / 2
		if _, err := f.ReadAt(cur[:], shas+mid*20); err != nil {
			return 0, false, err
		}
		switch c := bytes.Compare(cur[:], id); {
		case c == 0:
			return packEntryOffset(f, shas+total*24, mid, total)
		case c < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, false, nil
}

// packEntryOffset reads entry n's pack offset; tables is where the 4-byte offsets start
// (after the ids and the checksums). A set high bit points into the 8-byte table.
func packEntryOffset(f *os.File, tables, n, total int64) (int64, bool, error) {
	var small [4]byte
	if _, err := f.ReadAt(small[:], tables+4*n); err != nil {
		return 0, false, err
	}
	off := int64(binary.BigEndian.Uint32(small[:]))
	if off&(1<<31) == 0 {
		return off, true, nil
	}
	var big [8]byte
	if _, err := f.ReadAt(big[:], tables+4*total+8*(off&^(1<<31))); err != nil {
		return 0, false, err
	}
	return int64(binary.BigEndian.Uint64(big[:]) & (1<<63 - 1)), true, nil
}

// packObject reads the object at offset of a pack, resolving a delta chain.
func packObject(g *headRepo, packName string, offset int64, chain int) (int, []byte, error) {
	f, err := openRegular(g.Root, packName)
	if err != nil {
		return 0, nil, errNoRepository
	}
	defer f.Close()
	return packObjectAt(g, f, offset, chain)
}

func packObjectAt(g *headRepo, f *os.File, offset int64, chain int) (int, []byte, error) {
	if chain > headMaxChain {
		return 0, nil, errNoRepository
	}
	br := bufio.NewReader(io.NewSectionReader(f, offset, 1<<62-offset))
	c, err := br.ReadByte()
	if err != nil {
		return 0, nil, errNoRepository
	}
	kind, size, shift := int(c>>4&7), int64(c&15), uint(4)
	for c&0x80 != 0 {
		if c, err = br.ReadByte(); err != nil || shift > 56 {
			return 0, nil, errNoRepository
		}
		size |= int64(c&0x7f) << shift
		shift += 7
	}
	if size > headMaxObject {
		return 0, nil, errHeadTooLarge
	}
	var baseKind int
	var base []byte
	switch kind {
	case gitOfsDelta:
		back := int64(0)
		for first := true; first || c&0x80 != 0; first = false {
			if c, err = br.ReadByte(); err != nil || back > 1<<56 {
				return 0, nil, errNoRepository
			}
			if first {
				back = int64(c & 0x7f)
			} else {
				back = (back+1)<<7 | int64(c&0x7f)
			}
		}
		if back <= 0 || back > offset {
			return 0, nil, errNoRepository
		}
		baseKind, base, err = packObjectAt(g, f, offset-back, chain+1)
	case gitRefDelta:
		var raw [20]byte
		if _, err = io.ReadFull(br, raw[:]); err != nil {
			return 0, nil, errNoRepository
		}
		baseKind, base, err = gitObject(g, hex.EncodeToString(raw[:]), chain+1)
	case gitCommit, gitTree, gitBlob:
	default:
		return 0, nil, errNoRepository
	}
	if err != nil {
		return 0, nil, err
	}
	zr, err := zlib.NewReader(br)
	if err != nil {
		return 0, nil, errNoRepository
	}
	if err := g.spend(size); err != nil {
		return 0, nil, err
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(zr, data); err != nil {
		return 0, nil, errNoRepository
	}
	if kind != gitOfsDelta && kind != gitRefDelta {
		return kind, data, nil
	}
	out, err := applyDelta(base, data)
	if err != nil {
		return 0, nil, err
	}
	// The result is charged once it is built; applyDelta never builds more than 8 MiB.
	if err := g.spend(int64(len(out))); err != nil {
		return 0, nil, err
	}
	return baseKind, out, nil
}

// applyDelta runs git's delta program: sizes of the base and the result, then copy and
// insert instructions.
func applyDelta(base, delta []byte) ([]byte, error) {
	pos := 0
	varint := func() int64 {
		var n int64
		for shift := uint(0); pos < len(delta) && shift <= 56; shift += 7 {
			b := delta[pos]
			pos++
			n |= int64(b&0x7f) << shift
			if b&0x80 == 0 {
				return n
			}
		}
		return -1
	}
	baseSize, size := varint(), varint()
	if baseSize != int64(len(base)) || size < 0 {
		return nil, errNoRepository
	}
	if size > headMaxObject {
		return nil, errHeadTooLarge
	}
	out := make([]byte, 0, size)
	for pos < len(delta) {
		op := delta[pos]
		pos++
		if op&0x80 == 0 { // insert the next op bytes
			if op == 0 || pos+int(op) > len(delta) {
				return nil, errNoRepository
			}
			out = append(out, delta[pos:pos+int(op)]...)
			pos += int(op)
			continue
		}
		var off, n int64
		for i := uint(0); i < 7; i++ {
			if op&(1<<i) == 0 {
				continue
			}
			if pos >= len(delta) {
				return nil, errNoRepository
			}
			if i < 4 {
				off |= int64(delta[pos]) << (8 * i)
			} else {
				n |= int64(delta[pos]) << (8 * (i - 4))
			}
			pos++
		}
		if n == 0 {
			n = 0x10000
		}
		// A copy never leaves the base and never grows the result past its declared size:
		// the program is attacker-written and one byte of it copies 64 KiB, so the output
		// is bounded as it is built, not only checked at the end. (An insert cannot
		// amplify: it carries its own bytes, and the delta is itself bounded.)
		if off+n > int64(len(base)) || int64(len(out))+n > size {
			return nil, errNoRepository
		}
		out = append(out, base[off:off+n]...)
	}
	if int64(len(out)) != size {
		return nil, errNoRepository
	}
	return out, nil
}

// cleanFolderPath validates a folder-relative path the way every read of the folder does.
func cleanFolderPath(rel string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(rel, "./"))
	if rel == "" || path.IsAbs(rel) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fs.ErrInvalid
	}
	return clean, nil
}
