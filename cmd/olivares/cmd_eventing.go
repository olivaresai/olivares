// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/webhook"
	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// Entity kinds for the eventing module, matching the values in
// modules/eventing/schema.go. These are duplicated as string literals because
// the schema constants are package-private.
const (
	evtSubscriptionKind model.Kind = "eventing.subscription"
	evtEventKind        model.Kind = "eventing.event"
	evtDeliveryKind     model.Kind = "eventing.delivery"
)

// Column names for eventing entities, matching modules/eventing/schema.go.
const (
	// subscription columns
	evtColSubName         = "name"
	evtColSubEnabled      = "enabled"
	evtColSubTypes        = "event_types"
	evtColSubSources      = "match_sources"
	evtColSubEndpoint     = "endpoint"
	evtColSubSecret       = "secret_sealed"
	evtColSubSecretHint   = "secret_hint"
	evtColSubRole         = "role"
	evtColSubDescription  = "description"
	evtColSubAuthType     = "auth_type"
	evtColSubAuthValHint  = "auth_value_hint"
	evtColSubAuthHdrName  = "auth_header_name"
	evtColSubMaxAttempts  = "max_attempts"
	evtColSubInitInterval = "initial_interval_seconds"

	// event columns
	evtColEvSeq        = "seq"
	evtColEvEventID    = "event_id"
	evtColEvType       = "event_type"
	evtColEvSource     = "source"
	evtColEvOccurredAt = "occurred_at"
	evtColEvPayload    = "payload"

	// delivery columns
	evtColDelSubRef     = "subscription_ref"
	evtColDelEventID    = "event_id"
	evtColDelEventSeq   = "event_seq"
	evtColDelEventType  = "event_type"
	evtColDelStatus     = "status"
	evtColDelOrigin     = "origin"
	evtColDelAttempts   = "attempts"
	evtColDelNextAt     = "next_attempt_at"
	evtColDelLastAt     = "last_attempt_at"
	evtColDelLastStatus = "last_status"
)

// newEventingCmd creates the `olivares eventing` command group with
// subcommands for subscriptions, deliveries, dead-letters and events.

// eventingListCap is the ceiling these listings ask the store for: `maxLimit` from
// core/internal/store/sqlstore/generic.go, not a number someone liked.
//
// ⛔ WHY IT EXISTS. Four listings here called `repo.List(ctx, q)` with NO Limit and bound the page
//
//	to `_`. The generic store then served its DEFAULT page of ONE HUNDRED, so `eventing
//	subscriptions list`, `deliveries list`, `dead-letters` and `events list` printed a hundred
//	rows and looked complete. An operator counting dead letters to decide whether an outage is
//	over was reading a number the CLI never said was partial.
const eventingListCap = 1000

// warnEventingTruncated says, on STDERR, that the listing is not the whole set.
//
// ⛔ STDERR, AND THAT IS THE WHOLE DESIGN. `renderOut` serves two formats: text and JSON. Adding a
//
//	line to the text output would be fine; adding a field to the JSON would change the SHAPE of a
//	published contract and break every script that parses it. Stderr carries the caveat to the
//	human without touching the bytes a pipe consumes -- the same seam `cmd_agentexec.go` already
//	uses for its notices.
func warnEventingTruncated(cmd *cobra.Command, shown int, page model.Page, what string) {
	if !page.HasMore {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"notice: showing the first %d %s; more exist. Narrow the filters to see the rest.\n",
		shown, what)
}

func newEventingCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "eventing",
		Short: "Manage the eventing platform (webhook event subscriptions, deliveries, event log)",
		Long: "Offline CRUD and inspection over the eventing platform's durable entities:\n" +
			"subscriptions, captured events, deliveries and the dead-letter queue.\n" +
			"These commands operate against the store directly, without a running engine.",
		Example: "  olivares eventing subscriptions ls --tenant t_abc123\n" +
			"  olivares eventing deliveries ls --tenant t_abc123 --status dead\n" +
			"  olivares eventing dead-letters redeliver --tenant t_abc123 --id delivery-123",
	}
	addTextJSONFormatFlag(root)
	root.AddCommand(newEventingSubscriptionsCmd(), newEventingDeliveriesCmd(), newEventingDeadLettersCmd(), newEventingEventsCmd(), newEventingEgressCmd(), newEventingFenceCmd())
	return root
}

// ---------------------------------------------------------------------------
// olivares eventing subscriptions
// ---------------------------------------------------------------------------

func newEventingSubscriptionsCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "subscriptions",
		Short: "Manage event subscriptions (ls, get, create, update, rotate-secret, rm, test)",
		Long: "A subscription is a tenant's standing request to have matching events delivered\n" +
			"to an endpoint it owns. These verbs create, list, edit and remove them, and send\n" +
			"a test delivery so the endpoint can be proved reachable before real events\n" +
			"depend on it. `update` edits one in place and does NOT touch the signing secret:\n" +
			"before it existed, moving an endpoint meant rm + create, which issued a new\n" +
			"secret and broke every receiver still verifying with the old one.",
		Example: "  olivares eventing subscriptions ls --tenant t_abc123\n" +
			"  olivares eventing subscriptions get --tenant t_abc123 --id sub-123\n" +
			"  olivares eventing subscriptions update --tenant t_abc123 --id sub-123 --endpoint https://new.example/hook\n" +
			"  olivares eventing subscriptions test --tenant t_abc123 --id sub-123\n" +
			"  olivares eventing subscriptions rm --tenant t_abc123 --id sub-123",
	}
	root.AddCommand(eventingSubListCmd(), eventingSubGetCmd(), eventingSubCreateCmd(), eventingSubUpdateCmd(), eventingSubRotateSecretCmd(), eventingSubRemoveCmd(), eventingSubTestCmd())
	return root
}

func eventingSubListCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant string
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List event subscriptions for a tenant",
		Long:    "ls reads and lists all durable webhook event subscriptions for one tenant without requiring a running engine.",
		Example: "  olivares eventing subscriptions ls --tenant t_abc123",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			return eng.store.View(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtSubscriptionKind)
				if err != nil {
					return err
				}
				recs, page, err := repo.List(cmd.Context(), model.Query{Limit: eventingListCap})
				if err != nil {
					return err
				}
				warnEventingTruncated(cmd, len(recs), page, "subscriptions")
				items := make([]eventingSubscriptionListItem, 0, len(recs))
				for _, r := range recs {
					authType := r.String(evtColSubAuthType)
					if authType == "" {
						authType = "none"
					}
					items = append(items, eventingSubscriptionListItem{
						ID:         r.String(model.ColID),
						Name:       r.String(evtColSubName),
						Enabled:    r.Bool(evtColSubEnabled),
						Endpoint:   r.String(evtColSubEndpoint),
						Role:       r.String(evtColSubRole),
						EventTypes: r.String(evtColSubTypes),
						AuthType:   authType,
						CreatedAt:  r.String(model.ColCreatedAt),
					})
				}
				return renderOut(cmd, func(out io.Writer) error {
					if len(recs) == 0 {
						_, err := fmt.Fprintln(out, "no subscriptions")
						return err
					}
					tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "ID\tNAME\tENABLED\tENDPOINT\tROLE\tEVENT_TYPES\tAUTH_TYPE\tCREATED")
					for _, item := range items {
						fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\t%s\t%s\t%s\n",
							item.ID, item.Name, item.Enabled, ellipsis(item.Endpoint, 50), item.Role,
							item.EventTypes, item.AuthType, item.CreatedAt)
					}
					return tw.Flush()
				}, items)
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	return cmd
}

// eventingSubGetCmd closes one of the verb gaps C08-04 names: the API has served
// GET /v1/m/eventing/subscriptions/{id} since (modules/eventing/eventing.go:465)
// and the CLI could only LIST. Listing truncates the endpoint to 50 columns and drops
// every retry and auth field, so an operator inspecting ONE subscription had to read a
// table built for scanning many — or leave the CLI.
//
// Read-only by construction: auditBootRO and store.View, never Mutate.
func eventingSubGetCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, id string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show one event subscription in full",
		Long: "get reads one durable webhook event subscription by tenant and id and prints every\n" +
			"field, including the retry policy and the auth shape that `ls` leaves out. It never\n" +
			"prints the shared secret itself: the store keeps only a hint, and a verb that\n" +
			"reprinted a delivery credential would be a worse tool than no verb at all.",
		Example: "  olivares eventing subscriptions get --tenant t_abc123 --id sub-123",
		Args:    cobra.NoArgs,
		Aliases: []string{"show"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			mid := model.ID(strings.TrimSpace(id))
			if mid.IsZero() {
				return fmt.Errorf("--id is required")
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			return eng.store.View(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtSubscriptionKind)
				if err != nil {
					return err
				}
				r, err := repo.Get(cmd.Context(), mid)
				if err != nil {
					return err
				}
				authType := r.String(evtColSubAuthType)
				if authType == "" {
					authType = "none"
				}
				item := eventingSubscriptionDetail{
					ID:           r.String(model.ColID),
					Name:         r.String(evtColSubName),
					Description:  r.String(evtColSubDescription),
					Enabled:      r.Bool(evtColSubEnabled),
					Endpoint:     r.String(evtColSubEndpoint),
					Role:         r.String(evtColSubRole),
					EventTypes:   r.String(evtColSubTypes),
					Sources:      r.String(evtColSubSources),
					AuthType:     authType,
					AuthHeader:   r.String(evtColSubAuthHdrName),
					AuthHint:     r.String(evtColSubAuthValHint),
					SecretHint:   r.String(evtColSubSecretHint),
					MaxAttempts:  r.String(evtColSubMaxAttempts),
					InitInterval: r.String(evtColSubInitInterval),
					CreatedAt:    r.String(model.ColCreatedAt),
				}
				return renderOut(cmd, func(out io.Writer) error {
					tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
					fmt.Fprintf(tw, "ID\t%s\n", item.ID)
					fmt.Fprintf(tw, "NAME\t%s\n", item.Name)
					fmt.Fprintf(tw, "DESCRIPTION\t%s\n", item.Description)
					fmt.Fprintf(tw, "ENABLED\t%t\n", item.Enabled)
					fmt.Fprintf(tw, "ENDPOINT\t%s\n", item.Endpoint)
					fmt.Fprintf(tw, "ROLE\t%s\n", item.Role)
					fmt.Fprintf(tw, "EVENT_TYPES\t%s\n", item.EventTypes)
					fmt.Fprintf(tw, "SOURCES\t%s\n", item.Sources)
					fmt.Fprintf(tw, "AUTH_TYPE\t%s\n", item.AuthType)
					fmt.Fprintf(tw, "AUTH_HEADER\t%s\n", item.AuthHeader)
					fmt.Fprintf(tw, "AUTH_HINT\t%s\n", item.AuthHint)
					fmt.Fprintf(tw, "SECRET_HINT\t%s\n", item.SecretHint)
					fmt.Fprintf(tw, "MAX_ATTEMPTS\t%s\n", item.MaxAttempts)
					fmt.Fprintf(tw, "INITIAL_INTERVAL\t%s\n", item.InitInterval)
					fmt.Fprintf(tw, "CREATED\t%s\n", item.CreatedAt)
					return tw.Flush()
				}, item)
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&id, "id", "", "subscription id")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func eventingSubCreateCmd() *cobra.Command {
	var (
		dataDir, engine, dsn  string
		tenant                string
		name, endpoint        string
		role, description     string
		eventTypes            []string
		authType, authHdrName string
		maxAttempts           int64
		initialInterval       int64
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new event subscription",
		Long: "Create a new webhook event subscription. The signing secret is generated\n" +
			"server-side and printed ONCE — store it securely. The subscription starts enabled.",
		Example: `  olivares eventing subscriptions create --tenant t_abc123 --name siem \
    --endpoint https://siem.example/hooks/olivares --event-types audit.created,finding.created`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			endpoint = strings.TrimSpace(endpoint)
			if endpoint == "" {
				return fmt.Errorf("--endpoint is required")
			}
			if len(eventTypes) == 0 {
				return fmt.Errorf("--event-types is required (comma-separated list)")
			}
			if role == "" {
				role = "viewer"
			}
			if authType == "" {
				authType = "none"
			}

			// Build the sealer to seal the signing secret at rest.
			sealerDir, err := resolveDataDir(dataDir)
			if err != nil {
				return err
			}
			sealer, err := newEventingSealer(sealerDir, os.Getenv)
			if err != nil {
				return fmt.Errorf("secret sealer: %w", err)
			}

			// Generate the signing secret.
			secret, err := cliEventingNewSecret()
			if err != nil {
				return err
			}
			sealed, err := sealer.Seal(cmd.Context(), t, []byte(secret))
			if err != nil {
				return fmt.Errorf("seal secret: %w", err)
			}

			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			// The SAME destination rules the API applies. This path used to write the
			// endpoint column raw — no scheme check, no address guard, no operator
			// policy — while the --endpoint help text below promised "https required"
			// and nothing enforced it. An http:// endpoint written here was POSTed in
			// cleartext, and any authoring-time policy on the API was void for as long
			// as this second write path existed.
			//
			// It runs AFTER the store opens because since unit G the answer depends
			// on the deployment's durable disposition, which lives there: on a fresh
			// install with no policy authored, this create must be refused, and a
			// file-only check would have permitted it and then produced a subscription
			// that never delivers. NO subscription reference is passed — a create can
			// never inherit a compatibility exception.
			checker, err := cliEventingEndpointChecker(eng.store, eventing.EgressCreate, "")
			if err != nil {
				return err
			}
			if _, err := checker.Check(cmd.Context(), t, endpoint); err != nil {
				return err
			}

			// Unit H: the generation this writer OBSERVES, read before the write and carried
			// into the proof. An unreadable fence is an error here, not generation zero: "this CLI
			// could not establish what the fence requires" must not become "it requires nothing".
			fence, ok := newEventingWriterFence(eng.store)
			if !ok {
				return fmt.Errorf("this store does not expose durable rollout state, so the egress writer fence cannot be established")
			}
			fenceGeneration, err := eventing.FenceGeneration(cmd.Context(), fence)
			if err != nil {
				return err
			}

			var createdID string
			err = eng.store.Mutate(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtSubscriptionKind)
				if err != nil {
					return err
				}
				rec := model.Record{
					evtColSubName:         name,
					evtColSubEnabled:      true,
					evtColSubTypes:        strings.Join(eventTypes, ","),
					evtColSubSources:      "",
					evtColSubEndpoint:     endpoint,
					evtColSubSecret:       sealed,
					evtColSubSecretHint:   cliSecretHint(secret),
					evtColSubRole:         role,
					evtColSubDescription:  description,
					"owner_actor":         "cli:eventing",
					"owner_actor_kind":    "user",
					evtColSubAuthType:     authType,
					evtColSubAuthHdrName:  authHdrName,
					evtColSubMaxAttempts:  maxAttempts,
					evtColSubInitInterval: initialInterval,
				}
				// Unit H: the CLI is a SECOND writer against the same database, so it proves
				// its capability through the same helper the API uses. Not a copy of it — a copy of
				// a destination rule has been the wrong one twice in this campaign.
				if err := eventing.StampWriterProof(cmd.Context(), sc, rec, fenceGeneration); err != nil {
					return err
				}
				created, err := repo.Create(cmd.Context(), rec)
				if err != nil {
					return err
				}
				createdID = created.String(model.ColID)
				return nil
			})
			if err != nil {
				return err
			}

			return renderOut(cmd, func(out io.Writer) error {
				if _, werr := fmt.Fprintf(out, "created subscription %q (id %s)\n", name, createdID); werr != nil {
					return werr
				}
				if _, werr := fmt.Fprintf(out, "\nSigning secret (shown ONCE — store it now):\n  %s\n", secret); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(out, "\n→ reload a running engine to activate: POST /v1/console/runtime/reload, or `kill -HUP <pid>`")
				return werr
			}, eventingSubscriptionCreated{
				ID: createdID, Name: name, Secret: secret, ReloadRequired: true,
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&name, "name", "", "subscription name")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "webhook endpoint URL (https required)")
	cmd.Flags().StringSliceVar(&eventTypes, "event-types", nil, "event types to subscribe to (comma-separated)")
	cmd.Flags().StringVar(&role, "role", "viewer", "authorization role for the per-event RBAC filter (viewer|editor|admin|owner)")
	cmd.Flags().StringVar(&authType, "auth-type", "none", "additional auth header type: none|bearer|basic|header")
	_ = cmd.RegisterFlagCompletionFunc("role", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"viewer", "editor", "admin", "owner"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("auth-type", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"none", "bearer", "basic", "header"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&authHdrName, "auth-header-name", "", "custom header name (required when --auth-type=header)")
	cmd.Flags().Int64Var(&maxAttempts, "max-attempts", 0, "max delivery attempts (0 = module default)")
	cmd.Flags().Int64Var(&initialInterval, "initial-interval", 0, "initial retry interval in seconds (0 = module default)")
	cmd.Flags().StringVar(&description, "description", "", "optional description")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("endpoint")
	_ = cmd.MarkFlagRequired("event-types")
	return cmd
}

// eventingSubUpdateCmd — edit one subscription in place, WITHOUT reissuing its secret.
//
// WHY IT EXISTS, and the gap it closes is not "a missing verb" but a silent credential
// rotation. Until now the CLI could create and delete a subscription and nothing else, so
// changing an endpoint — the single most ordinary edit there is, a receiver moving host —
// meant `rm` followed by `create`. `create` GENERATES A NEW SIGNING SECRET and prints it
// once. Every receiver still verifying deliveries with the old secret starts rejecting
// them, and nothing in either command says that is about to happen: the operator asked to
// move a URL and got a key rotation they did not request and were not warned about.
//
// The HTTP API has had PUT since long before this: modules/eventing/subscription.go:436,
// whose own comment states the contract this verb honors — "an empty cred on update means
// keep the stored one". So the engine was already able to do the right thing and only the
// CLI could not ask for it. That is the C08 shape exactly.
//
// PARTIAL BY DESIGN, and this is where it deliberately differs from the API. The handler is
// a PUT: it writes every field from the request, so a caller who omits one clears it. A
// human at a terminal fixing one URL must not have to re-supply the retry policy, the event
// type list and the auth header to avoid losing them. Only flags the operator actually
// TYPED are applied (cobra's Flags().Changed), so an unmentioned field keeps its stored
// value rather than its zero value.
//
// THE SECRET COLUMNS ARE NEVER WRITTEN HERE — not set to "", not re-sealed, not touched. The
// record is read, the named fields are replaced and the record is written back, so the
// sealed secret and its hint survive by never being addressed. There is no --secret flag
// either: rotating a credential is a different operation with different consequences, and
// giving it a home inside "update" is how it ends up happening by accident again.
func eventingSubUpdateCmd() *cobra.Command {
	var (
		dataDir, engine, dsn  string
		tenant, id            string
		name, endpoint        string
		role, description     string
		eventTypes            []string
		authType, authHdrName string
		maxAttempts           int64
		initialInterval       int64
		enabled               bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Edit one event subscription in place (never reissues the secret)",
		Long: "update edits one durable webhook event subscription by tenant and id. Only the\n" +
			"flags you pass are changed; everything else keeps its stored value.\n\n" +
			"It NEVER reissues the signing secret. Before this verb existed, changing an\n" +
			"endpoint meant rm + create, and create generates a new secret — so moving a\n" +
			"receiver silently broke every signature it was still verifying. To rotate a\n" +
			"secret on purpose, that is a separate operation, not a side effect of this one.",
		Example: "  olivares eventing subscriptions update --tenant t_abc123 --id sub-123 --endpoint https://new.example/hook\n" +
			"  olivares eventing subscriptions update --tenant t_abc123 --id sub-123 --enabled=false",
		Args:    cobra.NoArgs,
		Aliases: []string{"set", "edit"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			mid := model.ID(strings.TrimSpace(id))
			if mid.IsZero() {
				return fmt.Errorf("--id is required")
			}
			f := cmd.Flags()
			// An update that names no field is not a no-op to be waved through: it is
			// almost always a mistyped flag name, and reporting success for it would
			// teach the operator that the command ran when nothing did.
			touched := false
			for _, n := range []string{"name", "endpoint", "role", "description", "event-types",
				"auth-type", "auth-header-name", "max-attempts", "initial-interval", "enabled"} {
				if f.Changed(n) {
					touched = true
					break
				}
			}
			if !touched {
				return fmt.Errorf("update changes nothing: pass at least one of --name, --endpoint, --role, --description, --event-types, --auth-type, --auth-header-name, --max-attempts, --initial-interval or --enabled")
			}

			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			// The SAME destination rules the API applies, and only when the destination
			// actually changes: re-checking an untouched endpoint would make a subscription
			// whose host predates today's policy impossible to disable, which is the exact
			// trap the API's own comment at subscription.go:452 warns about.
			//
			// The subscription id IS passed here, unlike in create. An update NAMES the
			// subscription, so one whose destination this deployment already had stays
			// editable under compatibility mode; a create can never inherit that exception
			// because there is nothing yet to be compatible with.
			if f.Changed("endpoint") {
				checker, cerr := cliEventingEndpointChecker(eng.store, eventing.EgressUpdate, mid)
				if cerr != nil {
					return cerr
				}
				if _, cerr := checker.Check(cmd.Context(), t, endpoint); cerr != nil {
					return cerr
				}
			} else if f.Changed("enabled") && enabled {
				// ⛔ REACTIVAR ES HACER EFECTIVO UN DESTINO, y la regla de arriba no lo ve.
				//
				// El comentario de arriba es correcto para lo que defiende —desactivar una
				// suscripcion cuyo host es anterior a la politica de hoy no puede quedar
				// prohibido— pero esa exencion se escribio pensando en «el endpoint no cambia»,
				// y `enabled: false -> true` no cambia el endpoint y SI hace efectivo el destino.
				//
				// Medido por el contraste Codex `sol max` el 2026-08-20 (hallazgo C08-04-3,
				// VERIFICADO, reproduccion de cuatro pasos): autorizado `127.0.0.1`, creada y
				// desactivada la suscripcion, cambiada la politica para autorizar solo
				// `localhost`, `update --endpoint <el mismo>` fue RECHAZADO … y
				// `update --enabled=true` salio 0 y dejo `enabled=true` almacenado.
				//
				// ⚠ Y el alcance, que el contraste acoto y no se debe inflar: NO es una fuga de
				// bytes. El envio vuelve a evaluar el URL de forma autoritaria justo antes de
				// permitirlo y devuelve `dead` ante una denegacion
				// (modules/eventing/dispatch.go:557-590). Lo que esto arregla es una
				// configuracion declarada ACTIVA y un exito de CLI que no pueden entregar nada.
				//
				// La definicion interna ya trataba la reactivacion como esa clase de mutacion:
				// el handler del modulo estampa proof al pasar de desactivado a activado
				// (modules/eventing/subscription.go:521-545) y el trigger SQLite aplica la misma
				// condicion (…/0004_writer_fence_subscription_upd.sql:14-22,52-59). El CLI era el
				// unico de los tres que no la aplicaba.
				//
				// Solo se comprueba en la direccion que ENCIENDE: `--enabled=false` sigue exento,
				// que es justo la trampa que la exencion original existe para cortar.
				var prevEnabled bool
				var prevEndpoint string
				if verr := eng.store.View(cmd.Context(), t, func(sc store.Scope) error {
					repo, rerr := sc.Ext(evtSubscriptionKind)
					if rerr != nil {
						return rerr
					}
					rec, rerr := repo.Get(cmd.Context(), mid)
					if rerr != nil {
						return rerr
					}
					prevEnabled = rec.Bool(evtColSubEnabled)
					prevEndpoint = rec.String(evtColSubEndpoint)
					return nil
				}); verr != nil {
					return verr
				}
				if !prevEnabled {
					checker, cerr := cliEventingEndpointChecker(eng.store, eventing.EgressUpdate, mid)
					if cerr != nil {
						return cerr
					}
					if _, cerr := checker.Check(cmd.Context(), t, prevEndpoint); cerr != nil {
						return cerr
					}
				}
			}

			// Unit H: this CLI is a SECOND writer against the same database, so it
			// establishes the fence generation it observed and stamps the same proof the
			// API does. An unreadable fence is an error, never generation zero.
			fence, ok := newEventingWriterFence(eng.store)
			if !ok {
				return fmt.Errorf("this store does not expose durable rollout state, so the egress writer fence cannot be established")
			}
			fenceGeneration, err := eventing.FenceGeneration(cmd.Context(), fence)
			if err != nil {
				return err
			}

			var changed []string
			err = eng.store.Mutate(cmd.Context(), t, func(sc store.Scope) error {
				repo, rerr := sc.Ext(evtSubscriptionKind)
				if rerr != nil {
					return rerr
				}
				rec, rerr := repo.Get(cmd.Context(), mid)
				if rerr != nil {
					return rerr
				}
				set := func(flag, col string, v any) {
					if f.Changed(flag) {
						rec[col] = v
						changed = append(changed, flag)
					}
				}
				set("name", evtColSubName, name)
				set("endpoint", evtColSubEndpoint, endpoint)
				set("role", evtColSubRole, role)
				set("description", evtColSubDescription, description)
				set("auth-type", evtColSubAuthType, authType)
				set("auth-header-name", evtColSubAuthHdrName, authHdrName)
				set("max-attempts", evtColSubMaxAttempts, maxAttempts)
				set("initial-interval", evtColSubInitInterval, initialInterval)
				set("enabled", evtColSubEnabled, enabled)
				if f.Changed("event-types") {
					rec[evtColSubTypes] = strings.Join(eventTypes, ",")
					changed = append(changed, "event-types")
				}
				// evtColSubSecret and evtColSubSecretHint are ABSENT from this block on
				// purpose. See the header: they survive because nothing here addresses them.
				if perr := eventing.StampWriterProof(cmd.Context(), sc, rec, fenceGeneration); perr != nil {
					return perr
				}
				_, uerr := repo.Update(cmd.Context(), rec)
				return uerr
			})
			if err != nil {
				return err
			}

			return renderOut(cmd, func(out io.Writer) error {
				if _, werr := fmt.Fprintf(out, "updated subscription %q (%s)\n", mid, strings.Join(changed, ", ")); werr != nil {
					return werr
				}
				// ⛔ ESTA LINEA AFIRMABA «the signing secret is unchanged» COMO CONSTANTE, y el
				// comando no lo comprueba: descarta el registro que devuelve la escritura y no lo
				// compara con el anterior. Lo levanto el contraste Codex `sol max` del 2026-08-20
				// (MEDIA, DE UNA PASADA), y es fragil precisamente por el hallazgo hermano: el SQL
				// SI nombra las dos columnas del secreto, aunque reescriba el mismo valor.
				//
				// Se cambia por lo que este verbo SI garantiza por construccion y cualquiera puede
				// comprobar leyendolo: no genera un secreto nuevo — no llama al generador en
				// ningun camino. Es una afirmacion mas debil y es la unica que el codigo sostiene.
				// Un guion solo puede afirmar lo que el mismo mide.
				if _, werr := fmt.Fprintln(out, "this verb never generates a new signing secret"); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(out, "\n\u2192 reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>`")
				return werr
			}, eventingSubscriptionUpdated{
				ID: mid.String(), Changed: changed, SecretUnchanged: true, ReloadRequired: true,
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&id, "id", "", "subscription id")
	cmd.Flags().StringVar(&name, "name", "", "new subscription name")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "new https endpoint (the signing secret is NOT reissued)")
	cmd.Flags().StringVar(&role, "role", "", "new delivery role")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringSliceVar(&eventTypes, "event-types", nil, "replacement event type list")
	cmd.Flags().StringVar(&authType, "auth-type", "", "new auth type")
	cmd.Flags().StringVar(&authHdrName, "auth-header-name", "", "new auth header name")
	cmd.Flags().Int64Var(&maxAttempts, "max-attempts", 0, "new maximum delivery attempts")
	cmd.Flags().Int64Var(&initialInterval, "initial-interval", 0, "new initial retry interval in seconds")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "enable or disable delivery (--enabled=false to pause)")
	return cmd
}

// eventingSubRotateSecretCmd — reissue the signing secret ON PURPOSE, and only that.
//
// IT IS THE OTHER HALF OF `update`, and neither is complete without it. `update` deliberately
// never touches the secret, because an operator moving an endpoint must not rotate a credential
// by accident. That is only defensible if rotating ON PURPOSE has a door of its own — otherwise
// the only way to reissue was `rm` + `create`, which does rotate, and ALSO mints a new
// subscription id, so every dashboard, runbook and script naming the old one breaks with it.
//
// The API has had this endpoint all along: modules/eventing/eventing.go:470,
// POST /subscriptions/{id}/rotate-secret. As with `update`, the engine could and the CLI could
// not ask.
//
// DESTRUCTIVE AND SAID SO. Between this command returning and the receiver being reconfigured,
// every delivery fails signature verification. That is not a side effect to discover — it is
// the whole operation, so it goes behind confirmDestructive like `rm`, and the new secret is
// printed ONCE with the same wording `create` uses.
func eventingSubRotateSecretCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, id string
	var yes bool
	cmd := &cobra.Command{
		Use:   "rotate-secret",
		Short: "Reissue the signing secret for one subscription (breaks delivery until the receiver is updated)",
		Long: "rotate-secret generates a new signing secret for one subscription, seals it and prints\n" +
			"it ONCE. Nothing else about the subscription changes — same id, same endpoint, same\n" +
			"retry policy — so dashboards and runbooks that name it keep working.\n\n" +
			"Until the receiver is reconfigured with the new secret, every delivery will fail its\n" +
			"signature check. Rotate when you mean to; `update` never does it for you.",
		Example: "  olivares eventing subscriptions rotate-secret --tenant t_abc123 --id sub-123",
		Args:    cobra.NoArgs,
		Aliases: []string{"rotate"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			mid := model.ID(strings.TrimSpace(id))
			if mid.IsZero() {
				return fmt.Errorf("--id is required")
			}
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"reissue the signing secret of subscription %q for tenant %s "+
					"(every delivery fails verification until the receiver is updated)",
				mid, t)); err != nil {
				return err
			}

			sealerDir, err := resolveDataDir(dataDir)
			if err != nil {
				return err
			}
			sealer, err := newEventingSealer(sealerDir, os.Getenv)
			if err != nil {
				return fmt.Errorf("secret sealer: %w", err)
			}
			secret, err := cliEventingNewSecret()
			if err != nil {
				return err
			}
			sealed, err := sealer.Seal(cmd.Context(), t, []byte(secret))
			if err != nil {
				return fmt.Errorf("seal secret: %w", err)
			}

			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			// No endpoint check here, deliberately: the destination does not change, and
			// re-asking would make a subscription whose host predates today's policy
			// impossible to re-key — locking an operator out of the one action that
			// recovers from a leaked secret.
			fence, ok := newEventingWriterFence(eng.store)
			if !ok {
				return fmt.Errorf("this store does not expose durable rollout state, so the egress writer fence cannot be established")
			}
			fenceGeneration, err := eventing.FenceGeneration(cmd.Context(), fence)
			if err != nil {
				return err
			}

			err = eng.store.Mutate(cmd.Context(), t, func(sc store.Scope) error {
				repo, rerr := sc.Ext(evtSubscriptionKind)
				if rerr != nil {
					return rerr
				}
				rec, rerr := repo.Get(cmd.Context(), mid)
				if rerr != nil {
					return rerr
				}
				// EXACTLY two columns, and the hint travels WITH the secret: a hint left
				// behind would name a credential that no longer exists, and `get` prints
				// the hint — so the operator would be told the old one is still current.
				rec[evtColSubSecret] = sealed
				rec[evtColSubSecretHint] = cliSecretHint(secret)
				if perr := eventing.StampWriterProof(cmd.Context(), sc, rec, fenceGeneration); perr != nil {
					return perr
				}
				_, uerr := repo.Update(cmd.Context(), rec)
				return uerr
			})
			if err != nil {
				return err
			}

			return renderOut(cmd, func(out io.Writer) error {
				if _, werr := fmt.Fprintf(out, "rotated the signing secret of subscription %q\n", mid); werr != nil {
					return werr
				}
				if _, werr := fmt.Fprintf(out, "\nNew signing secret (shown ONCE — store it now):\n  %s\n", secret); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(out, "\n\u2192 update the receiver, then reload a running engine: POST /v1/console/runtime/reload, or `kill -HUP <pid>`")
				return werr
			}, eventingSubscriptionSecretRotated{
				ID: mid.String(), Secret: secret, ReloadRequired: true,
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&id, "id", "", "subscription id")
	addYesFlag(cmd, &yes)
	return cmd
}

func eventingSubRemoveCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, id string
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm",
		Short:   "Delete an event subscription",
		Long:    "rm permanently deletes one durable event subscription identified by tenant and subscription ID.",
		Example: "  olivares eventing subscriptions rm --tenant t_abc123 --id sub-123",
		Args:    cobra.NoArgs,
		Aliases: []string{"remove", "delete"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			mid := model.ID(strings.TrimSpace(id))
			if mid.IsZero() {
				return fmt.Errorf("--id is required")
			}
			// A subscription is a tenant's standing delivery contract; deleting it
			// stops future deliveries silently from the consumer's side.
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete event subscription %q for tenant %s (it stops receiving events)",
				mid, t)); err != nil {
				return err
			}
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			err = eng.store.Mutate(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtSubscriptionKind)
				if err != nil {
					return err
				}
				return repo.Delete(cmd.Context(), mid)
			})
			if err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out, "deleted subscription %q\n", id)
				return werr
			}, eventingSubscriptionRemoved{ID: mid.String(), Deleted: true})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&id, "id", "", "subscription id")
	addYesFlag(cmd, &yes)
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func eventingSubTestCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, id string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Send a test delivery to a subscription's endpoint",
		Long: "Sends a synthetic test event to the subscription's endpoint, signed with its\n" +
			"real signing secret. The test event is not captured in the event log and creates\n" +
			"no delivery row. Use this to verify the receiver is wired correctly.",
		Example: "  olivares eventing subscriptions test --tenant t_abc123 --id sub-123",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			mid := model.ID(strings.TrimSpace(id))
			if mid.IsZero() {
				return fmt.Errorf("--id is required")
			}
			// Build the sealer to unseal the subscription's signing secret.
			sealerDir, err := resolveDataDir(dataDir)
			if err != nil {
				return err
			}
			sealer, err := newEventingSealer(sealerDir, os.Getenv)
			if err != nil {
				return fmt.Errorf("secret sealer: %w", err)
			}
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()

			var endpointURL, sealedSecret string
			err = eng.store.View(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtSubscriptionKind)
				if err != nil {
					return err
				}
				rec, err := repo.Get(cmd.Context(), mid)
				if err != nil {
					return err
				}
				endpointURL = rec.String(evtColSubEndpoint)
				sealedSecret = rec.String(evtColSubSecret)
				return nil
			})
			if err != nil {
				return err
			}

			secret, err := sealer.Open(cmd.Context(), t, sealedSecret)
			if err != nil {
				return fmt.Errorf("unseal secret: %w", err)
			}

			// The SAME destination rules the API applies, on the STORED endpoint. This
			// command sends a real, HMAC-signed request from the control-plane host, so
			// it is an egress path like any other — and it was the one path in this unit
			// that had none of the protections: no destination policy, no address guard,
			// and a client that followed redirects, so an https endpoint could bounce it
			// to a link-local address with nothing to refuse.
			// AS the subscription, not as a candidate: under compatibility mode a stored
			// destination this deployment already had is permitted, and a test that asked
			// the question without the subscription's identity would report a refusal for a
			// destination whose real deliveries succeed.
			checker, cerr := cliEventingEndpointChecker(eng.store, eventing.EgressTest, mid)
			if cerr != nil {
				return cerr
			}
			decision, cerr := checker.Check(cmd.Context(), t, endpointURL)
			if cerr != nil {
				return cerr
			}

			// Build and send the test delivery.
			eventID := model.NewID().String()
			payload, _ := json.Marshal(map[string]string{
				"message":         "Olivares eventing test delivery",
				"subscription_id": mid.String(),
			})
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			sig := "t=" + ts + ",v1=" + webhook.Sign(string(secret), ts, payload)

			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, endpointURL, bytes.NewReader(payload))
			if err != nil {
				return fmt.Errorf("build request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "olivares-eventing/1")
			req.Header.Set("X-Olivares-Timestamp", ts)
			req.Header.Set("X-Olivares-Signature", sig)
			req.Header.Set("X-Olivares-Event", eventID)
			req.Header.Set("X-Olivares-Event-Type", "eventing.test")
			req.Header.Set("X-Olivares-Delivery", model.NewID().String())

			// The addresses the authorization covered travel WITH the request, so the
			// dialer connects to the machine that was authorized rather than resolving
			// the name a second time. Discarding them — which the first version did,
			// because Check returned only an error — reopened the rebinding gap the pin
			// exists to close.
			ctx := egress.WithPin(req.Context(), decision.Pin)
			ctx = egress.WithReservedAuthorization(ctx, decision.ReservedAuthorized)
			req = req.WithContext(ctx)
			resp, err := cliGuardedClient().Do(req)
			if err != nil {
				// A transport failure is reported on STDOUT with exit 0, and that is this
				// command's pre-existing contract, not a decision taken here: the verb
				// answers "did the endpoint respond", so "it did not" is its answer rather
				// than its failure. The JSON pane says the same thing in the same place —
				// ok=false, http_status=0 and the reason — because moving it to stderr or
				// to a non-zero code would change what a script already reads.
				return renderOut(cmd, func(out io.Writer) error {
					_, werr := fmt.Fprintf(out, "test FAILED: %v\n", err)
					return werr
				}, eventingSubscriptionTestResult{OK: false, HTTPStatus: 0, Error: err.Error()})
			}
			defer func() {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
			}()

			result := eventingSubscriptionTestResult{
				OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
				HTTPStatus: resp.StatusCode,
			}
			return renderOut(cmd, func(out io.Writer) error {
				if result.OK {
					_, werr := fmt.Fprintf(out, "test OK: HTTP %d\n", resp.StatusCode)
					return werr
				}
				_, werr := fmt.Fprintf(out, "test FAILED: HTTP %d\n", resp.StatusCode)
				return werr
			}, result)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&id, "id", "", "subscription id to test")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// ---------------------------------------------------------------------------
// olivares eventing deliveries
// ---------------------------------------------------------------------------

func newEventingDeliveriesCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "deliveries",
		Short: "Inspect delivery state (ls)",
		Long: "A delivery is one attempt to hand one event to one subscription's endpoint.\n" +
			"Listing them, filtered by subscription or status, is how a tenant answers\n" +
			"\"did that event actually arrive, and if not, where did it stop\".",
		Example: "  olivares eventing deliveries ls --tenant t_abc123 --status dead",
	}
	root.AddCommand(eventingDeliveriesListCmd())
	return root
}

func eventingDeliveriesListCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, subscription, status string
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List deliveries (optionally filtered by --subscription, --status)",
		Long:    "ls lists durable webhook delivery records for one tenant, optionally filtering by subscription or delivery status.",
		Example: "  olivares eventing deliveries ls --tenant t_abc123 --status dead",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			return eng.store.View(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtDeliveryKind)
				if err != nil {
					return err
				}
				q := model.Query{Limit: eventingListCap}
				if subscription != "" {
					q.Filters = append(q.Filters, model.Filter{Column: evtColDelSubRef, Op: model.OpEq, Value: subscription})
				}
				if status != "" {
					q.Filters = append(q.Filters, model.Filter{Column: evtColDelStatus, Op: model.OpEq, Value: status})
				}
				recs, page, err := repo.List(cmd.Context(), q)
				if err != nil {
					return err
				}
				warnEventingTruncated(cmd, len(recs), page, "deliveries")
				items := eventingDeliveryListItems(recs)
				return renderOut(cmd, func(out io.Writer) error {
					if len(items) == 0 {
						_, err := fmt.Fprintln(out, "no deliveries")
						return err
					}
					tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "ID\tSUBSCRIPTION\tEVENT_TYPE\tSEQ\tSTATUS\tATTEMPTS\tLAST_STATUS\tLAST_ATTEMPT")
					for _, item := range items {
						fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%d\t%s\t%s\n",
							item.ID, item.Subscription, item.EventType, item.EventSeq, item.Status,
							item.Attempts, item.LastStatus, item.LastAttempt)
					}
					return tw.Flush()
				}, items)
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&subscription, "subscription", "", "filter by subscription id")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (queued|delivering|delivered|dead|denied)")
	_ = cmd.RegisterFlagCompletionFunc("status", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"queued", "delivering", "delivered", "dead", "denied"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// ---------------------------------------------------------------------------
// olivares eventing dead-letters
// ---------------------------------------------------------------------------

func newEventingDeadLettersCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "dead-letters",
		Short: "Inspect and redeliver dead-lettered deliveries",
		Long: "A delivery that exhausted its retries is parked as dead rather than dropped, so\n" +
			"an endpoint outage costs a redelivery and not the event itself. ls shows what\n" +
			"is parked; redeliver puts one back on the queue once the endpoint is healthy.",
		Example: "  olivares eventing dead-letters ls --tenant t_abc123 --subscription sub-123\n" +
			"  olivares eventing dead-letters redeliver --tenant t_abc123 --id delivery-123",
	}
	root.AddCommand(eventingDeadLettersListCmd(), eventingDeadLettersRedeliverCmd())
	return root
}

func eventingDeadLettersListCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, subscription string
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List dead-lettered deliveries (status=dead)",
		Long:    "ls lists terminal dead-letter deliveries for one tenant, optionally restricted to one subscription.",
		Example: "  olivares eventing dead-letters ls --tenant t_abc123 --subscription sub-123",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			return eng.store.View(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtDeliveryKind)
				if err != nil {
					return err
				}
				q := model.Query{
					Filters: []model.Filter{{Column: evtColDelStatus, Op: model.OpEq, Value: "dead"}},
					Limit:   eventingListCap,
				}
				if subscription != "" {
					q.Filters = append(q.Filters, model.Filter{Column: evtColDelSubRef, Op: model.OpEq, Value: subscription})
				}
				recs, page, err := repo.List(cmd.Context(), q)
				if err != nil {
					return err
				}
				warnEventingTruncated(cmd, len(recs), page, "dead-lettered deliveries")
				items := eventingDeliveryListItems(recs)
				return renderOut(cmd, func(out io.Writer) error {
					if len(items) == 0 {
						_, err := fmt.Fprintln(out, "no dead-lettered deliveries")
						return err
					}
					tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "ID\tSUBSCRIPTION\tEVENT_TYPE\tSEQ\tATTEMPTS\tLAST_STATUS\tEVENT_ID")
					for _, item := range items {
						fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
							item.ID, item.Subscription, item.EventType, item.EventSeq, item.Attempts,
							item.LastStatus, item.EventID)
					}
					return tw.Flush()
				}, items)
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&subscription, "subscription", "", "filter by subscription id")
	return cmd
}

func eventingDeadLettersRedeliverCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, id string
	cmd := &cobra.Command{
		Use:   "redeliver",
		Short: "Requeue a dead-lettered delivery for retry",
		Long: "Requeues a delivery that reached a terminal state (dead, delivered or denied)\n" +
			"so the retry ladder restarts from attempt 0. The event's idempotency key is stable.",
		Example: "  olivares eventing dead-letters redeliver --tenant t_abc123 --id delivery-123",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			mid := model.ID(strings.TrimSpace(id))
			if mid.IsZero() {
				return fmt.Errorf("--id is required")
			}
			eng, err := auditBoot(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			// Read back from the row the store RETURNED, so the JSON pane reports what
			// was written rather than restating the constants above it. A mutation that
			// requeued to some other status, or left the attempt count standing, would
			// otherwise be described as "queued, 0 attempts" by a document that never
			// looked.
			var requeued eventingDeliveryRequeued
			err = eng.store.Mutate(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtDeliveryKind)
				if err != nil {
					return err
				}
				rec, err := repo.Get(cmd.Context(), mid)
				if err != nil {
					return err
				}
				st := rec.String(evtColDelStatus)
				switch st {
				case "delivered", "dead", "denied":
				default:
					return fmt.Errorf("only a finished delivery (delivered, dead or denied) can be redelivered; current status is %q", st)
				}
				rec[evtColDelStatus] = "queued"
				rec[evtColDelAttempts] = int64(0)
				rec[evtColDelNextAt] = time.Now().UTC().Format(time.RFC3339)
				rec[evtColDelLastStatus] = "requeued"
				saved, err := repo.Update(cmd.Context(), rec)
				if err != nil {
					return err
				}
				requeued = eventingDeliveryRequeued{
					ID:       saved.String(model.ColID),
					Status:   saved.String(evtColDelStatus),
					Attempts: saved.Int(evtColDelAttempts),
				}
				return nil
			})
			if err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if _, werr := fmt.Fprintf(out, "requeued delivery %q (status → queued, attempts → 0)\n", id); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(out, "→ reload a running engine to dispatch immediately, or wait for the next pump tick")
				return werr
			}, requeued)
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().StringVar(&id, "id", "", "delivery id to redeliver")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// ---------------------------------------------------------------------------
// olivares eventing events
// ---------------------------------------------------------------------------

func newEventingEventsCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "events",
		Short: "Inspect the captured event log",
		Long: "The event log is what the platform captured, independently of whether any\n" +
			"subscription was interested at the time. Reading it from a sequence cursor is\n" +
			"how a consumer catches up after downtime without replaying everything.",
		Example: "  olivares eventing events ls --tenant t_abc123 --since-seq 100 --type finding.created",
	}
	root.AddCommand(eventingEventsListCmd())
	return root
}

func eventingEventsListCmd() *cobra.Command {
	var dataDir, engine, dsn, tenant, eventType string
	var sinceSeq int64
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List captured events (optionally from a seq cursor, filtered by --type)",
		Long:    "ls lists up to 100 captured durable events for one tenant in sequence order, with optional cursor and event-type filters.",
		Example: "  olivares eventing events ls --tenant t_abc123 --since-seq 100 --type finding.created",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedTenant, err := resolveTenant(tenant)
			if err != nil {
				return err
			}
			t, err := model.ParseTenantID(resolvedTenant)
			if err != nil {
				return fmt.Errorf("--tenant: %w", err)
			}
			eng, err := auditBootRO(cmd, dataDir, engine, dsn)
			if err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			return eng.store.View(cmd.Context(), t, func(sc store.Scope) error {
				repo, err := sc.Ext(evtEventKind)
				if err != nil {
					return err
				}
				// ⛔ ESTE 100 SE QUEDA, y es una decision, no un descuido. Los otros tres listados
				//    no pedian techo NINGUNO, asi que el almacen servia su pagina por omision: ahi
				//    subirlo al maximo corrige una omision. Aqui alguien ESCOGIO cien eventos, y
				//    cambiar un numero escogido es una decision de producto, no un arreglo. Lo que
				//    era falso no era el cien: era no decir que habia mas. Eso es lo que se arregla.
				q := model.Query{
					Sort:  []model.Sort{{Column: evtColEvSeq}},
					Limit: 100,
				}
				if sinceSeq > 0 {
					q.Filters = append(q.Filters, model.Filter{Column: evtColEvSeq, Op: model.OpGte, Value: sinceSeq})
				}
				if eventType != "" {
					q.Filters = append(q.Filters, model.Filter{Column: evtColEvType, Op: model.OpEq, Value: eventType})
				}
				recs, page, err := repo.List(cmd.Context(), q)
				if err != nil {
					return err
				}
				warnEventingTruncated(cmd, len(recs), page, "events")
				items := make([]eventingEventListItem, 0, len(recs))
				for _, r := range recs {
					items = append(items, eventingEventListItem{
						Seq:        r.Int(evtColEvSeq),
						Type:       r.String(evtColEvType),
						Source:     r.String(evtColEvSource),
						EventID:    r.String(evtColEvEventID),
						OccurredAt: r.String(evtColEvOccurredAt),
					})
				}
				return renderOut(cmd, func(out io.Writer) error {
					if len(items) == 0 {
						_, err := fmt.Fprintln(out, "no events")
						return err
					}
					tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "SEQ\tTYPE\tSOURCE\tEVENT_ID\tOCCURRED_AT")
					for _, item := range items {
						fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
							item.Seq, item.Type, item.Source, ellipsis(item.EventID, 36), item.OccurredAt)
					}
					return tw.Flush()
				}, items)
			})
		},
	}
	addStoreFlags(cmd, &dataDir, &engine, &dsn)
	cmd.Flags().StringVar(&tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT)")
	cmd.Flags().Int64Var(&sinceSeq, "since-seq", 0, "list events with seq >= this value")
	cmd.Flags().StringVar(&eventType, "type", "", "filter by event type")
	return cmd
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type eventingSubscriptionListItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Endpoint   string `json:"endpoint"`
	Role       string `json:"role"`
	EventTypes string `json:"event_types"`
	AuthType   string `json:"auth_type"`
	CreatedAt  string `json:"created_at"`
}

// eventingSubscriptionDetail is what `get` publishes. It carries the retry and auth
// fields `ls` has no room for, and deliberately carries HINTS and never the secret:
// evtColSubSecret exists in the record and is not read here.
type eventingSubscriptionDetail struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Enabled      bool   `json:"enabled"`
	Endpoint     string `json:"endpoint"`
	Role         string `json:"role"`
	EventTypes   string `json:"event_types"`
	Sources      string `json:"sources"`
	AuthType     string `json:"auth_type"`
	AuthHeader   string `json:"auth_header"`
	AuthHint     string `json:"auth_hint"`
	SecretHint   string `json:"secret_hint"`
	MaxAttempts  string `json:"max_attempts"`
	InitInterval string `json:"initial_interval"`
	CreatedAt    string `json:"created_at"`
}

type eventingDeliveryListItem struct {
	ID           string `json:"id"`
	Subscription string `json:"subscription"`
	EventType    string `json:"event_type"`
	EventSeq     int64  `json:"event_seq"`
	Status       string `json:"status"`
	Attempts     int64  `json:"attempts"`
	LastStatus   string `json:"last_status"`
	LastAttempt  string `json:"last_attempt"`
	EventID      string `json:"event_id"`
}

func eventingDeliveryListItems(recs []model.Record) []eventingDeliveryListItem {
	items := make([]eventingDeliveryListItem, 0, len(recs))
	for _, r := range recs {
		items = append(items, eventingDeliveryListItem{
			ID:           r.String(model.ColID),
			Subscription: r.String(evtColDelSubRef),
			EventType:    r.String(evtColDelEventType),
			EventSeq:     r.Int(evtColDelEventSeq),
			Status:       r.String(evtColDelStatus),
			Attempts:     r.Int(evtColDelAttempts),
			LastStatus:   r.String(evtColDelLastStatus),
			LastAttempt:  r.String(evtColDelLastAt),
			EventID:      r.String(evtColDelEventID),
		})
	}
	return items
}

type eventingEventListItem struct {
	Seq        int64  `json:"seq"`
	Type       string `json:"type"`
	Source     string `json:"source"`
	EventID    string `json:"event_id"`
	OccurredAt string `json:"occurred_at"`
}

// eventingSubscriptionCreated is what `subscriptions create` reports. `id` and
// `name` are eventingSubscriptionListItem's keys for the same two facts.
//
// THE SIGNING SECRET IS IN IT, and that is a decision rather than an oversight, so
// here is the reasoning and the alternative that was rejected.
//
// The secret exists for exactly this one call — it is generated here, sealed at
// rest, and never recoverable — and the TEXT pane already prints it, in the clear,
// on STDOUT. The JSON pane therefore adds no disclosure whatsoever: same process,
// same stream, same terminal, same redirect. What it removes is the need to scrape
// a secret out of prose. The alternative considered was to emit only the
// non-secret `secret_hint` and leave the value to the text pane, and it is worse
// in the one way that matters: it makes `-o json` USELESS for the only
// irreplaceable output this command has, so an operator automating a create is
// pushed back to grepping a human sentence for a credential — which is both more
// fragile and more likely to land the surrounding warning text in a log than a
// `jq -r .secret` ever is.
//
// The tree already took this decision for this exact class and recorded it:
// cmd_observeplane.go:426 notes that `tokens issue` keeps its "shown once" WARNING
// on stderr precisely so a `$(...)` capture "gets the secret alone". The rule is
// that the secret goes to stdout and the prose does not compete with it. That is
// what this struct does: under -o json the value is one named key and the
// "store it now" sentence stays in the text pane, where a human reads it.
//
// `secret_hint` is deliberately NOT here. It is not in the text pane, and it is
// already available from `subscriptions ls`; inventing a field one pane has and
// the other does not is how the two start to disagree.
type eventingSubscriptionCreated struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Secret         string `json:"secret"`
	ReloadRequired bool   `json:"reload_required"`
}

// eventingSubscriptionRemoved is what `subscriptions rm` reports.
//
// `id` carries the PARSED id — the one actually deleted — while the text pane
// echoes the operator's argument verbatim, as it always has. They differ only if
// the argument arrived with surrounding whitespace, and in that case the parsed
// value is the correct one for a machine to key on.
// eventingSubscriptionUpdated carries SecretUnchanged as a field rather than leaving it
// implied. A script that moves an endpoint needs to be able to ASSERT that no rotation
// happened, and "the absence of a secret in the output" is not an assertion — it is what a
// broken command would also produce.
type eventingSubscriptionUpdated struct {
	ID              string   `json:"id"`
	Changed         []string `json:"changed"`
	SecretUnchanged bool     `json:"secret_unchanged"`
	ReloadRequired  bool     `json:"reload_required"`
}

// eventingSubscriptionSecretRotated carries the new secret because this is the ONE moment it
// exists in the clear; the store keeps only a hint, and there is no second chance to read it.
type eventingSubscriptionSecretRotated struct {
	ID             string `json:"id"`
	Secret         string `json:"secret"`
	ReloadRequired bool   `json:"reload_required"`
}

type eventingSubscriptionRemoved struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// eventingSubscriptionTestResult is what `subscriptions test` reports.
//
// `ok` + `http_status` is the tree's existing vocabulary for "a request was made
// and this is how it went" (cmd_datalane.go:376). All three desenlaces emit all
// three keys: a transport failure is ok=false with http_status=0 and the reason in
// `error`, a 2xx is ok=true with `error` empty, and a non-2xx is ok=false with the
// code and `error` empty. A consumer never has to test for a key's existence
// before reading it.
type eventingSubscriptionTestResult struct {
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"http_status"`
	Error      string `json:"error"`
}

// eventingDeliveryRequeued is what `dead-letters redeliver` reports. Its three
// keys are eventingDeliveryListItem's keys for the same three facts, so the
// receipt of a requeue and a row of `deliveries ls` are read with one parser.
type eventingDeliveryRequeued struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Attempts int64  `json:"attempts"`
}

// addTextJSONFormatFlag retains --format as a deprecated persistent alias on
// command groups that historically exposed it.
func addTextJSONFormatFlag(cmd *cobra.Command) {
	addDeprecatedFormatFlag(cmd, true)
}

// cliEventingNewSecret generates a signing secret with the same format the
// eventing module uses (olvw_ prefix + 48 hex chars).
func cliEventingNewSecret() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return "olvw_" + hex.EncodeToString(b[:]), nil
}

// cliSecretHint is the non-secret display fingerprint (first 12 hex chars of
// the SHA-256), matching the eventing module's secretHint.
func cliSecretHint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:12]
}

// ellipsis shortens a string for tabular display.
func ellipsis(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit-1] + "…"
}
