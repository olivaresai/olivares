// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"context"
	"time"
)

// Human is the administrator E12's host layer created and recorded in its host record: the login
// and the uid it was created with.
type Human struct {
	Login string
	UID   uint32
}

// HostRecord reads E12's host record, which names the human its host layer created.
type HostRecord interface {
	RecordedHuman() (Human, error)
}

// AccountRecovery is E12's own account-recovery entry on this console, set-admin-password, which
// sets the credential of the human E12 recorded. Its conversation and its checks are E12's.
type AccountRecovery interface {
	SetAdminCredential(ctx context.Context, human Human) error
}

// recoveryTimeout bounds E12's entry, which holds the terminal while it asks.
const recoveryTimeout = 10 * time.Minute

// WithHostRecord returns the console reading E12's host record through r.
func (c Console) WithHostRecord(r HostRecord) Console {
	c.record = r
	return c
}

// WithAccountRecovery returns the console handing account recovery to E12's entry e.
func (c Console) WithAccountRecovery(e AccountRecovery) Console {
	c.recovery = e
	return c
}

// AccountRecovery hands the first credential of the human E12 recorded to E12's own entry, and
// states what happened. The operator names nobody: the target is the recorded human, and only when
// the host's account of that login is the one E12 created. It never creates a human, which is E12's
// restore-human entry, and it grants no act on this console: Run ends any sign-in before it, and
// the next ordinary act needs a new sign-in with the new credential. Every refusal changes nothing.
func (c Console) AccountRecovery() string {
	const refused = "account recovery: refused: "
	if !c.authority.attestedOnTty1() {
		return refused + "this entry runs only in the repair console's own process on tty1, so nothing was changed."
	}
	if c.record == nil {
		return refused + "E12's host record is not readable by this version, so no human is named and nothing was changed."
	}
	human, err := c.record.RecordedHuman()
	if err != nil {
		return refused + "E12's host record is absent, unreadable or corrupt, so no human is named and nothing was changed."
	}
	if !loginShape(human.Login) || human.Login == "root" || human.UID == 0 {
		return refused + "E12's host record does not name a human its host layer created, so nothing was changed."
	}
	if c.authority.Accounts == nil {
		return refused + "this host's accounts cannot be read, so nothing was changed."
	}
	uid, _, err := c.authority.Accounts.Lookup(human.Login)
	if err != nil {
		return refused + "the human E12 recorded, " + human.Login + ", does not exist on this host; bringing it back is " +
			"E12's own restore-human entry, and this console created nothing."
	}
	if uid != human.UID {
		return refused + "the account named " + human.Login + " is not the human E12 recorded, so nothing was changed."
	}
	if c.recovery == nil {
		return refused + "E12's account-recovery entry is not composed with this console in this version, so nothing was changed."
	}
	ctx, cancel := context.WithTimeout(context.Background(), recoveryTimeout)
	defer cancel()
	if err := c.recovery.SetAdminCredential(ctx, human); err != nil {
		return "account recovery: E12's entry refused or did not finish for " + human.Login +
			", so its credential is unchanged or unknown; read E12's own state before trying again."
	}
	return "account recovery: E12's entry set-admin-password set the credential of " + human.Login +
		", the human E12 recorded. It grants nothing here: sign in with that credential (s) for an ordinary act."
}
