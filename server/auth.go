package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

// Role is the access level a token grants (spec 16 §7.1).
type Role uint8

const (
	RoleNone      Role = iota
	RoleReader         // read-only: query, get, scroll, list
	RoleReadWrite      // reader plus upsert and delete
	RoleAdmin          // full control: create, drop, reindex, vacuum, backup
)

// parseRole maps a config role string to a Role. An unknown role is RoleNone.
func parseRole(s string) Role {
	switch strings.ToLower(s) {
	case "admin":
		return RoleAdmin
	case "readwrite", "read_write", "rw":
		return RoleReadWrite
	case "reader", "readonly", "read", "ro":
		return RoleReader
	default:
		return RoleNone
	}
}

// Identity is the authenticated caller attached to a request context (spec 16
// §6.5). It carries the role and the collection allow-list the handlers check
// before touching the engine.
type Identity struct {
	TokenID     string
	Role        Role
	Collections []string // empty means every collection
}

// anonymous is the identity used when auth mode is none.
var anonymous = Identity{TokenID: "anonymous", Role: RoleAdmin}

// CanAccess reports whether the identity may touch the named collection.
func (id Identity) CanAccess(collection string) bool {
	if len(id.Collections) == 0 {
		return true
	}
	for _, c := range id.Collections {
		if c == collection {
			return true
		}
	}
	return false
}

// CanRead reports whether the identity may run reads.
func (id Identity) CanRead() bool { return id.Role >= RoleReader }

// CanWrite reports whether the identity may upsert and delete.
func (id Identity) CanWrite() bool { return id.Role >= RoleReadWrite }

// CanAdmin reports whether the identity may run administrative operations.
func (id Identity) CanAdmin() bool { return id.Role >= RoleAdmin }

// Errors returned by the authenticator.
var (
	errNoToken     = errors.New("missing bearer token")
	errBadToken    = errors.New("invalid bearer token")
	errAuthMode    = errors.New("auth mode not supported by this build")
	errPermDenied  = errors.New("permission denied")
	errNoCollAcces = errors.New("token has no access to this collection")
)

// authenticator verifies a token against the static token table. Tokens are
// compared by the SHA-256 of the secret with a constant-time check so a match
// does not leak timing (spec 16 §6.1).
type authenticator struct {
	mode   string
	byHash map[string]Identity
}

// newAuthenticator builds the verifier from the configured tokens.
func newAuthenticator(cfg Config) *authenticator {
	a := &authenticator{mode: cfg.AuthMode, byHash: make(map[string]Identity)}
	for _, t := range cfg.Tokens {
		a.byHash[hashToken(t.Secret)] = Identity{
			TokenID:     t.ID,
			Role:        parseRole(t.Role),
			Collections: t.Collections,
		}
	}
	return a
}

// verify resolves a bearer token to an identity. In mode none every caller is an
// admin; in mode token the token must match a configured secret.
func (a *authenticator) verify(token string) (Identity, error) {
	if a.mode == "none" {
		return anonymous, nil
	}
	if a.mode != "token" {
		return Identity{}, errAuthMode
	}
	if token == "" {
		return Identity{}, errNoToken
	}
	want := hashToken(token)
	for h, id := range a.byHash {
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
			return id, nil
		}
	}
	return Identity{}, errBadToken
}

// bearer extracts the token from an "Authorization: Bearer x" header value.
func bearer(header string) string {
	const p = "Bearer "
	if len(header) > len(p) && strings.EqualFold(header[:len(p)], p) {
		return strings.TrimSpace(header[len(p):])
	}
	return ""
}

// hashToken returns the hex SHA-256 of a token secret.
func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// shortHash returns the first eight hex characters of a string's SHA-256, used
// to label a token in logs without printing the secret.
func shortHash(s string) string { return hashToken(s)[:8] }
