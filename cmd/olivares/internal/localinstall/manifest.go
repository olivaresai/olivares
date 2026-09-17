// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package localinstall owns the on-host installation manifest and the
// fail-closed removal plan derived from it.
package localinstall

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ManifestSchema is emitted by install-service.sh and package post-install hooks.
	ManifestSchema  = "olivares.ai/local-install/v2"
	maxManifestSize = 128 << 10

	// LayoutDefault names the fixed data/config/unit tuples of the release-index
	// install_layout. An absent layout field means the same thing, so manifests
	// written before the field existed keep validating exactly as they did.
	LayoutDefault = "default"
	// LayoutCustom admits an operator-selected data directory. Binary, config
	// and unit paths stay inside the closed release-index layout; the data
	// directory must satisfy customDataDir and, before any mutation, must be
	// corroborated by the root-owned unit that the closed layout does name.
	LayoutCustom = "custom"

	// AgentOps roles recorded by install-agentops.sh. Their paths are derived
	// from the closed unit and config paths, so they are closed by construction.
	RoleDropin     = "dropin"
	RoleRuntimeEnv = "runtime-env"
	// DropinName is the managed AgentOps drop-in below <unit>.d.
	DropinName = "agentops.conf"
	// RuntimeEnvName is the AgentOps runtime env file beside the service config.
	RuntimeEnvName = "agentops.env"
)

// File is one installed path. Managed means Olivares, rather than a package
// manager or the operator, owns removal of that path.
type File struct {
	Path    string `json:"path"`
	Role    string `json:"role"`
	Mode    string `json:"mode"`
	Managed bool   `json:"managed"`
}

// Account records identity ownership. A purge may remove only identities this
// installation actually created; preserve retains them with the retained data.
type Account struct {
	User         string `json:"user,omitempty"`
	Group        string `json:"group,omitempty"`
	UserCreated  bool   `json:"user_created"`
	GroupCreated bool   `json:"group_created"`
}

// Manifest is the complete local ownership record consumed by uninstall.
//
// Layout and WorkspaceDir are optional and were added after the first v2
// producers shipped: a manifest without them is a default-layout install with
// no recorded AgentOps workspace. The decoder still rejects unknown fields.
type Manifest struct {
	Schema       string  `json:"schema"`
	Mode         string  `json:"mode"`
	Init         string  `json:"init"`
	Layout       string  `json:"layout,omitempty"`
	DataDir      string  `json:"data_dir"`
	Config       string  `json:"config"`
	WorkspaceDir string  `json:"workspace_dir,omitempty"`
	Files        []File  `json:"files"`
	Account      Account `json:"account"`
	ManifestPath string  `json:"manifest"`
}

// Custom reports whether the manifest declares an operator-selected data
// directory outside the fixed release-index tuples.
func (m *Manifest) Custom() bool { return m != nil && m.Layout == LayoutCustom }

// Unit returns the recorded service definition path, or "" when absent.
func (m *Manifest) Unit() string {
	for _, f := range m.Files {
		if f.Role == "unit" {
			return f.Path
		}
	}
	return ""
}

// ExecutedProgram is the executable a service definition has to run before it
// can witness this estate: the launchd wrapper inside the data directory (the
// plist runs the wrapper, which execs the engine), and the recorded binary for
// systemd and OpenRC. An empty result means the manifest records no binary,
// which Validate already refuses.
func (m *Manifest) ExecutedProgram() string {
	if m == nil {
		return ""
	}
	if m.Init == "launchd" {
		return filepath.Join(m.DataDir, "launchd-run.sh")
	}
	for _, f := range m.Files {
		if f.Role == "binary" {
			return f.Path
		}
	}
	return ""
}

// DropinPath is the only drop-in path a systemd manifest may record.
func DropinPath(unit string) string {
	return unit + ".d/" + DropinName
}

// RuntimeEnvPath is the only runtime env path a manifest may record.
func RuntimeEnvPath(config string) string {
	return filepath.Join(filepath.Dir(config), RuntimeEnvName)
}

// Load reads one strict v2 manifest. A symlink is never accepted as an
// ownership record: replacing it could redirect a privileged purge. Live
// removal additionally requires an owner appropriate to the declared mode;
// offline roots set requireTrustedOwner=false because they cannot preserve the
// host uid namespace and cannot mutate host accounts or paths.
func Load(name string, requireTrustedOwner bool) (*Manifest, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("read local install manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("local install manifest must be a regular file, not a link: %s", name)
	}
	if info.Size() > maxManifestSize {
		return nil, fmt.Errorf("local install manifest is larger than %d bytes", maxManifestSize)
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("read local install manifest: %w", err)
	}
	defer func() { _ = f.Close() }()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("measure opened local install manifest: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("local install manifest changed while it was being opened")
	}
	dec := json.NewDecoder(io.LimitReader(f, maxManifestSize+1))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse local install manifest: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("parse local install manifest: trailing JSON value")
	}
	if err := Validate(&m, userHome()); err != nil {
		return nil, err
	}
	if requireTrustedOwner {
		if err := validateManifestOwner(openedInfo, m.Mode); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

func userHome() string {
	if h := strings.TrimSpace(os.Getenv("HOME")); filepath.IsAbs(h) {
		return filepath.Clean(h)
	}
	h, _ := os.UserHomeDir()
	return filepath.Clean(h)
}

// Validate proves every destructive path belongs to the install_layout emitted
// by render-release-index.sh, or, for the data directory of a custom layout,
// to the customDataDir policy. It validates the whole list before Execute can
// touch any entry. A custom data directory is additionally corroborated by the
// closed-layout unit in Execute, because Validate has no filesystem.
func Validate(m *Manifest, home string) error {
	if m == nil {
		return fmt.Errorf("local install manifest is nil")
	}
	if m.Schema != ManifestSchema {
		return fmt.Errorf("unsupported local install manifest schema %q (want %q); rerun the signed installer to migrate the ownership record", m.Schema, ManifestSchema)
	}
	if m.Mode != "system" && m.Mode != "user" {
		return fmt.Errorf("unexpected install mode %q", m.Mode)
	}
	switch m.Init {
	case "systemd", "openrc", "launchd":
	default:
		return fmt.Errorf("unexpected init adapter %q", m.Init)
	}
	if m.Init == "openrc" && m.Mode != "system" {
		return fmt.Errorf("OpenRC manifest cannot describe a user install")
	}
	switch m.Layout {
	case "", LayoutDefault, LayoutCustom:
	default:
		return fmt.Errorf("unexpected install layout %q (want %s or %s)", m.Layout, LayoutDefault, LayoutCustom)
	}
	if m.Mode == "user" && !filepath.IsAbs(home) {
		return fmt.Errorf("cannot establish an absolute HOME for user-install path validation")
	}
	if m.Custom() {
		if err := customDataDir(m.DataDir); err != nil {
			return err
		}
	} else if err := allowedPath(m.Mode, "data", m.DataDir, home); err != nil {
		return err
	}
	if err := allowedPath(m.Mode, "config", m.Config, home); err != nil {
		return err
	}
	if m.WorkspaceDir != "" {
		if err := cleanAbsolutePath("workspace", m.WorkspaceDir); err != nil {
			return err
		}
	}
	wantManifest := filepath.Join(m.DataDir, "install-manifest.json")
	if m.ManifestPath != wantManifest {
		return fmt.Errorf("unexpected manifest path %q (want %q)", m.ManifestPath, wantManifest)
	}
	seen := map[string]bool{}
	var binaries, configs, units, wrappers, dropins, runtimeEnvs []string
	for _, f := range m.Files {
		if seen[f.Path] {
			return fmt.Errorf("duplicate install path %q", f.Path)
		}
		seen[f.Path] = true
		switch f.Role {
		case "binary", "config", "unit":
			if err := allowedPath(m.Mode, f.Role, f.Path, home); err != nil {
				return err
			}
		case "wrapper":
			if m.Init != "launchd" || f.Path != filepath.Join(m.DataDir, "launchd-run.sh") {
				return fmt.Errorf("unexpected launchd wrapper path %q", f.Path)
			}
		case RoleDropin:
			// Checked against the one recorded unit after the loop.
		case RoleRuntimeEnv:
			if f.Path != RuntimeEnvPath(m.Config) {
				return fmt.Errorf("unexpected runtime env path %q (want %q)", f.Path, RuntimeEnvPath(m.Config))
			}
		default:
			return fmt.Errorf("unexpected install file role %q for %s", f.Role, f.Path)
		}
		// A custom data directory is purged as a tree, so it must not contain
		// any closed-layout file the plan lists separately; only the launchd
		// wrapper legitimately lives inside it.
		if m.Custom() && f.Role != "wrapper" && within(m.DataDir, f.Path) {
			return fmt.Errorf("custom data directory %q must not contain the %s path %q", m.DataDir, f.Role, f.Path)
		}
		switch f.Role {
		case "binary":
			binaries = append(binaries, f.Path)
		case "config":
			configs = append(configs, f.Path)
		case "unit":
			units = append(units, f.Path)
		case "wrapper":
			wrappers = append(wrappers, f.Path)
		case RoleDropin:
			dropins = append(dropins, f.Path)
		case RoleRuntimeEnv:
			runtimeEnvs = append(runtimeEnvs, f.Path)
		}
	}
	if len(binaries) != 1 || len(configs) != 1 || len(units) != 1 || configs[0] != m.Config {
		return fmt.Errorf("install manifest must contain one allowed binary, config and unit")
	}
	if m.Init == "launchd" {
		if len(wrappers) != 1 {
			return fmt.Errorf("launchd install manifest must contain its one allowed wrapper")
		}
	} else if len(wrappers) != 0 {
		return fmt.Errorf("non-launchd install manifest must not contain a wrapper")
	}
	if len(dropins) > 1 || len(runtimeEnvs) > 1 {
		return fmt.Errorf("install manifest must not record more than one AgentOps drop-in or runtime env")
	}
	if len(dropins) == 1 {
		if m.Init != "systemd" {
			return fmt.Errorf("AgentOps drop-in %q requires the systemd adapter, not %s", dropins[0], m.Init)
		}
		if dropins[0] != DropinPath(units[0]) {
			return fmt.Errorf("unexpected AgentOps drop-in path %q (want %q)", dropins[0], DropinPath(units[0]))
		}
	}
	if err := validateTuple(m, units[0], home); err != nil {
		return err
	}
	if m.Mode == "system" {
		wantUser, wantGroup := "olivares", "olivares"
		if m.Init == "launchd" {
			wantUser, wantGroup = "_olivares", "staff"
		}
		if m.Account.User != wantUser || m.Account.Group != wantGroup {
			return fmt.Errorf("unexpected system service account %q:%q (want %s:%s)", m.Account.User, m.Account.Group, wantUser, wantGroup)
		}
		if m.Init == "launchd" && m.Account.GroupCreated {
			return fmt.Errorf("launchd install manifest must not claim ownership of the shared staff group")
		}
	} else if m.Account.User != "" || m.Account.Group != "" || m.Account.UserCreated || m.Account.GroupCreated {
		return fmt.Errorf("user install manifest must not claim a system account")
	}
	return nil
}

// validateTuple binds config and unit to the adapter the manifest declares.
// The data directory is part of the tuple for the default layout; a custom
// layout already passed customDataDir and is corroborated by the unit later.
func validateTuple(m *Manifest, unit, home string) error {
	data := func(want string) bool { return m.Custom() || m.DataDir == want }
	switch m.Mode + ":" + m.Init {
	case "system:systemd":
		if data("/var/lib/olivares") && m.Config == "/etc/olivares/olivares.env" &&
			(unit == "/etc/systemd/system/olivares.service" || unit == "/usr/lib/systemd/system/olivares.service") {
			return nil
		}
	case "system:openrc":
		if data("/var/lib/olivares") && m.Config == "/etc/olivares/olivares.env" && unit == "/etc/init.d/olivares" {
			return nil
		}
	case "system:launchd":
		if data("/Library/Application Support/Olivares") &&
			m.Config == "/Library/Preferences/dev.olivares.olivares.env" &&
			unit == "/Library/LaunchDaemons/dev.olivares.olivares.plist" {
			return nil
		}
	case "user:systemd":
		if data(filepath.Join(home, ".local/share/olivares")) &&
			m.Config == filepath.Join(home, ".config/olivares/olivares.env") &&
			unit == filepath.Join(home, ".config/systemd/user/olivares.service") {
			return nil
		}
	case "user:launchd":
		if data(filepath.Join(home, "Library/Application Support/Olivares")) &&
			m.Config == filepath.Join(home, "Library/Preferences/dev.olivares.olivares.env") &&
			unit == filepath.Join(home, "Library/LaunchAgents/dev.olivares.olivares.plist") {
			return nil
		}
	}
	return fmt.Errorf("install manifest data/config/unit tuple is outside the release-index install_layout")
}

// cleanAbsolutePath is the shape every recorded path must have before any
// policy looks at it: absolute, canonical, not the filesystem root, and free
// of control characters that a service definition or a log line could hide.
func cleanAbsolutePath(role, name string) error {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name || name == string(filepath.Separator) {
		return fmt.Errorf("unexpected %s path %q: path must be clean, absolute and non-root", role, name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("unexpected %s path %q: path must not contain control characters", role, name)
		}
	}
	return nil
}

// customDataDir is the admission policy for an operator-selected data
// directory. It is deliberately a shape rule and not a directory allowlist,
// so no legitimate location is removed: the directory must be a dedicated
// path at least two levels deep, because a purge removes it as a tree and a
// top-level directory is a shared system root on every supported platform.
func customDataDir(name string) error {
	if err := cleanAbsolutePath("data", name); err != nil {
		return err
	}
	if strings.Count(name, string(filepath.Separator)) < 2 {
		return fmt.Errorf("unexpected data path %q: a custom data directory must be a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory", name)
	}
	return nil
}

// within reports whether name is dir itself or lies below it. This is a
// lexical statement about the recorded paths, used to keep the plan
// consistent; it is never used to decide what the filesystem contains.
func within(dir, name string) bool {
	return name == dir || strings.HasPrefix(name, dir+string(filepath.Separator))
}

var systemLayout = map[string]map[string]bool{
	"binary": {
		"/opt/olivares": true, "/opt/olivares/bin/olivares": true,
		"/usr/bin/olivares": true, "/usr/local/bin/olivares": true,
	},
	"config": {
		"/etc/olivares/olivares.env":                     true,
		"/Library/Preferences/dev.olivares.olivares.env": true,
	},
	"data": {
		"/var/lib/olivares":                     true,
		"/Library/Application Support/Olivares": true,
	},
	"unit": {
		"/etc/systemd/system/olivares.service":               true,
		"/etc/init.d/olivares":                               true,
		"/usr/lib/systemd/system/olivares.service":           true,
		"/Library/LaunchDaemons/dev.olivares.olivares.plist": true,
	},
}

var userSuffixLayout = map[string][]string{
	"binary": {"/.local/bin/olivares"},
	"config": {"/.config/olivares/olivares.env", "/Library/Preferences/dev.olivares.olivares.env"},
	"data":   {"/.local/share/olivares", "/Library/Application Support/Olivares"},
	"unit":   {"/.config/systemd/user/olivares.service", "/Library/LaunchAgents/dev.olivares.olivares.plist"},
}

func allowedPath(mode, role, name, home string) error {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name || name == string(filepath.Separator) {
		return fmt.Errorf("unexpected %s path %q: path must be clean, absolute and non-root", role, name)
	}
	if mode == "system" {
		if systemLayout[role][name] {
			return nil
		}
	} else {
		for _, suffix := range userSuffixLayout[role] {
			if name == filepath.Clean(home+suffix) {
				return nil
			}
		}
	}
	return fmt.Errorf("unexpected %s path %q: absent from release-index install_layout", role, name)
}
