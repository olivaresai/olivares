// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// Operation is one of the three public uninstall modes.
type Operation string

const (
	Plan     Operation = "plan"
	Preserve Operation = "preserve"
	Purge    Operation = "purge"
)

// Item is one disclosed effect of an uninstall operation. Note explains a
// deviation from the manifest's own record, such as a shared path that a later
// installation now owns.
type Item struct {
	Action string
	Role   string
	Path   string
	Note   string
}

// Options controls execution. Root stages a hermetic offline filesystem; no
// host init or account command is ever run when Root is non-empty.
type Options struct {
	Operation Operation
	Root      string
	Out       io.Writer
	Run       func(string, ...string) error
}

// BuildPlan returns the complete effect list after validating every path.
func BuildPlan(m *Manifest, op Operation, home string) ([]Item, error) {
	if err := Validate(m, home); err != nil {
		return nil, err
	}
	if op != Plan && op != Preserve && op != Purge {
		return nil, fmt.Errorf("unknown uninstall operation %q", op)
	}
	items := []Item{{Action: "stop-disable", Role: "service", Path: m.Init + ":olivares"}}
	for _, f := range m.Files {
		items = append(items, Item{Action: fileAction(f, op), Role: f.Role, Path: f.Path})
	}
	dataAction := "keep"
	if op == Purge {
		dataAction = "remove-tree"
	}
	items = append(items,
		Item{Action: dataAction, Role: "data", Path: m.DataDir},
		Item{Action: dataAction, Role: "logs", Path: filepath.Join(m.DataDir, "*.log")},
		Item{Action: dataAction, Role: "keys", Path: filepath.Join(m.DataDir, "*-signing.key")},
	)
	if m.WorkspaceDir != "" {
		// An explicitly selected workspace is operator territory: no operation
		// removes it. The recorded default location lies inside the data tree, so
		// a purge disclosing remove-tree there describes the data removal, not a
		// second primitive; a link at that path is unlinked, its target kept.
		action := "keep"
		if op == Purge && within(m.DataDir, m.WorkspaceDir) {
			action = "remove-tree"
		}
		items = append(items, Item{Action: action, Role: "workspace", Path: m.WorkspaceDir})
	}
	if m.Custom() {
		// Preserve records the uninstall witness beside the service config before
		// it removes the unit, so later operations keep a second, fixed-location
		// witness; purge removes it last, after the data tree is gone.
		action := "keep"
		switch op {
		case Preserve:
			action = "record"
		case Purge:
			action = "remove"
		}
		items = append(items, Item{Action: action, Role: "witness", Path: WitnessPath(m)})
	}
	if m.Mode == "system" {
		accountAction := "keep"
		if op == Purge && m.Account.UserCreated {
			accountAction = "remove"
		}
		items = append(items, Item{Action: accountAction, Role: "system-user", Path: m.Account.User})
		groupAction := "keep"
		if op == Purge && m.Account.GroupCreated {
			groupAction = "remove"
		}
		items = append(items, Item{Action: groupAction, Role: "system-group", Path: m.Account.Group})
	}
	return items, nil
}

// Execute applies a prevalidated preserve or purge. Plan only prints and never
// reaches a mutating primitive.
func Execute(m *Manifest, opts Options) error {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	items, err := BuildPlan(m, opts.Operation, userHome())
	if err != nil {
		return err
	}
	if opts.Root != "" {
		if !filepath.IsAbs(opts.Root) || filepath.Clean(opts.Root) != opts.Root || opts.Root == string(filepath.Separator) {
			return fmt.Errorf("offline root must be a clean absolute path other than /")
		}
	}
	// A custom data directory is admitted by the manifest's own policy, so the
	// closed-layout unit must vouch for it before even a plan is disclosed.
	witness, err := corroborateLayout(m, opts.Root)
	if err != nil {
		return err
	}
	// Owning this DATA is not the same as controlling the live SERVICE, and the
	// two are established by different evidence. The manifest — with, for a
	// custom layout, the unit or the witness this product wrote — says which
	// data directory this estate owns. Only a service definition at a closed
	// unit path that executes this estate's engine says the installation is
	// still the live one. When another installation holds that path its service
	// is never stopped, its software is never removed and the identities it
	// still uses are never deleted; when no definition is there at all, nothing
	// is stopped because there is nothing to stop.
	control, liveUnit, err := serviceControlOf(m, opts.Root)
	if err != nil {
		return err
	}
	kept := keptByOthers(m, control, liveUnit, witness)
	for i := range items {
		item := &items[i]
		switch {
		case item.Role == "service":
			switch control {
			case serviceForeign:
				item.Action, item.Note = "keep", "another installation's service is live at "+liveUnit
			case serviceAbsent:
				item.Action, item.Note = "skip", "no service definition at the closed unit path; nothing to stop"
			}
		case control == serviceForeign && item.Action == "remove" &&
			(item.Role == "system-user" || item.Role == "system-group"):
			item.Action, item.Note = "keep", "still used by the installation whose service is live at "+liveUnit
		case item.Action == "remove":
			if note, ok := kept[item.Path]; ok {
				item.Action, item.Note = "keep", note
			}
		}
	}
	for _, item := range items {
		if item.Note != "" {
			fmt.Fprintf(opts.Out, "  %-12s %-13s %s (%s)\n", item.Action, item.Role, item.Path, item.Note)
			continue
		}
		fmt.Fprintf(opts.Out, "  %-12s %-13s %s\n", item.Action, item.Role, item.Path)
	}
	if opts.Operation == Plan {
		return nil
	}
	if opts.Operation == Purge {
		target, err := rooted(opts.Root, m.DataDir)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to purge data directory through a symlink: %s", m.DataDir)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if opts.Root == "" && control == serviceOwn {
		if err := stopService(m, opts.Run); err != nil {
			return err
		}
	}
	// The witness is written before the unit it stands in for is removed, so a
	// preserve or an interrupted purge always leaves the successor operation a
	// corroborating record at a fixed root-owned location. It carries the
	// software this operation promised to remove and has not removed yet, so the
	// successor can tell "this product already removed that" from "this product
	// was interrupted before it could".
	pending := pendingSoftware(m, opts.Operation, kept)
	if m.Custom() {
		if err := writeWitness(m, opts.Root, opts.Operation, pending); err != nil {
			return err
		}
	}
	// recordPending keeps that list truthful for whatever comes next: a failure
	// returns with the record naming exactly what is still outstanding.
	remaining := append([]string(nil), pending...)
	recordPending := func(cause error) error {
		if !m.Custom() {
			return cause
		}
		if err := writeWitness(m, opts.Root, opts.Operation, remaining); err != nil {
			if cause == nil {
				return err
			}
			return fmt.Errorf("%w (and the retry record could not be updated: %v)", cause, err)
		}
		return cause
	}

	// Files first, data last. Validation above covered the whole list before the
	// first unlink, so a hostile late entry cannot produce a partial purge.
	files := append([]File(nil), m.Files...)
	sort.SliceStable(files, func(i, j int) bool {
		order := map[string]int{"wrapper": 0, RoleDropin: 1, "unit": 2, "binary": 3, "config": 4, RoleRuntimeEnv: 5}
		return order[files[i].Role] < order[files[j].Role]
	})
	removedUnit := false
	for _, f := range files {
		if fileAction(f, opts.Operation) != "remove" {
			continue
		}
		if _, notOurs := kept[f.Path]; notOurs {
			continue
		}
		target, err := rooted(opts.Root, f.Path)
		if err != nil {
			return recordPending(err)
		}
		if err := removeExact(target); err != nil {
			return recordPending(fmt.Errorf("remove %s %s: %w", f.Role, f.Path, err))
		}
		remaining = drop(remaining, f.Path)
		if f.Role == "unit" {
			removedUnit = true
		}
		if f.Role == RoleDropin {
			// The <unit>.d directory was created for this drop-in. Remove it only
			// when nothing else lives there; an operator's own drop-in keeps it.
			_ = os.Remove(filepath.Dir(target))
		}
	}
	if err := recordPending(nil); err != nil {
		return err
	}
	if opts.Root == "" && removedUnit {
		if err := reloadServiceManager(m, opts.Run); err != nil {
			return err
		}
	}
	if opts.Operation == Purge {
		target, err := rooted(opts.Root, m.DataDir)
		if err != nil {
			return err
		}
		if err := purgeDataTree(target, filepath.Base(m.ManifestPath)); err != nil {
			return fmt.Errorf("purge data directory %s: %w", m.DataDir, err)
		}
		if m.Custom() {
			witnessTarget, err := rooted(opts.Root, WitnessPath(m))
			if err != nil {
				return err
			}
			if err := removeExact(witnessTarget); err != nil {
				return fmt.Errorf("remove uninstall witness %s: %w", WitnessPath(m), err)
			}
		}
		if opts.Root == "" && m.Mode == "system" && control != serviceForeign {
			if m.Account.UserCreated {
				if err := run(opts.Run, "userdel", m.Account.User); err != nil {
					return fmt.Errorf("remove system user %s: %w", m.Account.User, err)
				}
			}
			if m.Account.GroupCreated {
				if err := run(opts.Run, "groupdel", m.Account.Group); err != nil {
					return fmt.Errorf("remove system group %s: %w", m.Account.Group, err)
				}
			}
		}
	}
	return nil
}

// serviceControl says who holds the live service definition at the closed unit
// paths of this manifest's adapter.
type serviceControl string

const (
	// serviceOwn: a definition there executes this estate's engine with this
	// estate's data directory. This installation is the live one.
	serviceOwn serviceControl = "own"
	// serviceForeign: a definition is there and it is not this estate's.
	serviceForeign serviceControl = "foreign"
	// serviceAbsent: no definition is there at all.
	serviceAbsent serviceControl = "absent"
)

// unitCandidates lists the service definition paths the closed install_layout
// allows for this manifest's mode and adapter, in the order the init system
// resolves them (for systemd, /etc overrides /usr/lib).
func unitCandidates(m *Manifest, home string) []string {
	switch m.Mode + ":" + m.Init {
	case "system:systemd":
		return []string{"/etc/systemd/system/olivares.service", "/usr/lib/systemd/system/olivares.service"}
	case "system:openrc":
		return []string{"/etc/init.d/olivares"}
	case "system:launchd":
		return []string{"/Library/LaunchDaemons/dev.olivares.olivares.plist"}
	case "user:systemd":
		return []string{filepath.Join(home, ".config/systemd/user/olivares.service")}
	case "user:launchd":
		return []string{filepath.Join(home, "Library/LaunchAgents/dev.olivares.olivares.plist")}
	}
	return nil
}

// serviceControlOf reads those paths and reports control. A definition counts
// as this estate's only when it executes the recorded program with the recorded
// data directory — the same rule that corroborates the layout. One that names
// another directory belongs to a later installation. One that says neither
// (unreadable, a link, an operator's own text) is attributed conservatively:
// this estate keeps its own default-layout tuple, which the release index
// closes, but a custom layout — whose authority came from the witness rather
// than from that definition — treats it as somebody else's.
func serviceControlOf(m *Manifest, root string) (serviceControl, string, error) {
	program := m.ExecutedProgram()
	present := ""
	for _, logical := range unitCandidates(m, userHome()) {
		target, err := rooted(root, logical)
		if err != nil {
			return "", "", err
		}
		info, err := os.Lstat(target)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", "", err
		}
		if present == "" {
			present = logical
		}
		if !info.Mode().IsRegular() {
			continue
		}
		body, err := readTrustedFile(target, logical, m.Mode, root)
		if err != nil {
			continue
		}
		dir, err := UnitDataDir(m.Init, string(body), program)
		if err != nil {
			continue
		}
		if dir == m.DataDir {
			return serviceOwn, logical, nil
		}
		return serviceForeign, logical, nil
	}
	if present == "" {
		return serviceAbsent, "", nil
	}
	if !m.Custom() && present == m.Unit() {
		return serviceOwn, present, nil
	}
	return serviceForeign, present, nil
}

// softwareRole reports the roles this product installs at closed, host-wide
// paths, where a later installation legitimately writes its own copy. They are
// the roles a retry has to reason about; configuration-like files are operator
// territory and follow the live-service rule alone.
func softwareRole(role string) bool {
	switch role {
	case "binary", "unit", RoleDropin, "wrapper":
		return true
	}
	return false
}

// keptByOthers is the one place that decides which recorded paths this
// operation may NOT touch, and why. It is used for both the disclosed plan and
// the removals, so the two can never disagree.
func keptByOthers(m *Manifest, control serviceControl, liveUnit string, w *Witness) map[string]string {
	kept := map[string]string{}
	switch control {
	case serviceForeign:
		note := "in use by the installation whose service is live at " + liveUnit
		for _, f := range m.Files {
			switch f.Role {
			case "binary", "unit", RoleDropin, "wrapper", "config", RoleRuntimeEnv:
				kept[f.Path] = note
			}
		}
	case serviceAbsent:
		if w == nil {
			// No witness: nothing says this product removed anything, so the
			// manifest's own record stands.
			return kept
		}
		if w.PendingSoftware == nil {
			// A record from before this product listed its pending effects. It
			// proves an uninstall STARTED, never that one finished, so the
			// software is disclosed as kept with that reason rather than removed
			// as if it were still ours or claimed as done.
			for _, f := range m.Files {
				if softwareRole(f.Role) {
					kept[f.Path] = "an earlier uninstall removed the software it recorded and left no list of what stayed pending"
				}
			}
			return kept
		}
		pending := map[string]bool{}
		for _, path := range w.PendingSoftware {
			pending[path] = true
		}
		for _, f := range m.Files {
			if softwareRole(f.Role) && !pending[f.Path] {
				kept[f.Path] = "removed earlier by this product; not this estate's any more"
			}
		}
	}
	return kept
}

// pendingSoftware is what this operation promises to remove and has not removed
// yet: exactly the software paths it is allowed to touch.
func pendingSoftware(m *Manifest, op Operation, kept map[string]string) []string {
	out := []string{}
	for _, f := range m.Files {
		if !softwareRole(f.Role) || fileAction(f, op) != "remove" {
			continue
		}
		if _, notOurs := kept[f.Path]; notOurs {
			continue
		}
		out = append(out, f.Path)
	}
	return out
}

func drop(list []string, value string) []string {
	out := list[:0]
	for _, v := range list {
		if v != value {
			out = append(out, v)
		}
	}
	return out
}

// fileAction is the one place that decides what happens to a recorded file.
// Configuration-like files (the service env and the AgentOps runtime env) are
// operator territory after creation: kept by preserve, removed only by purge.
// Every other role follows its managed flag.
func fileAction(f File, op Operation) string {
	switch {
	case f.Role == "config" || f.Role == RoleRuntimeEnv:
		if op == Purge {
			return "remove"
		}
		return "keep"
	case f.Managed:
		return "remove"
	}
	return "keep"
}

// WitnessSchema identifies the record Execute leaves beside the service
// configuration when it removes the unit of a custom layout.
//
// v2 adds pending_software. The difference is not cosmetic: a v1 record proves
// only that an uninstall STARTED, and this product used to read it as proof
// that the software it listed was already gone — so a purge interrupted before
// it could remove the binary reported success on the retry and left that binary
// installed. v1 records are still accepted, as "pending unknown", because they
// still corroborate the data directory; they no longer authorise concluding
// anything about software.
const (
	WitnessSchema   = "olivares.ai/uninstall-witness/v2"
	witnessSchemaV1 = "olivares.ai/uninstall-witness/v1"
)

// Witness is that record. It is written only after the unit corroborated the
// data directory, at a fixed root-owned location inside the closed layout, so
// a later plan or purge of the same estate still has a second witness the
// manifest alone cannot supply.
type Witness struct {
	Schema     string `json:"schema"`
	DataDir    string `json:"data_dir"`
	Unit       string `json:"unit"`
	RemovedBy  string `json:"removed_by"`
	RecordedAt string `json:"recorded_at"`
	// PendingSoftware lists the managed software paths the operation that wrote
	// this record had not removed yet. An empty list means every promised
	// removal completed; an ABSENT list (v1) means the record cannot say. The
	// two are deliberately different values and are never conflated.
	PendingSoftware []string `json:"pending_software"`
}

// WitnessPath derives the witness location from the closed config path: beside
// the service configuration, named by the data directory's digest so several
// preserved estates on one host never share a file. It is never read from the
// manifest, so a forged record cannot choose where its witness is looked for.
func WitnessPath(m *Manifest) string {
	sum := sha256.Sum256([]byte(m.DataDir))
	return filepath.Join(filepath.Dir(m.Config), fmt.Sprintf("olivares-uninstall-witness-%x.json", sum[:8]))
}

// corroborateLayout requires a second, independently trusted witness for a
// custom data directory. The manifest lives inside the directory it describes
// and is owner-checked. The first witness is the service definition at the
// closed-layout unit path, owner-checked too, whose execution directive must
// run the recorded program and name exactly that directory (UnitDataDir;
// comments, other sections and other programs never count). When this product
// itself removed that unit (preserve, or a purge that did not finish), the
// witness record it wrote at WitnessPath stands in: same closed directory, same
// owner check, same data directory and unit. A manifest with neither is refused
// before a plan is disclosed. A non-nil result means the witness, not the unit,
// is what corroborated the estate.
func corroborateLayout(m *Manifest, root string) (*Witness, error) {
	if !m.Custom() {
		return nil, nil
	}
	unitErr := corroborateByUnit(m, root)
	if unitErr == nil {
		return nil, nil
	}
	w, witnessErr := corroborateByWitness(m, root)
	if witnessErr == nil {
		return w, nil
	}
	return nil, fmt.Errorf("corroborate custom data directory %q: %v; and %v. Re-run the signed installer to render the unit for this data directory and retry, or remove the estate by hand", m.DataDir, unitErr, witnessErr)
}

func corroborateByUnit(m *Manifest, root string) error {
	unit := m.Unit()
	target, err := rooted(root, unit)
	if err != nil {
		return err
	}
	body, err := readTrustedFile(target, unit, m.Mode, root)
	if err != nil {
		return err
	}
	dir, err := UnitDataDir(m.Init, string(body), m.ExecutedProgram())
	if err != nil {
		return fmt.Errorf("unit %s does not name data directory %q (%v)", unit, m.DataDir, err)
	}
	if dir != m.DataDir {
		return fmt.Errorf("unit %s does not name data directory %q (it executes the engine with %q)", unit, m.DataDir, dir)
	}
	return nil
}

func corroborateByWitness(m *Manifest, root string) (*Witness, error) {
	logical := WitnessPath(m)
	target, err := rooted(root, logical)
	if err != nil {
		return nil, err
	}
	body, err := readTrustedFile(target, logical, m.Mode, root)
	if err != nil {
		return nil, fmt.Errorf("no uninstall witness: %w", err)
	}
	var w Witness
	dec := json.NewDecoder(bytesReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("uninstall witness %s is malformed: %w", logical, err)
	}
	switch w.Schema {
	case WitnessSchema:
		if w.PendingSoftware == nil {
			return nil, fmt.Errorf("uninstall witness %s is malformed: a %s record always lists pending_software, even when it is empty", logical, WitnessSchema)
		}
	case witnessSchemaV1:
		if w.PendingSoftware != nil {
			return nil, fmt.Errorf("uninstall witness %s is malformed: a %s record cannot carry pending_software", logical, witnessSchemaV1)
		}
	default:
		return nil, fmt.Errorf("uninstall witness %s has schema %q, not %q", logical, w.Schema, WitnessSchema)
	}
	if w.DataDir != m.DataDir || w.Unit != m.Unit() ||
		(w.RemovedBy != string(Preserve) && w.RemovedBy != string(Purge)) {
		return nil, fmt.Errorf("uninstall witness %s does not describe data directory %q and unit %q", logical, m.DataDir, m.Unit())
	}
	if _, err := time.Parse(time.RFC3339, w.RecordedAt); err != nil {
		return nil, fmt.Errorf("uninstall witness %s has no valid recorded_at", logical)
	}
	// A pending entry only ever authorises removing something this manifest
	// already records as managed software; anything else is a record about
	// another estate and is refused rather than acted on.
	managed := map[string]bool{}
	for _, f := range m.Files {
		if softwareRole(f.Role) {
			managed[f.Path] = true
		}
	}
	for _, path := range w.PendingSoftware {
		if !managed[path] {
			return nil, fmt.Errorf("uninstall witness %s lists %q as pending, which this manifest does not record as managed software", logical, path)
		}
	}
	return &w, nil
}

// readTrustedFile reads a witness file with the same posture as the manifest:
// a regular file that is not a link, bounded in size and, on a live host,
// owned by the uid the declared mode requires and not writable by others.
func readTrustedFile(target, logical, mode, root string) ([]byte, error) {
	info, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", logical, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a link", logical)
	}
	if info.Size() > maxManifestSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", logical, maxManifestSize)
	}
	if root == "" {
		if err := validateManifestOwner(info, mode); err != nil {
			return nil, fmt.Errorf("%s: %w", logical, err)
		}
	}
	body, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", logical, err)
	}
	return body, nil
}

// writeWitness records, atomically, that this operation is about to remove (or
// has finished removing) the unit that corroborated the data directory, and
// which of the software it promised to remove is still outstanding.
func writeWitness(m *Manifest, root string, op Operation, pending []string) error {
	logical := WitnessPath(m)
	target, err := rooted(root, logical)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("refusing to write uninstall witness over a link or non-regular file: %s", logical)
	}
	if pending == nil {
		pending = []string{}
	}
	body, err := json.Marshal(Witness{Schema: WitnessSchema, DataDir: m.DataDir, Unit: m.Unit(),
		RemovedBy: string(op), RecordedAt: time.Now().UTC().Format(time.RFC3339), PendingSoftware: pending})
	if err != nil {
		return err
	}
	perm := os.FileMode(0o640)
	if m.Mode == "user" {
		perm = 0o600
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), perm); err != nil {
		return fmt.Errorf("write uninstall witness %s: %w", logical, err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write uninstall witness %s: %w", logical, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write uninstall witness %s: %w", logical, err)
	}
	return nil
}

// purgeDataTree removes the data directory so that an interrupted purge can be
// retried: every entry except the ownership manifest goes first, the manifest
// only once the rest is gone, and the directory itself last.
func purgeDataTree(target, manifestName string) error {
	entries, err := os.ReadDir(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.Name() == manifestName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	if err := removeExact(filepath.Join(target, manifestName)); err != nil {
		return err
	}
	return os.Remove(target)
}

func bytesReader(b []byte) io.Reader { return &byteReader{b: b} }

type byteReader struct{ b []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

func reloadServiceManager(m *Manifest, runner func(string, ...string) error) error {
	switch m.Init + ":" + m.Mode {
	case "systemd:system":
		if err := run(runner, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("reload systemd after unit removal: %w", err)
		}
	case "systemd:user":
		if err := run(runner, "systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("reload user systemd after unit removal: %w", err)
		}
	}
	return nil
}

func rooted(root, logical string) (string, error) {
	if root == "" {
		return logical, nil
	}
	target := filepath.Join(root, logical[1:])
	if target == root || len(target) <= len(root) || target[:len(root)+1] != root+string(filepath.Separator) {
		return "", fmt.Errorf("path %q escapes offline root %q", logical, root)
	}
	return target, nil
}

func removeExact(name string) error {
	if _, err := os.Lstat(name); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Remove(name)
}

// openrcRunlevelDir is the root of OpenRC's runlevel membership tree on the
// hosts this adapter targets. `rc-update add olivares default` links
// <dir>/default/olivares to the unit, and `rc-update del olivares default`
// unlinks exactly that entry — and exits 1 when there is none to unlink.
// stopService passes this fixed path; tests stage their own tree and never
// read the host's.
const openrcRunlevelDir = "/etc/runlevels"

// runlevelLookup is the per-call filesystem seam the membership question is
// answered through. stat follows links, because the runlevel directory must
// RESOLVE to a directory before an absent entry inside it means anything;
// lstat does not, because the entry is whatever rc-update del would unlink,
// its target notwithstanding. Production passes os.Stat and os.Lstat; tests
// stage real trees or inject the exact error a lookup can return.
type runlevelLookup struct {
	stat  func(string) (os.FileInfo, error)
	lstat func(string) (os.FileInfo, error)
}

// openrcInRunlevel reports whether OpenRC lists service in runlevel. It first
// proves the runlevel directory itself: a missing, dangling or non-directory
// <dir>/<runlevel> is not a healthy membership state, and an ENOENT on the
// entry underneath it would be that parent's absence, not the service's. Only
// under a proven parent is the entry inspected, and only an entry that
// provably cannot exist (ENOENT) counts as absent. Any other failure to look —
// either level, any errno — is returned, never read as absence, because
// skipping the disable on an entry nobody could read would leave the service
// enabled while reporting that it is not.
func openrcInRunlevel(look runlevelLookup, runlevelDir, runlevel, service string) (bool, error) {
	parent := filepath.Join(runlevelDir, runlevel)
	info, err := look.stat(parent)
	if err != nil {
		return false, fmt.Errorf("read OpenRC runlevel directory %s: %w", parent, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("OpenRC runlevel directory %s is not a directory", parent)
	}
	entry := filepath.Join(parent, service)
	if _, err := look.lstat(entry); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read OpenRC runlevel entry %s: %w", entry, err)
	}
	return true, nil
}

func stopService(m *Manifest, runner func(string, ...string) error) error {
	return stopServiceWithOps(m, runner, runlevelLookup{stat: os.Stat, lstat: os.Lstat}, openrcRunlevelDir)
}

// stopServiceWithOps is stopService with its filesystem seam explicit, so tests
// decide what the runlevel tree answers without touching the host's /etc.
func stopServiceWithOps(m *Manifest, runner func(string, ...string) error, look runlevelLookup, runlevelDir string) error {
	switch m.Init + ":" + m.Mode {
	case "systemd:system":
		if err := run(runner, "systemctl", "disable", "--now", "olivares"); err != nil {
			return fmt.Errorf("stop/disable systemd service: %w", err)
		}
	case "systemd:user":
		if err := run(runner, "systemctl", "--user", "disable", "--now", "olivares"); err != nil {
			return fmt.Errorf("stop/disable user systemd service: %w", err)
		}
	case "openrc:system":
		if err := run(runner, "rc-service", "olivares", "stop"); err != nil {
			return fmt.Errorf("stop OpenRC service: %w", err)
		}
		// The signed adapter installs without enabling unless --start, so a
		// live service may have no runlevel entry. rc-update del exits 1 for
		// that, and reading it as failure left preserve and purge unable to
		// proceed on a service that had just been stopped. Membership decides
		// the disable: only a proved-absent entry under a proven runlevel
		// directory skips it, a present one still runs the real command with
		// its real verdict, and whether the service was running plays no part.
		enabled, err := openrcInRunlevel(look, runlevelDir, "default", "olivares")
		if err != nil {
			return fmt.Errorf("disable OpenRC service: %w", err)
		}
		if enabled {
			if err := run(runner, "rc-update", "del", "olivares", "default"); err != nil {
				return fmt.Errorf("disable OpenRC service: %w", err)
			}
		}
	case "launchd:system":
		if err := run(runner, "launchctl", "bootout", "system/dev.olivares.olivares"); err != nil {
			return fmt.Errorf("unload launchd service: %w", err)
		}
	case "launchd:user":
		if err := run(runner, "launchctl", "bootout", fmt.Sprintf("gui/%d/dev.olivares.olivares", os.Getuid())); err != nil {
			return fmt.Errorf("unload user launchd service: %w", err)
		}
	}
	return nil
}

// uninstallCommands is the CLOSED SET of programs this file may execute, and it is a set rather
// than a comment because gosec G204 asked the right question about the wrong thing.
//
// The question G204 asks of `exec.Command(name, args...)` is "can an attacker choose what runs".
// Here every call site passes a LITERAL name — six of them, counted: systemctl (4), launchctl (2),
// rc-service, rc-update, userdel, groupdel — so the answer was already no. But "the callers all
// happen to pass literals" is a property of today's file, checked by reading it; the day someone
// adds a seventh call with a name from the manifest, nothing would say so. The allowlist turns the
// reading into an invariant, and a refusal names it.
//
// ARGS ARE NOT PART OF THIS BOUND, and the distinction matters. Two call sites pass manifest data
// (`m.Account.User`, `m.Account.Group`), i.e. strings from a file on disk. That is safe here for a
// reason that has nothing to do with allowlists: `exec.Command` does NOT go through a shell, so an
// argument cannot become a command, a redirection or a second process. What an argument can do is
// name the wrong user to delete — which is an integrity question about the manifest, answered by
// whoever writes and verifies it, not by this seam.
var uninstallCommands = map[string]struct{}{
	"systemctl": {}, "launchctl": {}, "rc-service": {}, "rc-update": {},
	"userdel": {}, "groupdel": {},
}

func run(runner func(string, ...string) error, name string, args ...string) error {
	if runner != nil {
		return runner(name, args...)
	}
	if _, ok := uninstallCommands[name]; !ok {
		return fmt.Errorf("uninstall refuses to execute %q: not one of the service-manager commands this step may run", name)
	}
	// #nosec G204 -- name is bounded above to `uninstallCommands`, a closed set of six literals;
	// args reach exec without a shell, so they cannot introduce a second command.
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}
