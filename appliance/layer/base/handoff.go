// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"os"
	"path/filepath"

	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

// HostOwnerFile disables cloud-init's network and hostname ownership after verification.
const HostOwnerFile = "/etc/cloud/cloud.cfg.d/99-olivares-host-owner.cfg"
const hostOwnerConfig = "# Managed by Olivares Server after host-settings verification.\nnetwork: {config: disabled}\npreserve_hostname: true\n"

// HostSettingsHandoff durably transfers host settings without changing network profiles.
type HostSettingsHandoff struct{ Host Host }

func (s HostSettingsHandoff) Apply(context.Context, Input) (Effect, error) {
	target := s.Host.path(HostOwnerFile)
	existing, found, err := carriers.ReadProtected(target, 4096)
	if err != nil {
		return "", Refuse("host_owner_file_unreadable")
	}
	if found {
		if string(existing) != hostOwnerConfig {
			return "", Refuse("host_owner_file_conflict")
		}
		effect := Effect("host_settings_owner: appliance")
		return effect, s.Verify(context.Background(), Input{}, effect)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Dir(target), filepath.Base(target), []byte(hostOwnerConfig), 0644); err != nil {
		return "", err
	}
	return Effect("host_settings_owner: appliance"), nil
}

func (s HostSettingsHandoff) Verify(_ context.Context, _ Input, recorded Effect) error {
	info, err := os.Lstat(s.Host.path(HostOwnerFile))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0644 {
		return Refuse("host_owner_file_changed")
	}
	data, _, err := carriers.ReadProtected(s.Host.path(HostOwnerFile), 4096)
	if err != nil || string(data) != hostOwnerConfig || recorded != "host_settings_owner: appliance" {
		return Refuse("host_owner_file_changed")
	}
	return nil
}

// HandOverHostSettings is the package-upgrade entry. The caller holds Store.Lock.
// Only a completed verification grants this one stage; no answers are reloaded and
// no product, firewall or service stage runs. A crash between file and record is safe.
func HandOverHostSettings(ctx context.Context, store Store, step HostSettingsHandoff) (Record, error) {
	rec, found, err := store.Load()
	if err != nil {
		return rec, err
	}
	if !found {
		return rec, Refuse("host_settings_not_verified")
	}
	if _, ok := effectOf(rec, StageHostSettings); !ok {
		return rec, Refuse("host_settings_not_verified")
	}
	if effect, ok := effectOf(rec, StageHostHandoff); ok {
		if rec.HostSettingsOwner != "appliance" {
			return rec, Refuse("host_owner_record_conflict")
		}
		return rec, step.Verify(ctx, Input{}, effect)
	}
	if rec.HostSettingsOwner != "" {
		return rec, Refuse("host_owner_record_conflict")
	}
	effect, err := step.Apply(ctx, Input{})
	if err != nil {
		return rec, err
	}
	rec.Completed = append(rec.Completed, Completed{Stage: StageHostHandoff, Effect: effect})
	rec.HostSettingsOwner = "appliance"
	return rec, (&Machine{Store: store}).save(&rec)
}
