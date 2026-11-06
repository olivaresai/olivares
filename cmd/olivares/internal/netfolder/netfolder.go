// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package netfolder writes the systemd mount and automount units of a network folder, an NFS
// export or an SMB share mounted on the engine host so that a session can use it as its folder,
// and reads their state. Olivares does not mount anything itself: the mount is systemd's, the
// units are ordinary files an administrator can read and edit, and the SMB credential is a file
// that mount.cifs reads, never an argument or a unit line.
package netfolder

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The states List reports for a folder.
const (
	StateMounted       = "mounted"
	StateUnmounted     = "unmounted"
	StateNotResponding = "not responding"
)

// description starts the Description= line of both units; List recognizes its own units by it.
const description = "Olivares network folder "

// workspaceType is the SELinux type of a session folder in the appliance's policy. A share
// mounted with it as its context is readable and writable by the engine and its session tools
// without a rule of its own.
const workspaceType = "olivares_workspace_t"

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)
	segmentRe = regexp.MustCompile(`^[A-Za-z0-9._$-]+$`)
)

// Spec is one network folder: its name (also its mount point's name), its mount type and its
// source in mount(8) form.
type Spec struct {
	Name string
	Type string // cifs or nfs4
	What string // //server/share or server:/export
}

// Parse reads a folder's name and source. The source has mount(8)'s forms, //server/share for
// SMB and server:/export for NFS. The name is lower-case letters and digits so that it stays one
// word in the unit's file name; every part of the source is limited to characters that cannot
// add a mount option or a unit line.
func Parse(name, source string) (Spec, error) {
	if !nameRe.MatchString(name) {
		return Spec{}, errors.New("the name is 1 to 32 lower-case letters and digits")
	}
	var host, rest, kind string
	switch {
	case strings.HasPrefix(source, "//"):
		host, rest, _ = strings.Cut(strings.TrimPrefix(source, "//"), "/")
		rest, kind = "/"+rest, "cifs"
	case strings.Contains(source, ":/"):
		host, rest, _ = strings.Cut(source, ":")
		kind = "nfs4"
	default:
		return Spec{}, errors.New("the source is //server/share for SMB or server:/export for NFS")
	}
	if !hostRe.MatchString(host) {
		return Spec{}, fmt.Errorf("%q is not a server name or address", host)
	}
	if rest == "/" {
		return Spec{}, errors.New("the source names no share or export")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(rest, "/"), "/") {
		if !segmentRe.MatchString(segment) || segment == "." || segment == ".." {
			return Spec{}, fmt.Errorf("%q is not a valid share or export path element", segment)
		}
	}
	return Spec{Name: name, Type: kind, What: source}, nil
}

// Host is the machine the units are written for. The functions are the seams to systemd and the
// kernel; LinuxHost has the real ones.
type Host struct {
	UnitDir        string // where systemd reads local units
	CredDir        string // where SMB credential files live
	MountRoot      string // the directory that holds the mount points
	EngineUser     string // the account an SMB mount presents every file as
	SELinuxContext string // the mount's SELinux context, empty when the host does not use one
	Systemctl      func(args ...string) error
	Escape         func(path string) (string, error) // systemd's own file name for a path, no suffix
	IsActive       func(unit string) bool
	Responds       func(path string) bool
}

// LinuxHost is the host this process runs on. responds says whether a mount point answers from its
// file server; the engine's own probe is the one to pass.
func LinuxHost(responds func(path string) bool) Host {
	return Host{
		UnitDir:        "/etc/systemd/system",
		CredDir:        "/etc/olivares/netfolders",
		MountRoot:      "/srv/olivares/mnt",
		EngineUser:     "olivares",
		SELinuxContext: selinuxContext(),
		Systemctl: func(args ...string) error {
			out, err := exec.Command("systemctl", args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		Escape: func(p string) (string, error) {
			out, err := exec.Command("systemd-escape", "-p", p).Output()
			return strings.TrimSpace(string(out)), err
		},
		IsActive: func(unit string) bool { return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil },
		Responds: responds,
	}
}

// selinuxContext is the label of a session folder when the host enforces SELinux and has the
// appliance's policy, and empty otherwise: a kernel without SELinux refuses a context= option.
func selinuxContext() string {
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err != nil {
		return ""
	}
	contexts, err := os.ReadFile("/etc/selinux/targeted/contexts/files/file_contexts")
	if err != nil || !bytes.Contains(contexts, []byte(workspaceType)) {
		return ""
	}
	return "system_u:object_r:" + workspaceType + ":s0"
}

// Install writes the credential file (SMB only), the mount unit and the automount unit of the
// folder, and has systemd load and start the automount. Nothing waits for the file server at boot:
// the first use mounts it. An existing folder of that name is never overwritten. A failed step
// removes what this call wrote.
func (h Host) Install(s Spec, user, password string) (err error) {
	if s.Type == "cifs" {
		if user == "" || password == "" {
			return errors.New("an SMB folder needs a user and a password")
		}
		if strings.ContainsAny(user+password, "\r\n\x00") {
			return errors.New("the user and the password are one line each")
		}
	}
	where := filepath.Join(h.MountRoot, s.Name)
	unit, err := h.Escape(where)
	if err != nil || unit == "" {
		return fmt.Errorf("systemd-escape %s: %w", where, err)
	}
	cred := filepath.Join(h.CredDir, s.Name+".cred")
	mountFile := filepath.Join(h.UnitDir, unit+".mount")
	automountFile := filepath.Join(h.UnitDir, unit+".automount")

	options := "nodev,nosuid,vers=4.2,soft,timeo=50,retrans=2"
	if s.Type == "cifs" {
		options = fmt.Sprintf("credentials=%s,uid=%[2]s,gid=%[2]s,vers=3.1.1,nodev,nosuid,soft,echo_interval=5", cred, h.EngineUser)
	}
	if h.SELinuxContext != "" {
		options += ",context=" + h.SELinuxContext
	}
	mount := fmt.Sprintf("[Unit]\nDescription=%s%s\nAfter=network-online.target\nWants=network-online.target\n\n"+
		"[Mount]\nWhat=%s\nWhere=%s\nType=%s\nOptions=%s\nTimeoutSec=30\n", description, s.Name, s.What, where, s.Type, options)
	automount := fmt.Sprintf("[Unit]\nDescription=%s%s (mounted on first use)\n\n[Automount]\nWhere=%s\nTimeoutIdleSec=0\n\n"+
		"[Install]\nWantedBy=remote-fs.target\n", description, s.Name, where)

	var written []string
	defer func() {
		if err != nil {
			for _, p := range written {
				_ = os.Remove(p)
			}
		}
	}()
	create := func(p, text string, mode os.FileMode) error {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		written = append(written, p)
		_, err = f.WriteString(text)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	if err := os.MkdirAll(where, 0o755); err != nil {
		return err
	}
	if s.Type == "cifs" {
		if err := os.MkdirAll(h.CredDir, 0o700); err != nil {
			return err
		}
		if err := create(cred, "username="+user+"\npassword="+password+"\n", 0o600); err != nil {
			return fmt.Errorf("the folder %s exists already or its credential cannot be written: %w", s.Name, err)
		}
	}
	if err := create(mountFile, mount, 0o644); err != nil {
		return fmt.Errorf("the folder %s exists already or its unit cannot be written: %w", s.Name, err)
	}
	if err := create(automountFile, automount, 0o644); err != nil {
		return err
	}
	if err := h.Systemctl("daemon-reload"); err != nil {
		return err
	}
	return h.Systemctl("enable", "--now", unit+".automount")
}

// Entry is one network folder as its units describe it, with its state now.
type Entry struct {
	Name  string
	Type  string
	What  string
	Where string
	State string
}

// List reads the folders whose units this package wrote. A folder whose mount is not active is
// unmounted: it mounts on first use, and a state read does not trigger that. A mounted one that
// does not answer in time is not responding.
//
// shortcut: the folders are probed one after another, so each dead folder adds up to 10 seconds;
// probe them together when a host has more than a handful.
func (h Host) List() ([]Entry, error) {
	automounts, err := filepath.Glob(filepath.Join(h.UnitDir, "*.automount"))
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, file := range automounts {
		auto, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(auto), "\nDescription="+description) {
			continue
		}
		base := strings.TrimSuffix(file, ".automount")
		mount, err := os.ReadFile(base + ".mount")
		if err != nil {
			continue
		}
		where := unitValue(string(auto), "Where")
		e := Entry{Name: path.Base(where), Type: unitValue(string(mount), "Type"), What: unitValue(string(mount), "What"), Where: where}
		switch {
		case !h.IsActive(filepath.Base(base) + ".mount"):
			e.State = StateUnmounted
		case h.Responds(where):
			e.State = StateMounted
		default:
			e.State = StateNotResponding
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// unitValue is the first value of key in a unit file's text.
func unitValue(unit, key string) string {
	for _, line := range strings.Split(unit, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}
