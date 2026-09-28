// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/storage"
)

const blockDevices = "/org/freedesktop/UDisks2/block_devices/"

// host is a UDisks2 object set built one object at a time.
type host struct{ objects storage.Objects }

func newHost() *host { return &host{objects: newObjects()} }

func (h *host) disk(name, serial string, number, size uint64, table string) {
	drive := "/org/freedesktop/UDisks2/drives/" + serial
	h.objects.Drives[drive] = storage.Drive{Serial: serial}
	b := storage.Block{Device: "/dev/" + name, DeviceNumber: number, Size: size, Drive: drive, CryptoBackingDevice: storage.NoObject}
	if table != "" {
		b.PartitionTable = &storage.PartitionTableFacts{Type: table}
	}
	h.objects.Blocks[blockDevices+name] = b
}

func (h *host) partition(disk, name string, number uint32, uuid string) {
	parent := h.objects.Blocks[blockDevices+disk]
	h.objects.Blocks[blockDevices+name] = storage.Block{Device: "/dev/" + name, DeviceNumber: parent.DeviceNumber + uint64(number),
		Size: 1 << 30, Drive: parent.Drive, CryptoBackingDevice: storage.NoObject,
		Partition: &storage.PartitionFacts{Number: number, Offset: uint64(number) << 30, Size: 1 << 30, UUID: uuid, Table: blockDevices + disk}}
}

// with changes the block named name.
func (h *host) with(name string, change func(*storage.Block)) {
	b := h.objects.Blocks[blockDevices+name]
	change(&b)
	h.objects.Blocks[blockDevices+name] = b
}

func mounted(fstype string, points ...string) func(*storage.Block) {
	return func(b *storage.Block) {
		b.IDUsage, b.IDType, b.IDUUID = "filesystem", fstype, "uuid-of-"+b.Device
		b.Filesystem = &storage.FilesystemFacts{MountPoints: points}
	}
}

func consumers(pairs ...string) []storage.Consumer {
	out := []storage.Consumer{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, storage.Consumer{Kind: pairs[i], Name: pairs[i+1]})
	}
	return out
}

func partitionNamed(t *testing.T, d storage.Disk, device string) storage.Partition {
	t.Helper()
	for _, p := range d.Partitions {
		if p.Device == device {
			return p
		}
	}
	t.Fatalf("%s has no partition %s", d.Device, device)
	return storage.Partition{}
}

func TestInventory_MarksTheSystemDiskAndEveryConsumer(t *testing.T) {
	h := newHost()
	// sda: the EFI system partition, /boot, an encrypted / and swap.
	h.disk("sda", "SYS0001", 8<<8, 64<<30, "gpt")
	h.partition("sda", "sda1", 1, "c12a7328-f81f-11d2-ba4b-00a0c93ec93b")
	h.partition("sda", "sda2", 2, "4f68bce3-e8cd-4db1-96e7-fbcaf984b709")
	h.partition("sda", "sda3", 3, "ca7d7ccb-63ed-4c53-861c-1742536059cc")
	h.partition("sda", "sda4", 4, "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f")
	h.with("sda1", mounted("vfat", "/boot/efi"))
	h.with("sda2", mounted("ext4", "/boot"))
	h.with("sda3", func(b *storage.Block) { b.IDUsage, b.IDType = "crypto", "crypto_LUKS" })
	h.objects.Blocks[blockDevices+"dm_2d0"] = storage.Block{Device: "/dev/dm-0", DeviceNumber: 253 << 8, Size: 1 << 30,
		Drive: storage.NoObject, CryptoBackingDevice: blockDevices + "sda3"}
	h.with("dm_2d0", mounted("ext4", "/"))
	h.with("sda4", func(b *storage.Block) {
		b.IDUsage, b.IDType = "other", "swap"
		b.Swapspace = &storage.SwapspaceFacts{Active: true}
	})
	// sdb: the product's state volume and a host mount.
	h.disk("sdb", "DATA0001", 8<<8|16, 64<<30, "gpt")
	h.partition("sdb", "sdb1", 1, "7d0359a3-02b3-4f4a-9a5c-2f8b1e3d4c5a")
	h.partition("sdb", "sdb2", 2, "8e1460b4-13c4-4a5b-8b6d-3a9c2f4e5d6b")
	h.with("sdb1", mounted("ext4", "/var/lib/olivares-data"))
	h.with("sdb2", mounted("xfs", "/srv/olivares/mnt/host/backup"))
	// sdc: a host mount and a RAID member held by md0.
	h.disk("sdc", "DATA0002", 8<<8|32, 64<<30, "gpt")
	h.partition("sdc", "sdc1", 1, "9f2571c5-24d5-4b6c-9c7e-4b0d3a5f6e7c")
	h.partition("sdc", "sdc2", 2, "a03682d6-35e6-4c7d-8d8f-5c1e4b6a7f8d")
	h.with("sdc1", mounted("ext4", "/srv/olivares/mnt/host/archive"))
	h.with("sdc2", func(b *storage.Block) { b.IDUsage, b.IDType, b.IDLabel = "raid", "linux_raid_member", "appliance:0" })
	h.objects.Blocks[blockDevices+"md0"] = storage.Block{Device: "/dev/md0", DeviceNumber: 9 << 8, Size: 1 << 30, Drive: storage.NoObject,
		CryptoBackingDevice: storage.NoObject}
	// sdd: empty. sde: one filesystem on the whole disk, mounted.
	h.disk("sdd", "DATA0003", 8<<8|48, 64<<30, "")
	h.disk("sde", "DATA0004", 8<<8|64, 64<<30, "")
	h.with("sde", mounted("ext4", "/srv/olivares/mnt/host/scratch"))
	kernel := &fakeKernel{boot: bootA, holders: map[string][]string{"/dev/sda3": {"dm-0"}, "/dev/sdc2": {"md0"}}}

	inv := read(t, &fakeBus{objects: h.objects}, kernel)
	var devices []string
	for _, d := range inv.Disks {
		devices = append(devices, d.Device)
	}
	if !reflect.DeepEqual(devices, []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd", "/dev/sde"}) {
		t.Fatalf("disks %v: dm-0 and md0 are consumers, not disks", devices)
	}

	sda := diskNamed(t, inv, "/dev/sda")
	if !sda.System || !reflect.DeepEqual(sda.SystemReasons, []string{"/", "/boot", "/boot/efi", "swap"}) {
		t.Fatalf("sda system %v reasons %v", sda.System, sda.SystemReasons)
	}
	sdb := diskNamed(t, inv, "/dev/sdb")
	if !sdb.System || !reflect.DeepEqual(sdb.SystemReasons, []string{"/var/lib/olivares-data"}) {
		t.Fatalf("sdb system %v reasons %v", sdb.System, sdb.SystemReasons)
	}
	for _, device := range []string{"/dev/sdc", "/dev/sdd", "/dev/sde"} {
		if d := diskNamed(t, inv, device); d.System || len(d.SystemReasons) != 0 {
			t.Fatalf("%s is marked system: %v", device, d.SystemReasons)
		}
	}

	want := map[string][]storage.Consumer{
		"/dev/sda1": consumers(storage.ConsumerMount, "/boot/efi"),
		"/dev/sda2": consumers(storage.ConsumerMount, "/boot"),
		"/dev/sda3": consumers(storage.ConsumerEncrypted, "/dev/dm-0", storage.ConsumerHolder, "dm-0"),
		"/dev/sda4": consumers(storage.ConsumerSwap, "/dev/sda4"),
		"/dev/sdb1": consumers(storage.ConsumerMount, "/var/lib/olivares-data"),
		"/dev/sdb2": consumers(storage.ConsumerMount, "/srv/olivares/mnt/host/backup"),
		"/dev/sdc1": consumers(storage.ConsumerMount, "/srv/olivares/mnt/host/archive"),
		"/dev/sdc2": consumers(storage.ConsumerHolder, "md0", storage.ConsumerRaidMember, "appliance:0"),
	}
	for _, d := range inv.Disks {
		for _, p := range d.Partitions {
			if !reflect.DeepEqual(p.Consumers, want[p.Device]) {
				t.Errorf("%s consumers %v, want %v", p.Device, p.Consumers, want[p.Device])
			}
		}
	}
	for device, own := range map[string][]storage.Consumer{
		"/dev/sda": consumers(), "/dev/sdd": consumers(),
		"/dev/sde": consumers(storage.ConsumerMount, "/srv/olivares/mnt/host/scratch"),
	} {
		if got := diskNamed(t, inv, device).Consumers; !reflect.DeepEqual(got, own) {
			t.Errorf("%s own consumers %v, want %v", device, got, own)
		}
	}
	if p := partitionNamed(t, sda, "/dev/sda1"); p.Filesystem == nil || !reflect.DeepEqual(p.Filesystem.MountPoints, []string{"/boot/efi"}) {
		t.Fatalf("sda1 filesystem %+v", p.Filesystem)
	}
}

const lvmPath = "/org/freedesktop/UDisks2/lvm/"

// lvmHost is sda with the EFI partition and sda2, a physical volume of "system" holding root
// (/) and swap, and sdb, a whole-disk physical volume of "data" holding one mounted volume. The
// LVM2 module's interfaces are in the second object set, which the fake bus attaches only once
// the module is enabled.
func lvmHost() (plain, lvm storage.Objects) {
	h := newHost()
	h.disk("sda", "SYS0001", 8<<8, 64<<30, "gpt")
	h.partition("sda", "sda1", 1, "c12a7328-f81f-11d2-ba4b-00a0c93ec93b")
	h.partition("sda", "sda2", 2, "e6d6d379-f507-44c2-a23c-238f2a3df928")
	h.with("sda1", mounted("vfat", "/boot/efi"))
	h.with("sda2", func(b *storage.Block) { b.IDUsage, b.IDType = "raid", "LVM2_member" })
	h.disk("sdb", "DATA0001", 8<<8|16, 64<<30, "")
	h.with("sdb", func(b *storage.Block) { b.IDUsage, b.IDType = "raid", "LVM2_member" })
	for i := range 3 {
		h.objects.Blocks[blockDevices+"dm_2d"+string(rune('0'+i))] = storage.Block{Device: "/dev/dm-" + string(rune('0'+i)),
			DeviceNumber: 253<<8 | uint64(i), Size: 4 << 30, Drive: storage.NoObject, CryptoBackingDevice: storage.NoObject}
	}
	h.with("dm_2d0", mounted("ext4", "/"))
	h.with("dm_2d1", func(b *storage.Block) {
		b.IDUsage, b.IDType = "other", "swap"
		b.Swapspace = &storage.SwapspaceFacts{Active: true}
	})
	h.with("dm_2d2", mounted("xfs", "/srv/olivares/mnt/host/archive"))

	lvm = newObjects()
	lvm.Blocks[blockDevices+"sda2"] = storage.Block{PhysicalVolume: &storage.PhysicalVolumeFacts{VolumeGroup: lvmPath + "system"}}
	lvm.Blocks[blockDevices+"sdb"] = storage.Block{PhysicalVolume: &storage.PhysicalVolumeFacts{VolumeGroup: lvmPath + "data"}}
	lvm.VolumeGroups[lvmPath+"system"] = storage.VolumeGroup{Name: "system", UUID: "Yc3JtP-sys0-vg00-uuid-0000-0000-000001", Size: 63 << 30, FreeSize: 1 << 30}
	lvm.VolumeGroups[lvmPath+"data"] = storage.VolumeGroup{Name: "data", UUID: "Yc3JtP-dat0-vg00-uuid-0000-0000-000002", Size: 64 << 30, FreeSize: 60 << 30}
	for i, lv := range []struct{ vg, name string }{{"system", "root"}, {"system", "swap"}, {"data", "archive"}} {
		block := blockDevices + "dm_2d" + string(rune('0'+i))
		lvm.Blocks[block] = storage.Block{LogicalVolume: lvmPath + lv.vg + "/" + lv.name}
		lvm.LogicalVolumes[lvmPath+lv.vg+"/"+lv.name] = storage.LogicalVolume{VolumeGroup: lvmPath + lv.vg, Name: lv.name,
			UUID: "lv-uuid-" + lv.name, Type: "block", Active: true, Size: 4 << 30, BlockDevice: block}
	}
	return h.objects, lvm
}

func TestInventory_LvmModuleIsEnabledBeforeLvmReads(t *testing.T) {
	plain, lvm := lvmHost()
	kernel := &fakeKernel{boot: bootA, holders: map[string][]string{"/dev/sda2": {"dm-0", "dm-1"}, "/dev/sdb": {"dm-2"}}}
	bus := &fakeBus{objects: plain, lvm: lvm}
	inv := read(t, bus, kernel)

	if len(bus.calls) < 2 || bus.calls[0] != "enable lvm2" || bus.calls[1] != "objects" {
		t.Fatalf("calls %v: the LVM2 module is enabled before any object is read", bus.calls)
	}
	sda := diskNamed(t, inv, "/dev/sda")
	if !reflect.DeepEqual(sda.SystemReasons, []string{"/", "/boot/efi", "swap"}) {
		t.Fatalf("sda reasons %v: / and swap are logical volumes on sda2", sda.SystemReasons)
	}
	if diskNamed(t, inv, "/dev/sdb").System {
		t.Fatal("sdb holds no system volume")
	}
	if got := partitionNamed(t, sda, "/dev/sda2").Consumers; !reflect.DeepEqual(got,
		consumers(storage.ConsumerHolder, "dm-0", storage.ConsumerHolder, "dm-1", storage.ConsumerPhysicalVolume, "system")) {
		t.Fatalf("sda2 consumers %v", got)
	}
	if got := diskNamed(t, inv, "/dev/sdb").Consumers; !reflect.DeepEqual(got,
		consumers(storage.ConsumerHolder, "dm-2", storage.ConsumerPhysicalVolume, "data")) {
		t.Fatalf("sdb consumers %v", got)
	}
	want := storage.LVM{State: storage.LVMRead, VolumeGroups: []storage.VolumeGroupView{
		{Name: "data", UUID: "Yc3JtP-dat0-vg00-uuid-0000-0000-000002", Size: 64 << 30, Free: 60 << 30, PhysicalVolumes: []string{"/dev/sdb"},
			LogicalVolumes: []storage.LogicalVolumeView{{Name: "archive", UUID: "lv-uuid-archive", Type: "block", Active: true, Size: 4 << 30, Device: "/dev/dm-2",
				Filesystem: &storage.Filesystem{Type: "xfs", UUID: "uuid-of-/dev/dm-2", MountPoints: []string{"/srv/olivares/mnt/host/archive"}},
				Consumers:  consumers(storage.ConsumerMount, "/srv/olivares/mnt/host/archive")}}},
		{Name: "system", UUID: "Yc3JtP-sys0-vg00-uuid-0000-0000-000001", Size: 63 << 30, Free: 1 << 30, PhysicalVolumes: []string{"/dev/sda2"},
			LogicalVolumes: []storage.LogicalVolumeView{
				{Name: "root", UUID: "lv-uuid-root", Type: "block", Active: true, Size: 4 << 30, Device: "/dev/dm-0",
					Filesystem: &storage.Filesystem{Type: "ext4", UUID: "uuid-of-/dev/dm-0", MountPoints: []string{"/"}},
					Consumers:  consumers(storage.ConsumerMount, "/")},
				{Name: "swap", UUID: "lv-uuid-swap", Type: "block", Active: true, Size: 4 << 30, Device: "/dev/dm-1",
					Consumers: consumers(storage.ConsumerSwap, "/dev/dm-1")}}},
	}}
	if !reflect.DeepEqual(inv.LVM, want) {
		t.Fatalf("lvm\n %+v\nwant\n %+v", inv.LVM, want)
	}

	// A module that cannot be enabled is stated, never read as an empty LVM, and a physical
	// volume it cannot describe is still a consumer.
	plain, lvm = lvmHost()
	failing := &fakeBus{objects: plain, lvm: lvm, enableErr: errors.New("org.freedesktop.UDisks2.Error.Failed")}
	inv = read(t, failing, kernel)
	if inv.LVM.State != storage.LVMUnavailable || inv.LVM.Reason == "" || inv.LVM.VolumeGroups != nil {
		t.Fatalf("lvm %+v: an unavailable module is not a complete empty result", inv.LVM)
	}
	if got := diskNamed(t, inv, "/dev/sdb").Consumers; !reflect.DeepEqual(got,
		consumers(storage.ConsumerHolder, "dm-2", storage.ConsumerPhysicalVolume, storage.Unread)) {
		t.Fatalf("sdb consumers %v without the module", got)
	}
}

func operations(ops ...storage.Operation) []storage.Operation {
	if ops == nil {
		return []storage.Operation{}
	}
	return ops
}

func TestInventory_OffersOnlyOperationsTheHostReports(t *testing.T) {
	h := newHost()
	h.disk("sdb", "DATA0001", 8<<8|16, 64<<30, "gpt")
	h.partition("sdb", "sdb1", 1, "11111111-2222-4333-8444-555555555551")
	h.partition("sdb", "sdb2", 2, "11111111-2222-4333-8444-555555555552")
	h.partition("sdb", "sdb3", 3, "11111111-2222-4333-8444-555555555553")
	h.partition("sdb", "sdb4", 4, "11111111-2222-4333-8444-555555555554")
	h.with("sdb1", mounted("ext4"))
	h.with("sdb2", mounted("xfs"))
	h.with("sdb3", mounted("btrfs"))
	caps := map[string]storage.Capability{
		"format/ext4":  {Available: true},
		"format/xfs":   {Utility: "mkfs.xfs"},
		"check/ext4":   {Available: true},
		"check/xfs":    {Available: true},
		"check/btrfs":  {Utility: "btrfs"},
		"repair/ext4":  {Available: true},
		"repair/xfs":   {Utility: "xfs_repair"},
		"resize/ext4":  {Available: true, Modes: storage.ResizeOfflineShrink | storage.ResizeOfflineGrow | storage.ResizeOnlineGrow},
		"resize/xfs":   {Available: true, Modes: storage.ResizeOnlineGrow},
		"resize/btrfs": {Available: true, Modes: storage.ResizeOfflineShrink | storage.ResizeOfflineGrow | storage.ResizeOnlineShrink | storage.ResizeOnlineGrow},
	}
	bus := &fakeBus{objects: h.objects, caps: caps}
	sdb := diskNamed(t, read(t, bus, &fakeKernel{boot: bootA}), "/dev/sdb")

	formatExt4 := storage.Operation{Name: storage.OperationFormat, Type: "ext4"}
	if !reflect.DeepEqual(sdb.Operations, operations(formatExt4)) {
		t.Fatalf("sdb operations %v: format is offered for ext4 alone (xfs lacks mkfs.xfs, btrfs is not reported)", sdb.Operations)
	}
	want := map[string][]storage.Operation{
		"/dev/sdb1": operations(
			storage.Operation{Name: storage.OperationCheck, Type: "ext4"},
			formatExt4,
			storage.Operation{Name: storage.OperationGrow, Type: "ext4", Modes: []string{"offline", "online"}},
			storage.Operation{Name: storage.OperationRepair, Type: "ext4"},
			storage.Operation{Name: storage.OperationShrink, Type: "ext4", Modes: []string{"offline"}}),
		"/dev/sdb2": operations(
			storage.Operation{Name: storage.OperationCheck, Type: "xfs"},
			formatExt4,
			storage.Operation{Name: storage.OperationGrow, Type: "xfs", Modes: []string{"online"}}),
		"/dev/sdb3": operations(
			formatExt4,
			storage.Operation{Name: storage.OperationGrow, Type: "btrfs", Modes: []string{"offline", "online"}},
			storage.Operation{Name: storage.OperationShrink, Type: "btrfs", Modes: []string{"offline", "online"}}),
		"/dev/sdb4": operations(formatExt4),
	}
	for _, p := range sdb.Partitions {
		if !reflect.DeepEqual(p.Operations, want[p.Device]) {
			t.Errorf("%s operations\n %v\nwant\n %v", p.Device, p.Operations, want[p.Device])
		}
	}
	asked := map[string]int{}
	for _, call := range bus.calls {
		asked[call]++
	}
	if asked["can format ext4"] != 1 || asked["can resize ext4"] != 1 {
		t.Fatalf("each capability is asked once per inventory: %v", asked)
	}

	// A disk the inventory cannot name is offered nothing, on the disk or its partitions.
	h.disk("sdc", "", 8<<8|32, 64<<30, "gpt")
	h.partition("sdc", "sdc1", 1, "11111111-2222-4333-8444-555555555561")
	h.with("sdc1", mounted("ext4"))
	sdc := diskNamed(t, read(t, &fakeBus{objects: h.objects, caps: caps}, &fakeKernel{boot: bootA}), "/dev/sdc")
	if sdc.Target != "" || len(sdc.Operations) != 0 || len(sdc.Partitions[0].Operations) != 0 {
		t.Fatalf("an unnamed disk is offered operations: %+v", sdc)
	}
}

func TestInventory_OperationsOnTheSystemDiskOrAnInUseDeviceNameWhyTheyAreDisabled(t *testing.T) {
	h := newHost()
	h.disk("sda", "SYS0001", 8<<8, 64<<30, "gpt")
	h.partition("sda", "sda1", 1, "c12a7328-f81f-11d2-ba4b-00a0c93ec93b")
	h.partition("sda", "sda2", 2, "4f68bce3-e8cd-4db1-96e7-fbcaf984b709")
	h.with("sda1", mounted("vfat", "/boot/efi"))
	h.with("sda2", mounted("ext4", "/"))
	h.disk("sdb", "DATA0001", 8<<8|16, 64<<30, "gpt")
	h.partition("sdb", "sdb1", 1, "22222222-3333-4444-8555-666666666661")
	h.partition("sdb", "sdb2", 2, "22222222-3333-4444-8555-666666666662")
	h.partition("sdb", "sdb3", 3, "22222222-3333-4444-8555-666666666663")
	h.with("sdb1", mounted("ext4", "/srv/olivares/mnt/host/data"))
	h.with("sdb2", mounted("ext4"))
	h.disk("sdc", "DATA0002", 8<<8|32, 64<<30, "")
	caps := map[string]storage.Capability{
		"format/ext4": {Available: true}, "check/ext4": {Available: true}, "repair/ext4": {Available: true},
		"resize/ext4": {Available: true, Modes: storage.ResizeOfflineGrow | storage.ResizeOnlineGrow}, "check/vfat": {Available: true},
	}
	kernel := &fakeKernel{boot: bootA, holders: map[string][]string{"/dev/sdb3": {"dm-3"}}}
	inv := read(t, &fakeBus{objects: h.objects, caps: caps}, kernel)

	disabled := func(device string, ops []storage.Operation, want string) {
		t.Helper()
		if len(ops) == 0 {
			t.Fatalf("%s is offered no operation, so the test reads nothing", device)
		}
		for _, op := range ops {
			if op.Disabled != want {
				t.Errorf("%s: %s %s disabled %q, want %q", device, op.Name, op.Type, op.Disabled, want)
			}
		}
	}
	sda := diskNamed(t, inv, "/dev/sda")
	disabled("/dev/sda", sda.Operations, storage.CodeSystemDisk)
	disabled("/dev/sda1", partitionNamed(t, sda, "/dev/sda1").Operations, storage.CodeSystemDisk)
	disabled("/dev/sda2", partitionNamed(t, sda, "/dev/sda2").Operations, storage.CodeSystemDisk)
	sdb := diskNamed(t, inv, "/dev/sdb")
	disabled("/dev/sdb", sdb.Operations, storage.CodeTargetInUse)
	disabled("/dev/sdb1", partitionNamed(t, sdb, "/dev/sdb1").Operations, storage.CodeTargetInUse)
	disabled("/dev/sdb2", partitionNamed(t, sdb, "/dev/sdb2").Operations, "")
	disabled("/dev/sdb3", partitionNamed(t, sdb, "/dev/sdb3").Operations, storage.CodeTargetInUse)
	disabled("/dev/sdc", diskNamed(t, inv, "/dev/sdc").Operations, "")

	page := storage.Page(signIn, func(context.Context) (storage.Inventory, error) { return inv, nil })
	body := settled(t, page, func(rec *httptest.ResponseRecorder) bool { return rec.Code == http.StatusOK }).Body.String()
	for _, want := range []string{"format ext4 [disabled: storage_system_disk]", "check ext4 [disabled: storage_target_in_use]"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not name why an operation is disabled: lacks %q", want)
		}
	}
}
