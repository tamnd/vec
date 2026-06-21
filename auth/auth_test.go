package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

func TestRoleAllows(t *testing.T) {
	if !RoleReader.Allows(OpSearch) {
		t.Fatal("reader should allow search")
	}
	if RoleReader.Allows(OpInsert) {
		t.Fatal("reader must not allow insert")
	}
	if !RoleWriter.Allows(OpInsert) {
		t.Fatal("writer should allow insert")
	}
	if RoleWriter.Allows(OpDropColl) {
		t.Fatal("writer must not drop collections")
	}
	if !RoleAdmin.Allows(OpDropColl) {
		t.Fatal("admin should drop collections")
	}
	if RoleAdmin.Allows(OpKeyManagement) {
		t.Fatal("admin must not manage keys")
	}
	if !RoleKeyAdmin.Allows(OpKeyManagement) {
		t.Fatal("key_admin should manage keys")
	}
	if RoleKeyAdmin.Allows(OpSearch) {
		t.Fatal("key_admin must not search")
	}
	if !RoleSuperuser.Allows(OpServerConfig) {
		t.Fatal("superuser should allow everything")
	}
}

func TestACLCheck(t *testing.T) {
	acl := NewACL()
	if err := acl.Grant("alice", RoleReader, "docs_*"); err != nil {
		t.Fatal(err)
	}
	p := Principal{ID: "alice", Kind: "apikey"}
	if err := acl.Check(p, OpSearch, "docs_main"); err != nil {
		t.Fatalf("alice should read docs_main: %v", err)
	}
	if err := acl.Check(p, OpSearch, "secrets"); err == nil {
		t.Fatal("alice must not read outside docs_*")
	}
	if err := acl.Check(p, OpInsert, "docs_main"); err == nil {
		t.Fatal("reader must not insert")
	}
	var forbidden *ErrForbidden
	err := acl.Check(p, OpInsert, "docs_main")
	if err == nil || err.Error() != "vec/auth: access denied" {
		t.Fatalf("want generic forbidden message, got %v", err)
	}
	if _, ok := err.(*ErrForbidden); !ok {
		_ = forbidden
		t.Fatalf("want *ErrForbidden, got %T", err)
	}
}

func TestACLWildcard(t *testing.T) {
	acl := NewACL()
	_ = acl.Grant("root", RoleSuperuser, "*")
	p := Principal{ID: "root"}
	for _, coll := range []string{"a", "b_2", "anything"} {
		if err := acl.Check(p, OpServerConfig, coll); err != nil {
			t.Fatalf("superuser denied on %s: %v", coll, err)
		}
	}
}

func TestAPIKeyRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rec, raw, err := GenerateAPIKey("ci", []string{"docs_*"}, []Op{OpSearch}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !HasPrefix(raw) {
		t.Fatalf("raw key missing prefix: %q", raw)
	}
	if !rec.Verify(raw, now) {
		t.Fatal("freshly minted key should verify")
	}
	if rec.Verify(raw+"x", now) {
		t.Fatal("tampered token must not verify")
	}
	if rec.Verify("bearer-not-a-key", now) {
		t.Fatal("non-key token must not verify")
	}
}

func TestAPIKeyExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rec, raw, _ := GenerateAPIKey("temp", nil, nil, now)
	rec.ExpiresAt = now.Add(time.Hour)
	if !rec.Verify(raw, now) {
		t.Fatal("key should verify before expiry")
	}
	if rec.Verify(raw, now.Add(2*time.Hour)) {
		t.Fatal("key must not verify after expiry")
	}
}

func TestKeyStoreAuthenticate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rec, raw, _ := GenerateAPIKey("svc", nil, nil, now)
	store := NewKeyStore([]APIKey{rec})
	p, ok := store.Authenticate(raw, now)
	if !ok || p.ID != "svc" || p.Kind != "apikey" {
		t.Fatalf("authenticate failed: %+v ok=%v", p, ok)
	}
	if _, ok := store.Authenticate("vec_wrong", now); ok {
		t.Fatal("wrong key must not authenticate")
	}
}

func TestJWTHS256(t *testing.T) {
	secret := []byte("super-secret-signing-key")
	cfg := JWTConfig{Algorithm: "HS256", SharedSecret: secret, Issuer: "vec", Audience: "api"}
	now := time.Unix(1_700_000_000, 0)
	token := makeHS256(t, secret, map[string]any{
		"sub":             "bob",
		"iss":             "vec",
		"aud":             "api",
		"exp":             now.Add(time.Hour).Unix(),
		"vec_role":        "writer",
		"vec_collections": []string{"docs_a", "docs_b"},
	})
	claims, err := cfg.ValidateJWT(token, now)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if claims.Subject != "bob" || claims.Role != RoleWriter {
		t.Fatalf("bad claims: %+v", claims)
	}
	if len(claims.Collections) != 2 {
		t.Fatalf("want 2 collections, got %v", claims.Collections)
	}
	if claims.Principal().Kind != "jwt" {
		t.Fatal("principal kind should be jwt")
	}
}

func TestJWTExpired(t *testing.T) {
	secret := []byte("k")
	cfg := JWTConfig{Algorithm: "HS256", SharedSecret: secret}
	now := time.Unix(1_700_000_000, 0)
	token := makeHS256(t, secret, map[string]any{"sub": "x", "exp": now.Add(-time.Hour).Unix()})
	if _, err := cfg.ValidateJWT(token, now); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestJWTBadSignature(t *testing.T) {
	cfg := JWTConfig{Algorithm: "HS256", SharedSecret: []byte("real")}
	now := time.Unix(1_700_000_000, 0)
	token := makeHS256(t, []byte("forged"), map[string]any{"sub": "x", "exp": now.Add(time.Hour).Unix()})
	if _, err := cfg.ValidateJWT(token, now); err == nil {
		t.Fatal("token signed with wrong key must be rejected")
	}
}

func TestJWTAlgMismatch(t *testing.T) {
	cfg := JWTConfig{Algorithm: "RS256"}
	now := time.Unix(1_700_000_000, 0)
	token := makeHS256(t, []byte("k"), map[string]any{"sub": "x"})
	if _, err := cfg.ValidateJWT(token, now); err == nil {
		t.Fatal("HS256 token must be rejected when RS256 is configured")
	}
}

func TestJWTAudienceMismatch(t *testing.T) {
	secret := []byte("k")
	cfg := JWTConfig{Algorithm: "HS256", SharedSecret: secret, Audience: "api"}
	now := time.Unix(1_700_000_000, 0)
	token := makeHS256(t, secret, map[string]any{"sub": "x", "aud": "other", "exp": now.Add(time.Hour).Unix()})
	if _, err := cfg.ValidateJWT(token, now); err == nil {
		t.Fatal("wrong audience must be rejected")
	}
}

func TestJWTES256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := JWTConfig{Algorithm: "ES256", ECDSAPublicKey: &key.PublicKey}
	now := time.Unix(1_700_000_000, 0)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`))
	payloadJSON, _ := json.Marshal(map[string]any{"sub": "carol", "exp": now.Add(time.Hour).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := header + "." + payload
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	claims, err := cfg.ValidateJWT(token, now)
	if err != nil {
		t.Fatalf("valid ES256 token rejected: %v", err)
	}
	if claims.Subject != "carol" {
		t.Fatalf("bad subject: %q", claims.Subject)
	}
}

func TestJWTRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cfg := JWTConfig{Algorithm: "RS256", RSAPublicKey: &key.PublicKey}
	now := time.Unix(1_700_000_000, 0)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadJSON, _ := json.Marshal(map[string]any{"sub": "dave", "exp": now.Add(time.Hour).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := header + "." + payload
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	claims, err := cfg.ValidateJWT(token, now)
	if err != nil {
		t.Fatalf("valid RS256 token rejected: %v", err)
	}
	if claims.Subject != "dave" {
		t.Fatalf("bad subject: %q", claims.Subject)
	}
}

func TestPasswordAccount(t *testing.T) {
	acct, err := NewUserAccount("eve", "correct horse battery staple", RoleWriter, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if !acct.VerifyPassword("correct horse battery staple") {
		t.Fatal("correct password should verify")
	}
	if acct.VerifyPassword("wrong") {
		t.Fatal("wrong password must not verify")
	}
	acct.Locked = true
	if acct.VerifyPassword("correct horse battery staple") {
		t.Fatal("locked account must not verify even with correct password")
	}
}

func TestBackoff(t *testing.T) {
	b := NewBackoff()
	b.BaseDelay = time.Millisecond
	b.LockAfter = 3
	var lastDelay time.Duration
	for i := 1; i <= 2; i++ {
		d, lock := b.Fail("mallory")
		if lock {
			t.Fatalf("locked too early at attempt %d", i)
		}
		if d <= lastDelay {
			t.Fatalf("delay should grow: attempt %d delay %v not > %v", i, d, lastDelay)
		}
		lastDelay = d
	}
	if _, lock := b.Fail("mallory"); !lock {
		t.Fatal("should lock at LockAfter")
	}
	b.Reset("mallory")
	if b.Failures("mallory") != 0 {
		t.Fatal("reset should clear failures")
	}
}

func TestMTLSPrincipalFromCN(t *testing.T) {
	cfg := MTLSConfig{PrincipalFromCN: true}
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "svc.internal"}}
	p, err := cfg.PrincipalFromCert(cert)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "svc.internal" || p.Kind != "mtls" {
		t.Fatalf("bad principal: %+v", p)
	}
	if _, err := cfg.PrincipalFromCert(&x509.Certificate{}); err == nil {
		t.Fatal("missing CN must error")
	}
	if _, err := cfg.PrincipalFromCert(nil); err == nil {
		t.Fatal("nil cert must error")
	}
}

func TestMTLSPrincipalFromSAN(t *testing.T) {
	u, _ := url.Parse("spiffe://cluster/ns/svc")
	cert := &x509.Certificate{
		DNSNames:       []string{"a.example.com"},
		EmailAddresses: []string{"svc@example.com"},
		URIs:           []*url.URL{u},
	}
	cases := map[string]string{
		"dns":   "a.example.com",
		"email": "svc@example.com",
		"uri":   "spiffe://cluster/ns/svc",
	}
	for san, want := range cases {
		cfg := MTLSConfig{PrincipalFromSAN: san}
		p, err := cfg.PrincipalFromCert(cert)
		if err != nil {
			t.Fatalf("san %s: %v", san, err)
		}
		if p.ID != want {
			t.Fatalf("san %s: want %q got %q", san, want, p.ID)
		}
	}
	cfg := MTLSConfig{}
	if _, err := cfg.PrincipalFromCert(cert); err == nil {
		t.Fatal("no mapping configured must error")
	}
}

func makeHS256(t *testing.T, secret []byte, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}
