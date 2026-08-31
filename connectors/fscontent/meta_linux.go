// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package fscontent

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// POSIX.1e ACL constants (system.posix_acl_access xattr binary format).
const (
	aclVersion = 2
	aclUser    = 0x02 // a named user entry (id = uid)
	aclGroup   = 0x08 // a named group entry (id = gid)
	aclRead    = 0x04 // the read permission bit
)

// idNameCache memoizes uid/gid → name resolution (which can hit the host name service,
// including LDAP) so a walk of many files owned by the same principals resolves once.
var idNameCache sync.Map

// platformMeta derives the POSIX governance metadata for a file from its confined fd:
// owner/group/mode, the mapped ACL (owner + group-if-readable + POSIX.1e named
// entries), and classification/labels from xattrs. All reads are on the already-opened,
// confined descriptor — no path is reconstructed.
func platformMeta(f *os.File, sc *sourceConfig) posixMeta {
	fd := int(f.Fd())
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return posixMeta{classification: readClass(fd, sc), labels: readLabels(fd, sc)}
	}
	perm := st.Mode & 0o777
	owner := resolveUser(st.Uid)
	group := resolveGroup(st.Gid)

	attrs := map[string]string{
		"owner": owner,
		"group": group,
		"mode":  fmt.Sprintf("%04o", perm),
		"size":  strconv.FormatInt(st.Size, 10),
	}
	if perm&0o004 != 0 {
		attrs["world_readable"] = "true"
	}

	var acl []string
	if sc.mapPOSIXACL {
		acl = append(acl, sc.userPrefix+owner)
		if perm&0o040 != 0 { // group-readable → the group is a principal
			acl = append(acl, sc.groupPrefix+group)
		}
		acl = append(acl, posixACLRefs(fd, sc)...)
	}

	return posixMeta{
		acl:            acl,
		classification: readClass(fd, sc),
		labels:         readLabels(fd, sc),
		attrs:          attrs,
	}
}

// posixACLRefs parses the file's POSIX.1e access ACL (what getfacl shows) from its
// xattr and returns a principal reference for each NAMED user/group entry that carries
// the read bit — the honest "GRANT-derived" ACL beyond owner/group. Any error (no ACL,
// filesystem without ACL support) yields no extra refs, never a fabricated one.
func posixACLRefs(fd int, sc *sourceConfig) []string {
	raw, ok := fgetxattr(fd, "system.posix_acl_access")
	if !ok || len(raw) < 4 {
		return nil
	}
	if binary.LittleEndian.Uint32(raw[:4]) != aclVersion {
		return nil
	}
	var refs []string
	for i := 4; i+8 <= len(raw); i += 8 {
		tag := binary.LittleEndian.Uint16(raw[i : i+2])
		perm := binary.LittleEndian.Uint16(raw[i+2 : i+4])
		id := binary.LittleEndian.Uint32(raw[i+4 : i+8])
		if perm&aclRead == 0 {
			continue
		}
		switch tag {
		case aclUser:
			refs = append(refs, sc.userPrefix+resolveUser(id))
		case aclGroup:
			refs = append(refs, sc.groupPrefix+resolveGroup(id))
		}
	}
	return refs
}

// readClass reads the classification xattr (trimmed), or "" when absent.
func readClass(fd int, sc *sourceConfig) string {
	if raw, ok := fgetxattr(fd, sc.classXattr); ok {
		return strings.TrimSpace(string(raw))
	}
	return ""
}

// readLabels reads the external-labels xattr (comma-separated), or nil when absent.
func readLabels(fd int, sc *sourceConfig) []string {
	raw, ok := fgetxattr(fd, sc.labelsXattr)
	if !ok {
		return nil
	}
	var out []string
	for _, p := range strings.Split(string(raw), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fgetxattr reads an extended attribute from a confined descriptor.
func fgetxattr(fd int, name string) ([]byte, bool) {
	sz, err := unix.Fgetxattr(fd, name, nil)
	if err != nil || sz <= 0 {
		return nil, false
	}
	buf := make([]byte, sz)
	n, err := unix.Fgetxattr(fd, name, buf)
	if err != nil || n <= 0 {
		return nil, false
	}
	return buf[:n], true
}

// resolveUser maps a uid to a username through the host name service (LDAP included),
// falling back to the numeric id when it does not resolve. Cached.
func resolveUser(uid uint32) string {
	key := "u" + strconv.FormatUint(uint64(uid), 10)
	if v, ok := idNameCache.Load(key); ok {
		return v.(string)
	}
	name := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(name); err == nil && u.Username != "" {
		name = u.Username
	}
	idNameCache.Store(key, name)
	return name
}

// resolveGroup maps a gid to a group name (LDAP included), numeric fallback. Cached.
func resolveGroup(gid uint32) string {
	key := "g" + strconv.FormatUint(uint64(gid), 10)
	if v, ok := idNameCache.Load(key); ok {
		return v.(string)
	}
	name := strconv.FormatUint(uint64(gid), 10)
	if g, err := user.LookupGroupId(name); err == nil && g.Name != "" {
		name = g.Name
	}
	idNameCache.Store(key, name)
	return name
}
