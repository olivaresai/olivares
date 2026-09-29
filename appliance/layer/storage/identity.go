// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// Identity kinds.
const (
	IdentityDrive = "drive"
	IdentityBlank = "blank"
	IdentityLoop  = "loop"
)

// Identity sources: which fact names the device.
const (
	SourceWWN        = "wwn"
	SourceSerial     = "serial"
	SourceDriveID    = "drive_id"
	SourceBlockID    = "block_id"
	SourceByPath     = "by_path"
	SourceLoopStatus = "loop_status"
)

// Identity is what names one disk for a plan. Kind is empty when the disk has no stable
// identity, and Missing says why.
type Identity struct {
	Kind           string        `json:"kind,omitempty"`
	Source         string        `json:"source,omitempty"`
	Value          string        `json:"value,omitempty"`
	Size           uint64        `json:"size_bytes"`
	PartitionTable string        `json:"partition_table,omitempty"`
	PartitionUUIDs []string      `json:"partition_uuids,omitempty"`
	FirstMiBSHA256 string        `json:"first_mib_sha256,omitempty"`
	BootID         string        `json:"boot_id,omitempty"`
	Loop           *LoopIdentity `json:"loop,omitempty"`
	Digest         string        `json:"digest,omitempty"`
	Missing        string        `json:"missing,omitempty"`
	Ambiguous      bool          `json:"ambiguous,omitempty"`
}

// LoopIdentity is what the kernel reports for a loop device.
type LoopIdentity struct {
	BackingDevice uint64 `json:"backing_device"`
	BackingInode  uint64 `json:"backing_inode"`
	Offset        uint64 `json:"offset_bytes"`
	SizeLimit     uint64 `json:"size_limit_bytes"`
	BlockSize     uint32 `json:"block_size_bytes"`
}

// Refusal codes.
const (
	CodeIdentityChanged = "storage_identity_changed"
	CodeInputRefused    = "input_refused"
	// CodeSystemDisk disables every operation on the system disk and its partitions.
	CodeSystemDisk = "storage_system_disk"
	// CodeTargetInUse disables every operation on a device with a consumer.
	CodeTargetInUse = "storage_target_in_use"
)

// Refusal refuses a target. Code is closed; Reason is fixed text and never repeats a value the
// caller sent.
type Refusal struct {
	Code   string
	Reason string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Reason }

// Reresolve finds the one disk of current that captured names, as the storage helper does
// under the target lock before any change. An identity whose digest is not the digest of its
// own facts is refused as input. An identity bound to a boot is refused in any other boot, and
// an identity no disk has now, or more than one disk has now, is refused: each is
// storage_identity_changed.
func Reresolve(captured Identity, current Inventory) (Disk, error) {
	if captured.Kind == "" || captured.Missing != "" || !isHex(captured.Digest, 64) || digestOf(captured) != captured.Digest {
		return Disk{}, &Refusal{Code: CodeInputRefused, Reason: "target: not an identity an inventory issued"}
	}
	if captured.BootID != "" && captured.BootID != current.BootID {
		return Disk{}, &Refusal{Code: CodeIdentityChanged, Reason: "the identity was read in another boot and is valid in that boot only"}
	}
	var found []Disk
	for _, d := range current.Disks {
		if d.Identity.Digest == captured.Digest {
			found = append(found, d)
		}
	}
	switch {
	case len(found) == 0:
		return Disk{}, &Refusal{Code: CodeIdentityChanged, Reason: "no disk has this identity now"}
	case len(found) > 1 || found[0].Identity.Ambiguous:
		return Disk{}, &Refusal{Code: CodeIdentityChanged, Reason: "more than one disk has this identity now"}
	}
	return found[0], nil
}

// identitySchema is bound into every identity digest.
const identitySchema = "storage-identity/v1"

// byPathPrefix is the directory of the links udev names by bus path.
const byPathPrefix = "/dev/disk/by-path/"

// Reasons an identity is missing.
const (
	MissingNoStableIdentity   = "no_stable_identity"
	MissingFirstMiBUnreadable = "first_mib_unreadable"
	MissingLoopStatus         = "loop_status_unreadable"
)

// identify names disk b: a loop device by what the kernel reports for it, in this boot only
// (the name UDisks2 shows for its file is not used); any other disk by the first non-blank of
// the drive's WWN, serial and Id and the block Id; and a disk with all four blank by its
// by-path link and the digest of its first MiB, in this boot only. Each identity carries the
// disk's size and its partition UUIDs.
func identify(objects Objects, kernel Kernel, boot string, b Block, partitionUUIDs []string) Identity {
	id := Identity{Size: b.Size, PartitionUUIDs: slices.Clone(partitionUUIDs)}
	if b.Loop != nil {
		status, err := kernel.LoopStatus(b.Device, b.DeviceNumber)
		if err != nil {
			id.Missing = MissingLoopStatus
			return id
		}
		id.Kind, id.Source, id.BootID = IdentityLoop, SourceLoopStatus, boot
		id.Loop = &LoopIdentity{BackingDevice: status.BackingDevice, BackingInode: status.BackingInode,
			Offset: status.Offset, SizeLimit: status.SizeLimit, BlockSize: status.BlockSize}
		return sealed(id)
	}
	drive := objects.Drives[b.Drive]
	for _, named := range []struct{ source, value string }{
		{SourceWWN, drive.WWN}, {SourceSerial, drive.Serial}, {SourceDriveID, drive.ID}, {SourceBlockID, b.ID},
	} {
		if value := strings.TrimSpace(named.value); value != "" {
			id.Kind, id.Source, id.Value = IdentityDrive, named.source, value
			return sealed(id)
		}
	}
	link := byPath(b.Symlinks)
	if link == "" {
		id.Missing = MissingNoStableIdentity
		return id
	}
	sum, err := kernel.FirstMiB(b.Device, b.DeviceNumber)
	if err != nil || !isHex(sum, 64) {
		id.Missing = MissingFirstMiBUnreadable
		return id
	}
	id.Kind, id.Source, id.Value = IdentityBlank, SourceByPath, link
	if b.PartitionTable != nil {
		id.PartitionTable = b.PartitionTable.Type
	}
	id.FirstMiBSHA256, id.BootID = sum, boot
	return sealed(id)
}

// byPath returns the first, in byte order, of the disk's /dev/disk/by-path links.
func byPath(symlinks []string) string {
	var links []string
	for _, link := range symlinks {
		if strings.HasPrefix(link, byPathPrefix) && len(link) > len(byPathPrefix) && len(link) <= 255 && printable(link) {
			links = append(links, link)
		}
	}
	if len(links) == 0 {
		return ""
	}
	slices.Sort(links)
	return links[0]
}

// sealed returns id with its digest.
func sealed(id Identity) Identity {
	id.Digest = digestOf(id)
	return id
}

// digestOf is the SHA-256 of the identity's facts, without its digest and its marks.
func digestOf(id Identity) string {
	id.Digest, id.Missing, id.Ambiguous = "", "", false
	data, err := json.Marshal(struct {
		Schema   string   `json:"schema"`
		Identity Identity `json:"identity"`
	}{identitySchema, id})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHex(s[i:i+1], 1) {
				return false
			}
		}
	}
	return true
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}
