// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// rtbf_depth.go is the public half of enterprise RTBF-depth seam. The
// commercial coordinator lives in enterprise/rtbf and is build-tag gated outside
// this repo; compliance consumes it without importing it.

func (m *Module) validateCryptoShredReadiness(ctx context.Context, tenant model.TenantID, subjectKind, subjectRef string) (CryptoShredReadiness, bool, error) {
	if m.shredCoordinator == nil {
		return CryptoShredReadiness{}, false, nil
	}
	if c, ok := m.shredCoordinator.(CryptoShredCoordinator); ok {
		r, err := c.ValidateShredReadiness(ctx, tenant.String(), subjectKind, subjectRef)
		return r, true, err
	}
	out, err := callReflect(m.shredCoordinator, "ValidateShredReadiness",
		[]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(tenant.String()), reflect.ValueOf(subjectKind), reflect.ValueOf(subjectRef)})
	if err != nil {
		return CryptoShredReadiness{}, true, err
	}
	if len(out) != 2 {
		return CryptoShredReadiness{}, true, errors.New("rtbf coordinator readiness returned an unexpected result shape")
	}
	return readinessFromValue(out[0]), true, errorFromValue(out[1])
}

func (m *Module) notifyCryptoShredWORM(ctx context.Context, keyID string, at time.Time) (bool, error) {
	if m.shredCoordinator == nil {
		return false, nil
	}
	if c, ok := m.shredCoordinator.(CryptoShredCoordinator); ok {
		return true, c.NotifyWORMSinks(ctx, keyID, at)
	}
	out, err := callReflect(m.shredCoordinator, "NotifyWORMSinks",
		[]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(keyID), reflect.ValueOf(at)})
	if err != nil {
		return true, err
	}
	if len(out) != 1 {
		return true, errors.New("rtbf coordinator WORM notification returned an unexpected result shape")
	}
	return true, errorFromValue(out[0])
}

func (m *Module) verifyCryptoShredCompleteness(ctx context.Context, keyID string, targets []string, probes CryptoShredProbes) (CryptoShredVerification, bool, error) {
	if m.shredCoordinator == nil {
		return CryptoShredVerification{}, false, nil
	}
	if c, ok := m.shredCoordinator.(CryptoShredCoordinator); ok {
		v, err := c.VerifyShredCompleteness(ctx, keyID, targets, probes)
		return v, true, err
	}
	// The reflect adapter passes the probes struct by value; an implementation
	// that cannot accept it (a pre shape) fails the assignability check in
	// callReflect and the erasure fails closed — it never runs an unverifiable
	// coordinator silently.
	out, err := callReflect(m.shredCoordinator, "VerifyShredCompleteness",
		[]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(keyID), reflect.ValueOf(targets), reflect.ValueOf(probes)})
	if err != nil {
		return CryptoShredVerification{}, true, err
	}
	if len(out) != 2 {
		return CryptoShredVerification{}, true, errors.New("rtbf coordinator verification returned an unexpected result shape")
	}
	return verificationFromValue(out[0]), true, errorFromValue(out[1])
}

// CryptoShredProbesFor returns evidence probes bound to the module's LIVE store
// (each probe opens its own read view) for a POST-HOC re-verification of a past
// shred: KeyGone re-checks that no live subject-key row exists for (subjectKind,
// any of refs); ResidualScan re-runs the registry scan for those identifiers.
// Empty classes default to every class applicable to the subject kind (the same
// affectedClasses default the erasure create path uses) — a re-verification
// scans EVERYTHING unless the caller narrows it to the erasure's actual scope.
// The composition root and the wire-proof integration tests use it to prove a
// coordinator verdict against the real store OUTSIDE the erasure transaction —
// the in-transaction probes the execute path binds are the module's own
// (erasure.go, the shred transaction).
func (m *Module) CryptoShredProbesFor(tenant model.TenantID, subjectKind string, refs []string, classes []string) CryptoShredProbes {
	if len(classes) == 0 {
		classes = affectedClasses(subjectKind)
	}
	return CryptoShredProbes{
		KeyGone: func(ctx context.Context) (bool, error) {
			gone := true
			err := m.data.View(ctx, tenant, func(sc store.Scope) error {
				for _, ref := range refs {
					_, ok, err := findSubjectKey(ctx, sc, subjectKind, ref)
					if err != nil {
						return err
					}
					if ok {
						gone = false
						return nil
					}
				}
				return nil
			})
			if err != nil {
				return false, err
			}
			return gone, nil
		},
		ResidualScan: func(ctx context.Context) ([]string, int, error) {
			var residues []string
			var scanned int
			err := m.data.View(ctx, tenant, func(sc store.Scope) error {
				var verr error
				residues, scanned, verr = residualScanIn(ctx, sc, subjectKind, refs, classes)
				return verr
			})
			if err != nil {
				return nil, 0, err
			}
			return residues, scanned, nil
		},
	}
}

func callReflect(target any, method string, args []reflect.Value) ([]reflect.Value, error) {
	v := reflect.ValueOf(target)
	if !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return nil, errors.New("rtbf coordinator is nil")
	}
	m := v.MethodByName(method)
	if !m.IsValid() {
		return nil, fmt.Errorf("rtbf coordinator does not expose %s", method)
	}
	mt := m.Type()
	if mt.NumIn() != len(args) {
		return nil, fmt.Errorf("rtbf coordinator %s has %d args, want %d", method, mt.NumIn(), len(args))
	}
	for i, arg := range args {
		if !arg.Type().AssignableTo(mt.In(i)) {
			return nil, fmt.Errorf("rtbf coordinator %s arg %d has %s, want %s", method, i, arg.Type(), mt.In(i))
		}
	}
	return m.Call(args), nil
}

func errorFromValue(v reflect.Value) error {
	if !v.IsValid() {
		return nil
	}
	if err, ok := v.Interface().(error); ok {
		return err
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil
		}
	}
	return fmt.Errorf("rtbf coordinator returned non-error %s", v.Type())
}

func readinessFromValue(v reflect.Value) CryptoShredReadiness {
	v = deref(v)
	if !v.IsValid() {
		return CryptoShredReadiness{}
	}
	return CryptoShredReadiness{
		Ready:         boolField(v, "Ready"),
		Blockers:      blockersField(v, "Blockers"),
		Warnings:      stringSliceField(v, "Warnings"),
		PolicyApplied: stringField(v, "PolicyApplied"),
	}
}

func verificationFromValue(v reflect.Value) CryptoShredVerification {
	v = deref(v)
	if !v.IsValid() {
		return CryptoShredVerification{}
	}
	return CryptoShredVerification{
		Complete:      boolField(v, "Complete"),
		KeyDestroyed:  boolField(v, "KeyDestroyed"),
		WORMNotified:  boolField(v, "WORMNotified"),
		ResidualScan:  residualScanFromValue(field(v, "ResidualScan")),
		Unverified:    stringSliceField(v, "Unverified"),
		PolicyApplied: stringField(v, "PolicyApplied"),
	}
}

func residualScanFromValue(v reflect.Value) CryptoShredResidualScan {
	v = deref(v)
	if !v.IsValid() {
		return CryptoShredResidualScan{}
	}
	return CryptoShredResidualScan{
		ScanDepth:      stringField(v, "ScanDepth"),
		TargetsScanned: int(intField(v, "TargetsScanned")),
		ResiduesFound:  int(intField(v, "ResiduesFound")),
		Residues:       stringSliceField(v, "Residues"),
		Clean:          boolField(v, "Clean"),
	}
}

func blockersField(v reflect.Value, name string) []CryptoShredBlocker {
	f := field(v, name)
	if !f.IsValid() || f.Kind() != reflect.Slice {
		return nil
	}
	out := make([]CryptoShredBlocker, 0, f.Len())
	for i := 0; i < f.Len(); i++ {
		item := deref(f.Index(i))
		out = append(out, CryptoShredBlocker{
			Kind:   stringField(item, "Kind"),
			Detail: stringField(item, "Detail"),
		})
	}
	return out
}

func stringSliceField(v reflect.Value, name string) []string {
	f := field(v, name)
	if !f.IsValid() || f.Kind() != reflect.Slice {
		return nil
	}
	out := make([]string, 0, f.Len())
	for i := 0; i < f.Len(); i++ {
		if f.Index(i).Kind() == reflect.String {
			out = append(out, f.Index(i).String())
		}
	}
	return out
}

func stringField(v reflect.Value, name string) string {
	f := field(v, name)
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}

func boolField(v reflect.Value, name string) bool {
	f := field(v, name)
	if !f.IsValid() || f.Kind() != reflect.Bool {
		return false
	}
	return f.Bool()
}

func intField(v reflect.Value, name string) int64 {
	f := field(v, name)
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return f.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(f.Uint())
	default:
		return 0
	}
}

func field(v reflect.Value, name string) reflect.Value {
	v = deref(v)
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return v.FieldByName(name)
}

func deref(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}
