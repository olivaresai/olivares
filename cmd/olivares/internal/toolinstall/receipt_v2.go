// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ReceiptSchemaV2 is the Codex/Grok receipt. ReceiptSchema (v1) remains Claude's.
const ReceiptSchemaV2 = "olivares.ai/tool-install/receipt/v2"

// ReceiptV2 is written into a v2 release directory after probe and before the
// directory is renamed into place. Observation of the fetched object is always
// present, including origin-only Grok where the plan digest state is unknown.
type ReceiptV2 struct {
	Schema           string                   `json:"schema"`
	Driver           string                   `json:"driver"`
	Version          string                   `json:"version"`
	VendorPlatform   string                   `json:"vendor_platform"`
	Platform         PlatformV2               `json:"platform"`
	Destination      Destination              `json:"destination"`
	PlanDigest       string                   `json:"plan_digest"`
	PackagePolicyID  string                   `json:"package_policy_id"`
	VerificationKind string                   `json:"verification_kind"`
	FetchedObject    FetchedObjectObserved    `json:"fetched_object"`
	Payload          ObservedPayloadInventory `json:"payload"`
	Probe            ProbeReport              `json:"probe"`
	AuthObservation  AuthObservation          `json:"auth_observation"`
	InstalledAt      time.Time                `json:"installed_at"`
	InstallerVersion string                   `json:"installer_version"`
}

func readReceiptSchema(root *os.Root, rel string) (string, error) {
	b, err := readSmallFile(root, rel, maxReceiptBytes)
	if err != nil {
		return "", err
	}
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return "", fmt.Errorf("parse receipt schema: %w", err)
	}
	return head.Schema, nil
}

func readReceiptV2(root *os.Root, rel, wantReleaseDir string) (*ReceiptV2, error) {
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
	var rec ReceiptV2
	if err := dec.Decode(&rec); err != nil {
		return nil, fmt.Errorf("parse receipt: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("parse receipt: trailing JSON value")
	}
	if rec.Schema != ReceiptSchemaV2 {
		return nil, fmt.Errorf("receipt schema %q, want %q", rec.Schema, ReceiptSchemaV2)
	}
	if !ValidVersion(rec.Version) || rec.Driver == "" {
		return nil, fmt.Errorf("receipt names an invalid driver or version")
	}
	if !vendorPlatformV2Re.MatchString(rec.VendorPlatform) {
		return nil, fmt.Errorf("receipt names an invalid vendor platform")
	}
	if !isHex64(rec.FetchedObject.SHA256) || rec.FetchedObject.Size <= 0 {
		return nil, fmt.Errorf("receipt carries no complete fetched-object digest")
	}
	if rec.PlanDigest == "" || !isHex64(rec.PlanDigest) {
		return nil, fmt.Errorf("receipt carries no plan digest")
	}
	if wantReleaseDir != "" && rec.Destination.ReleaseDir != wantReleaseDir {
		return nil, fmt.Errorf("receipt was written for %s, not for %s (the release directory was moved or copied)", rec.Destination.ReleaseDir, wantReleaseDir)
	}
	entry := rec.Destination.Executable
	if entry == "" || !filepath.IsAbs(entry) {
		return nil, fmt.Errorf("receipt destination executable is not an absolute path")
	}
	return &rec, nil
}
