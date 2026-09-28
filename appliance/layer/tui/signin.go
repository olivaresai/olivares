// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
)

// LocalService is the sign-in service this console opens its transactions on: the
// distribution's own local-console service, the one its text login on a virtual console uses. It
// is independent of the portal's stack, auth.PAMService, so the failure this console exists to
// repair, a broken portal stack, cannot also close this door.
const LocalService = "login"

// IdleLimit is how long a sign-in lasts without input from its operator.
const IdleLimit = 5 * time.Minute

// LocalStack opens transactions on the host's sign-in services. A transaction carries the
// operator's credential from the terminal through the service's own conversation, so this console
// never holds it.
type LocalStack interface {
	Start(service, login string) (auth.Transaction, error)
}

// Accounts is the host's account database as this console reads it: a login's uid and the names of
// every group the host records it in. It is read again at every act.
type Accounts interface {
	Lookup(login string) (uid uint32, groups []string, err error)
}

// Terminal attests this console's own process: its uid and account, the unit its cgroup names and
// its controlling terminal, as the kernel states them.
type Terminal interface {
	Attest() (helperschema.Peer, error)
}

// ErrSignInRefused is every refused sign-in, whichever check refused it.
var ErrSignInRefused = errors.New("the sign-in is refused")

// Authority signs an operator in on this console. Physical access to the console alone grants
// nothing: the console's own process must be the repair console, root in its unit on /dev/tty1;
// the login must be a login; the local-console service must authenticate the operator and admit
// the account; and the login must be root or a current member of olivares-admins.
type Authority struct {
	Stack    LocalStack
	Accounts Accounts
	Terminal Terminal
	Now      func() time.Time
	Random   io.Reader
}

// attestedOnTty1 reports whether this console's own process is the repair console on tty1.
func (a Authority) attestedOnTty1() bool {
	if a.Terminal == nil {
		return false
	}
	peer, err := a.Terminal.Attest()
	return err == nil && peer.Attested && peer.UID == 0 && peer.Account == helperschema.RepairConsole.Account &&
		peer.Unit == helperschema.RepairConsole.Unit && peer.TTY == helperschema.RepairConsole.TTY
}

// SignIn signs login in, or refuses with ErrSignInRefused. Nothing reaches the sign-in service
// before the console's own process is attested and the login has a login's shape; the transaction
// is closed exactly once, whatever it answered.
func (a Authority) SignIn(login string) (*Session, error) {
	if !a.attestedOnTty1() || !loginShape(login) || a.Stack == nil || a.Accounts == nil || a.Now == nil {
		return nil, ErrSignInRefused
	}
	transaction, err := a.Stack.Start(LocalService, login)
	if err != nil || transaction == nil {
		return nil, ErrSignInRefused
	}
	if err := verify(transaction); err != nil {
		return nil, ErrSignInRefused
	}
	uid, groups, err := a.Accounts.Lookup(login)
	if err != nil || !administrator(uid, groups) {
		return nil, ErrSignInRefused
	}
	random := a.Random
	if random == nil {
		random = rand.Reader
	}
	var raw [16]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return nil, ErrSignInRefused
	}
	return &Session{login: login, uid: uid, ref: hex.EncodeToString(raw[:]), last: a.Now(), accounts: a.Accounts, now: a.Now}, nil
}

// verify runs the transaction's auth and account checks and closes it exactly once.
func verify(transaction auth.Transaction) error {
	defer func() { _ = transaction.Close() }()
	if err := transaction.Authenticate(); err != nil {
		return err
	}
	return transaction.Account()
}

// administrator reports whether an account may act on this console: root, or a member of
// olivares-admins.
func administrator(uid uint32, groups []string) bool {
	return uid == 0 || slices.Contains(groups, auth.AdministratorsGroup)
}

// loginShape reports whether s is a login: 1 to 32 lowercase letters, digits, underscores and
// hyphens, starting with a letter or an underscore.
func loginShape(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c == '_':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// Session is one qualified sign-in. It holds no credential: the login, its uid, a random audit
// reference, and the time of its operator's last input. It ends on End, after five minutes without
// input, and when its login stops being root or a member of olivares-admins or comes to name
// another uid; an ended session is never qualified again.
type Session struct {
	login    string
	uid      uint32
	ref      string
	last     time.Time
	ended    bool
	accounts Accounts
	now      func() time.Time
}

// Qualified implements SignIn. It reads the account's membership again each time it is asked.
func (s *Session) Qualified() bool {
	if s == nil || s.ended {
		return false
	}
	if s.now().Sub(s.last) > IdleLimit {
		s.ended = true
		return false
	}
	uid, groups, err := s.accounts.Lookup(s.login)
	if err != nil || uid != s.uid || !administrator(uid, groups) {
		s.ended = true
		return false
	}
	return true
}

// activity records the operator's input. It reports false, and ends the session, when the input
// came after five minutes without any.
func (s *Session) activity() bool {
	if s == nil || s.ended {
		return false
	}
	if s.now().Sub(s.last) > IdleLimit {
		s.ended = true
		return false
	}
	s.last = s.now()
	return true
}

// End ends the session.
func (s *Session) End() {
	if s != nil {
		s.ended = true
	}
}

// Login is the login signed in.
func (s *Session) Login() string { return s.login }

// Ref is the session's non-secret audit reference: 128 random bits in lowercase hexadecimal.
func (s *Session) Ref() string { return s.ref }

// WithAuthority returns the console signing its operator in through a.
func (c Console) WithAuthority(a Authority) Console {
	c.authority = a
	return c
}
