// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// olivares-portal-firewall-local is the host firewall's local entry point: the root helper through
// which the repair console on tty1 reads the firewall, reverts an unconfirmed window and restores the
// confirmed policy to the one derived from the validated appliance answers.
//
// It serves one connection of olivares-helper-firewall-local.socket
// (/run/olivares-helpers/firewall-local.sock, root:root 0600, Accept=yes), as root with CAP_NET_ADMIN
// alone. It takes no argument and no path: it reads one document, {"op": "status"} or {"op": "revert"
// | "restore", "operation_id"}, admits the invoker the kernel attests for the connection, the repair
// console on tty1 alone, and answers through the same firewall owner, network lock, renderer, load and
// read-back as olivares-portal-firewall. restore derives its target on this host as first boot does:
// first boot's record, the console selection it published, the SSH port sshd reports and the DHCPv6
// client links. It runs no shell, reads no nft text and takes no policy from its caller.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/olivaresai/olivares/appliance/layer/base"
	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"github.com/olivaresai/olivares/appliance/layer/portal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	code := invocation.Serve(ctx, helper(ownerOnHost, hostAnswers()), invocation.Linux{}, os.Args[1:], 0, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// helper is the local entry point over the owner open returns and the answers a restoration derives
// its target from, so that it can be measured without a host.
func helper(open func() (*firewall.Owner, error), answers firewall.Answers) invocation.Helper {
	return invocation.Helper{
		Name:       helperschema.HelperFirewallLocal,
		Rules:      helperschema.FirewallLocalRules(),
		NewRequest: func() helperschema.Request { return &helperschema.FirewallLocalRequest{} },
		Perform: func(ctx context.Context, _ helperschema.Peer, request helperschema.Request) helperschema.Response {
			req, ok := request.(*helperschema.FirewallLocalRequest)
			if !ok {
				return helperschema.Response{Result: helperschema.ResultRefused, Code: helperschema.CodeInputRefused, Detail: "not a document of this helper"}
			}
			o, err := open()
			if err != nil {
				return helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed,
					Detail: "the firewall owner's lock or clock is unavailable; nothing was asked"}
			}
			return firewall.LocalAnswer(ctx, o, answers, *req)
		},
	}
}

// ownerOnHost is the owner over this host: its state, nft, the network lock the NetworkManager plane's
// initializer creates and this boot's clock.
func ownerOnHost() (*firewall.Owner, error) {
	lock, err := netguard.NewFileLock()
	if err != nil {
		return nil, err
	}
	clock, err := netguard.NewBootClock()
	if err != nil {
		return nil, err
	}
	return &firewall.Owner{StateDir: firewall.StateDir, RunDir: firewall.RunDir, Kernel: firewall.NFT{}, Lock: lock, Clock: clock}, nil
}

// hostAnswers reads a restoration's answers on this host, each when the restoration runs.
func hostAnswers() firewall.Answers {
	return firewall.Answers{
		Published: published,
		Selection: selection,
		SSHPort:   func(ctx context.Context) (int, error) { return firewall.SSHPort(ctx, firewall.Exec) },
		Links:     func() ([]string, error) { return firewall.ClientInterfaces(firewall.SysClassNet) },
	}
}

// published reports whether first boot's record shows generate-product-config completed, after which
// the published selection is final.
func published() (bool, error) {
	record, found, err := base.Store{Dir: base.StateDir}.Load()
	if err != nil || !found {
		return false, err
	}
	return slices.ContainsFunc(record.Completed, func(c base.Completed) bool { return c.Stage == base.StageProductConfig }), nil
}

// selection reads the console selection first boot published. After first boot its absence is a
// missing answer, not the empty selection it is before, so the file must be present, and the same
// file, before and after it is read.
func selection() (firewall.Selection, bool, string) {
	before, err := os.Lstat(portal.SelectionFile)
	if errors.Is(err, os.ErrNotExist) {
		return firewall.Selection{}, false, ""
	}
	if err != nil {
		return firewall.Selection{}, true, "the published selection cannot be inspected"
	}
	sel, why := portal.ReadSelection(portal.SelectionFile)
	if why != "" {
		return firewall.Selection{}, true, why
	}
	after, err := os.Lstat(portal.SelectionFile)
	if err != nil || !os.SameFile(before, after) {
		return firewall.Selection{}, false, ""
	}
	enabled := sel.Enabled != nil && *sel.Enabled
	return firewall.Selection{Enabled: enabled, Listen: sel.Listen, ManagementInterfaces: sel.ManagementInterfaces}, true, ""
}
