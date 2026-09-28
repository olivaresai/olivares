// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
)

// BootIDFile is where the kernel reports this boot's identity.
const BootIDFile = "/proc/sys/kernel/random/boot_id"

// MeasurementReader reads the owner's published measurement for a reader that is not the owner:
// first boot's stage, the Firewall page's snapshot and the command line. It only reads.
type MeasurementReader struct {
	// Path is MeasurementFile on an appliance.
	Path string
	// BootID is BootIDFile on an appliance.
	BootID string
	// Owner reports the uid that owns a file; the file's own uid when nil.
	Owner func(os.FileInfo) (uint32, bool)
}

// Read returns the measurement and "", or a fixed reason it is unmeasured. The file is trusted
// only when it is a regular file, not a symbolic link, owned by root and writable by neither its
// group nor others, at most 64 KiB, of the closed schema, and of this boot.
func (r MeasurementReader) Read() (Measurement, string) {
	data, err := r.readRootOwned()
	if errors.Is(err, os.ErrNotExist) {
		return Measurement{}, "no measurement is published"
	}
	if err != nil {
		return Measurement{}, "the measurement is not a root-owned regular file that root alone may write"
	}
	var m Measurement
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil || m.SchemaVersion != MeasurementSchema || !digestShape(m.PolicyDigest) ||
		(m.InputPolicy != "drop" && m.InputPolicy != "accept") || m.Rows == nil {
		return Measurement{}, "the measurement does not follow its schema"
	}
	if _, err := time.Parse(time.RFC3339, m.MeasuredAt); err != nil {
		return Measurement{}, "the measurement does not follow its schema"
	}
	for _, row := range m.Rows {
		if !measuredRow(row) {
			return Measurement{}, "the measurement does not follow its schema"
		}
	}
	boot, err := os.ReadFile(r.BootID)
	if err != nil || strings.TrimSpace(string(boot)) == "" {
		return Measurement{}, "this boot's identity cannot be read"
	}
	if m.BootID != strings.TrimSpace(string(boot)) {
		return Measurement{}, "the measurement is of another boot"
	}
	return m, ""
}

func (r MeasurementReader) readRootOwned() ([]byte, error) {
	named, err := os.Lstat(r.Path)
	if err != nil {
		return nil, err
	}
	if !r.rootOwned(named) {
		return nil, errors.New("not a root-owned regular file")
	}
	f, err := os.OpenFile(r.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("cannot be opened without following a link")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(named, opened) || !r.rootOwned(opened) {
		return nil, errors.New("changed while it was read")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStateFile+1))
	if err != nil || len(data) > maxStateFile {
		return nil, errors.New("unreadable or too large")
	}
	return data, nil
}

func (r MeasurementReader) rootOwned(info os.FileInfo) bool {
	owner := r.Owner
	if owner == nil {
		owner = fileOwner
	}
	uid, known := owner(info)
	return known && uid == 0 && info.Mode().IsRegular() && info.Mode().Perm()&0o022 == 0
}

// fileOwner returns the uid that owns info's file.
func fileOwner(info os.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// digestShape reports whether s is "sha256:" and 64 lowercase hexadecimal digits.
func digestShape(s string) bool {
	hex, ok := strings.CutPrefix(s, "sha256:")
	if !ok || len(hex) != 64 {
		return false
	}
	for i := 0; i < len(hex); i++ {
		if (hex[i] < '0' || hex[i] > '9') && (hex[i] < 'a' || hex[i] > 'f') {
			return false
		}
	}
	return true
}

// measuredRow reports whether r is a port and "*" alone or one to sixteen interface names.
func measuredRow(r policy.MeasuredRow) bool {
	number, protocol, ok := strings.Cut(r.Port, "/")
	if !ok || (protocol != "tcp" && protocol != "udp") || number == "" || len(number) > 5 {
		return false
	}
	for i := 0; i < len(number); i++ {
		if number[i] < '0' || number[i] > '9' {
			return false
		}
	}
	if len(r.Interfaces) == 1 && r.Interfaces[0] == policy.EveryInterface {
		return true
	}
	if len(r.Interfaces) == 0 || len(r.Interfaces) > policy.MaxInterfaces {
		return false
	}
	for _, name := range r.Interfaces {
		if !policy.InterfaceName(name) {
			return false
		}
	}
	return true
}
