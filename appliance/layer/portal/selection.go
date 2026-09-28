// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
)

// SelectionFile is the one file the console reads the operator's exposure answers from. First
// boot publishes it, root-owned and mode 0644, as a view of the validated answers
// (portal.enabled, portal.listen, host.management_interfaces), because the answers carriers are
// readable by root only. The console reads no carrier and no other file for its selection.
const SelectionFile = "/etc/olivares-portal/selection.json"

const (
	selectionSchema   = "olivares-portal-selection/v1"
	maxSelectionBytes = 4096
	// The answers schema bounds host.management_interfaces to sixteen names.
	maxManagementInterfaces = 16
)

// selectionDocument is the published selection's closed schema.
type selectionDocument struct {
	SchemaVersion string `json:"schema_version"`
	Portal        *struct {
		Enabled *bool   `json:"enabled"`
		Listen  *string `json:"listen"`
	} `json:"portal"`
	Host *struct {
		ManagementInterfaces []string `json:"management_interfaces"`
	} `json:"host"`
}

// ReadSelection reads the selection first boot published at path.
//
// No file is the empty selection, which is loopback status only, and no reason. A file is read
// only when it is a regular file, not a symbolic link, owned by root and writable by neither
// its group nor others, at most 4 KiB, and holds one document of the closed schema: its schema
// version, no unknown field, no null, a listen scope of local or management, and one to sixteen
// distinct interface names, which management requires. Anything else is the empty selection and
// a fixed reason that names the failed check and carries no path and no value.
func ReadSelection(path string) (Selection, string) {
	data, present, reason := readRootOwned(path, maxSelectionBytes)
	if !present {
		return Selection{}, ""
	}
	if reason != "" {
		return Selection{}, "the published selection " + reason
	}
	selection, reason := parseSelection(data)
	if reason != "" {
		return Selection{}, "the published selection " + reason
	}
	return selection, ""
}

// parseSelection decodes and validates one selection document.
func parseSelection(data []byte) (Selection, string) {
	if reason := oneDocumentWithoutNull(data); reason != "" {
		return Selection{}, reason
	}
	var document selectionDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Selection{}, "does not follow its schema"
	}
	if document.SchemaVersion != selectionSchema {
		return Selection{}, "is not " + selectionSchema
	}
	var selection Selection
	if portal := document.Portal; portal != nil {
		if portal.Enabled != nil {
			enabled := *portal.Enabled
			selection.Enabled = &enabled
		}
		if portal.Listen != nil {
			if *portal.Listen != ListenLocal && *portal.Listen != ListenManagement {
				return Selection{}, "has a listen scope other than local or management"
			}
			selection.Listen = *portal.Listen
		}
	}
	if host := document.Host; host != nil {
		names := host.ManagementInterfaces
		if len(names) < 1 || len(names) > maxManagementInterfaces {
			return Selection{}, "does not name one to sixteen management interfaces"
		}
		for _, name := range names {
			if !interfaceName(name) {
				return Selection{}, "names a management interface that is not an interface name"
			}
		}
		sorted := slices.Clone(names)
		slices.Sort(sorted)
		if len(slices.Compact(slices.Clone(sorted))) != len(sorted) {
			return Selection{}, "names a management interface twice"
		}
		selection.ManagementInterfaces = sorted
	}
	if selection.Listen == ListenManagement && len(selection.ManagementInterfaces) == 0 {
		return Selection{}, "selects management with no management interface"
	}
	return selection, ""
}

// oneDocumentWithoutNull refuses data that is not exactly one JSON value, or that holds a null
// anywhere: encoding/json would read a null as an absent field.
func oneDocumentWithoutNull(data []byte) string {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return "is not one JSON document"
	}
	if containsNull(value) {
		return "holds a null"
	}
	return ""
}

func containsNull(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case map[string]any:
		for _, element := range v {
			if containsNull(element) {
				return true
			}
		}
	case []any:
		for _, element := range v {
			if containsNull(element) {
				return true
			}
		}
	}
	return false
}

// interfaceName accepts the Linux interface names the answers schema accepts: 1 to 15 ASCII
// letters, digits, dots, hyphens or underscores, beginning with a letter or digit.
func interfaceName(name string) bool {
	if len(name) < 1 || len(name) > 15 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '-' || c == '_'):
		default:
			return false
		}
	}
	return true
}

// readRootOwned reads a file only root can have written: a regular file, not a symbolic link,
// owned by root and writable by neither its group nor others, at most limit bytes. present is
// false when there is no file at all. Any other failure is a fixed phrase naming the check; the
// file is judged again after it is opened, so a file replaced in between is refused.
func readRootOwned(path string, limit int64) (data []byte, present bool, reason string) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, ""
	}
	if err != nil {
		return nil, true, "cannot be measured"
	}
	if reason := rootOwnedFile(info); reason != "" {
		return nil, true, reason
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, "cannot be opened"
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, true, "changed while it was read"
	}
	if reason := rootOwnedFile(opened); reason != "" {
		return nil, true, reason
	}
	data, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, true, "cannot be read"
	}
	if int64(len(data)) > limit {
		return nil, true, "exceeds its size bound"
	}
	return data, true, ""
}

// rootOwnedFile returns why info is not a file only root can have written, or "".
func rootOwnedFile(info os.FileInfo) string {
	uid, known := fileOwner(info)
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "is a symbolic link"
	case !info.Mode().IsRegular():
		return "is not a regular file"
	case !known || uid != 0:
		return "is not owned by root"
	case info.Mode().Perm()&0o022 != 0:
		return "is writable by its group or others"
	}
	return ""
}
