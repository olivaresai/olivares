// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const profileModelCatalogTTL = 2 * time.Minute

// errEmptyToolCatalog marks a read that succeeded but listed nothing; callers
// get the tool default and free text, never an empty "fresh" list.
var errEmptyToolCatalog = errors.New("tool reported no models")

// CatalogModel carries only metadata the installed tool reports. Missing
// capability/marker fields mean unknown; model names never imply capabilities.
type CatalogModel struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"display_name,omitempty"`
	EffortLevels     []string `json:"effort_levels,omitempty"`
	DefaultEffort    string   `json:"default_effort,omitempty"`
	IsDefault        *bool    `json:"is_default,omitempty"`
	IsNew            *bool    `json:"is_new,omitempty"`
	IsLegacy         *bool    `json:"is_legacy,omitempty"`
	SupportsFastMode *bool    `json:"supports_fast_mode,omitempty"`
	ServiceTiers     []string `json:"service_tiers,omitempty"`
}

type ProfileModelCatalog struct {
	ProfileRef             string         `json:"profile_ref"`
	Driver                 string         `json:"driver"`
	State                  string         `json:"state"`
	Models                 []CatalogModel `json:"models"`
	DefaultModel           string         `json:"default_model"` // empty delegates to the tool's default
	FreeText               bool           `json:"free_text"`
	Message                string         `json:"message,omitempty"`
	CheckedAt              string         `json:"checked_at"`
	RefreshIntervalSeconds int            `json:"refresh_interval_seconds"`
}

type profileCatalogEntry struct {
	revision string
	expires  time.Time
	result   ProfileModelCatalog
}

type profileCatalogCache struct {
	mu      sync.Mutex
	entries map[string]profileCatalogEntry
	pending map[string]chan struct{}
}

func validateCatalogModel(md CatalogModel) error {
	if !validToolCatalogModelID(md.ID) || len(md.DisplayName) > 200 || strings.IndexFunc(md.DisplayName, unicode.IsControl) >= 0 || len(md.EffortLevels) > 32 || len(md.ServiceTiers) > 32 {
		return errors.New("invalid tool model metadata")
	}
	for _, values := range [][]string{md.EffortLevels, md.ServiceTiers} {
		for _, value := range values {
			if !validToolCatalogModelID(value) {
				return errors.New("invalid tool model metadata")
			}
		}
	}
	if md.DefaultEffort != "" && !validToolCatalogModelID(md.DefaultEffort) {
		return errors.New("invalid default effort")
	}
	return nil
}

// profileCatalogContext revalidates authorization, local home custody and the
// bound record even on cache hits. File metadata invalidates a login cache;
// credential/configuration contents are never opened here.
func (m *Module) profileCatalogContext(ctx context.Context, tenant model.TenantID, ref string) (ProviderProfile, ProviderHomeSnapshot, ProviderRecord, string, error) {
	prof, err := m.GetProfile(ctx, tenant, ref)
	if err != nil {
		return prof, ProviderHomeSnapshot{}, ProviderRecord{}, "", err
	}
	home, err := m.snapshotForLaunch(tenant, prof, m.ExecutionEnvironmentRef())
	if err != nil {
		return prof, home, ProviderRecord{}, "", err
	}
	revision := strconv.FormatInt(prof.Version, 10)
	var rec ProviderRecord
	if prof.ProviderRecordRef != "" {
		rec, err = m.GetProviderRecord(ctx, tenant, prof.ProviderRecordRef)
		if err == nil && rec.State != ProviderRecordActive {
			err = ErrProviderRecordRevoked
		}
		revision += "/" + strconv.FormatInt(rec.Version, 10)
	} else {
		for _, name := range []string{".credentials.json", "auth.json", "config.toml", "settings.json"} {
			info, statErr := os.Lstat(filepath.Join(home.ConfigHome, name))
			if statErr == nil {
				revision += "/" + name + "/" + info.ModTime().UTC().Format(time.RFC3339Nano) + "/" + strconv.FormatInt(info.Size(), 10)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return prof, home, rec, "", errors.New("tool login metadata could not be inspected")
			}
		}
	}
	return prof, home, rec, revision, err
}

// ProfileModelCatalog discovers without inference. Concurrent reads of the same
// profile coalesce; other profiles remain independent. Failures are explicit and
// never publish stale models or raw vendor error text.
func (m *Module) ProfileModelCatalog(ctx context.Context, tenant model.TenantID, ref string, refresh bool) (ProfileModelCatalog, error) {
	key := tenant.String() + "/" + ref
	c := &m.profileCatalogs
	for {
		prof, home, rec, revision, err := m.profileCatalogContext(ctx, tenant, ref)
		if err != nil {
			return ProfileModelCatalog{}, err
		}
		c.mu.Lock()
		if pending := c.pending[key]; pending != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return ProfileModelCatalog{}, ctx.Err()
			case <-pending:
			}
			refresh = false // a concurrent refresh satisfies this request
			continue
		}
		if cached, ok := c.entries[key]; !refresh && ok && cached.revision == revision && m.now().Before(cached.expires) {
			result := cloneProfileCatalog(cached.result)
			c.mu.Unlock()
			return result, nil
		}
		if c.entries == nil {
			c.entries = make(map[string]profileCatalogEntry)
			c.pending = make(map[string]chan struct{})
		}
		// A canceled refresh must not satisfy its waiting callers with an older
		// cached answer. Only this flight's publication can refill the entry.
		delete(c.entries, key)
		done := make(chan struct{})
		c.pending[key] = done
		c.mu.Unlock()
		// Release the flight on every exit, including a panic recovered upstream.
		defer func() {
			c.mu.Lock()
			delete(c.pending, key)
			close(done)
			c.mu.Unlock()
		}()
		out := ProfileModelCatalog{ProfileRef: ref, Driver: prof.Driver, State: "fresh", Models: []CatalogModel{}, FreeText: true, RefreshIntervalSeconds: int(profileModelCatalogTTL.Seconds())}
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if prof.ProviderRecordRef != "" {
			var ids []string
			ids, err = m.ReadProviderModels(probeCtx, tenant, rec)
			for _, id := range ids {
				out.Models = append(out.Models, CatalogModel{ID: id})
			}
		} else if prof.AuthSource == AuthSourceAccountHome {
			out.Models, err = m.readToolModelCatalog(probeCtx, home)
		} else {
			err = ErrToolModelListUnsupported
		}
		cancel()
		if err == nil {
			if len(out.Models) > 1000 {
				err = errors.New("model catalog exceeded its model bound")
			}
			for _, md := range out.Models {
				if validationErr := validateCatalogModel(md); validationErr != nil {
					err = validationErr
					break
				}
				if md.IsDefault != nil && *md.IsDefault {
					out.DefaultModel = md.ID
				}
			}
			if err == nil && len(out.Models) == 0 {
				err = errEmptyToolCatalog
			}
		}
		if err != nil {
			out.Models = []CatalogModel{}
			out.DefaultModel = ""
			out.State = "failed"
			out.Message = "Models could not be refreshed. Check the provider connection or sign in again."
			switch {
			case errors.Is(err, ErrToolModelListUnsupported), errors.Is(err, ErrNoProviderProbe):
				out.State = "unsupported"
				out.Message = "This tool cannot report its model catalog. Use its default model or enter a model name."
			case errors.Is(err, errEmptyToolCatalog):
				out.State = "unsupported"
				out.Message = "The tool reported no models. Use its default model or enter a model name."
			case m.log != nil:
				m.log.Warn("sessions: model catalog read failed", "profile_ref", ref, "driver", prof.Driver, "error", redactErr(err))
			}
		}
		if out.Models == nil {
			out.Models = []CatalogModel{}
		}
		out.CheckedAt = m.now().UTC().Format(time.RFC3339Nano)
		_, _, _, after, checkErr := m.profileCatalogContext(ctx, tenant, ref)
		if checkErr == nil && after != revision {
			checkErr = ErrProviderRecordChanged
		}
		if ctx.Err() != nil {
			checkErr = ctx.Err()
		}
		c.mu.Lock()
		// A failed read is never cached: the next read asks the tool again.
		if checkErr == nil && out.State != "failed" {
			// Bound idle memory without retaining retired profile histories forever.
			if len(c.entries) >= 256 {
				for oldKey := range c.entries {
					delete(c.entries, oldKey)
					break
				}
			}
			c.entries[key] = profileCatalogEntry{revision: revision, expires: m.now().Add(profileModelCatalogTTL), result: cloneProfileCatalog(out)}
		}
		c.mu.Unlock()
		return out, checkErr
	}
}

func cloneProfileCatalog(in ProfileModelCatalog) ProfileModelCatalog {
	in.Models = append([]CatalogModel{}, in.Models...)
	for i := range in.Models {
		md := &in.Models[i]
		md.EffortLevels = append([]string(nil), md.EffortLevels...)
		md.ServiceTiers = append([]string(nil), md.ServiceTiers...)
		for _, flag := range []**bool{&md.IsDefault, &md.IsNew, &md.IsLegacy, &md.SupportsFastMode} {
			if *flag != nil {
				value := **flag
				*flag = &value
			}
		}
	}
	return in
}

// handleProfileModelCatalog lists the models and effort levels that the installed tool or bound provider of one provider profile reports now, cached for two minutes; it sends no prompt, starts no conversation and returns no credential.
func (m *Module) handleProfileModelCatalog(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	w.Header().Set("Cache-Control", "no-store")
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeRunErr(w, badRequest("invalid catalog query"))
		return
	}
	for name, values := range q {
		if name != "refresh" || len(values) != 1 || values[0] != "true" && values[0] != "false" {
			writeRunErr(w, badRequest("only refresh=true or refresh=false is accepted"))
			return
		}
	}
	ref := chi.URLParam(r, "ref")
	if err := m.requireProfileAccess(r.Context(), mc, ref); err != nil {
		writeRunErr(w, err)
		return
	}
	out, err := m.ProfileModelCatalog(r.Context(), mc.Tenant, ref, q.Get("refresh") == "true")
	if err != nil {
		writeRunErr(w, err)
		return
	}
	prof, err := m.GetProfile(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, err := sc.Audit().Append(r.Context(), model.AuditDraft{Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: "sessions.provider_profile.models.read", TargetKind: providerProfileKind, TargetID: prof.ID, Meta: map[string]any{"profile_ref": ref, "state": out.State}})
		return err
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
