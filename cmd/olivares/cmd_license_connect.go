// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// cmd_license_connect.go is `olivares license connect`: the installed client of connect-v1.

const connectApprovalHint = "Open the approval_url in any browser signed in to the customer portal (this machine or another), " +
	"check that it shows the same fingerprint, approve it, then run this same command again. No secret is in the link."

type connectCommonFlags struct {
	dataDir  string
	endpoint string
	license  string
	timeout  time.Duration
}

func (f *connectCommonFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.dataDir, "data-dir", "", "data directory that holds this deployment's connected identity (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	fl.StringVar(&f.endpoint, "endpoint", "", "licensing service origin (default "+defaultConnectEndpoint+" on first use; afterwards the one recorded in the data directory)")
	fl.StringVar(&f.license, "license", "", "explicit license path the engine is started with; a connected credential is refused while it outranks the data directory")
	fl.DurationVar(&f.timeout, "timeout", 60*time.Second, "overall network timeout")
}

type connectIdentifierFlags struct {
	provider, businessID, holderID, licenseID, purpose, parent, label, channel, evidence string
}

func (f *connectIdentifierFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.provider, "provider", "dodo", "commerce provider of the purchase")
	fl.StringVar(&f.businessID, "business-id", "", "provider business id shown in the customer portal")
	fl.StringVar(&f.holderID, "holder-id", "", "holder (subscription) id shown in the customer portal")
	fl.StringVar(&f.licenseID, "license-id", "", "license id shown in the customer portal")
	fl.StringVar(&f.purpose, "purpose", "production", "production | staging")
	fl.StringVar(&f.parent, "parent", "", "staging only: the production deployment id it belongs to")
	fl.StringVar(&f.label, "label", "", "optional display label (not an identity)")
	fl.StringVar(&f.channel, "channel", "stable", "release channel the credential and download token are resolved for: stable | security")
	fl.StringVar(&f.evidence, "evidence", "", "purchase credential to present (file, or - for stdin; default the installed license, which may be expired)")
}

func validConnectIdentifier(s string, max int) bool {
	return s != "" && len(s) <= max && !strings.ContainsAny(s, " \t\r\n/")
}

// newState builds the first state of a data directory from the identifier flags.
func (f *connectIdentifierFlags) newState(endpoint string) (*connectState, error) {
	for _, c := range []struct {
		flag, value string
		max         int
	}{{"--provider", f.provider, 16}, {"--business-id", f.businessID, 80}, {"--holder-id", f.holderID, 80}, {"--license-id", f.licenseID, 120}} {
		if !validConnectIdentifier(c.value, c.max) {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf("%s is required (the identifiers the customer portal shows for this purchase)", c.flag))
		}
	}
	switch f.purpose {
	case "production":
		if f.parent != "" {
			return nil, exitcode.New(exitcode.Usage, errors.New("--parent applies only to --purpose staging"))
		}
	case "staging":
		if !validConnectIdentifier(f.parent, 80) {
			return nil, exitcode.New(exitcode.Usage, errors.New("--purpose staging needs --parent <production deployment id>"))
		}
	default:
		return nil, exitcode.New(exitcode.Usage, errors.New("--purpose must be production or staging"))
	}
	if len(f.label) > 64 {
		return nil, exitcode.New(exitcode.Usage, errors.New("--label is at most 64 bytes"))
	}
	if f.channel != "stable" && f.channel != "security" {
		return nil, exitcode.New(exitcode.Usage, errors.New("--channel must be stable or security"))
	}
	return &connectState{Schema: connectStateSchema, Endpoint: endpoint, Provider: f.provider, BusinessID: f.businessID,
		HolderID: f.holderID, LicenseID: f.licenseID, Purpose: f.purpose, Parent: f.parent, Label: f.label, Channel: f.channel}, nil
}

// conflictsWith refuses identifier flags that name another purchase than the recorded one: a
// second environment uses another data directory.
func (f *connectIdentifierFlags) conflictsWith(cmd *cobra.Command, st *connectState) error {
	for _, c := range []struct{ flag, recorded string }{
		{"provider", st.Provider}, {"business-id", st.BusinessID}, {"holder-id", st.HolderID}, {"license-id", st.LicenseID},
		{"purpose", st.Purpose}, {"parent", st.Parent},
	} {
		if cmd.Flags().Changed(c.flag) {
			if v, _ := cmd.Flags().GetString(c.flag); v != c.recorded {
				return exitcode.New(exitcode.Usage, fmt.Errorf("this data directory is already connected for another --%s (%q); use a separate --data-dir for another deployment", c.flag, c.recorded))
			}
		}
	}
	return nil
}

// openConnectRun leases the data directory and loads (or, with create, initializes) its state.
func openConnectRun(f *connectCommonFlags, create func(endpoint string) (*connectState, error), check func(*connectState) error) (*connectRun, error) {
	dir, err := resolveCommandDataDir(f.dataDir)
	if err != nil {
		return nil, err
	}
	store, err := openConnectStore(dir, time.Now)
	if err != nil {
		return nil, connectExit(err)
	}
	fail := func(err error) (*connectRun, error) {
		_ = store.Close()
		return nil, connectExit(err)
	}
	st, err := store.loadState()
	if err != nil {
		return fail(err)
	}
	if st == nil {
		if create == nil {
			return fail(errConnectNoState)
		}
		raw := f.endpoint
		if raw == "" {
			raw = defaultConnectEndpoint
		}
		ep, err := newConnectEndpoint(raw, f.timeout)
		if err != nil {
			return fail(exitcode.New(exitcode.Usage, err))
		}
		if st, err = create(ep.origin); err != nil {
			return fail(err)
		}
	} else if check != nil {
		if err := check(st); err != nil {
			return fail(err)
		}
	}
	if f.endpoint != "" {
		given, err := newConnectEndpoint(f.endpoint, f.timeout)
		if err != nil {
			return fail(exitcode.New(exitcode.Usage, err))
		}
		if given.origin != st.Endpoint {
			return fail(exitcode.New(exitcode.Usage, fmt.Errorf("this data directory is connected to %s, not %s; its identity is bound there", st.Endpoint, given.origin)))
		}
	}
	ep, err := newConnectEndpoint(st.Endpoint, f.timeout)
	if err != nil {
		return fail(err)
	}
	return &connectRun{store: store, st: st, ep: ep, dataDir: dir, licenseExplicit: f.license, getenv: osGetenv, now: time.Now}, nil
}

func renderConnect(cmd *cobra.Command, rep connectReport) error {
	if err := renderReportOut(cmd, map[string]any(rep)); err != nil {
		return err
	}
	if rep["status"] == "approval_pending" {
		fmt.Fprintln(cmd.ErrOrStderr(), connectApprovalHint)
	}
	return nil
}

func connectContext(cmd *cobra.Command, f *connectCommonFlags) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cmd.Context(), f.timeout)
}

func licenseConnectCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "connect",
		Short: "Bind this deployment to its purchase with an owner-approved key, and refresh, rotate, recover or deactivate it",
		Long: "connect is the installed client of the connect-v1 licensing protocol. A deployment holds its own Ed25519\n" +
			"key under its data directory; the purchase owner approves it once in the customer portal, from any\n" +
			"browser; afterwards the deployment refreshes its credential and download token by proof of possession.\n" +
			"Every step is recorded before it is sent and repeated safely after a crash or timeout. A returned\n" +
			"credential replaces the installed license only after it verifies against this deployment's license\n" +
			"trust and confers a current right. Nothing here runs at boot: the engine stays offline.",
		Example: "  olivares license connect start --data-dir /var/lib/olivares --business-id bus_123 --holder-id sub_456 --license-id lic_789\n" +
			"  olivares license connect refresh --data-dir /var/lib/olivares",
	}
	root.AddCommand(licenseConnectStartCmd(), licenseConnectRefreshCmd(), licenseConnectRotateCmd(),
		licenseConnectApprovedRotationCmd("recover"), licenseConnectApprovedRotationCmd("reactivate"),
		licenseConnectDeactivateCmd(), licenseConnectStatusCmd(), licenseConnectAbandonCmd())
	return root
}

func licenseConnectStartCmd() *cobra.Command {
	var common connectCommonFlags
	var ids connectIdentifierFlags
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Request owner approval for this deployment's key and complete the binding once approved",
		Long: "start creates this data directory's PoP key if it has none, sends a binding request with the purchase\n" +
			"evidence, and prints the approval URL and key fingerprint. Run it again after the owner approves: it\n" +
			"repeats the same recorded request, learns of the approval and completes the binding, installing the\n" +
			"verified credential. An expired installed credential is accepted as evidence.",
		Example: "  olivares license connect start --data-dir /var/lib/olivares --business-id bus_123 --holder-id sub_456 --license-id lic_789\n" +
			"  olivares license connect start --data-dir /var/lib/olivares",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := openConnectRun(&common, ids.newState, func(st *connectState) error { return ids.conflictsWith(cmd, st) })
			if err != nil {
				return err
			}
			defer func() { _ = run.store.Close() }()
			ctx, cancel := connectContext(cmd, &common)
			defer cancel()
			rep, err := run.bind(ctx, func() (string, error) {
				return connectEvidence(run.dataDir, ids.evidence, cmd.InOrStdin(), time.Now().UTC())
			})
			if err != nil {
				return connectExit(err)
			}
			return renderConnect(cmd, rep)
		},
	}
	common.register(cmd)
	ids.register(cmd)
	return cmd
}

func licenseConnectRefreshCmd() *cobra.Command {
	var common connectCommonFlags
	var channel string
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Refresh the installed credential and download token by proof of possession",
		Long: "refresh proves possession of the bound key and obtains the current credential and download token. It\n" +
			"works when the installed credential has expired, as long as the purchase is current. A refusal, a\n" +
			"timeout or an answer that does not verify leaves the installed license and token unchanged.\n" +
			"It runs when you run it, or each time a connected upgrade timer fires (upgrade --install-timer\n" +
			"--enterprise --connect); nothing schedules it from the refresh planning boundary that `status` shows as\n" +
			"last.effective_until, which is the earliest boundary among the credential's active signed lines.",
		Example:      "  olivares license connect refresh --data-dir /var/lib/olivares",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := openConnectRun(&common, nil, nil)
			if err != nil {
				return err
			}
			defer func() { _ = run.store.Close() }()
			ch := run.st.Channel
			if cmd.Flags().Changed("channel") {
				ch = channel
			}
			if ch != "stable" && ch != "security" {
				return exitcode.New(exitcode.Usage, errors.New("--channel must be stable or security"))
			}
			ctx, cancel := connectContext(cmd, &common)
			defer cancel()
			rep, _, err := run.refresh(ctx, ch)
			if err != nil {
				return connectExit(err)
			}
			return renderConnect(cmd, rep)
		},
	}
	common.register(cmd)
	cmd.Flags().StringVar(&channel, "channel", "stable", "release channel: stable | security (default the recorded channel)")
	return cmd
}

func licenseConnectRotateCmd() *cobra.Command {
	var common connectCommonFlags
	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Replace this deployment's key, proving possession of both the bound and the new key",
		Long: "rotate-key creates a proposed key and asks the service to bind it, signing with the bound key and the\n" +
			"proposed key over the same message. On success the proposed key becomes the identity and the binding\n" +
			"epoch advances; credentials and tokens of the old binding stop being refreshable.",
		Example:      "  olivares license connect rotate-key --data-dir /var/lib/olivares",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := openConnectRun(&common, nil, nil)
			if err != nil {
				return err
			}
			defer func() { _ = run.store.Close() }()
			ctx, cancel := connectContext(cmd, &common)
			defer cancel()
			rep, err := run.rotate(ctx)
			if err != nil {
				return connectExit(err)
			}
			return renderConnect(cmd, rep)
		},
	}
	common.register(cmd)
	return cmd
}

func licenseConnectApprovedRotationCmd(intent string) *cobra.Command {
	var common connectCommonFlags
	var ids connectIdentifierFlags
	var deployment string
	var epoch int64
	short := map[string]string{
		"recover":    "Bind a new key to an existing deployment whose key is lost, with the owner's approval",
		"reactivate": "Reactivate an explicitly deactivated deployment with a new key and the owner's approval",
	}[intent]
	cmd := &cobra.Command{
		Use:   intent,
		Short: short,
		Long: intent + " creates a proposed key, requests the owner's approval for that key and deployment, and prints the\n" +
			"approval URL. A NEW request needs the current purchase credential: --evidence <file>, or --evidence - to read\n" +
			"it from standard input. The license installed in the data directory is not used as that evidence. Run the\n" +
			"command again after the owner approves: the recorded request is repeated with its exact bytes and does not\n" +
			"read --evidence again, the approval is learned and the deployment is completed with the proposed key and a\n" +
			"new binding epoch. The lost or deactivated key is never needed, and a copied license or download token cannot\n" +
			"do this.",
		Example: "  olivares license connect " + intent + " --data-dir /var/lib/olivares --deployment dep_abc --evidence ./purchase-credential.v3\n" +
			"  olivares license connect " + intent + " --data-dir /var/lib/olivares --deployment dep_abc --binding-epoch 2 --business-id bus_123 --holder-id sub_456 --license-id lic_789 --evidence ./purchase-credential.v3",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := openConnectRun(&common, ids.newState, func(st *connectState) error { return ids.conflictsWith(cmd, st) })
			if err != nil {
				return err
			}
			defer func() { _ = run.store.Close() }()
			dep, ep := deployment, epoch
			if b := run.st.Binding; b != nil && (dep == "" || dep == b.DeploymentID) {
				dep = b.DeploymentID
				if !cmd.Flags().Changed("binding-epoch") {
					ep = b.BindingEpoch
				}
			}
			if p := run.st.Pending; p != nil && p.Intent == intent && p.Phase == "complete" {
				dep, ep = p.Target, p.BindingEpoch
			}
			ctx, cancel := connectContext(cmd, &common)
			defer cancel()
			rep, err := run.approvedRotation(ctx, intent, dep, ep, func() (string, error) {
				return connectOwnerRequestEvidence(run.dataDir, ids.evidence, intent, cmd.InOrStdin(), time.Now().UTC())
			})
			if err != nil {
				return connectExit(err)
			}
			return renderConnect(cmd, rep)
		},
	}
	common.register(cmd)
	ids.register(cmd)
	cmd.Flags().StringVar(&deployment, "deployment", "", "deployment id (default the one recorded in the data directory)")
	cmd.Flags().Lookup("evidence").Usage = "current purchase credential for a NEW request (file, or - for stdin); required, the installed license is not used"
	cmd.Flags().Int64Var(&epoch, "binding-epoch", 0, "binding epoch of that deployment when the data directory no longer records it")
	return cmd
}

func licenseConnectDeactivateCmd() *cobra.Command {
	var common connectCommonFlags
	var deployment, evidence string
	var ownerApproval, yes bool
	cmd := &cobra.Command{
		Use:   "deactivate",
		Short: "Deactivate a deployment by proof of possession, or with the owner's approval",
		Long: "deactivate ends a deployment's binding. By default the bound key proves possession. With --owner-approval\n" +
			"it requests the owner's approval for that exact deployment instead, prints the approval URL, and completes\n" +
			"when run again after approval. A NEW owner-approved request needs the current purchase credential with\n" +
			"--evidence <file>, or --evidence - for standard input; the installed license is not used, and running the\n" +
			"command again repeats the recorded request without reading --evidence. The purchased term is unchanged,\n" +
			"the installed license file is kept, and a later renewal does not reactivate the deployment.",
		Example: "  olivares license connect deactivate --data-dir /var/lib/olivares --yes\n" +
			"  olivares license connect deactivate --data-dir /var/lib/olivares --deployment dep_abc --owner-approval --evidence ./purchase-credential.v3 --yes",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := openConnectRun(&common, nil, nil)
			if err != nil {
				return err
			}
			defer func() { _ = run.store.Close() }()
			dep := deployment
			var ep int64
			if b := run.st.Binding; b != nil && (dep == "" || dep == b.DeploymentID) {
				dep, ep = b.DeploymentID, b.BindingEpoch
			}
			if p := run.st.Pending; p != nil && p.Intent == "delete" {
				ownerApproval = ownerApproval || p.Phase == "request"
			}
			if run.st.Pending == nil {
				if err := confirmDestructive(cmd, yes, "deactivate deployment "+dep); err != nil {
					return err
				}
			}
			ctx, cancel := connectContext(cmd, &common)
			defer cancel()
			rep, err := run.deactivate(ctx, dep, ep, ownerApproval, func() (string, error) {
				return connectOwnerRequestEvidence(run.dataDir, evidence, "deactivate", cmd.InOrStdin(), time.Now().UTC())
			})
			if err != nil {
				return connectExit(err)
			}
			return renderConnect(cmd, rep)
		},
	}
	common.register(cmd)
	cmd.Flags().StringVar(&deployment, "deployment", "", "deployment id (default the one bound to this data directory)")
	cmd.Flags().BoolVar(&ownerApproval, "owner-approval", false, "request the owner's approval instead of proving possession of the bound key")
	cmd.Flags().StringVar(&evidence, "evidence", "", "with --owner-approval: current purchase credential for a NEW request (file, or - for stdin); required, the installed license is not used")
	addYesFlag(cmd, &yes)
	return cmd
}

func licenseConnectStatusCmd() *cobra.Command {
	var common connectCommonFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show this data directory's connected identity, binding, pending operation and last credential",
		Long: "status reads the connected client's state without contacting the service. It shows identifiers, the key\n" +
			"id and fingerprint, the binding, any owner-approval request and pending step, and the last installed\n" +
			"credential's serial and digest. last.effective_until is its refresh planning boundary: the earliest\n" +
			"boundary among the lines active when it was installed, such as an add-on's lease, not the end of\n" +
			"every line's right. It never prints a key, credential, token or approval reference.",
		Example:      "  olivares license connect status --data-dir /var/lib/olivares",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := openConnectRun(&common, nil, nil)
			if err != nil {
				return err
			}
			defer func() { _ = run.store.Close() }()
			status := "unbound"
			switch {
			case run.st.Pending != nil:
				status = "pending"
			case run.st.Binding != nil:
				status = run.st.Binding.Status
			}
			return renderConnect(cmd, run.report(status))
		},
	}
	common.register(cmd)
	return cmd
}

func licenseConnectAbandonCmd() *cobra.Command {
	var common connectCommonFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "abandon",
		Short: "Discard the pending connect operation of this data directory",
		Long: "abandon forgets the pending step and owner-approval request so a different operation can start. It keeps\n" +
			"the identity, any proposed key, the binding and the installed license. A step the service already\n" +
			"committed is not undone: a later refresh obtains the current state.",
		Example:      "  olivares license connect abandon --data-dir /var/lib/olivares --yes",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := openConnectRun(&common, nil, nil)
			if err != nil {
				return err
			}
			defer func() { _ = run.store.Close() }()
			if c := run.st.Completion; c != nil {
				// The service committed this transition and the client verified its answer. Discarding
				// the record would leave the data directory bound to a key the service no longer accepts.
				return exitcode.New(exitcode.Conflict, fmt.Errorf("refusing: the %s of deployment %s was verified and recorded; only its local completion remains — run `olivares license connect %s`, which sends nothing",
					c.Intent, c.DeploymentID, connectTransitionCommand(c.Intent)))
			}
			if run.st.Pending == nil && run.st.Request == nil {
				return renderConnect(cmd, run.report("nothing_pending"))
			}
			if err := confirmDestructive(cmd, yes, "discard the pending "+pendingIntent(run.st)+" operation"); err != nil {
				return err
			}
			if err := run.end(true); err != nil {
				return err
			}
			return renderConnect(cmd, run.report("abandoned"))
		},
	}
	common.register(cmd)
	addYesFlag(cmd, &yes)
	return cmd
}

func pendingIntent(st *connectState) string {
	if st.Pending != nil {
		return st.Pending.Intent
	}
	return st.Request.Operation
}

// connectRefreshForUpgrade is the connected branch of `upgrade --enterprise --connect`: a PoP
// refresh of the selected data directory, returning the fresh download token in memory. The
// verified credential is installed before the gated download, so the unchanged license gate,
// manifest signature, artifact digest, anti-rollback and minimum-version checks follow.
func connectRefreshForUpgrade(ctx context.Context, o *upgradeOptions, out io.Writer) (token, endpoint string, err error) {
	common := connectCommonFlags{dataDir: o.dataDir, endpoint: o.endpoint, license: o.license, timeout: o.timeout}
	run, err := openConnectRun(&common, nil, nil)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = run.store.Close() }()
	rep, token, err := run.refresh(ctx, o.channel)
	if err != nil {
		return "", "", connectExit(fmt.Errorf("connect refresh before the upgrade: %w", err))
	}
	version := ""
	if run.st.Last != nil {
		version = run.st.Last.Version
	}
	fmt.Fprintf(out, "connect: refreshed the credential of deployment %v by proof of possession (current release %s)\n", rep["deployment_id"], version)
	return token, run.st.Endpoint, nil
}
