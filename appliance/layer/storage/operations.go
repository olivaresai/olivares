// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import (
	"context"
	"slices"
	"strings"
)

// formatTypes are the filesystem types a format may create, in the order they are offered.
var formatTypes = []string{"ext4", "xfs", "btrfs"}

// capabilities asks the manager each question at most once per inventory.
type capabilities struct {
	ctx     context.Context
	bus     Bus
	answers map[string]answer
}

type answer struct {
	capability Capability
	reported   bool
}

func newCapabilities(ctx context.Context, bus Bus) *capabilities {
	return &capabilities{ctx: ctx, bus: bus, answers: map[string]answer{}}
}

// reported returns the manager's answer for check on fstype and whether it reports the
// operation available. An error is an operation the host does not report.
func (c *capabilities) reported(check Check, fstype string) (Capability, bool) {
	key := string(check) + "/" + fstype
	if a, ok := c.answers[key]; ok {
		return a.capability, a.reported
	}
	capability, err := c.bus.Can(c.ctx, check, fstype)
	a := answer{capability: capability, reported: err == nil && capability.Available}
	c.answers[key] = a
	return a.capability, a.reported
}

// operations returns what the host reports it can do to a device holding fs (nil for none),
// ordered by name and type: format for each type CanFormat reports, and for fs's type check,
// repair, and grow and shrink in each mode CanResize reports.
func (c *capabilities) operations(fs *Filesystem) []Operation {
	ops := []Operation{}
	for _, fstype := range formatTypes {
		if _, ok := c.reported(CanFormat, fstype); ok {
			ops = append(ops, Operation{Name: OperationFormat, Type: fstype})
		}
	}
	if fs != nil && fsTypeName(fs.Type) {
		if _, ok := c.reported(CanCheck, fs.Type); ok {
			ops = append(ops, Operation{Name: OperationCheck, Type: fs.Type})
		}
		if _, ok := c.reported(CanRepair, fs.Type); ok {
			ops = append(ops, Operation{Name: OperationRepair, Type: fs.Type})
		}
		if resize, ok := c.reported(CanResize, fs.Type); ok {
			if modes := resizeModes(resize.Modes, ResizeOfflineGrow, ResizeOnlineGrow); len(modes) > 0 {
				ops = append(ops, Operation{Name: OperationGrow, Type: fs.Type, Modes: modes})
			}
			if modes := resizeModes(resize.Modes, ResizeOfflineShrink, ResizeOnlineShrink); len(modes) > 0 {
				ops = append(ops, Operation{Name: OperationShrink, Type: fs.Type, Modes: modes})
			}
		}
	}
	slices.SortFunc(ops, func(a, b Operation) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.Type, b.Type)
	})
	return ops
}

// resizeModes names the modes of flags among offline and online.
func resizeModes(flags, offline, online uint64) []string {
	var modes []string
	if flags&offline != 0 {
		modes = append(modes, "offline")
	}
	if flags&online != 0 {
		modes = append(modes, "online")
	}
	return modes
}

// fsTypeName reports whether s can be a filesystem type the manager is asked about.
func fsTypeName(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// offerOperations sets the operations of every disk that has a target, and of its partitions.
// A disk without a target (no identity, or one another disk shares) is offered nothing. On the
// system disk and its partitions every operation is disabled with storage_system_disk; on a
// device with a consumer, and on a disk one of whose partitions has one, with
// storage_target_in_use.
func offerOperations(caps *capabilities, disks []Disk) {
	for i := range disks {
		d := &disks[i]
		if d.Target == "" {
			continue
		}
		d.Operations = disable(caps.operations(d.Filesystem), disabledReason(d.System, diskInUse(d)))
		for j := range d.Partitions {
			p := &d.Partitions[j]
			p.Operations = disable(caps.operations(p.Filesystem), disabledReason(d.System, len(p.Consumers) > 0))
		}
	}
}

// disabledReason is the closed code that disables operations on a device: the system disk
// first, then a device in use; "" for neither.
func disabledReason(system, inUse bool) string {
	switch {
	case system:
		return CodeSystemDisk
	case inUse:
		return CodeTargetInUse
	}
	return ""
}

// diskInUse reports whether the disk or one of its partitions has a consumer.
func diskInUse(d *Disk) bool {
	if len(d.Consumers) > 0 {
		return true
	}
	for _, p := range d.Partitions {
		if len(p.Consumers) > 0 {
			return true
		}
	}
	return false
}

// disable marks every operation of ops with reason.
func disable(ops []Operation, reason string) []Operation {
	for i := range ops {
		ops[i].Disabled = reason
	}
	return ops
}
