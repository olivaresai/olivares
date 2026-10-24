// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// RipgrepFolder holds, under the tools root, the ripgrep OpenCode's glob and grep
// run (#3173). OpenCode runs the rg on its PATH, else one it downloads from
// github.com at first use, which a confined session cannot reach; installing
// OpenCode places this one, and an OpenCode launch puts its folder first on PATH.
// The tools list does not report it: it is no tool of its own.
const RipgrepFolder = ".ripgrep"

// ripgrepPin is one ripgrep release archive, the one OpenCode 1.19.4 would
// download for the platform, bound by the archive's and its program's sha256
// (GitHub's asset digest and the release's own .sha256 file agree).
type ripgrepPin struct {
	Version, Platform, URL, SHA256, Member, ProgramSHA256 string
	Size, ProgramSize                                     int64
}

// ripgrepPins maps GOARCH to its pin. A variable so tests can serve a fixture.
var ripgrepPins = map[string]ripgrepPin{
	"amd64": {Version: "15.1.0", Platform: "x86_64-unknown-linux-musl",
		URL:  "https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-x86_64-unknown-linux-musl.tar.gz",
		Size: 2263077, SHA256: "1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599",
		Member: "ripgrep-15.1.0-x86_64-unknown-linux-musl/rg", ProgramSize: 5445512,
		ProgramSHA256: "ebeaf56f8a25e102e9419933423738b3a2a613a444fd749d695e15eba53f71f2"},
	"arm64": {Version: "15.1.0", Platform: "aarch64-unknown-linux-gnu",
		URL:  "https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-aarch64-unknown-linux-gnu.tar.gz",
		Size: 1869959, SHA256: "2b661c6ef508e902f388e9098d9c4c5aca72c87b55922d94abdba830b4dc885e",
		Member: "ripgrep-15.1.0-aarch64-unknown-linux-gnu/rg", ProgramSize: 4543848,
		ProgramSHA256: "968cabe8efed72fd8fd482cb76b6084fcb695fc5293af7fb62296b02f487fb69"},
}

// Ripgrep is the folder of this host's pinned ripgrep under the tools root and
// the sha256 of its rg; "" when ripgrep is not pinned for this platform.
func Ripgrep(root string) (dir, programSHA256 string) {
	if dir = ripgrepDir(root, runtime.GOARCH); dir == "" {
		return "", ""
	}
	return dir, ripgrepPins[runtime.GOARCH].ProgramSHA256
}

func ripgrepDir(root, arch string) string {
	pin, ok := ripgrepPins[arch]
	if !ok || root == "" {
		return ""
	}
	return filepath.Join(root, RipgrepFolder, pin.Version+"-"+pin.Platform)
}

// ensureRipgrep places the pinned ripgrep for arch under destRoot, unless it is
// already there: the archive is fetched and checked against its pin, only its rg
// is extracted, checked against its own pin, and the folder is renamed into
// place. A version folder whose rg is not the pinned program, or is missing, is
// damaged (#3202): the verified copy replaces it, and the damaged folder is moved
// aside and removed, so installing OpenCode again repairs it.
func ensureRipgrep(ctx context.Context, fetch fetcher, destRoot, arch string, progress io.Writer) error {
	pin, ok := ripgrepPins[arch]
	if !ok {
		fmt.Fprintf(progress, "no pinned ripgrep for %s: OpenCode's glob and grep need rg on the engine's PATH\n", arch)
		return nil
	}
	root, err := openRoot(destRoot, true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	release, err := lockRoot(root, destRoot)
	if err != nil {
		return err
	}
	defer release()
	rel := filepath.Join(RipgrepFolder, pin.Version+"-"+pin.Platform)
	if sum, _, err := fileSHA256Root(root, filepath.Join(rel, "rg")); err == nil && sum == pin.ProgramSHA256 {
		fmt.Fprintf(progress, "ripgrep %s for OpenCode's glob and grep: already in place\n", pin.Version)
		return nil
	}
	damaged := false
	if _, err := root.Lstat(rel); err == nil {
		damaged = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return refuse(KindDestinationUnsafe, "inspect %s: %v", filepath.Join(destRoot, rel), err)
	}
	if err := ensureDir(root, RipgrepFolder, releaseDirMode); err != nil {
		return err
	}
	staging := filepath.Join(RipgrepFolder, stagingPrefix+pin.Version+"-"+pin.Platform+"."+randHex(8))
	if err := root.Mkdir(staging, stagingDirMode); err != nil {
		return refuse(KindDestinationNotWritable, "create ripgrep staging: %v", err)
	}
	placed := false
	defer func() {
		if !placed {
			_ = root.RemoveAll(staging)
		}
	}()
	fmt.Fprintf(progress, "downloading ripgrep %s for OpenCode's glob and grep from %s\n", pin.Version, pin.URL)
	fetched := filepath.Join(staging, fetchedObjectName)
	f, err := root.OpenFile(fetched, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return refuse(KindDestinationNotWritable, "create ripgrep archive: %v", err)
	}
	origins := []string{"https://github.com", "https://release-assets.githubusercontent.com"}
	_, err = fetch.confine(RedirectPolicyV2{MaxHops: 3, AllowedOrigins: origins[1:]}, origins).exactSizeHash(ctx, pin.URL, pin.SHA256, pin.Size, pin.Size, f)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = refuse(KindDestinationNotWritable, "close ripgrep archive: %v", cerr)
	}
	if err != nil {
		return err
	}
	if sum, size, err := fileSHA256Root(root, fetched); err != nil || sum != pin.SHA256 || size != pin.Size {
		return refuse(KindDigestMismatch, "ripgrep archive does not match its pinned sha256")
	}
	if err := extractRipgrep(root, fetched, filepath.Join(staging, "rg"), pin); err != nil {
		return err
	}
	if err := root.Remove(fetched); err != nil {
		return refuse(KindDestinationNotWritable, "remove ripgrep archive: %v", err)
	}
	if err := root.Chmod(staging, releaseDirMode); err != nil {
		return refuse(KindDestinationNotWritable, "set ripgrep folder mode: %v", err)
	}
	aside := ""
	if damaged {
		aside = filepath.Join(RipgrepFolder, ".damaged."+pin.Version+"-"+pin.Platform+"."+randHex(8))
		if err := root.Rename(rel, aside); err != nil {
			return refuse(KindDestinationNotWritable, "move the damaged ripgrep at %s aside: %v", filepath.Join(destRoot, rel), err)
		}
	}
	if err := root.Rename(staging, rel); err != nil {
		return refuse(KindDestinationNotWritable, "place ripgrep at %s: %v", filepath.Join(destRoot, rel), err)
	}
	placed = true
	if aside != "" {
		if err := root.RemoveAll(aside); err != nil {
			return refuse(KindDestinationNotWritable, "remove the damaged ripgrep moved to %s: %v", filepath.Join(destRoot, aside), err)
		}
		fmt.Fprintf(progress, "replaced a damaged ripgrep at %s\n", filepath.Join(destRoot, rel))
	}
	if err := syncDir(root, RipgrepFolder); err != nil {
		return err
	}
	fmt.Fprintf(progress, "placed ripgrep %s at %s\n", pin.Version, filepath.Join(destRoot, rel, "rg"))
	return nil
}

// extractRipgrep writes the archive's pinned member, and nothing else, to dst
// (created as "rg", mode 0755) and checks it against its pin.
func extractRipgrep(root *os.Root, archive, dst string, pin ripgrepPin) error {
	in, err := root.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return refuse(KindManifestInvalid, "ripgrep archive is not gzip: %v", err)
	}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return refuse(KindManifestInvalid, "ripgrep archive has no %s", pin.Member)
		}
		if err != nil {
			return refuse(KindManifestInvalid, "read ripgrep archive: %v", err)
		}
		if hdr.Name != pin.Member {
			continue
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size != pin.ProgramSize {
			return refuse(KindManifestInvalid, "ripgrep archive's %s is not the pinned regular file", pin.Member)
		}
		out, err := root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err != nil {
			return refuse(KindDestinationNotWritable, "create ripgrep: %v", err)
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(tr, pin.ProgramSize))
		if serr := out.Sync(); err == nil {
			err = serr
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return refuse(KindDestinationNotWritable, "write ripgrep: %v", err)
		}
		if hex.EncodeToString(h.Sum(nil)) != pin.ProgramSHA256 {
			return refuse(KindDigestMismatch, "ripgrep program does not match its pinned sha256")
		}
		return root.Chmod(dst, 0o755)
	}
}
