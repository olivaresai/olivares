// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

const (
	keyFile        = "spool.key"
	actsDir        = "acts" // the act journal, kept here when the verb's adoption brings one
	outDir         = "out"  // the bundles this helper produced
	maxBundleBytes = 64 * 1024
	bundleSchema   = "olivares-support-bundle/v1"
)

// spool is the helper's own directory: its key and the bundles it produced.
type spool struct {
	dir    string
	random io.Reader
	facts  func() ([]byte, error)
}

// helper is the support-bundle helper over s.
func helper(s spool) invocation.Helper {
	return invocation.Helper{
		Name:       helperschema.HelperSupportBundle,
		Rules:      helperschema.SupportBundleRules(),
		NewRequest: func() helperschema.Request { return &helperschema.SupportBundleRequest{} },
		Perform: func(_ context.Context, _ helperschema.Peer, request helperschema.Request) helperschema.Response {
			r, ok := request.(*helperschema.SupportBundleRequest)
			switch {
			case !ok:
				return refused(helperschema.CodeInputRefused, "not a document of this helper")
			case r.Op == helperschema.SupportBundleProduce:
				return s.produce()
			case r.Op == helperschema.SupportBundleFetch:
				return s.fetch(r.Nonce)
			}
			return refused(helperschema.CodeInputRefused, "not a subcommand of this helper")
		},
	}
}

func refused(code, detail string) helperschema.Response {
	return helperschema.Response{Result: helperschema.ResultRefused, Code: code, Detail: detail}
}

func failed(detail string) helperschema.Response {
	return helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed, Detail: detail}
}

// produce collects the bundle and writes it into the spool under a nonce issued now, which it
// answers. The file is created, never replaced.
func (s spool) produce() helperschema.Response {
	key, err := s.key()
	if err != nil {
		return failed("the spool is not this helper's: " + err.Error())
	}
	nonce, err := helperschema.IssueNonce(key, s.random)
	if err != nil {
		return failed("no nonce could be issued")
	}
	name, err := key.SpoolName(string(nonce), helperschema.SpoolBundle)
	if err != nil {
		return failed("an issued nonce did not verify")
	}
	out, err := s.layout()
	if err != nil {
		return failed("the spool is not this helper's: " + err.Error())
	}
	bundle, err := s.facts()
	if err != nil || len(bundle) > maxBundleBytes {
		return failed("the bundle could not be collected")
	}
	if err := createOnce(filepath.Join(out, name), bundle); err != nil {
		return failed("the bundle could not be written to the spool")
	}
	return helperschema.Response{Result: helperschema.ResultPerformed, Nonce: string(nonce),
		Detail: "a support bundle was written to this helper's spool"}
}

// fetch answers the bundle of a nonce this helper issued. The file name is derived from the
// nonce, so no name the caller wrote reaches the file system.
func (s spool) fetch(nonce string) helperschema.Response {
	key, err := s.key()
	if err != nil {
		return failed("the spool is not this helper's: " + err.Error())
	}
	name, err := key.SpoolName(nonce, helperschema.SpoolBundle)
	if err != nil {
		return refused(helperschema.CodeInputRefused, "not a nonce this helper issued")
	}
	out, err := s.layout()
	if err != nil {
		return failed("the spool is not this helper's: " + err.Error())
	}
	data, err := readOwn(filepath.Join(out, name), maxBundleBytes)
	if err != nil {
		return refused(helperschema.CodeInputRefused, "no bundle of this helper has that nonce")
	}
	return helperschema.Response{Result: helperschema.ResultAnswered, Bundle: json.RawMessage(data)}
}

// layout makes sure the state home holds acts/ and out/, each a directory of this account with
// mode 0700, creating either one that is missing, and returns out/. A directory that is not
// private to this account is refused, never repaired.
func (s spool) layout() (string, error) {
	for _, sub := range []string{actsDir, outDir} {
		dir := filepath.Join(s.dir, sub)
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", errors.New("the spool's " + sub + " directory could not be created")
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedBySelf(info) {
			return "", errors.New("the spool's " + sub + " directory is not private to this account")
		}
	}
	return filepath.Join(s.dir, outDir), nil
}

// key reads the spool's key, or creates it on first use. The spool must be a directory of this
// account, mode 0700; the key a regular file of this account, mode 0600, of 32 bytes.
func (s spool) key() (helperschema.SpoolKey, error) {
	var key helperschema.SpoolKey
	info, err := os.Stat(s.dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedBySelf(info) {
		return key, errors.New("the spool directory is missing or not private to this account")
	}
	path := filepath.Join(s.dir, keyFile)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		fresh := make([]byte, len(key))
		if _, err := io.ReadFull(s.random, fresh); err != nil {
			return key, errors.New("no random bytes for the spool key")
		}
		if err := createOnce(path, fresh); err != nil && !errors.Is(err, os.ErrExist) {
			return key, errors.New("the spool key could not be created")
		}
	}
	data, err := readOwn(path, int64(len(key)))
	if err != nil || len(data) != len(key) {
		return key, errors.New("the spool key is unreadable or not this helper's")
	}
	copy(key[:], data)
	return key, nil
}

// createOnce creates path, mode 0600, with data, and syncs it and its directory. It never
// replaces a file: an existing path is os.ErrExist.
func createOnce(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// readOwn reads a regular file of this account, mode 0600, not a symbolic link, of at most
// limit bytes, refusing one replaced between the check and the read.
func readOwn(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedBySelf(info) || info.Size() > limit {
		return nil, errors.New("not a private file of this helper")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("changed while it was read")
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

func ownedBySelf(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// hostFacts collects the bundle from files every account may read under root: the operating
// system's identity, the kernel release and this boot's identity. It reads no secret, no
// journal and no configuration of the product.
func hostFacts(root string, now func() time.Time) func() ([]byte, error) {
	return func() ([]byte, error) {
		release := osRelease(filepath.Join(root, "etc/os-release"))
		bundle := map[string]any{
			"schema_version": bundleSchema,
			"collected_at":   now().UTC().Format(time.RFC3339),
			"os":             release,
			"kernel_release": firstLine(filepath.Join(root, "proc/sys/kernel/osrelease")),
			"boot_id":        firstLine(filepath.Join(root, "proc/sys/kernel/random/boot_id")),
		}
		return json.MarshalIndent(bundle, "", "  ")
	}
}

// osRelease reads the three identity fields of os-release(5).
func osRelease(path string) map[string]string {
	fields := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return fields
	}
	lines := bufio.NewScanner(bytes.NewReader(data))
	for lines.Scan() {
		key, value, ok := strings.Cut(lines.Text(), "=")
		if !ok || (key != "ID" && key != "VERSION_ID" && key != "PRETTY_NAME") {
			continue
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		fields[strings.ToLower(key)] = value
	}
	return fields
}

func firstLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line)
}
