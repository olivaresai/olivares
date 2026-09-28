// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package portal

import (
	"html/template"
	"net/http"
)

var networkPage = template.Must(template.New("network").Parse(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Network — Appliance Console</title></head><body><h1>Network</h1><p>{{.}}</p><p>Network changes are unavailable until authenticated confirmation and recovery are available together. Use the Appliance Repair Console on tty1 for qualified local recovery.</p><p><a href="/">Appliance status</a></p></body></html>`))

func serveNetwork(s Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = networkPage.Execute(w, s.SignIn.Statement())
	}
}
