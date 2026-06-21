package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// JWT validation is implemented over the standard library: a token is three
// base64url segments and a signature, so HS256, RS256, and ES256 need only
// crypto/hmac, crypto/rsa, and crypto/ecdsa (spec 23 section 6.3). vec does not
// pull in a third-party JWT library.

// JWTConfig configures token validation (spec 23 section 6.3).
type JWTConfig struct {
	Algorithm        string // "HS256", "RS256", "ES256"
	SharedSecret     []byte // HS256
	RSAPublicKey     *rsa.PublicKey
	ECDSAPublicKey   *ecdsa.PublicKey
	Audience         string
	Issuer           string
	CollectionsClaim string // default "vec_collections"
	RolesClaim       string // default "vec_role"
}

// Claims is the subset of registered and custom claims vec reads.
type Claims struct {
	Subject     string
	Audience    string
	Issuer      string
	ExpiresAt   int64
	Collections []string
	Role        Role
}

var (
	errMalformedToken = errors.New("vec/auth: malformed JWT")
	errBadSignature   = errors.New("vec/auth: JWT signature invalid")
	errTokenExpired   = errors.New("vec/auth: JWT expired")
	errBadClaim       = errors.New("vec/auth: JWT claim mismatch")
)

// ValidateJWT checks a token's signature, expiry, audience, and issuer, then
// extracts the principal claims (spec 23 section 6.3). The reason codes it
// returns (expired, bad signature, bad claim) are safe to surface to a client.
func (c JWTConfig) ValidateJWT(token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errMalformedToken
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, errMalformedToken
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return Claims{}, errMalformedToken
	}
	if header.Alg != c.Algorithm {
		return Claims{}, fmt.Errorf("vec/auth: JWT alg %q does not match configured %q", header.Alg, c.Algorithm)
	}

	signingInput := []byte(parts[0] + "." + parts[1])
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, errMalformedToken
	}
	if err := c.verifySignature(signingInput, sig); err != nil {
		return Claims{}, err
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, errMalformedToken
	}
	return c.extractClaims(payload, now)
}

func (c JWTConfig) verifySignature(signingInput, sig []byte) error {
	switch c.Algorithm {
	case "HS256":
		mac := hmac.New(sha256.New, c.SharedSecret)
		mac.Write(signingInput)
		if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
			return errBadSignature
		}
		return nil
	case "RS256":
		if c.RSAPublicKey == nil {
			return errors.New("vec/auth: RS256 configured without an RSA public key")
		}
		sum := sha256.Sum256(signingInput)
		if err := rsa.VerifyPKCS1v15(c.RSAPublicKey, crypto.SHA256, sum[:], sig); err != nil {
			return errBadSignature
		}
		return nil
	case "ES256":
		if c.ECDSAPublicKey == nil {
			return errors.New("vec/auth: ES256 configured without an ECDSA public key")
		}
		if len(sig) != 64 {
			return errBadSignature
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		sum := sha256.Sum256(signingInput)
		if !ecdsa.Verify(c.ECDSAPublicKey, sum[:], r, s) {
			return errBadSignature
		}
		return nil
	default:
		return fmt.Errorf("vec/auth: unsupported JWT algorithm %q", c.Algorithm)
	}
}

func (c JWTConfig) extractClaims(payload []byte, now time.Time) (Claims, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, errMalformedToken
	}

	out := Claims{}
	out.Subject = stringClaim(raw, "sub")
	out.Issuer = stringClaim(raw, "iss")
	out.Audience = stringClaim(raw, "aud")

	if v, ok := raw["exp"]; ok {
		var exp int64
		if err := json.Unmarshal(v, &exp); err == nil {
			out.ExpiresAt = exp
			if now.Unix() >= exp {
				return Claims{}, errTokenExpired
			}
		}
	}
	if c.Audience != "" && out.Audience != c.Audience {
		return Claims{}, fmt.Errorf("%w: audience", errBadClaim)
	}
	if c.Issuer != "" && out.Issuer != c.Issuer {
		return Claims{}, fmt.Errorf("%w: issuer", errBadClaim)
	}

	collClaim := c.CollectionsClaim
	if collClaim == "" {
		collClaim = "vec_collections"
	}
	if v, ok := raw[collClaim]; ok {
		_ = json.Unmarshal(v, &out.Collections)
	}
	roleClaim := c.RolesClaim
	if roleClaim == "" {
		roleClaim = "vec_role"
	}
	out.Role = Role(stringClaim(raw, roleClaim))
	return out, nil
}

func stringClaim(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

// Principal returns the JWT subject as an authenticated principal.
func (cl Claims) Principal() Principal {
	return Principal{ID: cl.Subject, Kind: "jwt"}
}
