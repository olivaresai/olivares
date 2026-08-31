// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// typedRepo presents the engine-neutral genericRepo as a typed
// store.Repository[T], using a hand-written Codec to map the entity to and from
// a Record. It adds no isolation logic of its own: every database operation goes
// through the tenant-pinned genericRepo.
type typedRepo[T any] struct {
	g     *genericRepo
	codec model.Codec[T]
}

// newTypedRepo wraps a genericRepo with a codec.
func newTypedRepo[T any](g *genericRepo, codec model.Codec[T]) store.Repository[T] {
	return &typedRepo[T]{g: g, codec: codec}
}

func (r *typedRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	var zero T
	rec, err := r.g.Get(ctx, id)
	if err != nil {
		return zero, err
	}
	return r.decode(rec)
}

func (r *typedRepo[T]) Lock(ctx context.Context, id model.ID) (T, error) {
	var zero T
	rec, err := r.g.Lock(ctx, id)
	if err != nil {
		return zero, err
	}
	return r.decode(rec)
}

func (r *typedRepo[T]) List(ctx context.Context, q model.Query) ([]T, model.Page, error) {
	recs, page, err := r.g.List(ctx, q)
	if err != nil {
		return nil, page, err
	}
	out := make([]T, 0, len(recs))
	for _, rec := range recs {
		v, err := r.decode(rec)
		if err != nil {
			return nil, page, err
		}
		out = append(out, v)
	}
	return out, page, nil
}

func (r *typedRepo[T]) Create(ctx context.Context, v T) (T, error) {
	var zero T
	rec, err := r.codec.Encode(v)
	if err != nil {
		return zero, err
	}
	full, err := r.g.Create(ctx, rec)
	if err != nil {
		return zero, err
	}
	return r.decode(full)
}

func (r *typedRepo[T]) Update(ctx context.Context, v T) (T, error) {
	var zero T
	b := r.codec.Base(&v)
	rec, err := r.codec.Encode(v)
	if err != nil {
		return zero, err
	}
	// Carry the optimistic-concurrency identity through to the generic update.
	rec[model.ColID] = b.ID.String()
	rec[model.ColVersion] = b.Version
	full, err := r.g.Update(ctx, rec)
	if err != nil {
		return zero, err
	}
	return r.decode(full)
}

func (r *typedRepo[T]) Delete(ctx context.Context, id model.ID) error {
	return r.g.Delete(ctx, id)
}

// decode parses base fields from a full row record and reconstructs the entity.
func (r *typedRepo[T]) decode(rec model.Record) (T, error) {
	var zero T
	base, err := baseFromRecord(rec)
	if err != nil {
		return zero, err
	}
	return r.codec.Decode(base, rec)
}
