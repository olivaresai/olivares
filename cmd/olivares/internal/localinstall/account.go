// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"errors"
	"fmt"
	"os/user"
	"strconv"
)

const (
	// ImageProfilePath is the image recipe's declaration, read before package installation.
	ImageProfilePath = "/etc/olivares-appliance/image-profile"
	// ImageProfileContent is exactly one Unix line, including its final newline.
	ImageProfileContent  = "service_account=olivares-svc\n"
	ServiceAccountSchema = "olivares.ai/service-account/v1"
)

var (
	ErrImageProfileInvalid = errors.New("IMAGE_PROFILE_INVALID")
	ErrAmbiguousAccount    = errors.New("AMBIGUOUS_ACCOUNT")
	ErrUserMode            = errors.New("USER_MODE")
)

// ImageProfile holds observations of the opened declaration at ImageProfilePath.
// Its reader must reject symlinks and measure content and metadata on the same
// file. An unreadable declaration is an error, never an absent declaration.
// Mode carries only the permission bits (os.FileMode.Perm); the type lives in
// Regular. UID and GID are the owner numbers of the opened file.
type ImageProfile struct {
	Content  []byte
	Regular  bool
	Mode     uint32
	UID, GID uint32
}

// DataDirectoryState describes /var/lib/olivares as observed by the installer.
// Exists means an actual directory, not a symlink or another file type.
type DataDirectoryState struct {
	Exists, Empty bool
	UID, GID      uint32
}

// AccountPresence distinguishes measured absence from a missing observation.
type AccountPresence string

const (
	AccountUnknown AccountPresence = ""
	AccountAbsent  AccountPresence = "absent"
	AccountPresent AccountPresence = "present"
)

// ServiceAccountInput contains read-only observations, not requested identities.
// Nil Manifest and ImageProfile mean absent; readers must propagate read/parse
// errors instead of presenting them as absence. ImageAccount describes only
// olivares-svc and is required only for a new image installation.
type ServiceAccountInput struct {
	Manifest        *Manifest
	ImageProfile    *ImageProfile
	PackageFormat   string
	PackageArgument string
	DataDir         DataDirectoryState
	ImageAccount    AccountPresence
}

// ServiceAccountChoice selects a name, without inventing numeric IDs before
// creation. image-new and non-image are installer decisions, not installed plans.
type ServiceAccountChoice struct {
	User, Group, Class string
}

// SelectServiceAccount applies the closed installation decision table without
// reading or changing the host. A valid split manifest remains authoritative
// after installation; an invalid declaration always refuses before any fallback.
func SelectServiceAccount(in ServiceAccountInput) (ServiceAccountChoice, error) {
	if p := in.ImageProfile; p != nil {
		if string(p.Content) != ImageProfileContent || !p.Regular || p.Mode != 0644 || p.UID != 0 || p.GID != 0 {
			return ServiceAccountChoice{}, ErrImageProfileInvalid
		}
	}
	if m := in.Manifest; m != nil {
		if m.Mode == "user" {
			if in.ImageProfile != nil {
				return ServiceAccountChoice{}, fmt.Errorf("%w: image declaration conflicts with a user installation", ErrAmbiguousAccount)
			}
			return ServiceAccountChoice{}, ErrUserMode
		}
		if err := Validate(m, ""); err != nil {
			return ServiceAccountChoice{}, fmt.Errorf("%w: %w", ErrAmbiguousAccount, err)
		}
		if m.Account.User == "olivares-svc" {
			return ServiceAccountChoice{User: "olivares-svc", Group: "olivares-svc", Class: "image-split"}, nil
		}
		if in.ImageProfile != nil {
			return ServiceAccountChoice{}, fmt.Errorf("%w: image declaration conflicts with the recorded account", ErrAmbiguousAccount)
		}
		// Validate admits only the legacy Linux or launchd account tuple here.
		return ServiceAccountChoice{User: m.Account.User, Group: m.Account.Group, Class: "legacy"}, nil
	}
	if in.ImageProfile == nil {
		return ServiceAccountChoice{User: "olivares", Group: "olivares", Class: "non-image"}, nil
	}
	if in.PackageFormat != "rpm" || in.PackageArgument != "1" ||
		!in.DataDir.Exists || !in.DataDir.Empty || in.DataDir.UID != 0 || in.DataDir.GID != 0 ||
		in.ImageAccount != AccountAbsent {
		return ServiceAccountChoice{}, fmt.Errorf("%w: image installation requires rpm first install, an empty root-owned data directory and no service identity", ErrAmbiguousAccount)
	}
	return ServiceAccountChoice{User: "olivares-svc", Group: "olivares-svc", Class: "image-new"}, nil
}

// AccountLookup observes the requested user and group. The resolver validates
// the returned names, numeric IDs and primary group before exposing a plan.
// Implementations must not create accounts or change ownership.
type AccountLookup func(name, group string) (*user.User, *user.Group, error)

// ServiceAccountPlan is the read-only installed identity consumed by package
// integrations. It contains neither account-creation authority nor a migration plan.
type ServiceAccountPlan struct {
	Schema string `json:"schema"`
	User   string `json:"user"`
	Group  string `json:"group"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Class  string `json:"class"`
}

// ServiceAccount resolves an installed, recorded identity. Selection for a new
// install precedes account creation; this plan follows creation and the durable
// manifest/drop-in write. Installers must not use it to bootstrap those writes.
// The caller owns atomic installation and must not treat validation as proof
// that a recorded drop-in exists or that systemd has loaded it.
func ServiceAccount(in ServiceAccountInput, lookup AccountLookup) (ServiceAccountPlan, error) {
	choice, err := SelectServiceAccount(in)
	if err != nil {
		return ServiceAccountPlan{}, err
	}
	if in.Manifest == nil || lookup == nil {
		return ServiceAccountPlan{}, fmt.Errorf("%w: installed account plan requires a manifest and an account lookup", ErrAmbiguousAccount)
	}
	u, g, err := lookup(choice.User, choice.Group)
	if err != nil {
		return ServiceAccountPlan{}, fmt.Errorf("%w: look up recorded service account: %w", ErrAmbiguousAccount, err)
	}
	if u == nil || g == nil || u.Username != choice.User || g.Name != choice.Group {
		return ServiceAccountPlan{}, fmt.Errorf("%w: lookup does not match the recorded identity", ErrAmbiguousAccount)
	}
	uid, err := serviceID(u.Uid)
	if err != nil {
		return ServiceAccountPlan{}, fmt.Errorf("%w: invalid service UID: %w", ErrAmbiguousAccount, err)
	}
	gid, err := serviceID(g.Gid)
	if err != nil {
		return ServiceAccountPlan{}, fmt.Errorf("%w: invalid service GID: %w", ErrAmbiguousAccount, err)
	}
	primaryGID, err := serviceID(u.Gid)
	if err != nil || primaryGID != gid {
		return ServiceAccountPlan{}, fmt.Errorf("%w: service primary group does not match the recorded group", ErrAmbiguousAccount)
	}
	return ServiceAccountPlan{Schema: ServiceAccountSchema, User: choice.User, Group: choice.Group, UID: uid, GID: gid, Class: choice.Class}, nil
}

func serviceID(value string) (uint32, error) {
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errors.New("service identity must not be root")
	}
	return uint32(n), nil
}
