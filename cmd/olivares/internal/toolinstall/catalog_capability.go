// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"reflect"
	"sort"
)

// ProviderCapability is the exact registered install capability for a driver.
// The constructors of CapabilityCatalog are the only code that pair a key with
// a capability; callers must not infer it from optional fields.
type ProviderCapability uint8

const (
	ProviderCapabilityV1 ProviderCapability = iota + 1
	ProviderCapabilityV2
)

// CapabilityCatalog registers exactly one capability per driver: Provider (v1)
// or PackageProviderV2. It wraps the existing v1 Catalog; it does not replace
// it, announce v2 drivers in production, or add methods to Provider.
type CapabilityCatalog struct {
	v1      *Catalog
	v2ByKey map[string]PackageProviderV2
}

// NewCapabilityCatalog builds a capability catalog. A nil v1 is an empty
// NewCatalog(). Duplicate v2 keys, empty keys, untyped or typed-nil providers,
// or a key present in both capabilities refuse as invalid_request. Production
// wiring is not performed here.
func NewCapabilityCatalog(v1 *Catalog, v2 ...PackageProviderV2) (*CapabilityCatalog, error) {
	if v1 == nil {
		v1 = NewCatalog()
	}
	v1Keys := map[string]struct{}{}
	if v1.byKey != nil {
		for k, p := range v1.byKey {
			if k == "" {
				return nil, refuse(KindInvalidRequest, "v1 provider key is empty")
			}
			if nillableValueIsNil(p) {
				return nil, refuse(KindInvalidRequest, "v1 provider %q is nil", k)
			}
			v1Keys[k] = struct{}{}
		}
	}
	c := &CapabilityCatalog{v1: v1, v2ByKey: map[string]PackageProviderV2{}}
	for i, p := range v2 {
		if nillableValueIsNil(p) {
			return nil, refuse(KindInvalidRequest, "v2 provider at index %d is nil", i)
		}
		key := p.Key()
		if key == "" {
			return nil, refuse(KindInvalidRequest, "v2 provider at index %d has an empty key", i)
		}
		if _, dup := c.v2ByKey[key]; dup {
			return nil, refuse(KindInvalidRequest, "v2 provider key %q is registered more than once", key)
		}
		if _, clash := v1Keys[key]; clash {
			return nil, refuse(KindInvalidRequest, "provider key %q collides across v1 and v2 capabilities", key)
		}
		c.v2ByKey[key] = p
	}
	return c, nil
}

// Capability reports the registered capability for driver. A missing driver is
// unsupported_provider and names the sorted union of keys. A key present in
// both maps, or a stored untyped/typed-nil provider (possible if a wrapped v1
// catalog is mutated later), is invalid_request, not unsupported_provider.
func (c *CapabilityCatalog) Capability(driver string) (ProviderCapability, error) {
	_, inv1, has1 := c.inspectV1(driver)
	_, inv2, has2 := c.inspectV2(driver)
	if inv1 {
		return 0, refuse(KindInvalidRequest, "driver %q has a nil v1 provider", driver)
	}
	if inv2 {
		return 0, refuse(KindInvalidRequest, "driver %q has a nil v2 provider", driver)
	}
	if has1 && has2 {
		return 0, refuse(KindInvalidRequest, "provider key %q collides across v1 and v2 capabilities", driver)
	}
	if has1 {
		return ProviderCapabilityV1, nil
	}
	if has2 {
		return ProviderCapabilityV2, nil
	}
	return 0, c.unsupported(driver)
}

// LookupV1 returns the v1 Provider after capability admission. A driver
// registered only as v2 is invalid_request and names the registered versus
// expected schema. It does not call Resolve or ResolveV2.
func (c *CapabilityCatalog) LookupV1(driver string) (Provider, error) {
	capab, err := c.Capability(driver)
	if err != nil {
		return nil, err
	}
	if capab != ProviderCapabilityV1 {
		return nil, c.wrongCapability(driver, capab, ProviderCapabilityV1)
	}
	p, err := c.v1.Lookup(driver)
	if err != nil {
		return nil, err
	}
	if nillableValueIsNil(p) {
		return nil, refuse(KindInvalidRequest, "driver %q has a nil v1 provider", driver)
	}
	return p, nil
}

// LookupV2 returns the PackageProviderV2 after capability admission. A driver
// registered only as v1 is invalid_request and names the registered versus
// expected schema. It does not call Resolve or ResolveV2.
func (c *CapabilityCatalog) LookupV2(driver string) (PackageProviderV2, error) {
	capab, err := c.Capability(driver)
	if err != nil {
		return nil, err
	}
	if capab != ProviderCapabilityV2 {
		return nil, c.wrongCapability(driver, capab, ProviderCapabilityV2)
	}
	p := c.v2ByKey[driver]
	if nillableValueIsNil(p) {
		return nil, refuse(KindInvalidRequest, "driver %q has a nil v2 provider", driver)
	}
	return p, nil
}

// Keys returns the unique sorted union of v1 and v2 driver keys.
func (c *CapabilityCatalog) Keys() []string {
	n := 0
	if c != nil && c.v1 != nil && c.v1.byKey != nil {
		n += len(c.v1.byKey)
	}
	if c != nil {
		n += len(c.v2ByKey)
	}
	keys := make([]string, 0, n)
	seen := map[string]struct{}{}
	if c != nil && c.v1 != nil && c.v1.byKey != nil {
		for k := range c.v1.byKey {
			if k == "" {
				continue
			}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	if c != nil {
		for k := range c.v2ByKey {
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func (c *CapabilityCatalog) inspectV1(driver string) (p Provider, invalid, present bool) {
	if c == nil || c.v1 == nil || c.v1.byKey == nil {
		return nil, false, false
	}
	p, ok := c.v1.byKey[driver]
	if !ok {
		return nil, false, false
	}
	if nillableValueIsNil(p) {
		return nil, true, true
	}
	return p, false, true
}

func (c *CapabilityCatalog) inspectV2(driver string) (p PackageProviderV2, invalid, present bool) {
	if c == nil || c.v2ByKey == nil {
		return nil, false, false
	}
	p, ok := c.v2ByKey[driver]
	if !ok {
		return nil, false, false
	}
	if nillableValueIsNil(p) {
		return nil, true, true
	}
	return p, false, true
}

// nillableValueIsNil reports an untyped nil or a typed-nil nillable value
// (pointer, interface, slice, map, chan, or func). It does not call methods
// on v and does not recover from panics.
func nillableValueIsNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func (c *CapabilityCatalog) unsupported(driver string) error {
	return refuse(KindUnsupportedProvider,
		"driver %q has no installer in this release (registered: %v)",
		driver, c.Keys())
}

func (c *CapabilityCatalog) wrongCapability(driver string, have, want ProviderCapability) error {
	return refuse(KindInvalidRequest,
		"driver %q is registered for schema %q; this lookup expects %q",
		driver, schemaOfCapability(have), schemaOfCapability(want))
}

func schemaOfCapability(c ProviderCapability) string {
	switch c {
	case ProviderCapabilityV1:
		return PlanSchema
	case ProviderCapabilityV2:
		return PlanSchemaV2
	default:
		return ""
	}
}
