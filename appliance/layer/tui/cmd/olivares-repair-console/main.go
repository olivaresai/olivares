// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// olivares-repair-console serves the Appliance Repair Console on the terminal it is given,
// which its unit makes tty1. It takes no arguments and listens on no socket. Its operator signs in
// through the distribution's local-console sign-in service, login, and its acts leave through the
// power, certificate and firewall-local helpers' sockets, the network guard's, and the repair
// package's restore of the portal's stack. Every power act passes the retained gates of the repair package's admission;
// a recovery reboot also commits its linkage record before the power helper is asked. It exits 0 when the operator leaves and 2 on an input or output failure, and
// it states that failure on standard error, which its unit sends to the journal rather than to the
// terminal, which may be what failed.
//
// It states the sign-in mode of the console on 9443 as the mode selector reports it. Neither
// the product probe nor the portal's sign-in stack is wired in this version, so that mode is
// the refusing one — nobody signs in over the network — which is exactly the state this
// console exists for. E12's host record and account-recovery entry are composed with E12's own
// console slice; until then the account-recovery entry refuses.
package main

import (
	"context"
	"crypto/rand"
	"os"
	"os/user"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/netguard"
	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
	"github.com/olivaresai/olivares/appliance/layer/portal/netclient"
	"github.com/olivaresai/olivares/appliance/layer/repair"
	"github.com/olivaresai/olivares/appliance/layer/tui"
	"github.com/olivaresai/olivares/appliance/layer/tui/localpam"
)

// portalTLS holds the public certificate; this console never reads its private key.
const portalTLS = "/etc/olivares-portal"

func main() {
	console := tui.NewConsole(auth.NewSelector(nil, nil).Select()).
		WithHelper(helperclient.Client{}).
		WithNetwork(netclient.Client{}).
		WithAuthority(tui.Authority{Stack: localpam.Stack{}, Accounts: tui.SystemAccounts(), Terminal: tui.SelfTerminal(),
			Now: time.Now, Random: rand.Reader}).
		WithCertificateFingerprint(certificateFingerprint).
		WithPortalPAM(repair.PortalPAM{Pristine: repair.PristinePortalPAM, Dir: "/etc/pam.d", Name: auth.PAMService,
			GroupExists: groupExists}).
		WithPowerAdmission(repair.Admission{
			Invoker:    tui.SelfTerminal().Attest,
			Serializer: repair.HostHold{LifecycleDir: localsession.LifecycleDirectory, RecordsDir: localsession.OperationDirectory},
			Gates: []repair.Gate{
				repair.WindowGate{Status: guardStatus, Boot: repair.CurrentBoot},
				repair.RecordsGate{Dir: localsession.OperationDirectory},
				repair.PackageGate{Locks: repair.PackageLocks, Phase: repair.PackagePhaseRecord},
				repair.StorageGate{Jobs: repair.UDisksJobs},
			},
			Store: repair.LinkageStore{Dir: repair.LinkageDirectory, Owner: 0},
			Now:   time.Now,
		}).
		WithBootID(repair.CurrentBoot)
	os.Exit(console.Run(os.Stdin, os.Stdout, os.Stderr))
}

// guardStatus asks the network guard for its status, as the console's network verb does.
func guardStatus(ctx context.Context) (netguard.EdgeResponse, error) {
	return netclient.Client{}.Call(ctx, netguard.EdgeRequest{Action: "status"})
}

// certificateFingerprint measures only the stored public certificate.
func certificateFingerprint() (string, bool, string) {
	return tui.ReadCertificateFingerprint(portalTLS)
}

// groupExists answers whether the host has the named group.
func groupExists(name string) error {
	_, err := user.LookupGroup(name)
	return err
}
