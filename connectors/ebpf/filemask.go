// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import "github.com/olivaresai/olivares/sdk/model"

// MAY_* permission-mask bits passed to the kernel's security_file_permission LSM
// hook (signature: int security_file_permission(struct file *, int mask)). The
// values are from include/linux/fs.h and are stable across the supported kernel
// range (verified identical in 5.10 LTS and 6.6 LTS). Tetragon forwards the mask
// verbatim as the hook's int argument; this connector classifies it into the R/RW
// AccessMode of the edge (ARCHITECTURE.md).
const (
	mayExec   = 0x01 // MAY_EXEC   — execute the file (reads its image)
	mayWrite  = 0x02 // MAY_WRITE  — write the file
	mayRead   = 0x04 // MAY_READ   — read the file
	mayAppend = 0x08 // MAY_APPEND — append to the file (a write)
)

// maskToMode classifies a MAY_* permission mask into an AccessMode.
//
// Read bits are MAY_READ and MAY_EXEC: executing a binary reads its image, so
// exec-only access is reported conservatively as a read (the access map cares
// that the file was read; whether it was to run it is a refinement can make
// from ToolRef). Write bits are MAY_WRITE and MAY_APPEND. A mask carrying both is
// readwrite; a mask carrying neither known bit (e.g. MAY_ACCESS/MAY_OPEN alone)
// is unknown rather than guessed (ARCHITECTURE.md: honest confidence, never fabricate).
func maskToMode(mask int) model.AccessMode {
	read := mask&(mayRead|mayExec) != 0
	write := mask&(mayWrite|mayAppend) != 0
	switch {
	case read && write:
		return model.ModeReadWrite
	case write:
		return model.ModeWrite
	case read:
		return model.ModeRead
	default:
		return model.ModeUnknown
	}
}
