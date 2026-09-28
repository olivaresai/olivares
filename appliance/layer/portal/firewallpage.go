// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"net/http"
	"slices"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
)

// FirewallReads is the console's last completed firewall read, which the Firewall page renders from
// memory: the owner's measurement of this boot, or why the firewall was unmeasured, and the owner's
// windows. ok is false while no read has completed. It has no method that asks a helper or changes
// the host.
type FirewallReads interface {
	FirewallSnapshot() (measurement firewall.Measurement, unmeasured string, windows []firewall.Window, ok bool)
}

// FirewallSnapshot implements FirewallReads.
func (m *moduleReads) FirewallSnapshot() (firewall.Measurement, string, []firewall.Window, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wallStamp.Generation == 0 {
		return firewall.Measurement{}, "", nil, false
	}
	return m.wall.measurement, m.wall.unmeasured, slices.Clone(m.wall.status.Windows), true
}

// notReadYet is why the Firewall page is unmeasured before the console's first firewall read.
const notReadYet = "the console has not read the firewall owner yet"

// serveFirewall serves the firewall module's page to a caller on this host: a snapshot of s's
// firewall read, taken from memory at the request, with every act disabled (act_not_adopted) and its
// equivalent command. The page reads nothing else and asks no helper; firewall.Page refuses every
// method but GET and HEAD. The page names interfaces and ports, so a caller on another host is
// answered 404, as for any detail it may not read.
func serveFirewall(s Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !loopbackPeer(r.RemoteAddr) {
			http.NotFound(w, r)
			return
		}
		snapshot := firewall.Snapshot{SignInStatement: s.SignIn.Statement(), ChangesCode: firewall.CodeActNotAdopted, Reason: notReadYet}
		if s.Firewalls != nil {
			if measurement, unmeasured, windows, ok := s.Firewalls.FirewallSnapshot(); ok {
				snapshot.Measurement, snapshot.Reason, snapshot.Windows = measurement, unmeasured, windows
			}
		}
		firewall.Page(snapshot).ServeHTTP(w, r)
	}
}
