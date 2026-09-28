// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localsession

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localframe"
	"github.com/olivaresai/olivares/appliance/layer/services"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// moduleModel is the portal's read model as a test describes it; it counts its reads.
type moduleModel struct {
	units   services.Inventory
	unitsOK bool
	disks   storage.Inventory
	disksOK bool
	fw      FirewallRead
	fwOK    bool
	stamp   Stamp
	reads   int
}

func (m *moduleModel) Units() (services.Inventory, Stamp, bool) {
	m.reads++
	return m.units, m.stamp, m.unitsOK
}

func (m *moduleModel) Storage() (storage.Inventory, Stamp, bool) {
	m.reads++
	return m.disks, m.stamp, m.disksOK
}

func (m *moduleModel) Firewall() (FirewallRead, Stamp, bool) {
	m.reads++
	return m.fw, m.stamp, m.fwOK
}

// measuredFirewall is a managed host's firewall as the portal read it: three measured ports, then a
// window reverted at its deadline and an open one.
func measuredFirewall() FirewallRead {
	digest := "sha256:" + strings.Repeat("1f", 32)
	return FirewallRead{
		Head: FirewallHead{PolicyDigest: digest, BootID: "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e", MeasuredAt: "2026-09-27T18:59:00Z",
			InputPolicy: "drop", ConfirmedDigest: digest},
		Rows: []FirewallRow{
			{Kind: FirewallPort, Port: "22/tcp", Interfaces: []string{"*"}},
			{Kind: FirewallPort, Port: "546/udp", Interfaces: []string{"eth0", "eth1"}},
			{Kind: FirewallPort, Port: "9443/tcp", Interfaces: []string{"eth0"}},
			{Kind: FirewallWindow, OperationID: strings.Repeat("ab", 16), State: "reverted", Reason: "deadline", CandidateDigest: "sha256:" + strings.Repeat("2e", 32)},
			{Kind: FirewallWindow, OperationID: strings.Repeat("cd", 16), State: "pending", CandidateDigest: "sha256:" + strings.Repeat("3d", 32)},
		},
	}
}

func unitRow(name, active, sub string) services.Listed {
	return services.Listed{Unit: services.Unit{Name: name, LoadState: "loaded", ActiveState: active, SubState: sub}, Class: services.ClassOther}
}

// measuredModel is a host with three units and one system disk of two partitions.
func measuredModel() *moduleModel {
	return &moduleModel{
		units: services.Inventory{Units: []services.Listed{
			unitRow("sshd.service", "active", "running"), unitRow("olivares.service", "active", "running"), unitRow("chronyd.service", "inactive", "dead"),
		}},
		unitsOK: true,
		disks: storage.Inventory{Schema: storage.Schema, BootID: strings.Repeat("ab", 16), Disks: []storage.Disk{{
			Device: "/dev/vda", Size: 20 << 30, Model: "QEMU HARDDISK", System: true, SystemReasons: []string{"/"},
			Consumers: []storage.Consumer{}, Operations: []storage.Operation{},
			Partitions: []storage.Partition{
				{Device: "/dev/vda1", Number: 1, Size: 1 << 30, Filesystem: &storage.Filesystem{Type: "vfat", MountPoints: []string{"/boot/efi"}},
					Consumers: []storage.Consumer{{Kind: storage.ConsumerMount}}, Operations: []storage.Operation{}},
				{Device: "/dev/vda2", Number: 2, Size: 19 << 30, Filesystem: &storage.Filesystem{Type: "xfs", MountPoints: []string{"/"}},
					Consumers: []storage.Consumer{{Kind: storage.ConsumerMount}}, Operations: []storage.Operation{}},
			},
		}}},
		disksOK: true,
		fw:      measuredFirewall(),
		fwOK:    true,
		stamp:   Stamp{Generation: 7, ReadAt: time.Date(2026, 9, 27, 19, 0, 0, 0, time.UTC)},
	}
}

// read sends one module.read request to server and returns its record and decoded answer.
func read(t *testing.T, server *Server, body string) (localframe.Record, ModuleAnswer) {
	t.Helper()
	result := server.handle(localframe.Record{Type: "request", Op: "module.read", Body: json.RawMessage(body)})
	var answer ModuleAnswer
	if result.Type == "response" {
		if err := hostops.DecodeClosed(result.Body, &answer); err != nil {
			t.Fatalf("the answer is not module.read's closed schema: %v %s", err, result.Body)
		}
	}
	return result, answer
}

func TestLocalSession_ModuleReadAnswersTheClosedQueriesFromThePortalsReader(t *testing.T) {
	catalog, err := hostops.NewCatalog([]hostops.Descriptor{hostops.StatusDescriptor()})
	if err != nil {
		t.Fatal(err)
	}
	records := &recordReader{}
	model := measuredModel()
	server := NewServer(records, catalog, nil).ReadModules(model)

	for _, surface := range []string{"cli", "tui"} {
		result, answer := read(t, server, `{"query":"services.list","surface":"`+surface+`"}`)
		if result.Type != "response" || result.Op != "module.read" || result.Status != 200 || answer.Query != QueryServicesList ||
			len(answer.Units) != 3 || answer.Total != 3 || answer.Next != 0 || answer.Generation != 7 || answer.ReadAt != "2026-09-27T19:00:00Z" {
			t.Fatalf("services.list on %s: %#v %+v", surface, result, answer)
		}
		if row := answer.Units[0]; row.Name != "sshd.service" || row.Class != string(services.Classify("sshd.service")) || row.ActiveState != "active" || row.SubState != "running" {
			t.Errorf("the first unit is %+v; its class is the class table's", row)
		}
	}

	result, answer := read(t, server, `{"query":"services.status","unit":"sshd.service","surface":"cli"}`)
	class := services.Classify("sshd.service")
	if result.Status != 200 || answer.Unit == nil || answer.Unit.Unit != "sshd.service" || answer.Unit.Class != string(class) ||
		answer.Unit.Consequence != class.Consequence() || answer.Unit.ActiveState != "active" || len(answer.Units) != 0 {
		t.Fatalf("services.status: %#v %+v", result, answer)
	}
	for _, op := range services.Ops() {
		if allowed := op != services.OpList && class.Allows(op); allowed != slices.Contains(answer.Unit.Allowed, op) {
			t.Errorf("services.status names %v as allowed; %s allowed=%v by the class table", answer.Unit.Allowed, op, allowed)
		}
	}

	result, answer = read(t, server, `{"query":"storage.inventory","surface":"tui"}`)
	if result.Status != 200 || answer.Query != QueryStorageInventory || answer.Total != 3 || len(answer.Devices) != 3 {
		t.Fatalf("storage.inventory: %#v %+v", result, answer)
	}
	disk, root := answer.Devices[0], answer.Devices[2]
	if disk.Device != "/dev/vda" || disk.Kind != "disk" || !disk.SystemDisk || disk.SizeBytes != 20<<30 || disk.InUse ||
		root.Device != "/dev/vda2" || root.Kind != "partition" || root.Disk != "/dev/vda" || root.Filesystem != "xfs" ||
		!slices.Equal(root.MountPoints, []string{"/"}) || !root.InUse || !root.SystemDisk {
		t.Fatalf("storage rows: %+v", answer.Devices)
	}

	// Each answer was the reader's, and nothing else was asked: no operation record was read.
	if model.reads != 4 || records.reads != 0 {
		t.Fatalf("the model was read %d times and the operation records %d times", model.reads, records.reads)
	}

	t.Run("an unread module is consumer_unavailable, never an empty answer", func(t *testing.T) {
		unread := NewServer(records, catalog, nil).ReadModules(&moduleModel{})
		for _, body := range []string{`{"query":"services.list","surface":"cli"}`, `{"query":"services.status","unit":"sshd.service","surface":"cli"}`, `{"query":"storage.inventory","surface":"cli"}`} {
			if result, _ := read(t, unread, body); result.Type != "error" || result.Status != 503 || result.Code != "consumer_unavailable" {
				t.Errorf("%s: %#v", body, result)
			}
		}
		if result, _ := read(t, NewServer(records, catalog, nil), `{"query":"services.list","surface":"cli"}`); result.Status != 503 || result.Code != "consumer_unavailable" {
			t.Errorf("a server without a module reader: %#v", result)
		}
	})

	t.Run("the op has no effect path", func(t *testing.T) {
		// The handler reads the model; it imports nothing that could reach a helper, a
		// subprocess or the network.
		syntax, err := parser.ParseFile(token.NewFileSet(), "modules.go", nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range syntax.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if slices.Contains([]string{"os/exec", "net", "net/http", "syscall", "os"}, path) || strings.HasSuffix(path, "/helperclient") || strings.HasSuffix(path, "/invocation") {
				t.Errorf("modules.go imports %s", path)
			}
		}
		if _, err := os.Stat("modules.go"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestLocalSession_ModuleReadRefusesEveryOtherQuery(t *testing.T) {
	model := measuredModel()
	server := NewServer(nil, nil, nil).ReadModules(model)
	for _, body := range []string{
		`{"query":"services.logs","unit":"sshd.service","surface":"cli"}`,
		`{"query":"services.start","unit":"sshd.service","surface":"cli"}`,
		`{"query":"services.restart","unit":"sshd.service","surface":"cli"}`,
		`{"query":"storage.format","surface":"cli"}`,
		`{"query":"storage.list","surface":"cli"}`,
		`{"query":"SERVICES.LIST","surface":"cli"}`,
		`{"query":"","surface":"cli"}`,
		`{"surface":"cli"}`,
		`{"query":"services.list","unit":"sshd.service","surface":"cli"}`,
		`{"query":"storage.inventory","unit":"sshd.service","surface":"cli"}`,
		`{"query":"services.status","surface":"cli"}`,
		`{"query":"services.status","unit":"/etc/passwd","surface":"cli"}`,
		`{"query":"services.status","unit":"*.service","surface":"cli"}`,
		`{"query":"services.status","unit":"absent.service","surface":"cli"}`,
		`{"query":"services.status","unit":"sshd.service","offset":1,"surface":"cli"}`,
		`{"query":"services.list","offset":-1,"surface":"cli"}`,
		`{"query":"services.list","offset":4,"surface":"cli"}`,
		`{"query":"services.list","surface":"web"}`,
		`{"query":"services.list"}`,
		`{"query":"services.list","surface":"cli","path":"/etc/passwd"}`,
		`{"query":"services.list","surface":"cli","operation_id":"0123456789abcdef0123456789abcdef"}`,
		`{"query":"services.list","surface":"cli","actor":"root"}`,
		`{"query":"services.list","surface":"cli","query":"storage.inventory"}`,
		`{"query":["services.list"],"surface":"cli"}`,
	} {
		before := model.reads
		result, _ := read(t, server, body)
		if result.Type != "error" || result.Op != "module.read" || result.Status != 422 || result.Code != "input_refused" || len(result.Body) != 0 {
			t.Errorf("%s: %#v, want 422 input_refused", body, result)
		}
		if strings.Contains(body, "absent.service") || strings.Contains(body, `"offset":4`) {
			continue // a refusal the read model decides
		}
		if model.reads != before {
			t.Errorf("%s: the model was read before the request was refused", body)
		}
	}
	if got := Queries(); !slices.Equal(got, []string{QueryServicesList, QueryServicesStatus, QueryStorageInventory, QueryFirewallStatus}) {
		t.Errorf("the closed query set is %v", got)
	}
}

func TestLocalSession_ModuleReadAnswersTheFirewallStatusFromThePortalsReader(t *testing.T) {
	model := measuredModel()
	server := NewServer(nil, nil, nil).ReadModules(model)
	want := measuredFirewall()
	for _, surface := range []string{"cli", "tui"} {
		result, answer := read(t, server, `{"query":"firewall.status","surface":"`+surface+`"}`)
		if result.Type != "response" || result.Op != "module.read" || result.Status != 200 || answer.Query != QueryFirewallStatus ||
			answer.Firewall == nil || answer.Total != 5 || answer.Next != 0 || answer.Generation != 7 || answer.ReadAt != "2026-09-27T19:00:00Z" {
			t.Fatalf("firewall.status on %s: %#v %+v", surface, result, answer)
		}
		if *answer.Firewall != want.Head || !reflect.DeepEqual(answer.FirewallRows, want.Rows) {
			t.Errorf("firewall.status on %s answered %+v %+v, want the reader's %+v", surface, *answer.Firewall, answer.FirewallRows, want)
		}
		if len(answer.Units) != 0 || answer.Unit != nil || len(answer.Devices) != 0 {
			t.Errorf("firewall.status carries another module's rows: %+v", answer)
		}
	}
	if model.reads != 2 {
		t.Fatalf("the model was read %d times for two requests", model.reads)
	}

	// Another query's answer carries no firewall member.
	if _, answer := read(t, server, `{"query":"services.list","surface":"cli"}`); answer.Firewall != nil || len(answer.FirewallRows) != 0 {
		t.Errorf("services.list carries the firewall: %+v", answer)
	}

	// An unmeasured firewall with no window is answered, with why it is unmeasured and no row.
	model.fw = FirewallRead{Head: FirewallHead{Unmeasured: "no measurement is published"}}
	result, answer := read(t, server, `{"query":"firewall.status","surface":"cli"}`)
	if result.Status != 200 || answer.Firewall == nil || answer.Firewall.Unmeasured != "no measurement is published" || answer.Firewall.PolicyDigest != "" ||
		answer.Total != 0 || len(answer.FirewallRows) != 0 {
		t.Fatalf("an unmeasured firewall: %#v %+v", result, answer)
	}

	// No completed read, or no reader, is consumer_unavailable, never an empty answer.
	model.fwOK = false
	if result, _ := read(t, server, `{"query":"firewall.status","surface":"cli"}`); result.Type != "error" || result.Status != 503 || result.Code != "consumer_unavailable" {
		t.Errorf("an unread firewall: %#v", result)
	}
	if result, _ := read(t, NewServer(nil, nil, nil), `{"query":"firewall.status","surface":"cli"}`); result.Status != 503 || result.Code != "consumer_unavailable" {
		t.Errorf("a server without a module reader: %#v", result)
	}

	// The handler reaches no owner: it imports neither the firewall module, whose owner loads the
	// table, nor a client, a process, a file or the network.
	syntax, err := parser.ParseFile(token.NewFileSet(), "modules.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range syntax.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		if strings.Contains(path, "/firewall") || slices.Contains([]string{"os/exec", "net", "net/http", "syscall", "os"}, path) ||
			strings.HasSuffix(path, "/helperclient") || strings.HasSuffix(path, "/invocation") {
			t.Errorf("modules.go imports %s", path)
		}
	}
}

func TestLocalSession_ModuleReadRefusesEveryFirewallQueryButStatus(t *testing.T) {
	model := measuredModel()
	server := NewServer(nil, nil, nil).ReadModules(model)
	for _, body := range []string{
		`{"query":"firewall.show","surface":"cli"}`,
		`{"query":"firewall.apply","surface":"cli"}`,
		`{"query":"firewall.confirm","surface":"cli"}`,
		`{"query":"firewall.revert","surface":"cli"}`,
		`{"query":"firewall.app-row","surface":"cli"}`,
		`{"query":"firewall.plan","surface":"cli"}`,
		`{"query":"firewall.rules","surface":"cli"}`,
		`{"query":"firewall","surface":"cli"}`,
		`{"query":"FIREWALL.STATUS","surface":"cli"}`,
		`{"query":"firewall.status ","surface":"cli"}`,
		`{"query":"firewall.status","unit":"olivares-firewall.service","surface":"cli"}`,
		`{"query":"firewall.status","surface":"web"}`,
		`{"query":"firewall.status"}`,
		`{"query":"firewall.status","surface":"cli","operation_id":"0123456789abcdef0123456789abcdef"}`,
		`{"query":"firewall.status","surface":"cli","op":"apply"}`,
		`{"query":"firewall.status","surface":"cli","candidate":{"schema_version":"x"}}`,
		`{"query":"firewall.status","surface":"cli","revert_after_s":60}`,
		`{"query":"firewall.status","surface":"cli","path":"/etc/nftables.conf"}`,
		`{"query":"firewall.status","offset":-1,"surface":"cli"}`,
		`{"query":"firewall.status","offset":5,"surface":"cli"}`,
	} {
		before := model.reads
		result, _ := read(t, server, body)
		if result.Type != "error" || result.Op != "module.read" || result.Status != 422 || result.Code != "input_refused" || len(result.Body) != 0 {
			t.Errorf("%s: %#v, want 422 input_refused", body, result)
		}
		if strings.Contains(body, `"offset":5`) {
			continue // a refusal the read model decides
		}
		if model.reads != before {
			t.Errorf("%s: the model was read before the request was refused", body)
		}
	}
	if got := Queries(); !slices.Contains(got, QueryFirewallStatus) || len(got) != 4 {
		t.Errorf("the closed query set is %v, want the three reads and firewall.status", got)
	}
}

func TestLocalSession_ModuleReadBoundsTheFirewallStatusToOneFrame(t *testing.T) {
	model := measuredModel()
	head := model.fw.Head
	model.fw = FirewallRead{Head: head}
	for i := 0; i < 64; i++ {
		var interfaces []string
		for j := 0; j < 16; j++ {
			interfaces = append(interfaces, fmt.Sprintf("enp%dmgmt%04d", j, i))
		}
		model.fw.Rows = append(model.fw.Rows, FirewallRow{Kind: FirewallPort, Port: fmt.Sprintf("%d/tcp", 10000+i), Interfaces: interfaces})
	}
	for i := 0; i < 256; i++ {
		model.fw.Rows = append(model.fw.Rows, FirewallRow{Kind: FirewallWindow, OperationID: fmt.Sprintf("%032x", i), State: "reverted",
			Reason: "load_failed", CandidateDigest: "sha256:" + fmt.Sprintf("%064x", i)})
	}
	server := NewServer(nil, nil, nil).ReadModules(model)
	total := len(model.fw.Rows)
	seen, offset, pages := map[string]bool{}, 0, 0
	for {
		result, answer := read(t, server, `{"query":"firewall.status","offset":`+strconv.Itoa(offset)+`,"surface":"cli"}`)
		if result.Type != "response" {
			t.Fatalf("firewall.status at %d: %#v", offset, result)
		}
		record, err := json.Marshal(result)
		if err != nil || len(result.Body) > MaxModuleAnswer || len(record) > 16384 {
			t.Fatalf("firewall.status at %d: a body of %d bytes in a record of %d: beyond one frame", offset, len(result.Body), len(record))
		}
		if answer.Total != total || answer.Offset != offset || answer.Generation != 7 || answer.Firewall == nil || *answer.Firewall != head {
			t.Fatalf("firewall.status page %d: %+v", pages, answer)
		}
		n := len(answer.FirewallRows)
		if n == 0 {
			t.Fatalf("firewall.status at %d: an empty page", offset)
		}
		for _, row := range answer.FirewallRows {
			seen[row.Port+row.OperationID] = true
		}
		pages++
		if answer.Next == 0 {
			if offset+n != total {
				t.Fatalf("firewall.status ended at %d of %d", offset+n, total)
			}
			break
		}
		if answer.Next != offset+n {
			t.Fatalf("firewall.status: next %d after %d rows from %d", answer.Next, n, offset)
		}
		offset = answer.Next
	}
	if len(seen) != total || pages < 2 {
		t.Fatalf("firewall.status: %d distinct rows in %d pages, want %d in more than one", len(seen), pages, total)
	}
}

func TestLocalSession_ModuleReadBoundsEachAnswerToOneFrame(t *testing.T) {
	model := measuredModel()
	model.units = services.Inventory{Truncated: true}
	for i := 0; i < 1000; i++ {
		model.units.Units = append(model.units.Units, unitRow(fmt.Sprintf("olivares-unit-with-a-long-name-%04d.service", i), "activating", "start-pre"))
	}
	model.disks.Disks = nil
	for i := 0; i < 120; i++ {
		disk := storage.Disk{Device: fmt.Sprintf("/dev/sd%04d", i), Size: 1 << 40, Model: strings.Repeat("M", 40), Consumers: []storage.Consumer{}, Operations: []storage.Operation{}}
		for p := 1; p <= 4; p++ {
			disk.Partitions = append(disk.Partitions, storage.Partition{Device: fmt.Sprintf("%s%d", disk.Device, p), Number: uint32(p), Size: 1 << 38,
				Filesystem: &storage.Filesystem{Type: "ext4", MountPoints: []string{fmt.Sprintf("/srv/olivares/mnt/volume-%04d-%d", i, p)}},
				Consumers:  []storage.Consumer{{Kind: storage.ConsumerMount}}, Operations: []storage.Operation{}})
		}
		model.disks.Disks = append(model.disks.Disks, disk)
	}
	server := NewServer(nil, nil, nil).ReadModules(model)

	for query, total := range map[string]int{QueryServicesList: 1000, QueryStorageInventory: 600} {
		seen, offset, pages := map[string]bool{}, 0, 0
		for {
			result, answer := read(t, server, `{"query":"`+query+`","offset":`+strconv.Itoa(offset)+`,"surface":"cli"}`)
			if result.Type != "response" {
				t.Fatalf("%s at %d: %#v", query, offset, result)
			}
			record, err := json.Marshal(result)
			if err != nil || len(result.Body) > MaxModuleAnswer || len(record) > 16384 {
				t.Fatalf("%s at %d: a body of %d bytes in a record of %d: beyond one frame", query, offset, len(result.Body), len(record))
			}
			if answer.Total != total || answer.Offset != offset || answer.Generation != 7 {
				t.Fatalf("%s page %d: %+v", query, pages, answer)
			}
			if query == QueryServicesList && !answer.Truncated {
				t.Fatalf("the helper's own truncation was not passed on")
			}
			n := len(answer.Units) + len(answer.Devices)
			if n == 0 {
				t.Fatalf("%s at %d: an empty page", query, offset)
			}
			for _, row := range answer.Units {
				seen[row.Name] = true
			}
			for _, row := range answer.Devices {
				seen[row.Device] = true
			}
			pages++
			if answer.Next == 0 {
				if offset+n != total {
					t.Fatalf("%s ended at %d of %d", query, offset+n, total)
				}
				break
			}
			if answer.Next != offset+n {
				t.Fatalf("%s: next %d after %d rows from %d", query, answer.Next, n, offset)
			}
			offset = answer.Next
		}
		if len(seen) != total || pages < 2 {
			t.Fatalf("%s: %d distinct rows in %d pages, want %d in more than one", query, len(seen), pages, total)
		}
	}

	// A row that cannot fit one frame is not cut: the read could not run.
	model.disks.Disks = []storage.Disk{{Device: "/dev/vdz", Filesystem: &storage.Filesystem{Type: "ext4", MountPoints: []string{strings.Repeat("/m", 9000)}},
		Consumers: []storage.Consumer{}, Operations: []storage.Operation{}}}
	if result, _ := read(t, server, `{"query":"storage.inventory","surface":"cli"}`); result.Type != "error" || result.Status != 503 || result.Code != "consumer_unavailable" {
		t.Fatalf("an oversized row: %#v", result)
	}
}
