// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const maxReceiptBytes = 256 << 10

// readReceipt loads one receipt strictly: a regular file (never a symlink),
// bounded, with no unknown fields, owned by the caller, recording the directory
// it was found in. A receipt that moved with its directory is not credited: the
// plan that produced it named another destination.
func readReceipt(root *os.Root, rel, wantReleaseDir string) (*Receipt, error) {
	fi, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("receipt must be a regular file, not a link")
	}
	if fi.Size() > maxReceiptBytes {
		return nil, fmt.Errorf("receipt is larger than %d bytes", maxReceiptBytes)
	}
	if err := ownedByCaller(fi); err != nil {
		return nil, fmt.Errorf("receipt %v", err)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(fi, opened) {
		return nil, fmt.Errorf("receipt changed while it was being opened")
	}
	dec := json.NewDecoder(io.LimitReader(f, maxReceiptBytes+1))
	dec.DisallowUnknownFields()
	var rec Receipt
	if err := dec.Decode(&rec); err != nil {
		return nil, fmt.Errorf("parse receipt: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("parse receipt: trailing JSON value")
	}
	if rec.Schema != ReceiptSchema {
		return nil, fmt.Errorf("receipt schema %q, want %q", rec.Schema, ReceiptSchema)
	}
	if !ValidVersion(rec.Version) || !vendorKeyRe.MatchString(rec.VendorPlatform) || rec.Driver == "" {
		return nil, fmt.Errorf("receipt names an invalid driver, version or platform")
	}
	if !isHex64(rec.Artifact.SHA256) || rec.Artifact.Size <= 0 {
		return nil, fmt.Errorf("receipt carries no complete artifact digest")
	}
	if wantReleaseDir != "" && rec.Destination.ReleaseDir != wantReleaseDir {
		return nil, fmt.Errorf("receipt was written for %s, not for %s (the release directory was moved or copied)", rec.Destination.ReleaseDir, wantReleaseDir)
	}
	return &rec, nil
}
