// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The Ask binding (docs/design/ask-olivares-agent-v1.md, D3): the model an administrator
// chose for Ask, and the others a person may pick instead. It is product state, one policy
// row per tenant, changed only through PUT /ask/binding, which needs the same permission
// that authorizes routed execution (choosing the model authorizes its egress). With no
// binding Ask is off. A local model is a routing policy over a local provider, such as
// Ollama, so one kind of reference covers both.
//
// The binding pins each routing policy at the revision the administrator chose: the digest
// of its routing spec. Anyone who may write routing policies can change where one sends a
// prompt (its gateway, its pinned model, its execution profile), so a turn refuses a policy
// whose spec no longer has the pinned digest until an administrator binds it again.
const askBindingKind = "ask_binding"

// askBindingLeaf is a routing policy id the Ask turn resolves.
var askBindingLeaf = model.None("a routing policy id, resolved only to a routing policy of the same tenant: ask_binding.go:70, ask_binding.go:105, ask_turn.go:198")

func init() {
	model.MustRegisterPolicyKind(askBindingKind, model.Nested(askBinding{}, model.ClassEvidence,
		model.Leaf("model", askBindingLeaf),
		model.Leaf("models[]", askBindingLeaf),
		model.Leaf("revisions{key}", askBindingLeaf),
		model.Leaf("revisions{}", model.None("a routing spec digest, compared with the digest of the policy the turn resolves: ask_binding.go:60, execute.go:333")),
	))
}

// askMaxModels bounds the models a person may pick from.
const askMaxModels = 16

type askBinding struct {
	Model  string   `json:"model"`
	Models []string `json:"models,omitempty"`
	// Revisions maps each bound routing policy id to the hex digest of its routing spec
	// when it was bound.
	Revisions map[string]string `json:"revisions,omitempty"`
}

// revision returns the spec digest a turn on the routing policy id must find; false when the
// binding pins none, which a turn refuses like a changed policy.
func (b askBinding) revision(id string) (pin [32]byte, ok bool) {
	raw, err := hex.DecodeString(b.Revisions[id])
	if err != nil || len(raw) != len(pin) {
		return pin, false
	}
	copy(pin[:], raw)
	return pin, true
}

// allows reports whether a turn may use the routing policy id.
func (b askBinding) allows(id string) bool {
	return b.Model != "" && (id == b.Model || slices.Contains(b.Models, id))
}

// readAskBinding returns the tenant's binding and its row; an absent binding is the zero value.
func readAskBinding(ctx context.Context, sc store.Scope) (askBinding, model.Policy, error) {
	rows, _, err := sc.Policies().List(ctx, model.Query{Limit: 2, Filters: []model.Filter{eq("kind", askBindingKind)}})
	if err != nil || len(rows) == 0 {
		return askBinding{}, model.Policy{}, err
	}
	var b askBinding
	raw, err := json.Marshal(rows[0].Spec)
	if err == nil {
		err = json.Unmarshal(raw, &b)
	}
	return b, rows[0], err
}

type askModelDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type askBindingDTO struct {
	Model  string        `json:"model"`
	Models []askModelDTO `json:"models"`
}

// askBindingView names the bound model and every model a person may pick, bound model
// first. A routing policy deleted since it was bound is left out.
func askBindingView(ctx context.Context, sc store.Scope, b askBinding) (askBindingDTO, error) {
	out := askBindingDTO{Models: []askModelDTO{}}
	if b.Model == "" {
		return out, nil
	}
	for _, id := range append([]string{b.Model}, b.Models...) {
		p, err := sc.Policies().Get(ctx, model.ID(id))
		if errors.Is(err, store.ErrNotFound) || err == nil && p.Kind != policyKindRouting {
			continue
		}
		if err != nil {
			return out, err
		}
		if id == b.Model {
			out.Model = id
		}
		if !slices.ContainsFunc(out.Models, func(m askModelDTO) bool { return m.ID == id }) {
			out.Models = append(out.Models, askModelDTO{ID: id, Name: p.Name})
		}
	}
	return out, nil
}

// handleSetAskBinding binds Ask to the routing policy an administrator chose and,
// optionally, the others a person may pick. An empty model turns Ask off.
func (m *Module) handleSetAskBinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req struct {
		Model  string   `json:"model"`
		Models []string `json:"models"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	in := askBinding{Model: req.Model, Models: req.Models, Revisions: map[string]string{}}
	in.Model = strings.TrimSpace(in.Model)
	if in.Model == "" && len(in.Models) > 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("models needs a model to bind first"))
		return
	}
	if len(in.Models) > askMaxModels {
		writeJSON(w, http.StatusBadRequest, errorBody("models lists more than 16 routing policies"))
		return
	}
	var out askBindingDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		for _, id := range append([]string{in.Model}, in.Models...) {
			if id == "" {
				continue
			}
			p, err := sc.Policies().Get(r.Context(), model.ID(id))
			if err != nil {
				return err
			}
			if p.Kind != policyKindRouting {
				return store.ErrNotFound
			}
			digest, err := routingSpecDigest(parseRoutingSpec(p.Spec))
			if err != nil {
				return err
			}
			in.Revisions[id] = hex.EncodeToString(digest[:])
		}
		_, row, err := readAskBinding(r.Context(), sc)
		switch {
		case err != nil:
			return err
		case in.Model == "" && row.ID.IsZero():
		case in.Model == "":
			err = sc.Policies().Delete(r.Context(), row.ID)
		case row.ID.IsZero():
			_, err = sc.Policies().Create(r.Context(), model.Policy{Name: "ask", Kind: askBindingKind, Enabled: true, Spec: askBindingSpec(in)})
		default:
			row.Spec = askBindingSpec(in)
			_, err = sc.Policies().Update(r.Context(), row)
		}
		if err != nil {
			return err
		}
		if _, err := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: "ask.binding",
			TargetKind: askTurnKind, TargetID: model.ID(in.Model), Meta: map[string]any{"models": len(in.Models)},
		}); err != nil {
			return err
		}
		out, err = askBindingView(r.Context(), sc, in)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func askBindingSpec(b askBinding) map[string]any {
	models := make([]any, 0, len(b.Models))
	for _, id := range b.Models {
		if id = strings.TrimSpace(id); id != "" && id != b.Model && !slices.Contains(models, any(id)) {
			models = append(models, id)
		}
	}
	revisions := make(map[string]any, len(b.Revisions))
	for id, digest := range b.Revisions {
		revisions[id] = digest
	}
	return map[string]any{"model": b.Model, "models": models, "revisions": revisions}
}
