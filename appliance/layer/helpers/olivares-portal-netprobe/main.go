// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
	"os"
	"strings"
)

type probeRequest struct {
	OperationID string `json:"operation_id"`
}

func (r *probeRequest) Subcommand() string { return "probe" }
func (r *probeRequest) Operation() string  { return r.OperationID }
func (r *probeRequest) Validate() error {
	if len(r.OperationID) != 32 || strings.Trim(r.OperationID, "0123456789abcdef") != "" {
		return errors.New("network_probe_input_refused")
	}
	return nil
}
func probeRules() []helperschema.Rule {
	return []helperschema.Rule{{Subcommand: "probe", Invokers: []helperschema.Invoker{helperschema.Portal, helperschema.RepairConsole}}}
}
func main() {
	h := invocation.Helper{Name: "netprobe", Rules: probeRules(), NewRequest: func() helperschema.Request { return &probeRequest{} }, Perform: func(context.Context, helperschema.Peer, helperschema.Request) helperschema.Response {
		// A request supplies no target or authority. The protected plan producer and
		// receipt consumer arrive together with ordinary act and qualified-console admission.
		return helperschema.Response{Result: helperschema.ResultRefused, Code: "network_probe_plan_unavailable", Detail: "No authorized plan and confirmation receipt consumer are composed."}
	}}
	os.Exit(invocation.Serve(context.Background(), h, invocation.Linux{}, os.Args[1:], 0, os.Stdin, os.Stdout, os.Stderr))
}
