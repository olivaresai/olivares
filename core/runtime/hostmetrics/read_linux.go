//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostmetrics

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// cpuTimes are the cumulative jiffies of every CPU from /proc/stat.
type cpuTimes struct{ idle, total uint64 }

func readCPU() (cpuTimes, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return parseCPU(string(b))
}

// parseCPU reads the first line: cpu user nice system idle iowait irq softirq steal.
// Guest time is already inside user, so it is not added again.
func parseCPU(stat string) (cpuTimes, error) {
	line, _, _ := strings.Cut(stat, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}, errors.New("hostmetrics: /proc/stat has no cpu line")
	}
	var t cpuTimes
	for i, s := range f[1:] {
		if i == 8 { // guest, guest_nice
			break
		}
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return cpuTimes{}, fmt.Errorf("hostmetrics: /proc/stat: %w", err)
		}
		t.total += v
		if i == 3 || i == 4 { // idle, iowait
			t.idle += v
		}
	}
	return t, nil
}

// cpuPercent is the share of non-idle time between two readings, 0 when no time passed.
func cpuPercent(prev, cur cpuTimes) float64 {
	dt := cur.total - prev.total
	if cur.total < prev.total || cur.idle < prev.idle || dt == 0 {
		return 0
	}
	return float64(dt-(cur.idle-prev.idle)) / float64(dt) * 100
}

func readLoad() (Load, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return Load{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return Load{}, errors.New("hostmetrics: /proc/loadavg is malformed")
	}
	var l Load
	for i, dst := range []*float64{&l.One, &l.Five, &l.Fifteen} {
		if *dst, err = strconv.ParseFloat(f[i], 64); err != nil {
			return Load{}, fmt.Errorf("hostmetrics: /proc/loadavg: %w", err)
		}
	}
	return l, nil
}

func readMemory() (Memory, Swap, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Memory{}, Swap{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return parseMemory(string(b))
}

// parseMemory reads /proc/meminfo (values in kB). Used is MemTotal - MemAvailable,
// the same as free(1); a kernel without MemAvailable is refused rather than guessed.
func parseMemory(info string) (Memory, Swap, error) {
	kb := map[string]uint64{}
	for _, line := range strings.Split(info, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if f := strings.Fields(rest); len(f) > 0 {
			if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
				kb[name] = v
			}
		}
	}
	total, ok1 := kb["MemTotal"]
	avail, ok2 := kb["MemAvailable"]
	if !ok1 || !ok2 || avail > total {
		return Memory{}, Swap{}, errors.New("hostmetrics: /proc/meminfo lacks MemTotal or MemAvailable")
	}
	swapTotal, swapFree := kb["SwapTotal"], kb["SwapFree"]
	if swapFree > swapTotal {
		swapFree = swapTotal
	}
	return Memory{TotalBytes: total << 10, UsedBytes: (total - avail) << 10, AvailableBytes: avail << 10},
		Swap{TotalBytes: swapTotal << 10, UsedBytes: (swapTotal - swapFree) << 10}, nil
}

// skipFS are the filesystems that are not storage: kernel interfaces, loop-mounted
// read-only images that always read full, and network filesystems, where a
// stale mount would hang statfs and with it the request.
var skipFS = map[string]bool{
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "devtmpfs": true, "devpts": true,
	"securityfs": true, "debugfs": true, "tracefs": true, "pstore": true, "bpf": true, "configfs": true,
	"fusectl": true, "mqueue": true, "hugetlbfs": true, "autofs": true, "binfmt_misc": true,
	"selinuxfs": true, "efivarfs": true, "nsfs": true, "rpc_pipefs": true, "squashfs": true,
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "9p": true, "fuse.sshfs": true,
}

func readFilesystems() ([]Filesystem, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer f.Close()
	seen := map[string]bool{}
	out := []Filesystem{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		mount, dev, typ, src, ok := parseMountinfo(sc.Text())
		if !ok || skipFS[typ] || seen[dev] || strings.HasPrefix(mount, "/proc/") || strings.HasPrefix(mount, "/sys/") {
			continue
		}
		// A bind-mounted file (a container's init binary) is not a filesystem to watch.
		if fi, err := os.Stat(mount); err != nil || !fi.IsDir() {
			continue
		}
		var st unix.Statfs_t
		if unix.Statfs(mount, &st) != nil || st.Blocks == 0 {
			continue
		}
		seen[dev] = true
		bs := uint64(st.Bsize)
		out = append(out, Filesystem{Mount: mount, Device: src, Type: typ,
			TotalBytes: st.Blocks * bs, UsedBytes: (st.Blocks - st.Bfree) * bs})
	}
	if err = sc.Err(); err != nil {
		return nil, fmt.Errorf("hostmetrics: /proc/self/mountinfo: %w", err)
	}
	return out, nil
}

// parseMountinfo splits one mountinfo line: "id parent major:minor root mount opts
// [optional...] - fstype source superopts". dev is major:minor, the key that
// folds a bind mount into the filesystem it shows.
func parseMountinfo(line string) (mount, dev, typ, src string, ok bool) {
	left, right, found := strings.Cut(line, " - ")
	lf, rf := strings.Fields(left), strings.Fields(right)
	if !found || len(lf) < 5 || len(rf) < 2 {
		return "", "", "", "", false
	}
	return unescapeMount(lf[4]), lf[2], rf[0], unescapeMount(rf[1]), true
}

// unescapeMount decodes the \040 style octal escapes the kernel uses for
// spaces, tabs, newlines and backslashes in a path.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// measureFolders sums the allocated blocks of every entry under each directory of
// root. It does not follow symlinks and counts a hard-linked file once per link; a
// subtree it cannot read adds nothing. It stops at MaxFolderEntries entries or when
// ctx ends, and then reports Truncated.
func measureFolders(ctx context.Context, root string, now time.Time) Folders {
	res := Folders{Root: root, MeasuredAt: now, Folders: []Folder{}}
	if root == "" {
		return res
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return res // a fresh install has no session folder yet
	}
	visited := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		var bytes uint64
		walkErr := filepath.WalkDir(filepath.Join(root, d.Name()), func(_ string, e fs.DirEntry, err error) error {
			if err != nil {
				if e != nil && e.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			visited++
			if visited > MaxFolderEntries || (visited&1023 == 0 && ctx.Err() != nil) {
				return errTruncated
			}
			if info, ierr := e.Info(); ierr == nil {
				if st, ok := info.Sys().(*syscall.Stat_t); ok {
					bytes += uint64(st.Blocks) * 512
				}
			}
			return nil
		})
		res.Folders = append(res.Folders, Folder{Name: d.Name(), Bytes: bytes})
		res.TotalBytes += bytes
		if errors.Is(walkErr, errTruncated) {
			res.Truncated = true
			break
		}
	}
	sort.Slice(res.Folders, func(i, j int) bool { return res.Folders[i].Bytes > res.Folders[j].Bytes })
	if len(res.Folders) > MaxFolders {
		res.Folders = res.Folders[:MaxFolders]
	}
	return res
}

var errTruncated = errors.New("hostmetrics: folder measurement reached its bound")
