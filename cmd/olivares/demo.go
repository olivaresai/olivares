// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// Demo credentials minted by `serve --seed-demo`. They are intentionally
// well-known: this mode loads SYNTHETIC, minimal-data sample observations so an
// operator (or the web E2E) sees the R/RW graph and the executive dashboard the
// minute they boot a throwaway data-dir. It must never be used for a
// real estate — the data is fabricated and the password is public.
const (
	demoEmail    = "demo@olivares.local"
	demoPassword = "olivares-demo-estate"
	demoOrgName  = "Demo Estate"
	demoOrgSlug  = "demo"
)

// seedDemoEstate provisions the demo tenant and request-driven read models at the
// store level, then registers the demo seed SourceConnector with the runtime — all
// BEFORE rt.Start. The agents therefore exist when the source's agent-origin edges
// attribute, while knowledge/scoping/evals are immediately available to their
// normal HTTP readers without inventing observation kinds. It returns the demo
// tenant id. Called only from boot when DemoSeed is set.
// demoOrgExists answers whether the demo org is already in this store. It exists so the
// conflict advice above is EARNED: store.ErrConflict covers both a unique-key collision and
// an optimistic-concurrency mismatch, and telling an operator "you already seeded this" when
// they actually hit a concurrent write is a confident wrong answer, which is worse than the
// raw error it replaced.
func demoOrgExists(ctx context.Context, st store.Store) (bool, error) {
	found := false
	err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			if o.Slug == demoOrgSlug {
				found = true
				return nil
			}
		}
		return nil
	})
	return found, err
}

func seedDemoEstate(ctx context.Context, st store.Store, rt *runtime.Runtime, now time.Time) (model.TenantID, error) {
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: demoOrgName, Slug: demoOrgSlug, Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		// ⛔ UN DATA-DIR YA SEMBRADO NO ES UN FALLO DEL MOTOR, y decirlo con el código de
		// constraint de SQLite delante lo parece. Medido el 2026-08-24 relanzando `serve
		// --seed-demo` sobre su propio data-dir:
		//
		//   Error: seed demo estate: create demo org: version conflict: constraint failed:
		//   UNIQUE constraint failed: orgs.slug (2067)
		//
		// El motor hace lo correcto —REHÚSA en vez de sembrar a medias— y luego se lo cuenta
		// al operador en el idioma del store. Quien lo lee no sabe que la respuesta es «usa
		// otro --data-dir»: sabe que algo se rompió con un número. El canon llama a esto
		// parecer amateur, y cuesta un ciclo de reinicio averiguarlo.
		//
		// El conflicto SIGUE siendo el mismo error envuelto: nada se traga, sólo se nombra.
		if errors.Is(err, store.ErrConflict) {
			// ⛔ Y LA ADVERTENCIA SE GANA, NO SE SUPONE. La primera version de esta rama
			// afirmaba dos cosas que no podia sostener, y una revision sobre mi propio
			// diff las tumbo:
			//
			//  · «refuses rather than half-seeding» es FALSO. La org se crea en SU
			//    transaccion (st.System, arriba) y los read models en OTRA (st.Mutate,
			//    abajo, que puede fallar en :138 con «seed demo read models»). Un fallo
			//    posterior deja la org commiteada: media siembra es exactamente lo que
			//    puede pasar. Decirle al operador «sirve este dir SIN --seed-demo» podia
			//    dejarle sirviendo un estate incompleto.
			//  · `store.ErrConflict` es «optimistic-concurrency mismatch OR unique-key
			//    collision» (core/store/errors.go:16), asi que atribuirlo al slug sin
			//    mirar es adivinar. Ahora se MIRA: si la org demo existe, la causa esta
			//    establecida; si no, se dice que no se ha podido establecer.
			if existe, verr := demoOrgExists(ctx, st); verr != nil {
				return tenant, fmt.Errorf(
					"demo seeding hit a conflict and I could not check whether org %q already "+
						"exists (%v), so I cannot tell you which: %w", demoOrgSlug, verr, err)
			} else if existe {
				return tenant, fmt.Errorf(
					"this data dir already has the demo org %q, so --seed-demo has run here "+
						"before. NOTE it seeds in more than one transaction, so if a previous "+
						"run was interrupted the estate may be INCOMPLETE — prefer a fresh "+
						"--data-dir over serving this one: %w", demoOrgSlug, err)
			}
			return tenant, fmt.Errorf(
				"demo seeding conflicted and org %q does NOT exist, so this is a concurrent "+
					"write rather than a re-seed: %w", demoOrgSlug, err)
		}
		return tenant, fmt.Errorf("create demo org: %w", err)
	}

	// Pre-create the named workspace before the cooperative agents so the reviewer
	// can carry its explicit workspace_id. The other agents retain a zero id, which
	// the store resolves to the tenant's default workspace.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		workspaceIDs := make(map[string]model.ID, len(seed.Workspaces()))
		for _, ws := range seed.Workspaces() {
			created, err := sc.Workspaces().Create(ctx, model.Workspace{
				Name: ws.Name, Slug: ws.Slug, Status: model.StatusActive,
			})
			if err != nil {
				return fmt.Errorf("create workspace %q: %w", ws.Name, err)
			}
			workspaceIDs[ws.Slug] = created.ID
		}

		agents := make(map[string]model.Agent, len(seed.Agents()))
		for _, a := range seed.Agents() {
			workspaceID := model.ID("")
			if a.WorkspaceSlug != "" {
				var ok bool
				workspaceID, ok = workspaceIDs[a.WorkspaceSlug]
				if !ok {
					return fmt.Errorf("agent %q references unknown workspace %q", a.ExternalID, a.WorkspaceSlug)
				}
			}
			created, err := sc.Agents().Create(ctx, model.Agent{
				Name: a.Name, Kind: a.Kind, ExternalID: a.ExternalID,
				Status: model.StatusActive, WorkspaceID: workspaceID,
			})
			if err != nil {
				return fmt.Errorf("create agent %q: %w", a.ExternalID, err)
			}
			agents[a.ExternalID] = created
		}

		for _, g := range seed.AgentGroups() {
			workspaceID, ok := workspaceIDs[g.WorkspaceSlug]
			if !ok {
				return fmt.Errorf("agent group %q references unknown workspace %q", g.Name, g.WorkspaceSlug)
			}
			member, ok := agents[g.MemberAgentRef]
			if !ok {
				return fmt.Errorf("agent group %q references unknown agent %q", g.Name, g.MemberAgentRef)
			}
			group, err := sc.AgentGroups().Create(ctx, model.AgentGroup{
				WorkspaceID: workspaceID, Name: g.Name, Slug: g.Slug,
				Description: g.Description, Status: model.StatusActive,
			})
			if err != nil {
				return fmt.Errorf("create agent group %q: %w", g.Name, err)
			}
			if _, err := sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{
				GroupID: group.ID, AgentID: member.ID,
			}); err != nil {
				return fmt.Errorf("add agent %q to group %q: %w", g.MemberAgentRef, g.Name, err)
			}
		}

		if err := seedDemoKnowledge(ctx, sc, now); err != nil {
			return err
		}
		return seedDemoEvals(ctx, sc, now)
	}); err != nil {
		return tenant, fmt.Errorf("seed demo read models: %w", err)
	}

	if err := rt.AddSource(seed.NewSource(seed.SourceName, now), sdk.Config{}, tenant.String()); err != nil {
		return tenant, fmt.Errorf("register demo source: %w", err)
	}
	return tenant, nil
}

func seedDemoKnowledge(ctx context.Context, sc store.Scope, now time.Time) error {
	spec := seed.Knowledge()
	baseRepo, err := sc.Ext(model.Kind("knowledge.base"))
	if err != nil {
		return fmt.Errorf("open demo knowledge bases: %w", err)
	}
	kb, err := baseRepo.Create(ctx, model.Record{
		"name": spec.BaseName, "classification": spec.Classification, "residency_region": "global",
		"embed_policy": "auto", "embed_model": "local-hash", "dim": int64(256),
		"default_acl": "[]", "owner_ref": seed.SourceName, "status": "active",
		"doc_count": int64(len(spec.Documents)), "chunk_count": int64(0),
	})
	if err != nil {
		return fmt.Errorf("create demo knowledge base: %w", err)
	}
	kbID := kb.String(model.ColID)

	documentRepo, err := sc.Ext(model.Kind("knowledge.document"))
	if err != nil {
		return fmt.Errorf("open demo knowledge documents: %w", err)
	}
	documentIDs := make([]string, 0, len(spec.Documents))
	for _, document := range spec.Documents {
		rec, err := documentRepo.Create(ctx, model.Record{
			"kb_ref": kbID, "source_kind": "demo", "source_ref": "demo", "source_mode": "direct",
			"source_doc_id": document.SourceDocID, "title": document.Title, "content_type": "text/plain",
			"classification": spec.Classification, "residency_region": "global", "acl": "[]",
			"content_hash": demoSeedHash(document.Content), "redaction_count": int64(0),
			"space_ref": "", "chunk_count": int64(0), "status": "indexed",
		})
		if err != nil {
			return fmt.Errorf("create demo knowledge document %q: %w", document.Title, err)
		}
		documentIDs = append(documentIDs, rec.String(model.ColID))
	}

	promptHash := demoSeedHash(spec.PromptContent)
	promptRepo, err := sc.Ext(model.Kind("knowledge.prompt"))
	if err != nil {
		return fmt.Errorf("open demo knowledge prompts: %w", err)
	}
	prompt, err := promptRepo.Create(ctx, model.Record{
		"name": spec.PromptName, "current_rev": int64(1), "latest_hash": promptHash,
		"owner_ref": seed.SourceName, "status": "active",
	})
	if err != nil {
		return fmt.Errorf("create demo knowledge prompt: %w", err)
	}
	revisionRepo, err := sc.Ext(model.Kind("knowledge.prompt_revision"))
	if err != nil {
		return fmt.Errorf("open demo knowledge prompt revisions: %w", err)
	}
	if _, err := revisionRepo.Create(ctx, model.Record{
		"prompt_ref": prompt.String(model.ColID), "rev_num": int64(1), "label": "demo",
		"template": spec.PromptContent, "template_hash": promptHash,
		"note": "Synthetic demo prompt.", "created_by": seed.SourceName,
	}); err != nil {
		return fmt.Errorf("create demo knowledge prompt revision: %w", err)
	}

	memoryRepo, err := sc.Ext(model.Kind("knowledge.memory"))
	if err != nil {
		return fmt.Errorf("open demo knowledge memory: %w", err)
	}
	if _, err := memoryRepo.Create(ctx, model.Record{
		"agent_ref": spec.MemoryAgentRef, "mkey": spec.MemoryKey, "content": spec.MemoryContent,
		"content_hash": demoSeedHash(spec.MemoryContent), "classification": spec.MemoryClassification,
		"residency_region": "global", "expires_at": nil, "created_by": seed.SourceName,
	}); err != nil {
		return fmt.Errorf("create demo knowledge memory: %w", err)
	}

	lineageRepo, err := sc.Ext(model.Kind("knowledge.lineage"))
	if err != nil {
		return fmt.Errorf("open demo knowledge lineage: %w", err)
	}
	for i, documentID := range documentIDs {
		sourceRefs, err := json.Marshal([]string{documentID})
		if err != nil {
			return fmt.Errorf("encode demo knowledge source refs: %w", err)
		}
		occurredAt := model.NewTimestamp(now.Add(-time.Duration(i+1) * time.Minute)).String()
		if _, err := lineageRepo.Create(ctx, model.Record{
			"kb_ref": kbID, "agent_ref": seed.AgentReviewer, "session_ref": seed.SessionEvade,
			"query_hash": demoSeedHash("demo-lineage:" + documentID), "chunk_refs": "[]",
			"source_refs": string(sourceRefs), "residency_region": "global", "decision": "allowed",
			"reason": "Seeded document provenance.", "egress": false, "egress_provider": "",
			"result_count": int64(0), "occurred_at": occurredAt,
		}); err != nil {
			return fmt.Errorf("create demo knowledge lineage: %w", err)
		}
	}
	return nil
}

func seedDemoEvals(ctx context.Context, sc store.Scope, now time.Time) error {
	suiteSpec, runSpecs := seed.Evals()
	suiteRepo, err := sc.Ext(model.Kind("evals.suite"))
	if err != nil {
		return fmt.Errorf("open demo eval suites: %w", err)
	}
	suite, err := suiteRepo.Create(ctx, model.Record{
		"name": suiteSpec.Name, "description": suiteSpec.Description, "subject_kind": suiteSpec.SubjectKind,
		"scorer": suiteSpec.Scorer, "criterion": suiteSpec.Criterion,
		"pass_threshold": suiteSpec.PassThreshold, "regression_threshold": suiteSpec.RegressionThreshold,
		"judge_model": "", "suite_version": suiteSpec.SuiteVersion, "status": "active",
	})
	if err != nil {
		return fmt.Errorf("create demo eval suite: %w", err)
	}

	runRepo, err := sc.Ext(model.Kind("evals.run"))
	if err != nil {
		return fmt.Errorf("open demo eval runs: %w", err)
	}
	for _, run := range runSpecs {
		at := model.NewTimestamp(now.Add(-time.Duration(run.AgeMinutes) * time.Minute)).String()
		if _, err := runRepo.Create(ctx, model.Record{
			"suite_ref": suite.String(model.ColID), "suite_version": suiteSpec.SuiteVersion,
			"subject_kind": suiteSpec.SubjectKind, "subject_ref": run.SubjectRef,
			"model_ref": "", "prompt_variant": "", "scorer": suiteSpec.Scorer, "status": run.Status,
			"total": run.Total, "passed": run.Passed, "failed": run.Failed,
			"errors": run.Errors, "skipped": run.Skipped, "score": run.Score, "pass_rate": run.PassRate,
			"baseline_ref": nil, "regressed": false, "drift": float64(0),
			"started_at": at, "finished_at": at, "launched_by": seed.SourceName,
		}); err != nil {
			return fmt.Errorf("create demo eval run: %w", err)
		}
	}
	return nil
}

func demoSeedHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// announceDemo mints the well-known demo superadmin (on a fresh estate) and prints
// how to log in. It is the human counterpart to seedDemoEstate: the data is loaded
// by the source; this makes it reachable.
//
// The superadmin is granted OWNERSHIP of the seeded demo tenant, atomically with
// the account. Without that grant the account has no tenant grants at all, so the
// console's tenant switcher is empty and the banner's "switch to the demo
// organization" is impossible to follow — the same unusable first boot /v1/setup
// had. seedDemoEstate always runs before this (boot returns its tenant); a zero
// demoTenant degrades to the credential-only bootstrap rather than inventing one.
func announceDemo(ctx context.Context, out io.Writer, eng *engine) error {
	has, err := eng.authr.HasAnyUser(ctx)
	if err != nil {
		return err
	}
	if !has {
		if _, _, err := eng.authr.BootstrapSuperadminOwning(ctx, demoEmail, demoPassword, eng.demoTenant); err != nil {
			return fmt.Errorf("bootstrap demo admin: %w", err)
		}
	}
	fmt.Fprintf(out, "\n=== DEMO MODE (SYNTHETIC DATA) ===\n"+
		"Loaded a sample AI-agent estate (R/RW graph, sessions, FinOps, findings, knowledge, workspaces, evals, and a seed backup).\n"+
		"Log in:  %s  /  %s\n"+
		"Then switch to the \"%s\" organization to see the seeded data.\n"+
		"WARNING: the data is fabricated and the password is public — demo only.\n"+
		"==================================\n\n", demoEmail, demoPassword, demoOrgName)
	return nil
}
