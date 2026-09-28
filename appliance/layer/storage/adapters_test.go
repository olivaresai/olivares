// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/godbus/dbus/v5"
)

type interfaces = map[string]map[string]dbus.Variant

func TestUDisks_ParsesTheManagedObjectsTheInventoryUses(t *testing.T) {
	v := dbus.MakeVariant
	managed := map[dbus.ObjectPath]interfaces{
		"/org/freedesktop/UDisks2/drives/disk0": {"org.freedesktop.UDisks2.Drive": {
			"WWN": v(""), "Serial": v("S1"), "Id": v("VENDOR-MODEL-S1"), "Vendor": v("VENDOR"), "Model": v("MODEL")}},
		"/org/freedesktop/UDisks2/block_devices/sda2": {
			"org.freedesktop.UDisks2.Block": {
				"Device": v([]byte("/dev/sda2\x00")), "DeviceNumber": v(uint64(8<<8 | 2)), "Id": v("by-id-part2"), "Size": v(uint64(1 << 30)),
				"Drive":    v(dbus.ObjectPath("/org/freedesktop/UDisks2/drives/disk0")),
				"Symlinks": v([][]byte{[]byte("/dev/disk/by-path/pci-0000:00:1f.2-ata-1-part2\x00"), []byte("/dev/disk/by-partuuid/e6d6\x00")}),
				"IdUsage":  v("raid"), "IdType": v("LVM2_member"), "IdUUID": v("pv-uuid"), "IdLabel": v(""),
				"HintSystem": v(true), "CryptoBackingDevice": v(dbus.ObjectPath("/"))},
			"org.freedesktop.UDisks2.Partition": {"Number": v(uint32(2)), "Type": v("e6d6d379-f507-44c2-a23c-238f2a3df928"),
				"Offset": v(uint64(1 << 20)), "Size": v(uint64(1 << 30)), "UUID": v("part-uuid"),
				"Table": v(dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/sda"))},
			"org.freedesktop.UDisks2.PhysicalVolume": {"VolumeGroup": v(dbus.ObjectPath("/org/freedesktop/UDisks2/lvm/system")),
				"Size": v(uint64(1 << 30)), "FreeSize": v(uint64(0))},
		},
		"/org/freedesktop/UDisks2/block_devices/dm_2d0": {
			"org.freedesktop.UDisks2.Block": {"Device": v([]byte("/dev/dm-0\x00")), "Size": v(uint64(4 << 30)),
				"Drive": v(dbus.ObjectPath("/")), "CryptoBackingDevice": v(dbus.ObjectPath("/")), "IdUsage": v("filesystem"), "IdType": v("ext4")},
			"org.freedesktop.UDisks2.Filesystem": {"MountPoints": v([][]byte{[]byte("/\x00")}), "Size": v(uint64(4 << 30))},
			"org.freedesktop.UDisks2.Block.LVM2": {"LogicalVolume": v(dbus.ObjectPath("/org/freedesktop/UDisks2/lvm/system/root"))},
		},
		"/org/freedesktop/UDisks2/block_devices/loop0": {
			"org.freedesktop.UDisks2.Block":     {"Device": v([]byte("/dev/loop0\x00")), "Size": v(uint64(64 << 20)), "Drive": v(dbus.ObjectPath("/"))},
			"org.freedesktop.UDisks2.Loop":      {"BackingFile": v([]byte("/var/tmp/a.img\x00"))},
			"org.freedesktop.UDisks2.Swapspace": {"Active": v(false)},
		},
		"/org/freedesktop/UDisks2/lvm/system": {"org.freedesktop.UDisks2.VolumeGroup": {"Name": v("system"), "UUID": v("vg-uuid"),
			"Size": v(uint64(1 << 30)), "FreeSize": v(uint64(0)), "ExtentSize": v(uint64(4 << 20)), "MissingPhysicalVolumes": v([]string{})}},
		"/org/freedesktop/UDisks2/lvm/system/root": {"org.freedesktop.UDisks2.LogicalVolume": {"VolumeGroup": v(dbus.ObjectPath("/org/freedesktop/UDisks2/lvm/system")),
			"Name": v("root"), "UUID": v("lv-uuid"), "Type": v("block"), "Active": v(true), "Size": v(uint64(4 << 30)),
			"BlockDevice": v(dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/dm_2d0"))}},
		"/org/freedesktop/UDisks2/Manager": {"org.freedesktop.UDisks2.Manager": {"Version": v("2.10.1")}},
	}
	objects, err := parseObjects(managed)
	if err != nil {
		t.Fatal(err)
	}
	want := Objects{
		Drives: map[string]Drive{"/org/freedesktop/UDisks2/drives/disk0": {Serial: "S1", ID: "VENDOR-MODEL-S1", Vendor: "VENDOR", Model: "MODEL"}},
		Blocks: map[string]Block{
			"/org/freedesktop/UDisks2/block_devices/sda2": {Device: "/dev/sda2", DeviceNumber: 8<<8 | 2, ID: "by-id-part2", Size: 1 << 30,
				Drive:    "/org/freedesktop/UDisks2/drives/disk0",
				Symlinks: []string{"/dev/disk/by-path/pci-0000:00:1f.2-ata-1-part2", "/dev/disk/by-partuuid/e6d6"},
				IDUsage:  "raid", IDType: "LVM2_member", IDUUID: "pv-uuid", HintSystem: true, CryptoBackingDevice: "/",
				Partition: &PartitionFacts{Number: 2, Type: "e6d6d379-f507-44c2-a23c-238f2a3df928", Offset: 1 << 20, Size: 1 << 30,
					UUID: "part-uuid", Table: "/org/freedesktop/UDisks2/block_devices/sda"},
				PhysicalVolume: &PhysicalVolumeFacts{VolumeGroup: "/org/freedesktop/UDisks2/lvm/system", Size: 1 << 30}},
			"/org/freedesktop/UDisks2/block_devices/dm_2d0": {Device: "/dev/dm-0", Size: 4 << 30, Drive: "/", CryptoBackingDevice: "/",
				IDUsage: "filesystem", IDType: "ext4", Filesystem: &FilesystemFacts{MountPoints: []string{"/"}, Size: 4 << 30},
				LogicalVolume: "/org/freedesktop/UDisks2/lvm/system/root"},
			"/org/freedesktop/UDisks2/block_devices/loop0": {Device: "/dev/loop0", Size: 64 << 20, Drive: "/",
				Loop: &LoopFacts{BackingFile: "/var/tmp/a.img"}, Swapspace: &SwapspaceFacts{}},
		},
		VolumeGroups: map[string]VolumeGroup{"/org/freedesktop/UDisks2/lvm/system": {Name: "system", UUID: "vg-uuid", Size: 1 << 30,
			ExtentSize: 4 << 20, MissingPhysicalVolumes: []string{}}},
		LogicalVolumes: map[string]LogicalVolume{"/org/freedesktop/UDisks2/lvm/system/root": {VolumeGroup: "/org/freedesktop/UDisks2/lvm/system",
			Name: "root", UUID: "lv-uuid", Type: "block", Active: true, Size: 4 << 30, BlockDevice: "/org/freedesktop/UDisks2/block_devices/dm_2d0"}},
	}
	if !reflect.DeepEqual(objects, want) {
		t.Fatalf("parsed\n %+v\nwant\n %+v", objects, want)
	}

	// A wrongly typed property is blank, never a panic; too many block objects refuse the read.
	objects, err = parseObjects(map[dbus.ObjectPath]interfaces{"/org/freedesktop/UDisks2/block_devices/x": {
		"org.freedesktop.UDisks2.Block": {"Device": v("not a bytestring"), "Size": v("big"), "Symlinks": v([]string{"/dev/x"})}}})
	if err != nil || objects.Blocks["/org/freedesktop/UDisks2/block_devices/x"].Device != "" || objects.Blocks["/org/freedesktop/UDisks2/block_devices/x"].Size != 0 {
		t.Fatalf("%+v %v", objects, err)
	}
	many := map[dbus.ObjectPath]interfaces{}
	for i := 0; i <= MaxBlocks; i++ {
		many[dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/b"+strconv.Itoa(i))] = interfaces{"org.freedesktop.UDisks2.Block": {}}
	}
	if _, err := parseObjects(many); err == nil {
		t.Fatalf("%d block objects were read", len(many))
	}
}

func TestKernel_OpensOnlyADeviceNodeOfTheReportedNumber(t *testing.T) {
	for node, want := range map[string]bool{
		"/dev/sda": true, "/dev/nvme0n1p2": true, "/dev/vda": true, "/dev/loop7": true, "/dev/mmcblk0p1": true,
		"/dev/../etc/shadow": false, "/dev/sda/..": false, "/etc/shadow": false, "/dev/": false, "/dev/dm-0": true, "/dev/-x": false,
		"/dev/disk/by-path/pci-0000:00:04.0": false, "dev/sda": false, "/dev/SDA": false,
	} {
		if got := deviceNode(node); got != want {
			t.Errorf("deviceNode(%q) = %v", node, got)
		}
	}
	for node, want := range map[string]bool{"/dev/loop0": true, "/dev/loop12": true, "/dev/sda": false, "/dev/loop": false, "/dev/loopx": false} {
		if got := loopNode(node); got != want {
			t.Errorf("loopNode(%q) = %v", node, got)
		}
	}
	for _, tc := range []struct {
		sysfs  string
		number uint64
		want   bool
	}{
		{"8:16\n", 8<<8 | 16, true},
		{"259:1\n", 259<<8 | 1, true},
		{"8:300\n", (300 & 0xff) | (300&^0xff)<<12 | 8<<8, true},
		{"4097:0\n", (4097&0xfff)<<8 | (4097&^0xfff)<<32, true},
		// Independent literals at each split in glibc's 32-bit major/minor encoding.
		{"4095:255\n", 0x00000000000fffff, true},
		{"4096:256\n", 0x0000100000100000, true},
		{"0:4294967295\n", 0x00000ffffff000ff, true},
		{"4294967295:0\n", 0xfffff000000fff00, true},
		{"4294967295:4294967295\n", 0xffffffffffffffff, true},
		{"4096:0\n", 0x0000100000100000, false},
		{"0:256\n", 0x0000100000100000, false},
		{"8:16\n", 8<<8 | 17, false},
		{"1:16\n", 8<<8 | 16, false},
		{"8:16:1\n", 8<<8 | 16, false},
		{" 8:16\n", 8<<8 | 16, false},
		{"", 0, false},
	} {
		if got := sysfsDevIs(tc.sysfs, tc.number); got != tc.want {
			t.Errorf("sysfsDevIs(%q, %#x) = %v", tc.sysfs, tc.number, got)
		}
	}
}
