// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// fakeBus stands in for UDisks2. Like udisksd, it attaches the LVM2 module's interfaces (lvm)
// to the objects it returns only after EnableModule("lvm2") succeeded.
type fakeBus struct {
	objects   storage.Objects
	lvm       storage.Objects
	caps      map[string]storage.Capability
	enableErr error
	enabled   bool
	calls     []string
}

func (b *fakeBus) EnableModule(_ context.Context, name string) error {
	b.calls = append(b.calls, "enable "+name)
	if b.enableErr != nil {
		return b.enableErr
	}
	if name == "lvm2" {
		b.enabled = true
	}
	return nil
}

func (b *fakeBus) Objects(context.Context) (storage.Objects, error) {
	b.calls = append(b.calls, "objects")
	out := storage.Objects{Drives: map[string]storage.Drive{}, Blocks: map[string]storage.Block{},
		VolumeGroups: map[string]storage.VolumeGroup{}, LogicalVolumes: map[string]storage.LogicalVolume{}}
	for path, drive := range b.objects.Drives {
		out.Drives[path] = drive
	}
	for path, block := range b.objects.Blocks {
		out.Blocks[path] = block
	}
	if !b.enabled {
		return out, nil
	}
	for path, attached := range b.lvm.Blocks {
		block := out.Blocks[path]
		block.PhysicalVolume = attached.PhysicalVolume
		block.LogicalVolume = attached.LogicalVolume
		out.Blocks[path] = block
	}
	for path, vg := range b.lvm.VolumeGroups {
		out.VolumeGroups[path] = vg
	}
	for path, lv := range b.lvm.LogicalVolumes {
		out.LogicalVolumes[path] = lv
	}
	return out, nil
}

func (b *fakeBus) Can(_ context.Context, check storage.Check, fstype string) (storage.Capability, error) {
	b.calls = append(b.calls, "can "+string(check)+" "+fstype)
	c, ok := b.caps[string(check)+"/"+fstype]
	if !ok {
		return storage.Capability{}, errors.New("unknown or unsupported filesystem type")
	}
	return c, nil
}

// fakeKernel stands in for the kernel, keyed by device node.
type fakeKernel struct {
	boot     string
	loops    map[string]storage.LoopStatus
	firstMiB map[string]string
	holders  map[string][]string
}

func (k *fakeKernel) BootID() (string, error) { return k.boot, nil }

func (k *fakeKernel) LoopStatus(device string, _ uint64) (storage.LoopStatus, error) {
	s, ok := k.loops[device]
	if !ok {
		return storage.LoopStatus{}, errors.New("ENXIO")
	}
	return s, nil
}

func (k *fakeKernel) FirstMiB(device string, _ uint64) (string, error) {
	sum, ok := k.firstMiB[device]
	if !ok {
		return "", errors.New("EIO")
	}
	return sum, nil
}

func (k *fakeKernel) Holders(device string, _ uint64) ([]string, error) {
	return k.holders[device], nil
}

const (
	bootA = "3f2b9c1e-8a7d-4e6f-b5c4-d3e2f1a0b9c8"
	bootB = "71c0de42-19ab-4cd3-8e5f-60718293a4b5"
)

func newObjects() storage.Objects {
	return storage.Objects{Drives: map[string]storage.Drive{}, Blocks: map[string]storage.Block{},
		VolumeGroups: map[string]storage.VolumeGroup{}, LogicalVolumes: map[string]storage.LogicalVolume{}}
}

// fixture reads a UDisks2 object set from testdata.
func fixture(t *testing.T, name string) storage.Objects {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	objects := newObjects()
	if err := json.Unmarshal(data, &objects); err != nil {
		t.Fatal(err)
	}
	return objects
}

func read(t *testing.T, bus *fakeBus, kernel *fakeKernel) storage.Inventory {
	t.Helper()
	inv, err := storage.Read(context.Background(), bus, kernel)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return inv
}

func diskNamed(t *testing.T, inv storage.Inventory, device string) storage.Disk {
	t.Helper()
	for _, d := range inv.Disks {
		if d.Device == device {
			return d
		}
	}
	t.Fatalf("the inventory has no disk %s: %+v", device, inv.Disks)
	return storage.Disk{}
}
