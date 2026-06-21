package vec

// Secret types redact themselves on every serialization path so a key never
// reaches a log line, an error string, or a config dump (spec 23 section 12.3).
// The most common operational mistake is logging the encryption key by passing a
// struct that holds it to a structured logger; these types make that safe by
// construction. They implement fmt.Stringer, fmt.GoStringer, and json.Marshaler,
// which is every path the standard logger and encoding/json take.

const redacted = "[REDACTED]"

// EncryptionKey is a raw key that redacts itself. The real bytes are reachable
// only through Bytes, which the crypto package calls explicitly; nothing prints
// them.
type EncryptionKey []byte

func (EncryptionKey) String() string               { return redacted }
func (EncryptionKey) GoString() string             { return "vec.EncryptionKey([REDACTED])" }
func (EncryptionKey) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Bytes returns the underlying key material. Callers must not log or store the
// result; it exists so the crypto layer can derive the master key.
func (k EncryptionKey) Bytes() []byte { return []byte(k) }

// Passphrase is a human-entered secret that redacts itself.
type Passphrase string

func (Passphrase) String() string               { return redacted }
func (Passphrase) GoString() string             { return "vec.Passphrase([REDACTED])" }
func (Passphrase) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Reveal returns the passphrase text. Used only at the key-derivation call site.
func (p Passphrase) Reveal() string { return string(p) }

// APIKey is a server bearer token that redacts itself. It is the wire form an
// operator pastes into a client; the auth package stores only its hash.
type APIKey string

func (APIKey) String() string               { return redacted }
func (APIKey) GoString() string             { return "vec.APIKey([REDACTED])" }
func (APIKey) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Reveal returns the token text.
func (k APIKey) Reveal() string { return string(k) }

// JWTSecret is the HS256 shared signing secret that redacts itself.
type JWTSecret []byte

func (JWTSecret) String() string               { return redacted }
func (JWTSecret) GoString() string             { return "vec.JWTSecret([REDACTED])" }
func (JWTSecret) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Bytes returns the underlying secret for the JWT validator.
func (s JWTSecret) Bytes() []byte { return []byte(s) }

// RedactFieldKey reports whether a structured-log field name names a secret and
// its value should be replaced with [REDACTED] (spec 23 section 12.3 item 2). The
// match is case-insensitive on the substrings key, secret, password, token, and
// passphrase. The logger calls this before writing each field.
func RedactFieldKey(name string) bool {
	lower := toLowerASCII(name)
	for _, needle := range []string{"key", "secret", "password", "token", "passphrase"} {
		if containsSub(lower, needle) {
			return true
		}
	}
	return false
}

// toLowerASCII lowercases an ASCII string without allocating through the unicode
// tables; field names are ASCII identifiers.
func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func containsSub(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
