// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/storage"
)

const firstMiBOfVda = "5d41402abc4b2a76b9719d911017c592aaf4c3a1f39e5b2c8d7e6f5a4b3c2d1e"

func isDigest(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

func TestIdentity_FallsBackWhenWWNSerialAndIdAreBlank(t *testing.T) {
	const drivePath = "/org/freedesktop/UDisks2/drives/virtio_disk_0"
	const blockPath = "/org/freedesktop/UDisks2/block_devices/vda"
	for _, tc := range []struct {
		name          string
		drive         storage.Drive
		blockID       string
		source, value string
	}{
		{"wwn first", storage.Drive{WWN: "0x5000c500a1b2c3d4", Serial: "ZA1B2C3D", ID: "ST4000-ZA1B2C3D"}, "by-id-wwn-0x5000c500a1b2c3d4", storage.SourceWWN, "0x5000c500a1b2c3d4"},
		{"serial when wwn is blank", storage.Drive{Serial: "ZA1B2C3D", ID: "ST4000-ZA1B2C3D"}, "by-id-ata-ST4000-ZA1B2C3D", storage.SourceSerial, "ZA1B2C3D"},
		{"drive id when wwn and serial are blank", storage.Drive{ID: "QEMU-HARDDISK-1"}, "by-id-ata-QEMU_HARDDISK", storage.SourceDriveID, "QEMU-HARDDISK-1"},
		{"block id when the drive's three are blank", storage.Drive{}, "by-id-virtio-data0", storage.SourceBlockID, "by-id-virtio-data0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := fixture(t, "virtio-blank-serial.json")
			objects.Drives[drivePath] = tc.drive
			block := objects.Blocks[blockPath]
			block.ID = tc.blockID
			objects.Blocks[blockPath] = block
			d := diskNamed(t, read(t, &fakeBus{objects: objects}, &fakeKernel{boot: bootA}), "/dev/vda")
			id := d.Identity
			if id.Kind != storage.IdentityDrive || id.Source != tc.source || id.Value != tc.value {
				t.Fatalf("identity %+v, want kind drive from %s %q", id, tc.source, tc.value)
			}
			if id.Size != 17179869184 || !reflect.DeepEqual(id.PartitionUUIDs, []string{"f15e0c2a-7b3d-4c1e-9a8f-2d6b4e1c0a93", "2a7d9e3c-1f4b-4a6d-8c2e-5b9f0d1e3c7a"}) {
				t.Fatalf("identity %+v does not carry the size and the partition UUIDs in partition order", id)
			}
			if id.BootID != "" || id.FirstMiBSHA256 != "" {
				t.Fatalf("a named drive's identity is bound to a boot or to its content: %+v", id)
			}
			if !isDigest(id.Digest) || d.Target != "drive:"+id.Digest {
				t.Fatalf("digest %q target %q", id.Digest, d.Target)
			}
		})
	}

	t.Run("all four blank: by-path link, size, table, partitions and first MiB in this boot", func(t *testing.T) {
		kernel := &fakeKernel{boot: bootA, firstMiB: map[string]string{"/dev/vda": firstMiBOfVda}}
		d := diskNamed(t, read(t, &fakeBus{objects: fixture(t, "virtio-blank-serial.json")}, kernel), "/dev/vda")
		id := d.Identity
		want := storage.Identity{Kind: storage.IdentityBlank, Source: storage.SourceByPath, Value: "/dev/disk/by-path/pci-0000:00:04.0",
			Size: 17179869184, PartitionTable: "gpt",
			PartitionUUIDs: []string{"f15e0c2a-7b3d-4c1e-9a8f-2d6b4e1c0a93", "2a7d9e3c-1f4b-4a6d-8c2e-5b9f0d1e3c7a"},
			FirstMiBSHA256: firstMiBOfVda, BootID: bootA, Digest: id.Digest}
		if !reflect.DeepEqual(id, want) {
			t.Fatalf("identity\n %+v\nwant\n %+v", id, want)
		}
		if !isDigest(id.Digest) || d.Target != "drive:"+id.Digest {
			t.Fatalf("digest %q target %q", id.Digest, d.Target)
		}
		if !d.HintSystem || d.System {
			t.Fatalf("UDisks2's hint is shown as a hint, not as the system mark: hint %v system %v", d.HintSystem, d.System)
		}
	})

	t.Run("all four blank and no by-path link: no identity, no target", func(t *testing.T) {
		objects := fixture(t, "virtio-blank-serial.json")
		block := objects.Blocks[blockPath]
		block.Symlinks = []string{"/dev/disk/by-diskseq/1"}
		objects.Blocks[blockPath] = block
		kernel := &fakeKernel{boot: bootA, firstMiB: map[string]string{"/dev/vda": firstMiBOfVda}}
		d := diskNamed(t, read(t, &fakeBus{objects: objects}, kernel), "/dev/vda")
		if d.Identity.Kind != "" || d.Identity.Missing == "" || d.Identity.Digest != "" || d.Target != "" || len(d.Operations) != 0 {
			t.Fatalf("a disk with no stable identity was identified: %+v target %q operations %v", d.Identity, d.Target, d.Operations)
		}
	})

	t.Run("all four blank and the first MiB unreadable: no identity", func(t *testing.T) {
		d := diskNamed(t, read(t, &fakeBus{objects: fixture(t, "virtio-blank-serial.json")}, &fakeKernel{boot: bootA}), "/dev/vda")
		if d.Identity.Kind != "" || d.Identity.Missing == "" || d.Target != "" {
			t.Fatalf("identified without its content digest: %+v target %q", d.Identity, d.Target)
		}
	})
}

func refusedWith(t *testing.T, err error, code string) {
	t.Helper()
	var refusal *storage.Refusal
	if !errors.As(err, &refusal) || refusal.Code != code {
		t.Fatalf("got %v, want the refusal %s", err, code)
	}
}

func TestIdentity_BlankIdentityPlanIsValidInThisBootOnly(t *testing.T) {
	objects := fixture(t, "virtio-blank-serial.json")
	inBoot := func(boot, firstMiB string) storage.Inventory {
		return read(t, &fakeBus{objects: objects}, &fakeKernel{boot: boot, firstMiB: map[string]string{"/dev/vda": firstMiB}})
	}
	planned := diskNamed(t, inBoot(bootA, firstMiBOfVda), "/dev/vda").Identity
	if planned.Kind != storage.IdentityBlank || planned.BootID != bootA {
		t.Fatalf("the fixture's disk is not a blank identity of boot A: %+v", planned)
	}

	d, err := storage.Reresolve(planned, inBoot(bootA, firstMiBOfVda))
	if err != nil || d.Device != "/dev/vda" || d.Identity.Digest != planned.Digest {
		t.Fatalf("the same disk in the same boot did not resolve: %+v %v", d, err)
	}

	_, err = storage.Reresolve(planned, inBoot(bootB, firstMiBOfVda))
	refusedWith(t, err, storage.CodeIdentityChanged)

	const rewritten = "0000000000000000000000000000000000000000000000000000000000000001"
	_, err = storage.Reresolve(planned, inBoot(bootA, rewritten))
	refusedWith(t, err, storage.CodeIdentityChanged)

	forged := planned
	forged.BootID = bootB
	_, err = storage.Reresolve(forged, inBoot(bootB, firstMiBOfVda))
	refusedWith(t, err, storage.CodeInputRefused)

	_, err = storage.Reresolve(storage.Identity{}, inBoot(bootA, firstMiBOfVda))
	refusedWith(t, err, storage.CodeInputRefused)

	named := fixture(t, "virtio-blank-serial.json")
	named.Drives["/org/freedesktop/UDisks2/drives/virtio_disk_0"] = storage.Drive{Serial: "data0"}
	serial := diskNamed(t, read(t, &fakeBus{objects: named}, &fakeKernel{boot: bootA}), "/dev/vda").Identity
	d, err = storage.Reresolve(serial, read(t, &fakeBus{objects: named}, &fakeKernel{boot: bootB}))
	if err != nil || d.Device != "/dev/vda" {
		t.Fatalf("a disk named by its serial is bound to one boot: %+v %v", d, err)
	}
}

const scratchName = "/var/tmp/storage-test/scratch.img"

// scratchLoop is what the kernel reports for loop0: the backing file's device and inode, its
// offset and size limit, and the logical block size.
var scratchLoop = storage.LoopStatus{BackingDevice: 0x10302, BackingInode: 131073, BlockSize: 512}

// loopObjects is loop0 bound to a 64 MiB file UDisks2 shows as backingFile, and loop1 unbound.
func loopObjects(backingFile string) storage.Objects {
	objects := newObjects()
	objects.Blocks["/org/freedesktop/UDisks2/block_devices/loop0"] = storage.Block{Device: "/dev/loop0", DeviceNumber: 7 << 8,
		Size: 64 << 20, Drive: storage.NoObject, CryptoBackingDevice: storage.NoObject, Loop: &storage.LoopFacts{BackingFile: backingFile}}
	objects.Blocks["/org/freedesktop/UDisks2/block_devices/loop1"] = storage.Block{Device: "/dev/loop1", DeviceNumber: 7<<8 | 1,
		Drive: storage.NoObject, CryptoBackingDevice: storage.NoObject, Loop: &storage.LoopFacts{}}
	return objects
}

func loopKernel(status storage.LoopStatus) *fakeKernel {
	return &fakeKernel{boot: bootA, loops: map[string]storage.LoopStatus{"/dev/loop0": status}}
}

func TestIdentity_LoopDeviceUsesItsBackingFile(t *testing.T) {
	inv := read(t, &fakeBus{objects: loopObjects(scratchName)}, loopKernel(scratchLoop))
	if len(inv.Disks) != 1 {
		t.Fatalf("an unbound loop device is listed as a disk: %+v", inv.Disks)
	}
	d := diskNamed(t, inv, "/dev/loop0")
	want := storage.Identity{Kind: storage.IdentityLoop, Source: storage.SourceLoopStatus, Size: 64 << 20, BootID: bootA,
		Loop:   &storage.LoopIdentity{BackingDevice: 0x10302, BackingInode: 131073, BlockSize: 512},
		Digest: d.Identity.Digest}
	if !reflect.DeepEqual(d.Identity, want) {
		t.Fatalf("identity\n %+v\nwant\n %+v", d.Identity, want)
	}
	if !isDigest(d.Identity.Digest) || d.Target != "drive:"+d.Identity.Digest || d.BackingFile != scratchName {
		t.Fatalf("digest %q target %q backing file shown %q", d.Identity.Digest, d.Target, d.BackingFile)
	}

	// The name UDisks2 shows is display only: the same kernel file under another name is the
	// same identity, and another file under the same name is another.
	renamed := diskNamed(t, read(t, &fakeBus{objects: loopObjects(scratchName + " (deleted)")}, loopKernel(scratchLoop)), "/dev/loop0")
	if renamed.Identity.Digest != d.Identity.Digest || renamed.BackingFile != scratchName+" (deleted)" {
		t.Fatalf("the shown name changed the identity: %+v", renamed)
	}
	replaced := scratchLoop
	replaced.BackingInode = 131074
	other := diskNamed(t, read(t, &fakeBus{objects: loopObjects(scratchName)}, loopKernel(replaced)), "/dev/loop0")
	if other.Identity.Digest == d.Identity.Digest {
		t.Fatal("another file behind the same name kept the identity")
	}

	unread := diskNamed(t, read(t, &fakeBus{objects: loopObjects(scratchName)}, &fakeKernel{boot: bootA}), "/dev/loop0")
	if unread.Identity.Kind != "" || unread.Identity.Missing != storage.MissingLoopStatus || unread.Target != "" || len(unread.Operations) != 0 {
		t.Fatalf("a loop device the kernel did not describe was identified: %+v", unread)
	}
}

func TestIdentity_LoopOffsetOrInodeChangedIsRefused(t *testing.T) {
	now := func(status storage.LoopStatus) storage.Inventory {
		return read(t, &fakeBus{objects: loopObjects(scratchName)}, loopKernel(status))
	}
	planned := diskNamed(t, now(scratchLoop), "/dev/loop0").Identity
	if planned.Kind != storage.IdentityLoop {
		t.Fatalf("loop0 has no loop identity to plan on: %+v", planned)
	}
	if d, err := storage.Reresolve(planned, now(scratchLoop)); err != nil || d.Device != "/dev/loop0" {
		t.Fatalf("the unchanged loop device did not resolve: %+v %v", d, err)
	}
	changes := map[string]func(*storage.LoopStatus){
		"offset":         func(s *storage.LoopStatus) { s.Offset = 1 << 20 },
		"inode":          func(s *storage.LoopStatus) { s.BackingInode = 131074 },
		"backing device": func(s *storage.LoopStatus) { s.BackingDevice = 0x10303 },
		"size limit":     func(s *storage.LoopStatus) { s.SizeLimit = 32 << 20 },
		"block size":     func(s *storage.LoopStatus) { s.BlockSize = 4096 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			status := scratchLoop
			change(&status)
			_, err := storage.Reresolve(planned, now(status))
			refusedWith(t, err, storage.CodeIdentityChanged)
		})
	}
	_, err := storage.Reresolve(planned, read(t, &fakeBus{objects: newObjects()}, &fakeKernel{boot: bootA}))
	refusedWith(t, err, storage.CodeIdentityChanged)
	nextBoot := loopKernel(scratchLoop)
	nextBoot.boot = bootB
	_, err = storage.Reresolve(planned, read(t, &fakeBus{objects: loopObjects(scratchName)}, nextBoot))
	refusedWith(t, err, storage.CodeIdentityChanged)
}

// twinDisks is sdb, and with twin also sdc: two empty disks whose drives report the same
// serial, as cloned virtual disks can.
func twinDisks(twin bool) storage.Objects {
	objects := newObjects()
	add := func(name string, number uint64) {
		drive := "/org/freedesktop/UDisks2/drives/QEMU_HARDDISK_" + name
		objects.Drives[drive] = storage.Drive{Serial: "QM00001", Vendor: "ATA", Model: "QEMU HARDDISK"}
		objects.Blocks["/org/freedesktop/UDisks2/block_devices/"+name] = storage.Block{Device: "/dev/" + name, DeviceNumber: number,
			Size: 8 << 30, Drive: drive, CryptoBackingDevice: storage.NoObject}
	}
	add("sdb", 8<<8|16)
	if twin {
		add("sdc", 8<<8|32)
	}
	return objects
}

func TestIdentity_AmbiguousTargetRefuses(t *testing.T) {
	caps := map[string]storage.Capability{"format/ext4": {Available: true}, "format/xfs": {Available: true}, "format/btrfs": {Available: true}}
	single := read(t, &fakeBus{objects: twinDisks(false), caps: caps}, &fakeKernel{boot: bootA})
	planned := diskNamed(t, single, "/dev/sdb")
	if planned.Identity.Ambiguous || planned.Target == "" {
		t.Fatalf("a disk alone is not ambiguous: %+v", planned)
	}

	both := read(t, &fakeBus{objects: twinDisks(true), caps: caps}, &fakeKernel{boot: bootA})
	for _, device := range []string{"/dev/sdb", "/dev/sdc"} {
		d := diskNamed(t, both, device)
		if !d.Identity.Ambiguous || d.Target != "" || len(d.Operations) != 0 {
			t.Fatalf("%s shares its identity but is %+v with target %q and operations %v", device, d.Identity, d.Target, d.Operations)
		}
	}
	_, err := storage.Reresolve(planned.Identity, both)
	refusedWith(t, err, storage.CodeIdentityChanged)
}
