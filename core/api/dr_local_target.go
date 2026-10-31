// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var drLocalTargetPattern = regexp.MustCompile(`^/mnt/olivares-backups/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validDRLocalTarget(path string) bool {
	return path == "" || drLocalTargetPattern.MatchString(path)
}

// The exact writable ext4 mount must exist. A missing nofail disk must never
// redirect a backup into the directory underneath its mountpoint.
func drLocalTargetMountID(target string, data []byte) string {
	if target == "" || !validDRLocalTarget(target) {
		return ""
	}
	id := ""
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[4] != target {
			continue
		}
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+3 >= len(f) || f[sep+1] != "ext4" || !strings.Contains(","+f[5]+",", ",rw,") || !strings.Contains(","+f[sep+3]+",", ",rw,") {
			return ""
		}
		if id != "" {
			return ""
		}
		id = f[0]
	}
	return id
}
func (ds *drService) backupDir() string {
	if target := ds.scheduleSnapshot().LocalTarget; target != "" {
		return target
	}
	return ds.cfg.BackupDir
}

type drPinnedDirectory struct {
	Path, Target string
	file         *os.File
}

func (d drPinnedDirectory) Close() error {
	if d.file != nil {
		return d.file.Close()
	}
	return nil
}

func (ds *drService) openBackupDirectory() (drPinnedDirectory, error) {
	target := ds.scheduleSnapshot().LocalTarget
	if target == "" {
		target = ds.cfg.BackupDir
		if err := os.MkdirAll(target, 0700); err != nil {
			return drPinnedDirectory{}, err
		}
		return drPinnedDirectory{Path: target, Target: target}, nil
	}
	if !validDRLocalTarget(target) {
		return drPinnedDirectory{}, errors.New("invalid managed backup target")
	}
	return pinDRBackupDirectory(target, true)
}

// Keep bundles made before choosing a disk available for download and restore.
// An absent selected disk is never treated as a directory on the system volume.
func (ds *drService) backupReadDirectories() []string {
	dirs := []string{}
	target := ds.backupDir()
	if target != ds.cfg.BackupDir {
		if pin, err := pinDRBackupDirectory(target, true); err == nil {
			dirs = append(dirs, target)
			_ = pin.Close()
		}
	}
	return append(dirs, ds.cfg.BackupDir)
}
func (ds *drService) bundlePath(id string) string {
	for _, dir := range ds.backupReadDirectories() {
		path := filepath.Join(dir, id)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return filepath.Join(ds.cfg.BackupDir, id)
}
