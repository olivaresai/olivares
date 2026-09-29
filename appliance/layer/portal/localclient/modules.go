// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localclient

import "github.com/olivaresai/olivares/appliance/layer/portal/localsession"

// Read reads one closed module query from the portal's read model: services.list,
// storage.inventory and firewall.status from the row offset, services.status of unit. It sends one
// request and grants no act. An answer to another query, page or unit, a firewall.status answer
// without its head and another query's answer with the firewall's members are unverified and end
// the session.
func (c *Client) Read(query, unit string, offset int, surface string) (localsession.ModuleAnswer, error) {
	var answer localsession.ModuleAnswer
	_, err := c.call("module.read", struct {
		Query   string `json:"query"`
		Unit    string `json:"unit,omitempty"`
		Offset  int    `json:"offset,omitempty"`
		Surface string `json:"surface"`
	}{query, unit, offset, surface}, &answer)
	if err != nil {
		return localsession.ModuleAnswer{}, err
	}
	firewall := answer.Firewall != nil || len(answer.FirewallRows) > 0
	if answer.Query != query || answer.Offset != offset || (query == localsession.QueryServicesStatus && (answer.Unit == nil || answer.Unit.Unit != unit)) ||
		(query == localsession.QueryFirewallStatus) != firewall || (query == localsession.QueryFirewallStatus && answer.Firewall == nil) {
		_ = c.Close()
		return localsession.ModuleAnswer{}, &Failure{Code: "response_unverified", Reason: "record_malformed", Sent: true}
	}
	return answer, nil
}
