// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A document without an ACL is denied (aclAllows). "anyone" is therefore only
// ever stored by an explicit decision, and this file is where those decisions
// are made: effectiveACL for new content, stampLegacyDirectACL for the direct
// uploads that predate the rule.

// aclAnyone is the ACL entry that makes a document visible to the whole tenant.
const aclAnyone = "anyone"

// publicSource is implemented by a content source whose operator declared it
// public (the document source's "public" setting). Nothing else makes a
// connector's documents visible to the whole tenant.
type publicSource interface{ Public() bool }

func sourcePublic(src contentsource.Source) bool {
	p, ok := src.(publicSource)
	return ok && p.Public()
}

// effectiveACL is the one rule for the ACL a document is stored with: its own,
// else the knowledge base's default_acl, else "anyone" when the source is public
// or the content is a direct upload by an administrator, else empty (denied at
// retrieval). madePublic reports "anyone" written because the SOURCE is public;
// that is the decision the ledger records.
func effectiveACL(own, kbDefault []string, public, direct bool) (acl []string, madePublic bool) {
	switch {
	case len(own) > 0:
		return own, false
	case len(kbDefault) > 0:
		return kbDefault, false
	case public:
		return []string{aclAnyone}, true
	case direct:
		return []string{aclAnyone}, false
	}
	return nil, false
}

func countMadePublic(prepared []preparedDoc) int {
	n := 0
	for _, pd := range prepared {
		if pd.madePublic {
			n++
		}
	}
	return n
}

// auditPublicSource appends the ledger entry for n documents made visible to the
// whole tenant because their source is public. Nothing is written for n == 0.
func auditPublicSource(ctx context.Context, sc store.Scope, mc api.ModuleContext, kbID model.ID, n int) error {
	if n == 0 {
		return nil
	}
	return auditEvent(ctx, sc, mc, "knowledge.source.public", baseKind, kbID, map[string]any{"documents": n})
}

// emptyACLColumn reports an ACL column that is blank or parses to an empty list
// (null included). A column that does not parse is NOT empty: a corrupt value is
// never widened.
func emptyACLColumn(rec model.Record) bool {
	raw := strings.TrimSpace(rec.String(colACL))
	if raw == "" {
		return true
	}
	var acl []string
	return json.Unmarshal([]byte(raw), &acl) == nil && len(acl) == 0
}

// legacyDirectUpload reports a document row written by a direct upload before
// the ACL became explicit: empty ACL, and a direct source mode (rows older than
// the source_mode column carry none and the "inline" source kind).
func legacyDirectUpload(rec model.Record) bool {
	if !emptyACLColumn(rec) {
		return false
	}
	mode := strings.TrimSpace(rec.String(colSourceMode))
	return mode == sourceModeDirect || (mode == "" && rec.String(colSourceKind) == "inline")
}

// legacyDirectDocs groups the ids of the tenant's legacy direct uploads by KB.
func legacyDirectDocs(ctx context.Context, sc store.Scope) (map[string][]string, error) {
	repo, err := sc.Ext(documentKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	byKB := map[string][]string{}
	for _, rec := range recs {
		if legacyDirectUpload(rec) {
			kb := rec.String(colKBRef)
			byKB[kb] = append(byKB[kb], rec.String(model.ColID))
		}
	}
	return byKB, nil
}

const (
	aclStampTimeout = 30 * time.Second // the stamp outlives a client that hangs up
	aclStampRetry   = time.Minute      // after a failure, try again no sooner than this
)

// aclStampDue reports whether the tenant still needs a stamp attempt: never tried,
// or the last attempt failed long enough ago.
func (m *Module) aclStampDue(tenant model.TenantID) bool {
	v, tried := m.aclStamped.Load(tenant)
	if !tried {
		return true
	}
	failedAt, failed := v.(time.Time)
	return failed && time.Since(failedAt) > aclStampRetry
}

// stampLegacyDirectACL writes "anyone" into the empty-ACL direct uploads of the
// request's tenant, so that what people saw before an empty ACL denied is still
// what they see. It never touches a connector source. A module cannot enumerate
// tenants, so it runs on the first read of a tenant in this process and is
// idempotent: nothing is left to stamp afterwards because new uploads store
// "anyone" at ingest. A tenant with nothing to stamp costs one read. A failure is
// logged and leaves those rows denied (deny-closed); it is retried after a
// minute, not on every read.
//
// ponytail: the "done" marker lives in memory, so a direct row whose ACL is
// emptied later is stamped again at the next restart, and one that appears
// after the first read (a restore into a running process) stays denied until
// then; upgrade trigger: an API that clears a direct upload's ACL.
func (m *Module) stampLegacyDirectACL(ctx context.Context, mc api.ModuleContext) {
	if !m.aclStampDue(mc.Tenant) {
		return
	}
	m.aclStampMu.Lock()
	defer m.aclStampMu.Unlock()
	if !m.aclStampDue(mc.Tenant) { // another reader finished while this one waited
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aclStampTimeout)
	defer cancel()
	if err := m.stampLegacy(ctx, mc); err != nil {
		m.errorf("knowledge: legacy direct-upload ACL stamp failed; those documents stay denied, retrying in a minute", "err", err)
		m.aclStamped.Store(mc.Tenant, time.Now())
		return
	}
	m.aclStamped.Store(mc.Tenant, struct{}{})
}

func (m *Module) stampLegacy(ctx context.Context, mc api.ModuleContext) error {
	anyLegacy := false
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		byKB, err := legacyDirectDocs(ctx, sc)
		anyLegacy = len(byKB) > 0
		return err
	}); err != nil {
		return err
	}
	if !anyLegacy {
		return nil
	}
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		byKB, err := legacyDirectDocs(ctx, sc)
		if err != nil {
			return err
		}
		kbs := make([]string, 0, len(byKB))
		for kb := range byKB {
			kbs = append(kbs, kb)
		}
		sort.Strings(kbs)
		for _, kb := range kbs {
			for _, id := range byKB[kb] {
				if _, _, err := m.setDocACL(ctx, sc, id, []string{aclAnyone}, ""); err != nil {
					return err
				}
			}
			if err := auditEvent(ctx, sc, mc, "knowledge.acl.legacy_direct", baseKind, model.ID(kb), map[string]any{
				"documents": len(byKB[kb]), "reason": "direct upload stored before an empty ACL denied",
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
