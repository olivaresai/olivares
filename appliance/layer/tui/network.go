// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package tui

import (
	"context"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"strings"
	"time"
)

type NetworkClient interface {
	Call(context.Context, netguard.EdgeRequest) (netguard.EdgeResponse, error)
}

func (c Console) WithNetwork(client NetworkClient) Console { c.networkClient = client; return c }

// Network restores open windows to their recorded confirmed baselines. Its caller
// must qualify the ordinary console sign-in again before every request.
func (c Console) Network(answer string) string {
	if !c.qualified() {
		return "network: an ordinary act requires its qualified sign-in; nothing was requested."
	}
	if answer != "restore" {
		return "Nothing was asked of the network guard."
	}
	if c.networkClient == nil {
		return "network: the network guard is unavailable."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := c.networkClient.Call(ctx, netguard.EdgeRequest{Action: "status"})
	if err != nil || status.Code != "ok" {
		return "network: status is unknown; no restoration was requested."
	}
	if len(status.OpenWindows) == 0 {
		return "network: no open change window; the guard was asked for no change."
	}
	var states []string
	for _, w := range status.OpenWindows {
		if !c.qualified() {
			return "network: the qualified sign-in ended; no further restoration was requested."
		}
		response, err := c.networkClient.Call(ctx, netguard.EdgeRequest{Action: "revert", OperationID: w.OperationID})
		if err != nil {
			return "network: a restoration outcome is unknown; read guard status before retrying."
		}
		if response.Code != "ok" {
			return "network: restoration is unavailable; the guard refused the request."
		}
		if response.Status == nil {
			return "network: restoration remains unknown."
		}
		states = append(states, string(response.Status.State))
	}
	return "network: " + strings.Join(states, ", ") + ". Only the guard's measured states are reported."
}
