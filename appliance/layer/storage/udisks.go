// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import (
	"context"
	"errors"
	"strings"

	"github.com/godbus/dbus/v5"
)

// UDisks2's bus name, object paths and interfaces the inventory reads.
const (
	udisksName       = "org.freedesktop.UDisks2"
	udisksRoot       = "/org/freedesktop/UDisks2"
	udisksManager    = "/org/freedesktop/UDisks2/Manager"
	ifManager        = "org.freedesktop.UDisks2.Manager"
	ifDrive          = "org.freedesktop.UDisks2.Drive"
	ifBlock          = "org.freedesktop.UDisks2.Block"
	ifPartition      = "org.freedesktop.UDisks2.Partition"
	ifPartitionTable = "org.freedesktop.UDisks2.PartitionTable"
	ifFilesystem     = "org.freedesktop.UDisks2.Filesystem"
	ifSwapspace      = "org.freedesktop.UDisks2.Swapspace"
	ifLoop           = "org.freedesktop.UDisks2.Loop"
	ifPhysicalVolume = "org.freedesktop.UDisks2.PhysicalVolume"
	ifBlockLVM2      = "org.freedesktop.UDisks2.Block.LVM2"
	ifVolumeGroup    = "org.freedesktop.UDisks2.VolumeGroup"
	ifLogicalVolume  = "org.freedesktop.UDisks2.LogicalVolume"
)

// Bounds of what one reading of UDisks2 accepts.
const (
	maxObjects   = 4 * MaxBlocks
	maxText      = 4096
	maxTextItems = 64
)

// SystemBus is UDisks2 on the system bus, through one private connection. It calls
// GetManagedObjects, EnableModule and the manager's Can methods, and no other method.
type SystemBus struct{ conn *dbus.Conn }

// DialSystemBus opens a private, authenticated connection to the system bus.
func DialSystemBus(ctx context.Context) (*SystemBus, error) {
	conn, err := dbus.SystemBusPrivate(dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &SystemBus{conn: conn}, nil
}

// Close closes the connection.
func (b *SystemBus) Close() error { return b.conn.Close() }

// EnableModule implements Bus with Manager.EnableModule(name, true).
func (b *SystemBus) EnableModule(ctx context.Context, name string) error {
	return b.conn.Object(udisksName, udisksManager).CallWithContext(ctx, ifManager+".EnableModule", 0, name, true).Err
}

// Objects implements Bus with ObjectManager.GetManagedObjects.
func (b *SystemBus) Objects(ctx context.Context) (Objects, error) {
	var managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err := b.conn.Object(udisksName, udisksRoot).
		CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&managed)
	if err != nil {
		return Objects{}, err
	}
	return parseObjects(managed)
}

// Can implements Bus with Manager.CanFormat, CanResize, CanCheck and CanRepair.
func (b *SystemBus) Can(ctx context.Context, check Check, fstype string) (Capability, error) {
	manager := b.conn.Object(udisksName, udisksManager)
	switch check {
	case CanResize:
		var answer struct {
			Available bool
			Modes     uint64
			Utility   string
		}
		if err := manager.CallWithContext(ctx, ifManager+".CanResize", 0, fstype).Store(&answer); err != nil {
			return Capability{}, err
		}
		return Capability{Available: answer.Available, Modes: answer.Modes, Utility: answer.Utility}, nil
	case CanFormat, CanCheck, CanRepair:
		method := map[Check]string{CanFormat: "CanFormat", CanCheck: "CanCheck", CanRepair: "CanRepair"}[check]
		var answer struct {
			Available bool
			Utility   string
		}
		if err := manager.CallWithContext(ctx, ifManager+"."+method, 0, fstype).Store(&answer); err != nil {
			return Capability{}, err
		}
		return Capability{Available: answer.Available, Utility: answer.Utility}, nil
	}
	return Capability{}, errors.New("not a capability question of the manager")
}

// parseObjects keeps the interfaces the inventory uses. A property of another type than the
// interface's is blank.
func parseObjects(managed map[dbus.ObjectPath]map[string]map[string]dbus.Variant) (Objects, error) {
	errTooMany := errors.New("UDisks2 reports more objects than one inventory reads")
	if len(managed) > maxObjects {
		return Objects{}, errTooMany
	}
	objects := Objects{Drives: map[string]Drive{}, Blocks: map[string]Block{},
		VolumeGroups: map[string]VolumeGroup{}, LogicalVolumes: map[string]LogicalVolume{}}
	for objectPath, ifaces := range managed {
		path := string(objectPath)
		if p, ok := ifaces[ifDrive]; ok {
			objects.Drives[path] = Drive{WWN: text(p, "WWN"), Serial: text(p, "Serial"), ID: text(p, "Id"),
				Vendor: text(p, "Vendor"), Model: text(p, "Model")}
		}
		if p, ok := ifaces[ifBlock]; ok {
			b := Block{Device: bytestring(p, "Device"), DeviceNumber: u64(p, "DeviceNumber"), ID: text(p, "Id"),
				Size: u64(p, "Size"), Drive: object(p, "Drive"), Symlinks: bytestrings(p, "Symlinks"),
				IDUsage: text(p, "IdUsage"), IDType: text(p, "IdType"), IDUUID: text(p, "IdUUID"), IDLabel: text(p, "IdLabel"),
				HintSystem: boolean(p, "HintSystem"), CryptoBackingDevice: object(p, "CryptoBackingDevice")}
			if q, ok := ifaces[ifPartition]; ok {
				b.Partition = &PartitionFacts{Number: u32(q, "Number"), Type: text(q, "Type"), Offset: u64(q, "Offset"),
					Size: u64(q, "Size"), UUID: text(q, "UUID"), Table: object(q, "Table")}
			}
			if q, ok := ifaces[ifPartitionTable]; ok {
				b.PartitionTable = &PartitionTableFacts{Type: text(q, "Type")}
			}
			if q, ok := ifaces[ifFilesystem]; ok {
				b.Filesystem = &FilesystemFacts{MountPoints: bytestrings(q, "MountPoints"), Size: u64(q, "Size")}
			}
			if q, ok := ifaces[ifSwapspace]; ok {
				b.Swapspace = &SwapspaceFacts{Active: boolean(q, "Active")}
			}
			if q, ok := ifaces[ifLoop]; ok {
				b.Loop = &LoopFacts{BackingFile: bytestring(q, "BackingFile")}
			}
			if q, ok := ifaces[ifPhysicalVolume]; ok {
				b.PhysicalVolume = &PhysicalVolumeFacts{VolumeGroup: object(q, "VolumeGroup"), Size: u64(q, "Size"), FreeSize: u64(q, "FreeSize")}
			}
			if q, ok := ifaces[ifBlockLVM2]; ok {
				if lv := object(q, "LogicalVolume"); isObject(lv) {
					b.LogicalVolume = lv
				}
			}
			objects.Blocks[path] = b
		}
		if p, ok := ifaces[ifVolumeGroup]; ok {
			objects.VolumeGroups[path] = VolumeGroup{Name: text(p, "Name"), UUID: text(p, "UUID"), Size: u64(p, "Size"),
				FreeSize: u64(p, "FreeSize"), ExtentSize: u64(p, "ExtentSize"), MissingPhysicalVolumes: texts(p, "MissingPhysicalVolumes")}
		}
		if p, ok := ifaces[ifLogicalVolume]; ok {
			objects.LogicalVolumes[path] = LogicalVolume{VolumeGroup: object(p, "VolumeGroup"), Name: text(p, "Name"),
				UUID: text(p, "UUID"), Type: text(p, "Type"), Active: boolean(p, "Active"), Size: u64(p, "Size"),
				BlockDevice: object(p, "BlockDevice")}
		}
	}
	if len(objects.Blocks) > MaxBlocks {
		return Objects{}, errTooMany
	}
	return objects, nil
}

func text(p map[string]dbus.Variant, name string) string {
	s, _ := p[name].Value().(string)
	return boundedText(s)
}

func boundedText(s string) string {
	if len(s) > maxText || strings.ContainsRune(s, 0) {
		return ""
	}
	return s
}

func texts(p map[string]dbus.Variant, name string) []string {
	list, ok := p[name].Value().([]string)
	if !ok || len(list) > maxTextItems {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, boundedText(s))
	}
	return out
}

// bytestring reads a D-Bus bytestring (ay), which carries a terminating NUL.
func bytestring(p map[string]dbus.Variant, name string) string {
	b, _ := p[name].Value().([]byte)
	return boundedText(strings.TrimRight(string(b), "\x00"))
}

func bytestrings(p map[string]dbus.Variant, name string) []string {
	list, ok := p[name].Value().([][]byte)
	if !ok || len(list) > maxTextItems {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, b := range list {
		out = append(out, boundedText(strings.TrimRight(string(b), "\x00")))
	}
	return out
}

func u64(p map[string]dbus.Variant, name string) uint64 {
	n, _ := p[name].Value().(uint64)
	return n
}

func u32(p map[string]dbus.Variant, name string) uint32 {
	n, _ := p[name].Value().(uint32)
	return n
}

func boolean(p map[string]dbus.Variant, name string) bool {
	b, _ := p[name].Value().(bool)
	return b
}

func object(p map[string]dbus.Variant, name string) string {
	o, _ := p[name].Value().(dbus.ObjectPath)
	return boundedText(string(o))
}
