// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// An edition keeps the administrator's choices for its own add-ons in the deployment
// settings record, one JSON document per key (the add-on's name). The core never reads
// them: the edition does, and the record stays the one place a deployment's choices live.

// editionSettingMaxBytes bounds one document. A policy is a few kilobytes; the bound keeps
// the record, which every node reads whole, from growing without limit.
const editionSettingMaxBytes = 256 << 10

// editionSettingKey is the shape of an add-on name: it names an audit action, so it stays
// short and plain.
var editionSettingKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func validEditionSetting(key string, value json.RawMessage) error {
	switch {
	case !editionSettingKey.MatchString(key):
		return fmt.Errorf("edition setting %q: a key is lowercase letters, digits and hyphens", key)
	case len(value) == 0:
		return errors.New("edition setting: no value")
	case len(value) > editionSettingMaxBytes:
		return fmt.Errorf("edition setting %q: the document is over %d bytes", key, editionSettingMaxBytes)
	case !json.Valid(value):
		return fmt.Errorf("edition setting %q: the document is not JSON", key)
	}
	return nil
}

// EditionSettings returns every stored edition document, never nil.
func (p *productSettings) EditionSettings(ctx context.Context) (map[string]json.RawMessage, error) {
	doc, _, err := p.load(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(doc.Edition))
	maps.Copy(out, doc.Edition)
	return out, nil
}

// SaveEditionSetting stores one document, audited as actor in the same write as the record.
// meta is what the audit event carries about it, never the document itself: a policy can
// name hosts and a receiver can name a credential.
func (p *productSettings) SaveEditionSetting(ctx context.Context, actor auth.Principal, key string, value json.RawMessage, meta map[string]any) error {
	if err := validEditionSetting(key, value); err != nil {
		return err
	}
	return p.updateWithAudit(ctx, actor, "deployment.settings.edition."+key, editionAuditMeta(key, meta), func(doc *productSettingsDoc) {
		if doc.Edition == nil {
			doc.Edition = map[string]json.RawMessage{}
		}
		doc.Edition[key] = append(json.RawMessage(nil), value...)
	}, true)
}

// DeleteEditionSetting removes one document. A key that is not stored is not an error: the
// administrator's intent, that nothing is stored under it, already holds.
func (p *productSettings) DeleteEditionSetting(ctx context.Context, actor auth.Principal, key string, meta map[string]any) error {
	if !editionSettingKey.MatchString(key) {
		return fmt.Errorf("edition setting %q: a key is lowercase letters, digits and hyphens", key)
	}
	current, err := p.EditionSettings(ctx)
	if err != nil {
		return err
	}
	if _, stored := current[key]; !stored {
		return nil
	}
	return p.updateWithAudit(ctx, actor, "deployment.settings.edition."+key+".reset", editionAuditMeta(key, meta), func(doc *productSettingsDoc) {
		delete(doc.Edition, key)
		if len(doc.Edition) == 0 {
			doc.Edition = nil
		}
	}, true)
}

func editionAuditMeta(key string, meta map[string]any) map[string]any {
	out := map[string]any{"key": key}
	maps.Copy(out, meta)
	return out
}

// CompareAndSwapEditionSetting refuses a stale editor inside the transaction. A nil
// expected document means the caller observed no stored choice for this key.
func (p *productSettings) CompareAndSwapEditionSetting(ctx context.Context, actor auth.Principal, key string, expected, value json.RawMessage, meta map[string]any) error {
	if err := validEditionSetting(key, value); err != nil {
		return err
	}
	return p.updateChecked(ctx, actor, "deployment.settings.edition."+key, editionAuditMeta(key, meta), func(doc *productSettingsDoc) error {
		if !bytes.Equal(doc.Edition[key], expected) {
			return fmt.Errorf("edition setting changed: %w", store.ErrConflict)
		}
		if doc.Edition == nil {
			doc.Edition = map[string]json.RawMessage{}
		}
		doc.Edition[key] = append(json.RawMessage(nil), value...)
		return nil
	}, true)
}
