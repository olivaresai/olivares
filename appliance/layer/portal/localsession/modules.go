// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localsession

import (
	"encoding/json"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localframe"
	"github.com/olivaresai/olivares/appliance/layer/services"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// The closed queries of module.read, the local API's module read. Each reads the portal's read
// model of one module helper; none reaches a helper, and none names an act.
const (
	// QueryServicesList is the loaded units, each with its class and state.
	QueryServicesList = "services.list"
	// QueryServicesStatus is one unit's class, what the class admits and its state.
	QueryServicesStatus = "services.status"
	// QueryStorageInventory is the block devices, flat: each disk, then each of its partitions.
	QueryStorageInventory = "storage.inventory"
	// QueryFirewallStatus is the firewall: the owner's measurement of this boot, or why it is
	// unmeasured, and the confirmed policy's digest, then flat rows: each measured port, then each
	// window of the owner.
	QueryFirewallStatus = "firewall.status"
)

// Queries returns module.read's closed query set in a fixed order.
func Queries() []string {
	return []string{QueryServicesList, QueryServicesStatus, QueryStorageInventory, QueryFirewallStatus}
}

// MaxModuleAnswer bounds one module.read answer body, so that the answer and its record fit one
// local frame of 16384 bytes. A list longer than one answer is read in pages: offset asks from a
// row, and next names the first row left out, 0 on the last page.
const MaxModuleAnswer = 15 << 10

// Stamp says which completed helper read an answer comes from: its generation, which grows with
// each completed read of that helper, and when that read completed.
type Stamp struct {
	Generation uint64
	ReadAt     time.Time
}

// ModuleReader is the portal's read model of the module helpers: the last completed answer of
// each, which the portal reads on its own schedule, outside every local request. ok is false while
// no read of that helper has completed. It has no method that asks a helper or changes the host.
type ModuleReader interface {
	Units() (inventory services.Inventory, stamp Stamp, ok bool)
	Storage() (inventory storage.Inventory, stamp Stamp, ok bool)
	Firewall() (read FirewallRead, stamp Stamp, ok bool)
}

// moduleReadRequest is module.read's closed request: the query, the unit of services.status alone,
// the first row of a page and the surface.
type moduleReadRequest struct {
	Query   string `json:"query"`
	Unit    string `json:"unit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Surface string `json:"surface"`
}

// UnitRow is one loaded unit of services.list. Its class is the class table's, never the
// helper's.
type UnitRow struct {
	Name        string `json:"name"`
	Class       string `json:"class"`
	LoadState   string `json:"load_state"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
}

// UnitStatus is services.status's answer: the unit's class, what the class admits, and its state
// as the last unit inventory read it. The unit-file state and the dependents are the units
// helper's status read, which no local request makes.
type UnitStatus struct {
	Unit        string   `json:"unit"`
	Class       string   `json:"class"`
	Consequence string   `json:"consequence"`
	Allowed     []string `json:"allowed"`
	LoadState   string   `json:"load_state"`
	ActiveState string   `json:"active_state"`
	SubState    string   `json:"sub_state"`
}

// Device kinds of storage.inventory.
const (
	DeviceDisk      = "disk"
	DevicePartition = "partition"
)

// DeviceRow is one block device of storage.inventory: a disk, or one of its partitions, whose
// disk it names. SystemDisk marks every row of the disk that holds the system; InUse says the
// device has a consumer. The rows are flat so that an answer stays within the frame's depth.
type DeviceRow struct {
	Device      string   `json:"device"`
	Kind        string   `json:"kind"`
	Disk        string   `json:"disk,omitempty"`
	SizeBytes   uint64   `json:"size_bytes"`
	Model       string   `json:"model,omitempty"`
	Filesystem  string   `json:"filesystem,omitempty"`
	MountPoints []string `json:"mount_points,omitempty"`
	SystemDisk  bool     `json:"system_disk"`
	InUse       bool     `json:"in_use"`
}

// FirewallRead is the portal's last completed read of the firewall, as firewall.status answers it:
// its head and its flat rows.
type FirewallRead struct {
	Head FirewallHead
	Rows []FirewallRow
}

// FirewallHead is firewall.status's head, on every page: the owner's measurement of this boot as the
// console read it, or why the firewall is unmeasured, and the digest of the confirmed policy the
// owner answered, "" while none is confirmed.
type FirewallHead struct {
	Unmeasured      string `json:"unmeasured,omitempty"`
	PolicyDigest    string `json:"policy_digest,omitempty"`
	BootID          string `json:"boot_id,omitempty"`
	MeasuredAt      string `json:"measured_at,omitempty"`
	InputPolicy     string `json:"input_policy,omitempty"`
	ConfirmedDigest string `json:"confirmed_digest,omitempty"`
}

// Row kinds of firewall.status.
const (
	FirewallPort   = "port"
	FirewallWindow = "window"
)

// FirewallRow is one row of firewall.status: a measured port and the interfaces it answers on ("*"
// alone for every interface), or one window of the owner with its state, why it closed and the
// candidate's digest.
type FirewallRow struct {
	Kind            string   `json:"kind"`
	Port            string   `json:"port,omitempty"`
	Interfaces      []string `json:"interfaces,omitempty"`
	OperationID     string   `json:"operation_id,omitempty"`
	State           string   `json:"state,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	CandidateDigest string   `json:"candidate_digest,omitempty"`
}

// ModuleAnswer is module.read's closed answer: the query, the stamp of the read it comes from
// (read_at in RFC 3339, UTC), the page (offset, next, total) and the rows of its query. Truncated
// passes on the helper's own bound.
type ModuleAnswer struct {
	Query        string        `json:"query"`
	Generation   uint64        `json:"generation"`
	ReadAt       string        `json:"read_at"`
	Offset       int           `json:"offset"`
	Next         int           `json:"next"`
	Total        int           `json:"total"`
	Truncated    bool          `json:"truncated"`
	Units        []UnitRow     `json:"units,omitempty"`
	Unit         *UnitStatus   `json:"unit,omitempty"`
	Devices      []DeviceRow   `json:"devices,omitempty"`
	Firewall     *FirewallHead `json:"firewall,omitempty"`
	FirewallRows []FirewallRow `json:"firewall_rows,omitempty"`
}

// ReadModules sets the portal's module read model that module.read answers from and returns s.
// Without one, module.read is consumer_unavailable.
func (s *Server) ReadModules(m ModuleReader) *Server {
	s.modules = m
	return s
}

// moduleRead answers module.read. A request is refused, before the model is read, unless it is one
// closed query with only its own fields; then the answer is the model's, never a helper's, and
// nothing on the host changes.
func (s *Server) moduleRead(op string, body json.RawMessage) localframe.Record {
	var req moduleReadRequest
	if hostops.DecodeClosed(body, &req) != nil || !localSurface(req.Surface) || req.Offset < 0 {
		return refusal(op, 422, "input_refused")
	}
	switch req.Query {
	case QueryServicesList, QueryStorageInventory, QueryFirewallStatus:
		if req.Unit != "" {
			return refusal(op, 422, "input_refused")
		}
	case QueryServicesStatus:
		if !services.ValidUnitName(req.Unit) || req.Offset != 0 {
			return refusal(op, 422, "input_refused")
		}
	default:
		return refusal(op, 422, "input_refused")
	}
	if s.modules == nil {
		return refusal(op, 503, "consumer_unavailable")
	}
	if req.Query == QueryFirewallStatus {
		read, stamp, ok := s.modules.Firewall()
		if !ok {
			return refusal(op, 503, "consumer_unavailable")
		}
		answer := stamped(req, stamp)
		head := read.Head
		answer.Firewall = &head
		return page(op, answer, read.Rows, func(a *ModuleAnswer, rows []FirewallRow) { a.FirewallRows = rows })
	}
	if req.Query == QueryStorageInventory {
		inventory, stamp, ok := s.modules.Storage()
		if !ok {
			return refusal(op, 503, "consumer_unavailable")
		}
		return page(op, stamped(req, stamp), deviceRows(inventory), func(a *ModuleAnswer, rows []DeviceRow) { a.Devices = rows })
	}
	inventory, stamp, ok := s.modules.Units()
	if !ok {
		return refusal(op, 503, "consumer_unavailable")
	}
	answer := stamped(req, stamp)
	answer.Truncated = inventory.Truncated
	if req.Query == QueryServicesList {
		return page(op, answer, unitRows(inventory), func(a *ModuleAnswer, rows []UnitRow) { a.Units = rows })
	}
	status, found := unitStatus(inventory, req.Unit)
	switch {
	case found:
		answer.Unit, answer.Total = &status, 1
		return bounded(op, answer)
	case inventory.Truncated:
		// The unit may lie beyond the helper's own bound: the read could not run.
		return refusal(op, 503, "consumer_unavailable")
	}
	return refusal(op, 422, "input_refused")
}

// stamped is the answer to req from the read stamp names, with no rows yet.
func stamped(req moduleReadRequest, stamp Stamp) ModuleAnswer {
	return ModuleAnswer{Query: req.Query, Generation: stamp.Generation, ReadAt: stamp.ReadAt.UTC().Format(time.RFC3339), Offset: req.Offset}
}

// page answers rows from answer's offset, as many as fit MaxModuleAnswer, with next the first row
// left out. An offset past the rows is refused; a first row that cannot fit alone is never cut,
// so the read could not run.
func page[R any](op string, answer ModuleAnswer, rows []R, set func(*ModuleAnswer, []R)) localframe.Record {
	answer.Total = len(rows)
	if answer.Offset > 0 && answer.Offset >= len(rows) {
		return refusal(op, 422, "input_refused")
	}
	// The room is measured with next at its widest and the widest rows member's name and brackets.
	widest := answer
	widest.Next = len(rows)
	head, err := json.Marshal(widest)
	if err != nil {
		return refusal(op, 503, "consumer_unavailable")
	}
	used, end := len(head)+len(`,"firewall_rows":[]`), answer.Offset
	for end < len(rows) {
		row, err := json.Marshal(rows[end])
		if err != nil || used+len(row)+1 > MaxModuleAnswer {
			break
		}
		used += len(row) + 1
		end++
	}
	if end == answer.Offset && end < len(rows) {
		return refusal(op, 503, "consumer_unavailable")
	}
	set(&answer, rows[answer.Offset:end])
	if end < len(rows) {
		answer.Next = end
	}
	return bounded(op, answer)
}

// bounded answers answer when it fits MaxModuleAnswer, and consumer_unavailable otherwise.
func bounded(op string, answer ModuleAnswer) localframe.Record {
	body, err := json.Marshal(answer)
	if err != nil || len(body) > MaxModuleAnswer {
		return refusal(op, 503, "consumer_unavailable")
	}
	return response(op, 200, json.RawMessage(body))
}

// unitRows are the inventory's units that are unit names, each with the class table's class.
func unitRows(inventory services.Inventory) []UnitRow {
	rows := make([]UnitRow, 0, len(inventory.Units))
	for _, u := range inventory.Units {
		if !services.ValidUnitName(u.Name) {
			continue
		}
		rows = append(rows, UnitRow{Name: u.Name, Class: string(services.Classify(u.Name)), LoadState: u.LoadState,
			ActiveState: u.ActiveState, SubState: u.SubState})
	}
	return rows
}

// unitStatus is unit's status from the inventory, and false when the inventory does not list it.
func unitStatus(inventory services.Inventory, unit string) (UnitStatus, bool) {
	for _, u := range inventory.Units {
		if u.Name != unit {
			continue
		}
		class := services.Classify(unit)
		status := UnitStatus{Unit: unit, Class: string(class), Consequence: class.Consequence(), Allowed: []string{},
			LoadState: u.LoadState, ActiveState: u.ActiveState, SubState: u.SubState}
		for _, op := range services.Ops() {
			if op != services.OpList && class.Allows(op) {
				status.Allowed = append(status.Allowed, op)
			}
		}
		return status, true
	}
	return UnitStatus{}, false
}

// deviceRows flattens the inventory: each disk, then each of its partitions.
func deviceRows(inventory storage.Inventory) []DeviceRow {
	var rows []DeviceRow
	for _, d := range inventory.Disks {
		disk := DeviceRow{Device: d.Device, Kind: DeviceDisk, SizeBytes: d.Size, Model: d.Model, SystemDisk: d.System, InUse: len(d.Consumers) > 0}
		if d.Filesystem != nil {
			disk.Filesystem, disk.MountPoints = d.Filesystem.Type, d.Filesystem.MountPoints
		}
		rows = append(rows, disk)
		for _, p := range d.Partitions {
			part := DeviceRow{Device: p.Device, Kind: DevicePartition, Disk: d.Device, SizeBytes: p.Size, SystemDisk: d.System, InUse: len(p.Consumers) > 0}
			if p.Filesystem != nil {
				part.Filesystem, part.MountPoints = p.Filesystem.Type, p.Filesystem.MountPoints
			}
			rows = append(rows, part)
		}
	}
	return rows
}
