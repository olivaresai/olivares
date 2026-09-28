// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Snapshot is what the Services page renders. The page performs no read of its own.
type Snapshot struct {
	// Inventory is the units helper's last list answer.
	Inventory Inventory
	// Measured is false until an inventory was read.
	Measured bool
	// SignInStatement is the sign-in mode statement every page shows.
	SignInStatement string
	// ChangesCode is the closed code that says why acts are not available on this page.
	ChangesCode string
}

// pageRow is one unit as the page shows it.
type pageRow struct {
	Name, Class, Active, Sub, Consequence, Refused, Owner string
	Reads, Acts                                           []string
}

// pageView is the page's data, computed once from a snapshot.
type pageView struct {
	SignInStatement, ChangesCode string
	Measured, Truncated          bool
	Hidden                       int
	Rows                         []pageRow
}

var servicesPage = template.Must(template.New("services").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Services</title>
</head>
<body>
<h1>Services</h1>
<p>{{.SignInStatement}}</p>
<p>Acts on units are unavailable here: {{.ChangesCode}}. Each act is one operation with its own act authorization; its equivalent command is shown. Reading a unit's logs is an authorized act too, because the journal is sensitive.</p>
{{if .Measured}}{{if .Truncated}}<p>The inventory is truncated at its bound.</p>
{{end}}{{if .Hidden}}<p>{{.Hidden}} entries were not unit names and are not shown.</p>
{{end}}<table>
<thead><tr><th>Unit</th><th>Class</th><th>State</th><th>Reads</th><th>Acts</th><th>Class rule</th></tr></thead>
<tbody>
{{range .Rows}}<tr><td>{{.Name}}</td><td>{{.Class}}</td><td>{{.Active}} ({{.Sub}})</td><td>{{range .Reads}}{{.}}<br>{{end}}</td><td>{{range .Acts}}{{.}}<br>{{else}}none{{end}}</td><td>{{if .Refused}}{{.Refused}}: unit_protected, changed by {{.Owner}}.<br>{{end}}{{.Consequence}}</td></tr>
{{end}}</tbody>
</table>
{{else}}<p>The unit inventory is unmeasured.</p>
{{end}}</body>
</html>
`))

// Page serves the Services page for s, GET and HEAD only. It names each unit's class from the
// class table (never from the list answer), what the class admits with each act's equivalent
// command, what it refuses with unit_protected and the owner that changes such a unit, and why
// acts are unavailable here. It has no form and no act of its own, and shows only entries that
// are unit names.
func Page(s Snapshot) http.Handler {
	view := pageView{SignInStatement: s.SignInStatement, ChangesCode: s.ChangesCode, Measured: s.Measured, Truncated: s.Inventory.Truncated}
	if view.ChangesCode == "" {
		view.ChangesCode = CodeActNotAdopted
	}
	for _, u := range s.Inventory.Units {
		if !ValidUnitName(u.Name) {
			view.Hidden++
			continue
		}
		class := Classify(u.Name)
		row := pageRow{Name: u.Name, Class: string(class), Active: u.ActiveState, Sub: u.SubState, Consequence: class.Consequence(), Owner: class.Owner(),
			Reads: []string{
				"status: olivares-appliance service status " + u.Name,
				"logs (an authorized act): olivares-appliance service logs " + u.Name + " --lines " + strconv.Itoa(DefaultLogLines),
			}}
		var refused []string
		for _, op := range Ops() {
			switch {
			case !IsEffect(op):
			case class.Allows(op):
				row.Acts = append(row.Acts, op+": olivares-appliance service "+op+" "+u.Name)
			default:
				refused = append(refused, op)
			}
		}
		row.Refused = strings.Join(refused, ", ")
		view.Rows = append(view.Rows, row)
	}
	slices.SortFunc(view.Rows, func(a, b pageRow) int { return strings.Compare(a.Name, b.Name) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = servicesPage.Execute(w, view)
	})
}
