// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Schema names the inventory document.
const Schema = "storage-inventory/v1"

// Inventory is one reading of the host's storage. It is the document the storage helper
// answers and every surface renders.
type Inventory struct {
	Schema string `json:"schema"`
	BootID string `json:"boot_id"`
	Disks  []Disk `json:"disks"`
	LVM    LVM    `json:"lvm"`
}

// Disk is one disk or loop device.
type Disk struct {
	Device string `json:"device"`
	Size   uint64 `json:"size_bytes"`
	Vendor string `json:"vendor,omitempty"`
	Model  string `json:"model,omitempty"`
	// BackingFile is the name UDisks2 shows for a loop device's file. It is display only.
	BackingFile string   `json:"backing_file,omitempty"`
	Identity    Identity `json:"identity"`
	// Target is the lock key "drive:<identity digest>", empty when the identity is missing or
	// ambiguous.
	Target     string `json:"target,omitempty"`
	HintSystem bool   `json:"hint_system"`
	// System marks the disk that holds /, /boot, /boot/efi, a /var/lib/olivares volume or active
	// swap; SystemReasons names each.
	System         bool        `json:"system"`
	SystemReasons  []string    `json:"system_reasons,omitempty"`
	PartitionTable string      `json:"partition_table,omitempty"`
	Filesystem     *Filesystem `json:"filesystem,omitempty"`
	// Consumers are the disk's own consumers; each partition lists its own.
	Consumers  []Consumer  `json:"consumers"`
	Partitions []Partition `json:"partitions"`
	Operations []Operation `json:"operations"`
}

// Partition is one entry of a disk's partition table.
type Partition struct {
	Device     string      `json:"device"`
	Number     uint32      `json:"number"`
	Offset     uint64      `json:"offset_bytes"`
	Size       uint64      `json:"size_bytes"`
	UUID       string      `json:"uuid,omitempty"`
	Type       string      `json:"type,omitempty"`
	Filesystem *Filesystem `json:"filesystem,omitempty"`
	Consumers  []Consumer  `json:"consumers"`
	Operations []Operation `json:"operations"`
}

// Filesystem is what probing found on a block device.
type Filesystem struct {
	Type        string   `json:"type"`
	UUID        string   `json:"uuid,omitempty"`
	Label       string   `json:"label,omitempty"`
	MountPoints []string `json:"mount_points,omitempty"`
}

// Consumer kinds.
const (
	ConsumerMount          = "mount"
	ConsumerSwap           = "swap"
	ConsumerHolder         = "holder"
	ConsumerPhysicalVolume = "physical-volume"
	ConsumerEncrypted      = "encrypted"
	ConsumerRaidMember     = "raid-member"
)

// Unread names a consumer that exists but whose owner could not be read.
const Unread = "unread"

// Consumer is one use of a device: a mount point, active swap, a kernel holder, membership of
// a volume group, an unlocked encrypted device or membership of a RAID array.
type Consumer struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Operation names.
const (
	OperationFormat = "format"
	OperationCheck  = "check"
	OperationRepair = "repair"
	OperationGrow   = "grow"
	OperationShrink = "shrink"
)

// Operation is one operation the host reports it can perform: its name, the filesystem type,
// for grow and shrink the modes ("offline", "online"), and the closed code of why it is disabled
// on this device, if it is.
type Operation struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Modes    []string `json:"modes,omitempty"`
	Disabled string   `json:"disabled,omitempty"`
}

// LVM states.
const (
	LVMRead        = "read"
	LVMUnavailable = "unavailable"
)

// LVM is the LVM part of the inventory. State "read" with no volume group is a complete empty
// result; "unavailable" means the module could not be enabled and nothing was read.
type LVM struct {
	State        string            `json:"state"`
	Reason       string            `json:"reason,omitempty"`
	VolumeGroups []VolumeGroupView `json:"volume_groups,omitempty"`
}

// VolumeGroupView is one volume group.
type VolumeGroupView struct {
	Name            string              `json:"name"`
	UUID            string              `json:"uuid"`
	Size            uint64              `json:"size_bytes"`
	Free            uint64              `json:"free_bytes"`
	PhysicalVolumes []string            `json:"physical_volumes"`
	Missing         []string            `json:"missing_physical_volumes,omitempty"`
	LogicalVolumes  []LogicalVolumeView `json:"logical_volumes"`
}

// LogicalVolumeView is one logical volume.
type LogicalVolumeView struct {
	Name       string      `json:"name"`
	UUID       string      `json:"uuid"`
	Type       string      `json:"type,omitempty"`
	Active     bool        `json:"active"`
	Size       uint64      `json:"size_bytes"`
	Device     string      `json:"device,omitempty"`
	Filesystem *Filesystem `json:"filesystem,omitempty"`
	Consumers  []Consumer  `json:"consumers"`
}

// Read reads one inventory from bus and kernel. It fails only when UDisks2's objects or the
// kernel's boot id cannot be read, or when the host has more than MaxBlocks block objects; a
// fact that cannot be read for one device is stated on that device.
func Read(ctx context.Context, bus Bus, kernel Kernel) (Inventory, error) {
	// UDisks2 attaches the LVM2 module's interfaces to block objects only once the module is
	// enabled, so it is enabled before the first read.
	lvmErr := bus.EnableModule(ctx, lvmModule)
	objects, err := bus.Objects(ctx)
	if err != nil {
		return Inventory{}, errors.New("UDisks2's objects could not be read")
	}
	if len(objects.Blocks) > MaxBlocks {
		return Inventory{}, errors.New("the host has more block devices than one inventory reads")
	}
	boot, err := kernel.BootID()
	if err != nil || !isUUID(boot) {
		return Inventory{}, errors.New("the kernel's boot id could not be read")
	}
	inv := Inventory{Schema: Schema, BootID: boot, Disks: []Disk{}}
	reasons := systemReasons(objects)
	for _, path := range diskPaths(objects) {
		d := readDisk(objects, kernel, boot, path)
		d.SystemReasons = reasons[path]
		d.System = len(d.SystemReasons) > 0
		inv.Disks = append(inv.Disks, d)
	}
	markAmbiguous(inv.Disks)
	offerOperations(newCapabilities(ctx, bus), inv.Disks)
	if lvmErr != nil {
		inv.LVM = LVM{State: LVMUnavailable, Reason: ReasonLVMModuleUnavailable}
	} else {
		inv.LVM = lvmView(objects, kernel)
	}
	return inv, nil
}

// lvmModule is the name of UDisks2's LVM2 module.
const lvmModule = "lvm2"

// ReasonLVMModuleUnavailable states that the LVM2 module could not be enabled, so nothing about
// LVM was read.
const ReasonLVMModuleUnavailable = "lvm_module_unavailable"

// lvmView lists the volume groups by name, each with its physical volumes and its logical
// volumes.
func lvmView(objects Objects, kernel Kernel) LVM {
	view := LVM{State: LVMRead}
	var groups []string
	for path := range objects.VolumeGroups {
		groups = append(groups, path)
	}
	slices.SortFunc(groups, func(a, b string) int {
		return strings.Compare(objects.VolumeGroups[a].Name+"\x00"+a, objects.VolumeGroups[b].Name+"\x00"+b)
	})
	for _, path := range groups {
		vg := objects.VolumeGroups[path]
		g := VolumeGroupView{Name: vg.Name, UUID: vg.UUID, Size: vg.Size, Free: vg.FreeSize, PhysicalVolumes: []string{},
			Missing: slices.Clone(vg.MissingPhysicalVolumes), LogicalVolumes: []LogicalVolumeView{}}
		for _, pv := range physicalVolumes(objects, path) {
			g.PhysicalVolumes = append(g.PhysicalVolumes, objects.Blocks[pv].Device)
		}
		slices.Sort(g.PhysicalVolumes)
		var volumes []string
		for lvPath, lv := range objects.LogicalVolumes {
			if lv.VolumeGroup == path {
				volumes = append(volumes, lvPath)
			}
		}
		slices.SortFunc(volumes, func(a, b string) int {
			return strings.Compare(objects.LogicalVolumes[a].Name+"\x00"+a, objects.LogicalVolumes[b].Name+"\x00"+b)
		})
		for _, lvPath := range volumes {
			lv := objects.LogicalVolumes[lvPath]
			v := LogicalVolumeView{Name: lv.Name, UUID: lv.UUID, Type: lv.Type, Active: lv.Active, Size: lv.Size, Consumers: []Consumer{}}
			if block, ok := objects.Blocks[lv.BlockDevice]; ok && isObject(lv.BlockDevice) {
				v.Device, v.Filesystem = block.Device, filesystemOf(block)
				v.Consumers = consumersOf(objects, kernel, lv.BlockDevice)
			}
			g.LogicalVolumes = append(g.LogicalVolumes, v)
		}
		view.VolumeGroups = append(view.VolumeGroups, g)
	}
	return view
}

// physicalVolumes returns the object paths of the blocks that are physical volumes of the
// volume group at path.
func physicalVolumes(objects Objects, path string) []string {
	var pvs []string
	for p, b := range objects.Blocks {
		if b.PhysicalVolume != nil && b.PhysicalVolume.VolumeGroup == path {
			pvs = append(pvs, p)
		}
	}
	slices.Sort(pvs)
	return pvs
}

// markAmbiguous marks every disk whose identity another disk shares: it names no one disk,
// so none of them has a target.
func markAmbiguous(disks []Disk) {
	count := map[string]int{}
	for _, d := range disks {
		if d.Identity.Digest != "" {
			count[d.Identity.Digest]++
		}
	}
	for i := range disks {
		if count[disks[i].Identity.Digest] > 1 {
			disks[i].Identity.Ambiguous = true
			disks[i].Target = ""
		}
	}
}

// isObject reports whether path names an object: UDisks2 writes "/" for none.
func isObject(path string) bool { return path != "" && path != NoObject }

// isDisk reports whether b is a disk or a loop device with a size: not a partition, not a
// logical volume and not the cleartext side of an encrypted device.
func isDisk(b Block) bool {
	return b.Size > 0 && b.Partition == nil && b.LogicalVolume == "" && !isObject(b.CryptoBackingDevice) &&
		(isObject(b.Drive) || b.Loop != nil)
}

// diskPaths returns the object paths of the disks, ordered by device node.
func diskPaths(objects Objects) []string {
	var paths []string
	for path, b := range objects.Blocks {
		if isDisk(b) {
			paths = append(paths, path)
		}
	}
	slices.SortFunc(paths, func(a, b string) int {
		if c := strings.Compare(objects.Blocks[a].Device, objects.Blocks[b].Device); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return paths
}

// partitionPaths returns the object paths of the partitions of the table at path, in
// partition number order.
func partitionPaths(objects Objects, path string) []string {
	var paths []string
	for p, b := range objects.Blocks {
		if b.Partition != nil && b.Partition.Table == path {
			paths = append(paths, p)
		}
	}
	slices.SortFunc(paths, func(a, b string) int {
		na, nb := objects.Blocks[a].Partition.Number, objects.Blocks[b].Partition.Number
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		}
		return strings.Compare(a, b)
	})
	return paths
}

// filesystemOf returns what probing found on b when it is a filesystem.
func filesystemOf(b Block) *Filesystem {
	if b.IDUsage != "filesystem" {
		return nil
	}
	fs := &Filesystem{Type: b.IDType, UUID: b.IDUUID, Label: b.IDLabel}
	if b.Filesystem != nil && len(b.Filesystem.MountPoints) > 0 {
		fs.MountPoints = slices.Clone(b.Filesystem.MountPoints)
	}
	return fs
}

func readDisk(objects Objects, kernel Kernel, boot, path string) Disk {
	b := objects.Blocks[path]
	d := Disk{Device: b.Device, Size: b.Size, HintSystem: b.HintSystem, Filesystem: filesystemOf(b),
		Consumers: consumersOf(objects, kernel, path), Partitions: []Partition{}, Operations: []Operation{}}
	if drive, ok := objects.Drives[b.Drive]; ok {
		d.Vendor, d.Model = drive.Vendor, drive.Model
	}
	if b.Loop != nil {
		d.BackingFile = b.Loop.BackingFile
	}
	if b.PartitionTable != nil {
		d.PartitionTable = b.PartitionTable.Type
	}
	var uuids []string
	for _, pp := range partitionPaths(objects, path) {
		p := objects.Blocks[pp]
		d.Partitions = append(d.Partitions, Partition{Device: p.Device, Number: p.Partition.Number, Offset: p.Partition.Offset,
			Size: p.Partition.Size, UUID: p.Partition.UUID, Type: p.Partition.Type, Filesystem: filesystemOf(p),
			Consumers: consumersOf(objects, kernel, pp), Operations: []Operation{}})
		if p.Partition.UUID != "" {
			uuids = append(uuids, p.Partition.UUID)
		}
	}
	d.Identity = identify(objects, kernel, boot, b, uuids)
	if d.Identity.Digest != "" {
		d.Target = "drive:" + d.Identity.Digest
	}
	return d
}

// maxDepth bounds the chain from a mounted device down to its disk: partition, encryption and
// logical volume, each at most a few levels deep.
const maxDepth = 8

// maxHolders bounds the holders read for one device.
const maxHolders = 64

// systemMount reports whether a filesystem mounted at point makes its disk the system disk.
func systemMount(point string) bool {
	return point == "/" || point == "/boot" || point == "/boot/efi" || strings.HasPrefix(point, "/var/lib/olivares")
}

// systemReasons returns, for each disk that holds a system mount point or active swap, the
// sorted mount points and "swap".
func systemReasons(objects Objects) map[string][]string {
	found := map[string]map[string]bool{}
	for path, b := range objects.Blocks {
		var why []string
		if b.Filesystem != nil {
			for _, point := range b.Filesystem.MountPoints {
				if systemMount(point) {
					why = append(why, point)
				}
			}
		}
		if b.Swapspace != nil && b.Swapspace.Active {
			why = append(why, "swap")
		}
		if len(why) == 0 {
			continue
		}
		for _, disk := range disksUnder(objects, path, 0) {
			if found[disk] == nil {
				found[disk] = map[string]bool{}
			}
			for _, reason := range why {
				found[disk][reason] = true
			}
		}
	}
	out := map[string][]string{}
	for disk, set := range found {
		for reason := range set {
			out[disk] = append(out[disk], reason)
		}
		slices.Sort(out[disk])
	}
	return out
}

// disksUnder returns the disks a block device lies on: through its partition table, the device
// its encryption is backed by, and the physical volumes of its volume group.
func disksUnder(objects Objects, path string, depth int) []string {
	b, ok := objects.Blocks[path]
	if !ok || depth > maxDepth {
		return nil
	}
	switch {
	case b.Partition != nil:
		return disksUnder(objects, b.Partition.Table, depth+1)
	case isObject(b.CryptoBackingDevice):
		return disksUnder(objects, b.CryptoBackingDevice, depth+1)
	case b.LogicalVolume != "":
		lv, ok := objects.LogicalVolumes[b.LogicalVolume]
		if !ok {
			return nil
		}
		var disks []string
		for _, pv := range physicalVolumes(objects, lv.VolumeGroup) {
			disks = append(disks, disksUnder(objects, pv, depth+1)...)
		}
		slices.Sort(disks)
		return slices.Compact(disks)
	case isDisk(b):
		return []string{path}
	}
	return nil
}

// consumersOf lists what uses the block device at path, ordered by kind and name: its mount
// points, active swap, its volume group (unread when the LVM2 module could not describe an LVM2
// member), RAID membership, the cleartext device of its encryption, and each holder the kernel
// reports. A holder list the kernel does not give is one unread holder.
func consumersOf(objects Objects, kernel Kernel, path string) []Consumer {
	b := objects.Blocks[path]
	out := []Consumer{}
	if b.Filesystem != nil {
		for _, point := range b.Filesystem.MountPoints {
			out = append(out, Consumer{Kind: ConsumerMount, Name: point})
		}
	}
	if b.Swapspace != nil && b.Swapspace.Active {
		out = append(out, Consumer{Kind: ConsumerSwap, Name: b.Device})
	}
	switch {
	case b.PhysicalVolume != nil:
		name := Unread
		if vg, ok := objects.VolumeGroups[b.PhysicalVolume.VolumeGroup]; ok && vg.Name != "" {
			name = vg.Name
		}
		out = append(out, Consumer{Kind: ConsumerPhysicalVolume, Name: name})
	case b.IDType == "LVM2_member":
		out = append(out, Consumer{Kind: ConsumerPhysicalVolume, Name: Unread})
	}
	if b.IDType == "linux_raid_member" {
		name := b.IDLabel
		if name == "" {
			name = Unread
		}
		out = append(out, Consumer{Kind: ConsumerRaidMember, Name: name})
	}
	for _, other := range objects.Blocks {
		if other.CryptoBackingDevice == path {
			out = append(out, Consumer{Kind: ConsumerEncrypted, Name: other.Device})
		}
	}
	holders, err := kernel.Holders(b.Device, b.DeviceNumber)
	if err != nil || len(holders) > maxHolders {
		holders = []string{Unread}
	}
	for _, holder := range holders {
		out = append(out, Consumer{Kind: ConsumerHolder, Name: holder})
	}
	slices.SortFunc(out, func(a, b Consumer) int {
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return slices.Compact(out)
}
