// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

var pepServiceDescriptor = model.EntityDescriptor{
	Kind:  "core.pep_service",
	Table: "pep_services",
	Fields: []model.FieldSpec{
		field("target_tenant_id", model.KindUUID, false),
		field("name", model.KindText, false),
		field("pdp_audience", model.KindText, false),
		field("capabilities", model.KindJSON, true),
		field("capability_version", model.KindInt, false),
		field("disabled_at", model.KindTimestamp, true),
	},
	Indexes: []model.IndexSpec{
		{
			Name:    "pep_services_name_uniq",
			Columns: []string{"tenant_id", "target_tenant_id", "name"},
			Unique:  true,
		},
	},
}

var pepServiceCodec = model.Codec[model.PEPService]{
	Base: func(s *model.PEPService) *model.BaseFields { return &s.BaseFields },
	Encode: func(s model.PEPService) (model.Record, error) {
		capabilities, err := encBools(s.Capabilities)
		if err != nil {
			return nil, err
		}
		return model.Record{
			"target_tenant_id": encTenant(s.TargetTenantID),
			"name":             s.Name, "pdp_audience": s.PDPAudience, "capabilities": capabilities,
			"capability_version": int64(s.CapabilityVersion), "disabled_at": encOptTS(s.DisabledAt),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.PEPService, error) {
		capabilities, err := decBools(r, "capabilities")
		if err != nil {
			return model.PEPService{}, err
		}
		disabled, err := decOptTS(r, "disabled_at")
		if err != nil {
			return model.PEPService{}, err
		}
		return model.PEPService{
			BaseFields: b, TargetTenantID: decTenant(r, "target_tenant_id"),
			Name: r.String("name"), PDPAudience: r.String("pdp_audience"),
			Capabilities: capabilities, CapabilityVersion: int(r.Int("capability_version")),
			DisabledAt: disabled,
		}, nil
	},
}
