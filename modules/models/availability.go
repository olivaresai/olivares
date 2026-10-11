// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	availabilityKind            model.Kind = "models.availability"
	availabilityRefreshInterval            = time.Hour
	availabilityPollInterval               = time.Minute
	availabilityFailure                    = "Models could not be refreshed. Check the provider connection or sign in again."
)

// ErrModelDiscoveryUnsupported means the tool has no read-only model-list surface.
var ErrModelDiscoveryUnsupported = errors.New("model discovery is unsupported")

// AvailabilitySource identifies one configured provider or one tool account home.
// Revision is an opaque, non-secret digest of configuration/login metadata. It
// changes on rotation or sign-in, never contains a credential, and is not public.
type AvailabilitySource struct {
	Ref          string `json:"source_ref"`
	ProviderRef  string `json:"provider_ref,omitempty"`
	ProviderKind string `json:"provider_kind"`
	AccountRef   string `json:"account_ref,omitempty"`
	Driver       string `json:"driver,omitempty"`
	Revision     string `json:"-"`
}

// ModelDiscovery lists configured sources and reads models without inference,
// prompts or remote mutations. Discover must honor cancellation and must not
// publish a result after its source configuration changed. Errors are never
// logged or returned to a browser: provider errors can contain credential echoes.
type ModelDiscovery interface {
	Sources(context.Context, model.TenantID) ([]AvailabilitySource, error)
	Discover(context.Context, model.TenantID, AvailabilitySource) ([]string, error)
}

type AvailableModel struct {
	ID     string `json:"id"`
	SeenAt string `json:"seen_at"`
}

// Availability describes one source. Only fresh models are offered by a picker.
// Failed results retain the last successful list as historical evidence.
type Availability struct {
	AvailabilitySource
	State     string           `json:"state"`
	Models    []AvailableModel `json:"models"`
	SeenAt    string           `json:"seen_at,omitempty"`
	CheckedAt string           `json:"checked_at,omitempty"`
	Message   string           `json:"message,omitempty"`
	Version   int64            `json:"-"`
}

type availabilityObservation struct {
	revision      string
	checked, seen time.Time
}

type availabilityCatalog struct {
	mu           sync.Mutex
	refresh      sync.Mutex // only discovery calls serialize; reads never wait for a vendor
	source       ModelDiscovery
	tenants      func(context.Context) ([]model.TenantID, error)
	now          func() time.Time
	observations map[string]availabilityObservation
	cancel       context.CancelFunc
	ctx          context.Context
	done         chan struct{}
	wake         chan model.TenantID
}

// UseAvailabilitySource wires discovery before module Start. Nil leaves it off.
func (m *Module) UseAvailabilitySource(source ModelDiscovery, tenants func(context.Context) ([]model.TenantID, error)) {
	m.availability = &availabilityCatalog{source: source, tenants: tenants, now: time.Now,
		observations: make(map[string]availabilityObservation), wake: make(chan model.TenantID, 64)}
}

func registerAvailabilitySchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{Kind: availabilityKind, Table: "models_availability", Audited: true, Fields: []model.FieldSpec{
		{Name: "source_ref", Kind: model.KindText, Principal: model.None("provider/profile/tool-login reference, not a user: availability.go:270")},
		{Name: "revision", Kind: model.KindText, Principal: model.None("non-secret configuration digest: availability.go:298")},
		{Name: "snapshot", Kind: model.KindJSON, Principal: model.Scan(model.ClassEvidence)},
	}, Indexes: []model.IndexSpec{{Name: "models_availability_source_uniq", Columns: []string{model.ColTenantID, "source_ref"}, Unique: true}}})
}

func (m *Module) startAvailability() {
	a := m.availability
	if a == nil || a.source == nil || a.tenants == nil || m.data == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel, a.done = cancel, make(chan struct{})
	a.ctx = ctx
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(availabilityPollInterval)
		defer ticker.Stop()
		m.refreshAvailableTenants(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case tenant := <-a.wake:
				_ = m.RefreshAvailability(ctx, tenant)
			case <-ticker.C:
				m.refreshAvailableTenants(ctx)
			}
		}
	}()
}

func (m *Module) stopAvailability() {
	a := m.availability
	if a == nil {
		return
	}
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.cancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (m *Module) refreshAvailableTenants(ctx context.Context) {
	tenants, err := m.availability.tenants(ctx)
	if err != nil {
		return
	}
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			return
		}
		_ = m.RefreshAvailability(ctx, tenant)
	}
}

// WakeAvailability queues a bounded refresh after sign-in or configuration
// change. It performs no I/O, coalesces through the existing refresh interval,
// and does nothing when the module is stopped or off. Source revisions bypass
// the interval; an unchanged sign-in/configuration does not make a hot loop.
func (m *Module) WakeAvailability(tenant model.TenantID) {
	a := m.availability
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel == nil {
		return
	}
	select {
	case a.wake <- tenant:
	default:
	}
}

// RefreshAvailability reconciles one tenant, at most once an hour per unchanged
// source. Startup and a configuration revision change refresh immediately.
// It is also the in-process seam used by qualification; HTTP reads never probe.
func (m *Module) RefreshAvailability(ctx context.Context, tenant model.TenantID) error {
	a := m.availability
	if a == nil || tenant.IsZero() || tenant.IsSystem() {
		return nil
	}
	a.refresh.Lock()
	defer a.refresh.Unlock()
	a.mu.Lock()
	active := a.cancel != nil
	lifecycle := a.ctx
	a.mu.Unlock()
	if !active || ctx.Err() != nil {
		return ctx.Err()
	}
	ctx, cancelRefresh := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(lifecycle, cancelRefresh)
	defer func() { stopCancellation(); cancelRefresh() }()
	sources, err := a.source.Sources(ctx, tenant)
	if err != nil {
		return errors.New("model sources could not be read")
	}
	for _, source := range sources {
		key := tenant.String() + "/" + source.Ref
		now := a.now().UTC()
		a.mu.Lock()
		previous := a.observations[key]
		a.mu.Unlock()
		if previous.revision == source.Revision && !previous.checked.IsZero() && now.Sub(previous.checked) < availabilityRefreshInterval {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		ids, probeErr := a.source.Discover(probeCtx, tenant, source)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(ids) > 1000 {
			probeErr = errors.New("model catalog exceeded its model bound")
		}
		if probeErr == nil {
			ids, probeErr = cleanAvailableModelIDs(ids)
		}
		state, message := "fresh", ""
		if probeErr != nil {
			state, message = "failed", availabilityFailure
		}
		if errors.Is(probeErr, ErrModelDiscoveryUnsupported) {
			state, message = "unsupported", "Automatic model discovery is unavailable for this tool. Use its default model."
		}
		if err := m.persistAvailability(ctx, tenant, source, state, message, ids, now); err != nil {
			return err
		}
		seen := previous.seen
		if previous.revision != source.Revision {
			seen = time.Time{}
		}
		if state == "fresh" {
			seen = now
		}
		a.mu.Lock()
		a.observations[key] = availabilityObservation{revision: source.Revision, checked: now, seen: seen}
		a.mu.Unlock()
	}
	return nil
}

func cleanAvailableModelIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 200 || strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return nil, errors.New("model catalog contained an invalid model ID")
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func (m *Module) persistAvailability(ctx context.Context, tenant model.TenantID, source AvailabilitySource, state, message string, ids []string, now time.Time) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(availabilityKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(ctx, repo, eq("source_ref", source.Ref))
		if err != nil {
			return err
		}
		var old Availability
		if found {
			_ = json.Unmarshal([]byte(rec.String("snapshot")), &old)
		}
		next := Availability{AvailabilitySource: source, State: state, Message: message, CheckedAt: now.Format(time.RFC3339Nano), Models: []AvailableModel{}}
		if state == "fresh" {
			next.SeenAt = next.CheckedAt
			for _, id := range ids {
				next.Models = append(next.Models, AvailableModel{ID: id, SeenAt: next.SeenAt})
			}
		} else if found && rec.String("revision") == source.Revision {
			next.SeenAt, next.Models = old.SeenAt, old.Models
		}
		// Observation times advance in memory on an identical refresh. The durable
		// snapshot records the last changed observation, so an idle poll writes no
		// entity, timestamp or audit row. Restart conservatively refreshes it.
		comparable := next
		comparable.Models = slices.Clone(next.Models)
		comparable.CheckedAt, comparable.SeenAt = old.CheckedAt, old.SeenAt
		for i := range comparable.Models {
			comparable.Models[i].SeenAt = old.SeenAt
		}
		before, _ := json.Marshal(old)
		after, _ := json.Marshal(comparable)
		if found && rec.String("revision") == source.Revision && string(before) == string(after) {
			return nil
		}
		encoded, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if !found {
			rec = model.Record{"source_ref": source.Ref}
		}
		rec["revision"], rec["snapshot"] = source.Revision, string(encoded)
		if found {
			_, err = repo.Update(ctx, rec)
		} else {
			_, err = repo.Create(ctx, rec)
		}
		return err
	})
}

// AvailableModels reads only the tenant's currently configured sources. Removed
// providers and retired accounts never remain selectable through old snapshots.
func (m *Module) AvailableModels(ctx context.Context, tenant model.TenantID) ([]Availability, error) {
	out := []Availability{}
	a := m.availability
	if a == nil || a.source == nil || m.data == nil {
		return out, nil
	}
	sources, err := a.source.Sources(ctx, tenant)
	if err != nil {
		return nil, errors.New("model sources could not be read")
	}
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(availabilityKind)
		if err != nil {
			return err
		}
		for _, source := range sources {
			row := Availability{AvailabilitySource: source, State: "stale", Models: []AvailableModel{}, Message: "Models have not been refreshed yet."}
			rec, found, err := findOne(ctx, repo, eq("source_ref", source.Ref))
			if err != nil {
				return err
			}
			if found && rec.String("revision") == source.Revision {
				row.Message = ""
				if err := json.Unmarshal([]byte(rec.String("snapshot")), &row); err != nil {
					return err
				}
				row.Version = rec.Int(model.ColVersion)
				a.mu.Lock()
				observation, observed := a.observations[tenant.String()+"/"+source.Ref]
				a.mu.Unlock()
				if observed && observation.revision == source.Revision {
					row.CheckedAt = observation.checked.Format(time.RFC3339Nano)
					if !observation.seen.IsZero() {
						row.SeenAt = observation.seen.Format(time.RFC3339Nano)
					}
				}
				if seen, err := time.Parse(time.RFC3339Nano, row.SeenAt); row.State == "fresh" && (err != nil || a.now().Sub(seen) >= availabilityRefreshInterval*2) {
					row.State, row.Message = "stale", "This model list is out of date. It will refresh automatically."
				}
				for i := range row.Models {
					row.Models[i].SeenAt = row.SeenAt
				}
			}
			out = append(out, row)
		}
		return nil
	})
	return out, err
}

// handleAvailability lists the tenant's observed model IDs and discovery states
// for configured providers and tool accounts without triggering discovery.
// Provider, account and driver filters combine with AND.
func (m *Module) handleAvailability(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	items, err := m.AvailableModels(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := []Availability{}
	for _, item := range items {
		q := r.URL.Query()
		if q.Get("provider_ref") != "" && q.Get("provider_ref") != item.ProviderRef {
			continue
		}
		if q.Get("account_ref") != "" && q.Get("account_ref") != item.AccountRef {
			continue
		}
		if q.Get("driver") != "" && q.Get("driver") != item.Driver {
			continue
		}
		out = append(out, item)
	}
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditOwned(r.Context(), sc, mc, availabilityKind, "read", "")
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "refresh_interval_seconds": int(availabilityRefreshInterval.Seconds())})
}
