// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// correlatingReader is a standing reader that also correlates. It records what
// it was asked and answers a fixed correlation.
type correlatingReader struct {
	answer  auth.Correlation
	tenants []model.TenantID
	docs    []auth.Document
	limits  []auth.CensusLimits
	keys    [][]auth.QualifiedKey
}

// Standing implements auth.StandingReader.
func (r *correlatingReader) Standing(context.Context, model.TenantID, []model.ID) (map[model.ID]auth.Standing, error) {
	return nil, errors.New("standing is not read in this test")
}

// CorrelateContent implements auth.Correlator.
func (r *correlatingReader) CorrelateContent(_ context.Context, tenant model.TenantID, doc auth.Document, lim auth.CensusLimits) (auth.Correlation, error) {
	r.tenants = append(r.tenants, tenant)
	r.docs = append(r.docs, doc)
	r.limits = append(r.limits, lim)
	return r.answer, nil
}

// CorrelateOwnerKeys implements auth.Correlator.
func (r *correlatingReader) CorrelateOwnerKeys(_ context.Context, tenant model.TenantID, keys []auth.QualifiedKey) (auth.Correlation, error) {
	r.tenants = append(r.tenants, tenant)
	r.keys = append(r.keys, keys)
	return r.answer, nil
}

// standingOnly is a standing reader that cannot correlate.
type standingOnly struct{}

// Standing implements auth.StandingReader.
func (standingOnly) Standing(context.Context, model.TenantID, []model.ID) (map[model.ID]auth.Standing, error) {
	return nil, errors.New("standing is not read in this test")
}

// TestStandingPortForwardsCorrelator: the one standing port forwards both
// correlations to its reader and returns the reader's answer unchanged; a port
// whose reader cannot correlate refuses.
func TestStandingPortForwardsCorrelator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenant := model.NewTenantID()
	reader := &correlatingReader{answer: auth.Correlation{
		Directory: store.AuthorizationFactRef{Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 7},
		Accounts:  []model.ID{model.NewID(), model.NewID()},
	}}
	var port auth.Correlator = &standingPort{reader: reader}

	doc := auth.Document{Raw: `{"owner":"a@x.test"}`, Decoded: []string{"owner", "a@x.test"}}
	lim := auth.TenantCensusLimits()
	got, err := port.CorrelateContent(ctx, tenant, doc, lim)
	if err != nil || !reflect.DeepEqual(got, reader.answer) {
		t.Fatalf("CorrelateContent = %+v, %v; want the reader's %+v", got, err, reader.answer)
	}
	if !reflect.DeepEqual(reader.docs, []auth.Document{doc}) || !reflect.DeepEqual(reader.limits, []auth.CensusLimits{lim}) {
		t.Errorf("the reader was asked %+v with %+v, want the document and limits given once", reader.docs, reader.limits)
	}

	key, err := auth.QualifiedSubjectKey("https://idp.example", "sub-1")
	if err != nil {
		t.Fatalf("QualifiedSubjectKey: %v", err)
	}
	keys := []auth.QualifiedKey{key}
	got, err = port.CorrelateOwnerKeys(ctx, tenant, keys)
	if err != nil || !reflect.DeepEqual(got, reader.answer) {
		t.Fatalf("CorrelateOwnerKeys = %+v, %v; want the reader's %+v", got, err, reader.answer)
	}
	if !reflect.DeepEqual(reader.keys, [][]auth.QualifiedKey{keys}) || !reflect.DeepEqual(reader.tenants, []model.TenantID{tenant, tenant}) {
		t.Errorf("the reader was asked keys %q in tenants %v, want the keys given once and the tenant twice", reader.keys, reader.tenants)
	}

	for name, p := range map[string]*standingPort{"a reader that cannot correlate": {reader: standingOnly{}}, "no port": nil} {
		if got, err := p.CorrelateContent(ctx, tenant, doc, lim); err == nil || !reflect.DeepEqual(got, auth.Correlation{}) {
			t.Errorf("%s: CorrelateContent = %+v, %v; want a refusal", name, got, err)
		}
		if got, err := p.CorrelateOwnerKeys(ctx, tenant, keys); err == nil || !reflect.DeepEqual(got, auth.Correlation{}) {
			t.Errorf("%s: CorrelateOwnerKeys = %+v, %v; want a refusal", name, got, err)
		}
	}
}
