// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// refreshTimeout bounds one background read of the storage helper, which bounds its own read to
// 20 seconds.
const refreshTimeout = 25 * time.Second

var pageFuncs = template.FuncMap{
	"join": strings.Join,
	"consumers": func(list []Consumer) string {
		if len(list) == 0 {
			return "none"
		}
		out := make([]string, 0, len(list))
		for _, c := range list {
			out = append(out, c.Kind+" "+c.Name)
		}
		return strings.Join(out, ", ")
	},
	"operations": func(list []Operation) string {
		if len(list) == 0 {
			return "none reported"
		}
		out := make([]string, 0, len(list))
		for _, o := range list {
			s := o.Name + " " + o.Type
			if len(o.Modes) > 0 {
				s += " (" + strings.Join(o.Modes, ", ") + ")"
			}
			if o.Disabled != "" {
				s += " [disabled: " + o.Disabled + "]"
			}
			out = append(out, s)
		}
		return strings.Join(out, ", ")
	},
	"filesystem": func(fs *Filesystem) string {
		if fs == nil {
			return "none"
		}
		s := fs.Type
		if fs.Label != "" {
			s += " label " + fs.Label
		}
		if len(fs.MountPoints) > 0 {
			s += " mounted at " + strings.Join(fs.MountPoints, ", ")
		}
		return s
	},
	"identity": func(id Identity) string {
		switch {
		case id.Kind == "":
			return "none (" + id.Missing + "): no plan can name this disk"
		case id.Ambiguous:
			return id.Kind + " by " + id.Source + ", shared with another disk: no plan can name this disk"
		case id.BootID != "":
			return id.Kind + " by " + id.Source + " " + id.Value + ", valid in boot " + id.BootID + " only"
		}
		return id.Kind + " by " + id.Source + " " + id.Value
	},
}

var storagePage = template.Must(template.New("storage").Funcs(pageFuncs).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Storage — Appliance Console</title>
</head>
<body>
<h1>Storage</h1>
<p>{{.SignIn}}</p>
<p>Read-only inventory of disks, partitions, filesystems, swap and LVM. Nothing on this page changes the appliance; storage changes are not available in this version. An LVM metadata backup or a snapshot is not a data backup.</p>
<p>Command: olivares-appliance storage list --plan</p>
{{if .Have}}{{if .Code}}<p>The last refresh of the storage inventory failed: {{.Code}}. The inventory below was read at {{.ReadAt}}.</p>
{{else}}<p>Inventory read at {{.ReadAt}}; each visit starts a refresh in the background.</p>
{{end}}{{with .Inventory}}{{range .Disks}}<h2>{{.Device}}</h2>
<ul>
<li>Size: {{.Size}} bytes{{if .Model}}; model {{.Model}}{{end}}{{if .BackingFile}}; backing file shown by UDisks2: {{.BackingFile}}{{end}}</li>
<li>Identity: {{identity .Identity}}</li>
<li>Target: {{if .Target}}{{.Target}}{{else}}none{{end}}</li>
<li>System disk: {{if .System}}yes ({{join .SystemReasons ", "}}){{else}}no{{end}}; UDisks2 system hint: {{.HintSystem}}</li>
<li>Partition table: {{if .PartitionTable}}{{.PartitionTable}}{{else}}none{{end}}; filesystem: {{filesystem .Filesystem}}</li>
<li>Consumers: {{consumers .Consumers}}</li>
<li>Operations the host reports: {{operations .Operations}}</li>
</ul>
{{if .Partitions}}<table>
<tr><th>Partition</th><th>Number</th><th>Size (bytes)</th><th>Filesystem</th><th>Consumers</th><th>Operations the host reports</th></tr>
{{range .Partitions}}<tr><td>{{.Device}}</td><td>{{.Number}}</td><td>{{.Size}}</td><td>{{filesystem .Filesystem}}</td><td>{{consumers .Consumers}}</td><td>{{operations .Operations}}</td></tr>
{{end}}</table>
{{end}}{{end}}<h2>LVM</h2>
<p>LVM: {{.LVM.State}}{{if .LVM.Reason}} ({{.LVM.Reason}}){{end}}</p>
{{range .LVM.VolumeGroups}}<h3>Volume group {{.Name}}</h3>
<p>Size {{.Size}} bytes, free {{.Free}} bytes; physical volumes: {{join .PhysicalVolumes ", "}}{{if .Missing}}; missing: {{join .Missing ", "}}{{end}}</p>
<ul>
{{range .LogicalVolumes}}<li>{{.Name}}: {{.Size}} bytes{{if .Device}}, {{.Device}}{{end}}, {{if .Active}}active{{else}}inactive{{end}}; filesystem: {{filesystem .Filesystem}}; consumers: {{consumers .Consumers}}</li>
{{end}}</ul>
{{end}}<p>Boot {{.BootID}}</p>
{{end}}{{else if .Code}}<p>The storage inventory is unavailable: {{.Code}}. The storage helper did not answer; the socket olivares-helper-storage.socket must be active and UDisks2 with its LVM2 module installed.</p>
{{else}}<p>The first storage inventory read has not completed yet; reload this page in a moment.</p>
{{end}}<p><a href="/">Appliance status</a></p>
</body>
</html>
`))

type pageView struct {
	SignIn    string
	Have      bool
	ReadAt    string
	Code      string
	Inventory Inventory
}

// inventoryPage keeps the last completed inventory and at most one background read.
type inventoryPage struct {
	signIn string
	read   Reader

	mu     sync.Mutex
	inv    Inventory
	have   bool
	readAt time.Time
	code   string
	busy   bool
}

// Page serves the Storage page for GET and HEAD from the last completed inventory: the sign-in
// statement, then each disk with its identity, target, system mark, consumers and the
// operations the host reports, its partitions, and the LVM part. It never waits on the storage
// helper: each request starts one background read through read when none is running, bounded
// to 25 seconds, and answers at once. Before the first read completes it answers 503 with
// Retry-After and says so; a failed read is shown by the closed code of its refusal, or
// consumer_unavailable, never by the error's text, beside the last inventory when there is one
// (503 when there is none). Every other method is 405 and reads nothing. The page has no form.
func Page(signIn string, read Reader) http.Handler {
	return &inventoryPage{signIn: signIn, read: read}
}

func (p *inventoryPage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p.refresh()
	p.mu.Lock()
	view := pageView{SignIn: p.signIn, Have: p.have, Code: p.code, Inventory: p.inv}
	if p.have {
		view.ReadAt = p.readAt.UTC().Format(time.RFC3339)
	}
	p.mu.Unlock()
	status := http.StatusOK
	if !view.Have {
		status = http.StatusServiceUnavailable
		w.Header().Set("Retry-After", "2")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = storagePage.Execute(w, view)
}

// refresh starts one background read unless one is running. A completed read replaces the
// inventory; a failed one keeps it and records its closed code.
func (p *inventoryPage) refresh() {
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return
	}
	p.busy = true
	p.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()
		inv, err := p.read(ctx)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.busy = false
		if err != nil {
			p.code = closedCode(err)
			return
		}
		p.inv, p.have, p.readAt, p.code = inv, true, time.Now(), ""
	}()
}

// closedCode is the closed code of a failed read: its refusal's, or consumer_unavailable.
func closedCode(err error) string {
	var refusal *Refusal
	if errors.As(err, &refusal) && refusal.Code != "" {
		return refusal.Code
	}
	return helperschema.CodeConsumerUnavailable
}
