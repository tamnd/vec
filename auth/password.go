package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"time"

	"golang.org/x/crypto/argon2"
)

// UserAccount is a local password account for deployments without an external
// identity provider (spec 23 section 6.5). The password is stored as an Argon2id
// hash with a per-user salt.
type UserAccount struct {
	Username    string
	Hash        []byte
	Salt        []byte
	Collections []string
	Role        Role
	Locked      bool
}

// NewUserAccount hashes a password and returns a ready account record.
func NewUserAccount(username, password string, role Role, collections []string) (UserAccount, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return UserAccount{}, err
	}
	return UserAccount{
		Username:    username,
		Hash:        argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32),
		Salt:        salt,
		Collections: collections,
		Role:        role,
	}, nil
}

// VerifyPassword reports whether a password matches the account, running the full
// Argon2id computation and a constant-time compare with no short circuit, so the
// response time does not leak whether the user exists or the hash partly matched
// (spec 23 section 17.4).
func (u UserAccount) VerifyPassword(password string) bool {
	candidate := argon2.IDKey([]byte(password), u.Salt, 3, 64*1024, 4, 32)
	return subtle.ConstantTimeCompare(candidate, u.Hash) == 1 && !u.Locked
}

// Backoff tracks consecutive authentication failures per principal and computes
// the exponential delay and lockout from spec 23 section 17.4. It is the
// online-brute-force defense for the local account store; it does not lock API
// keys, which the server rate-limits at the IP level instead.
type Backoff struct {
	BaseDelay time.Duration // default 100ms
	MaxShift  uint          // cap on the exponent, default 10
	LockAfter int           // failures before lockout, default 20

	failures map[string]int
}

// NewBackoff builds a Backoff with the spec defaults.
func NewBackoff() *Backoff {
	return &Backoff{
		BaseDelay: 100 * time.Millisecond,
		MaxShift:  10,
		LockAfter: 20,
		failures:  make(map[string]int),
	}
}

// Fail records a failed attempt and returns the delay to apply before the next
// response and whether the account should now be locked.
func (b *Backoff) Fail(principal string) (delay time.Duration, lock bool) {
	b.failures[principal]++
	n := b.failures[principal]
	shift := uint(n)
	if shift > b.MaxShift {
		shift = b.MaxShift
	}
	delay = b.BaseDelay << shift
	return delay, n >= b.LockAfter
}

// Reset clears the failure count for a principal after a successful login.
func (b *Backoff) Reset(principal string) {
	delete(b.failures, principal)
}

// Failures returns the current consecutive-failure count for a principal.
func (b *Backoff) Failures(principal string) int {
	return b.failures[principal]
}
