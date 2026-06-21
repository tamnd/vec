package vec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tamnd/vec/crypto"
)

func TestUnencryptedHasNoCrypto(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Encryption().Enabled {
		t.Fatal("plain database must not report encryption")
	}
	if _, ok := db.crypt.(crypto.NoCrypto); !ok {
		t.Fatalf("plain database should use NoCrypto, got %T", db.crypt)
	}
}

func TestOpenWithPassphrase(t *testing.T) {
	db, err := Open(":memory:", WithPassphrase("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info := db.Encryption()
	if !info.Enabled {
		t.Fatal("encrypted database should report enabled")
	}
	if info.Cipher != crypto.CipherAES256GCM.String() {
		t.Fatalf("default cipher should be AES-256-GCM, got %q", info.Cipher)
	}
	if info.KDF != "argon2id" {
		t.Fatalf("passphrase KDF should be argon2id, got %q", info.KDF)
	}
	if info.Epoch != 0 {
		t.Fatalf("fresh database starts at epoch 0, got %d", info.Epoch)
	}
}

func TestOpenWithChaCha(t *testing.T) {
	db, err := Open(":memory:", WithPassphrase("p"), WithCipher(crypto.CipherChaCha20Poly1305))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if db.Encryption().Cipher != crypto.CipherChaCha20Poly1305.String() {
		t.Fatalf("cipher should be ChaCha20-Poly1305, got %q", db.Encryption().Cipher)
	}
}

func TestOpenWithRawKey(t *testing.T) {
	key := make(EncryptionKey, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := Open(":memory:", WithEncryptionKey(key))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info := db.Encryption()
	if !info.Enabled || info.KDF != "raw-key" {
		t.Fatalf("raw key database should report raw-key KDF, got %+v", info)
	}
}

func TestRotateDEK(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:", WithPassphrase("pw"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Encryption().Epoch; got != 0 {
		t.Fatalf("start epoch 0, got %d", got)
	}
	if err := db.RotateDEK(ctx, "pw"); err != nil {
		t.Fatal(err)
	}
	if got := db.Encryption().Epoch; got != 1 {
		t.Fatalf("after rotate epoch 1, got %d", got)
	}
	if err := db.RotateDEK(ctx, "wrong"); err == nil {
		t.Fatal("rotate with wrong passphrase must fail")
	}
	if got := db.Encryption().Epoch; got != 1 {
		t.Fatalf("failed rotate must not advance epoch, got %d", got)
	}
}

func TestChangePassphrase(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:", WithPassphrase("old"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ChangePassphrase(ctx, "wrong", "new"); err == nil {
		t.Fatal("change with wrong old passphrase must fail")
	}
	if err := db.ChangePassphrase(ctx, "old", "new"); err != nil {
		t.Fatal(err)
	}
	if err := db.RotateDEK(ctx, "new"); err != nil {
		t.Fatalf("new passphrase should rotate: %v", err)
	}
	if err := db.RotateDEK(ctx, "old"); err == nil {
		t.Fatal("old passphrase must no longer work")
	}
}

func TestChangePassphraseOnPlainDB(t *testing.T) {
	db, _ := Open(":memory:")
	defer db.Close()
	if err := db.ChangePassphrase(context.Background(), "a", "b"); err == nil {
		t.Fatal("plain database has no passphrase to change")
	}
}

func TestRekeyVacuum(t *testing.T) {
	ctx := context.Background()
	db, _ := Open(":memory:", WithPassphrase("pw"))
	defer db.Close()
	stats, err := db.RekeyVacuum(ctx, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if stats.OldEpoch != 0 || stats.NewEpoch != 1 {
		t.Fatalf("vacuum should advance epoch 0 to 1, got %+v", stats)
	}
}

func TestSecretRedaction(t *testing.T) {
	cases := []fmt.Stringer{
		EncryptionKey("0123456789abcdef0123456789abcdef"),
		Passphrase("hunter2"),
		APIKey("vec_supersecrettoken"),
		JWTSecret("signing-secret"),
	}
	for _, s := range cases {
		if s.String() != "[REDACTED]" {
			t.Fatalf("%T.String leaked: %q", s, s.String())
		}
		if got := fmt.Sprintf("%v", s); got != "[REDACTED]" {
			t.Fatalf("%T %%v leaked: %q", s, got)
		}
		if got := fmt.Sprintf("%#v", s); !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("%T %%#v leaked: %q", s, got)
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != `"[REDACTED]"` {
			t.Fatalf("%T json leaked: %s", s, b)
		}
	}
}

func TestSecretRevealStillWorks(t *testing.T) {
	if Passphrase("pw").Reveal() != "pw" {
		t.Fatal("Reveal must return the real passphrase")
	}
	if APIKey("vec_x").Reveal() != "vec_x" {
		t.Fatal("Reveal must return the real token")
	}
	if string(EncryptionKey("k").Bytes()) != "k" {
		t.Fatal("Bytes must return the real key")
	}
}

func TestRedactFieldKey(t *testing.T) {
	redact := []string{"api_key", "Secret", "user_password", "auth_token", "passphrase", "JWTSecret"}
	for _, k := range redact {
		if !RedactFieldKey(k) {
			t.Fatalf("field %q should be redacted", k)
		}
	}
	keep := []string{"collection", "count", "latency_ms", "user_id"}
	for _, k := range keep {
		if RedactFieldKey(k) {
			t.Fatalf("field %q should not be redacted", k)
		}
	}
}

func TestEncryptionErrorCodes(t *testing.T) {
	if errCode(ErrKeyRequired) != 21 {
		t.Fatalf("ErrKeyRequired code: %d", errCode(ErrKeyRequired))
	}
	if errCode(ErrWrongPassphrase) != 22 {
		t.Fatalf("ErrWrongPassphrase code: %d", errCode(ErrWrongPassphrase))
	}
	if errCode(ErrNotEncrypted) != 23 {
		t.Fatalf("ErrNotEncrypted code: %d", errCode(ErrNotEncrypted))
	}
}
