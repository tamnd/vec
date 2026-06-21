package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// keyPrefix is the sentinel that lets the server reject non-key bearer tokens
// fast, before it spends an Argon2id verification (spec 23 section 6.2).
const keyPrefix = "vec_"

// APIKey is a stored key record (spec 23 section 6.2). The raw key is never
// stored: only its Argon2id hash and the per-key salt, so a stolen key store does
// not yield usable keys.
type APIKey struct {
	Label       string
	Hash        []byte
	Salt        []byte
	Collections []string // allowed collection globs, nil means all
	Operations  []Op     // allowed operations
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// GenerateAPIKey mints a new 256-bit key and returns the record plus the raw key
// string (spec 23 section 6.2). The raw key is shown to the operator once and is
// not recoverable from the record.
func GenerateAPIKey(label string, collections []string, ops []Op, createdAt time.Time) (APIKey, string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return APIKey{}, "", err
	}
	raw := keyPrefix + base64.RawURLEncoding.EncodeToString(secret)
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return APIKey{}, "", err
	}
	rec := APIKey{
		Label:       label,
		Hash:        hashKey(raw, salt),
		Salt:        salt,
		Collections: collections,
		Operations:  ops,
		CreatedAt:   createdAt,
	}
	return rec, raw, nil
}

// hashKey derives the Argon2id hash of a raw key under a salt. The parameters
// match the database key derivation defaults (spec 23 section 6.5).
func hashKey(raw string, salt []byte) []byte {
	return argon2.IDKey([]byte(raw), salt, 3, 64*1024, 4, 32)
}

// Verify reports whether a presented bearer token matches this key record, using
// a constant-time comparison and always running the full Argon2id computation to
// avoid a timing oracle (spec 23 section 17.4). A non-key token is rejected by the
// prefix check.
func (k APIKey) Verify(token string, now time.Time) bool {
	if !strings.HasPrefix(token, keyPrefix) {
		return false
	}
	candidate := hashKey(token, k.Salt)
	if subtle.ConstantTimeCompare(candidate, k.Hash) != 1 {
		return false
	}
	if !k.ExpiresAt.IsZero() && now.After(k.ExpiresAt) {
		return false
	}
	return true
}

// HasPrefix reports whether a token looks like an API key (spec 23 section 6.2).
// The server uses this to route a bearer token to the key path versus the JWT
// path.
func HasPrefix(token string) bool {
	return strings.HasPrefix(token, keyPrefix)
}

// KeyStore holds API key records and resolves a presented token to a principal.
type KeyStore struct {
	keys []APIKey
}

// NewKeyStore builds a key store over a set of records.
func NewKeyStore(keys []APIKey) *KeyStore {
	return &KeyStore{keys: keys}
}

// Authenticate verifies a bearer token against every record and returns the
// matching principal (spec 23 section 6.2). It checks every key so the response
// time does not reveal which label matched.
func (s *KeyStore) Authenticate(token string, now time.Time) (Principal, bool) {
	matched := false
	var label string
	for _, k := range s.keys {
		if k.Verify(token, now) {
			matched = true
			label = k.Label
		}
	}
	if !matched {
		return Principal{}, false
	}
	return Principal{ID: label, Kind: "apikey"}, true
}
