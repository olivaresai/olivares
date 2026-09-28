// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !(linux && (amd64 || arm64))

package localsession

import (
	"errors"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

type Linux struct{ invocation.Linux }

func (Linux) PassCred(int) (bool, error) {
	return false, errors.New("local API requires Linux amd64 or arm64")
}
func (Linux) Admin(uint32) (bool, error) {
	return false, errors.New("local API requires Linux amd64 or arm64")
}
func (Linux) Unread(int) (int, error) {
	return 0, errors.New("local API requires Linux amd64 or arm64")
}
