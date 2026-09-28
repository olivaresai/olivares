// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import "context"

// Bus is UDisks2 as the inventory reads it. The installed appliance's adapter is SystemBus.
type Bus interface {
	// EnableModule asks UDisks2 to load one module, which attaches the module's interfaces to
	// the objects before it returns. It changes no disk.
	EnableModule(ctx context.Context, name string) error
	// Objects returns UDisks2's managed objects, bounded by MaxBlocks.
	Objects(ctx context.Context) (Objects, error)
	// Can asks the manager whether the host can perform check for filesystem type fstype.
	// An error means the host does not report it.
	Can(ctx context.Context, check Check, fstype string) (Capability, error)
}

// Kernel is what the inventory reads from the kernel. Each device is named by its node, as
// UDisks2 reported it, and its device number; an adapter opens only a node whose device number
// is that one. The installed appliance's adapter is LinuxKernel.
type Kernel interface {
	// BootID returns the kernel's boot id.
	BootID() (string, error)
	// LoopStatus returns what LOOP_GET_STATUS64 and the logical block size report for a loop
	// device.
	LoopStatus(device string, number uint64) (LoopStatus, error)
	// FirstMiB returns the lowercase hexadecimal SHA-256 of the device's first MiB (or of the
	// whole device when it is smaller).
	FirstMiB(device string, number uint64) (string, error)
	// Holders returns the kernel names of the devices that hold device, from sysfs.
	Holders(device string, number uint64) ([]string, error)
}

// Check names one of the manager's capability questions.
type Check string

// The capability questions the inventory asks: Manager.CanFormat, CanResize, CanCheck and
// CanRepair.
const (
	CanFormat Check = "format"
	CanResize Check = "resize"
	CanCheck  Check = "check"
	CanRepair Check = "repair"
)

// Resize mode flags, as Manager.CanResize reports them (libblockdev's BDFSResizeFlags).
const (
	ResizeOfflineShrink uint64 = 2
	ResizeOfflineGrow   uint64 = 4
	ResizeOnlineShrink  uint64 = 8
	ResizeOnlineGrow    uint64 = 16
)

// Capability is one answer of the manager: whether the operation is available, the resize
// modes for CanResize, and the program it lacks when it is not.
type Capability struct {
	Available bool
	Modes     uint64
	Utility   string
}

// LoopStatus is what the kernel reports for a bound loop device.
type LoopStatus struct {
	BackingDevice uint64
	BackingInode  uint64
	Offset        uint64
	SizeLimit     uint64
	BlockSize     uint32
}

// MaxBlocks bounds the block objects one inventory reads.
const MaxBlocks = 256

// NoObject is UDisks2's object path for "no such object".
const NoObject = "/"

// Objects are UDisks2's managed objects the inventory uses, by object path.
type Objects struct {
	Drives         map[string]Drive
	Blocks         map[string]Block
	VolumeGroups   map[string]VolumeGroup
	LogicalVolumes map[string]LogicalVolume
}

// Drive is org.freedesktop.UDisks2.Drive.
type Drive struct {
	WWN    string
	Serial string
	ID     string
	Vendor string
	Model  string
}

// Block is org.freedesktop.UDisks2.Block with the interfaces other objects attach to it.
type Block struct {
	Device              string
	DeviceNumber        uint64
	ID                  string
	Size                uint64
	Drive               string
	Symlinks            []string
	IDUsage             string
	IDType              string
	IDUUID              string
	IDLabel             string
	HintSystem          bool
	CryptoBackingDevice string
	Partition           *PartitionFacts
	PartitionTable      *PartitionTableFacts
	Filesystem          *FilesystemFacts
	Swapspace           *SwapspaceFacts
	Loop                *LoopFacts
	PhysicalVolume      *PhysicalVolumeFacts
	// LogicalVolume is org.freedesktop.UDisks2.Block.LVM2's LogicalVolume, "" when absent.
	LogicalVolume string
}

// PartitionFacts is org.freedesktop.UDisks2.Partition.
type PartitionFacts struct {
	Number uint32
	Type   string
	Offset uint64
	Size   uint64
	UUID   string
	Table  string
}

// PartitionTableFacts is org.freedesktop.UDisks2.PartitionTable.
type PartitionTableFacts struct {
	Type string
}

// FilesystemFacts is org.freedesktop.UDisks2.Filesystem.
type FilesystemFacts struct {
	MountPoints []string
	Size        uint64
}

// SwapspaceFacts is org.freedesktop.UDisks2.Swapspace.
type SwapspaceFacts struct {
	Active bool
}

// LoopFacts is org.freedesktop.UDisks2.Loop. BackingFile is shown, never trusted as identity.
type LoopFacts struct {
	BackingFile string
}

// PhysicalVolumeFacts is org.freedesktop.UDisks2.PhysicalVolume (LVM2 module).
type PhysicalVolumeFacts struct {
	VolumeGroup string
	Size        uint64
	FreeSize    uint64
}

// VolumeGroup is org.freedesktop.UDisks2.VolumeGroup (LVM2 module).
type VolumeGroup struct {
	Name                   string
	UUID                   string
	Size                   uint64
	FreeSize               uint64
	ExtentSize             uint64
	MissingPhysicalVolumes []string
}

// LogicalVolume is org.freedesktop.UDisks2.LogicalVolume (LVM2 module).
type LogicalVolume struct {
	VolumeGroup string
	Name        string
	UUID        string
	Type        string
	Active      bool
	Size        uint64
	BlockDevice string
}
