package crypto

import (
	"bytes"
	"errors"
	"testing"
)

// newEnc builds a passphrase-backed encryptor for tests.
func newEnc(t *testing.T, c Cipher) (*PageEncryptor, *Descriptor) {
	t.Helper()
	d, mk, err := NewDescriptor("a-test-passphrase-16chars", c)
	if err != nil {
		t.Fatalf("NewDescriptor: %v", err)
	}
	Zeroize(mk)
	enc, err := OpenWithPassphrase(d, "a-test-passphrase-16chars")
	if err != nil {
		t.Fatalf("OpenWithPassphrase: %v", err)
	}
	t.Cleanup(func() { _ = enc.Close() })
	return enc, d
}

// TestRoundTrip encrypts and decrypts a page for both ciphers and every class
// (spec 23 section 18.4 correctness).
func TestRoundTrip(t *testing.T) {
	classes := []uint8{ClassVectorSegment, ClassHNSWGraph, ClassIVFDiskANN, ClassMetadata, ClassCatalog, ClassFreelist, ClassOverflow}
	for _, c := range []Cipher{CipherAES256GCM, CipherChaCha20Poly1305} {
		enc, _ := newEnc(t, c)
		for _, class := range classes {
			plain := bytes.Repeat([]byte{byte(class), 0xab, 0xcd}, 100)
			env, err := enc.EncryptPage(plain, class, 42, 7)
			if err != nil {
				t.Fatalf("cipher %s class %d encrypt: %v", c, class, err)
			}
			if len(env) != len(plain)+EnvelopeOverhead {
				t.Fatalf("envelope len = %d, want %d", len(env), len(plain)+EnvelopeOverhead)
			}
			got, err := enc.DecryptPage(env, class, 42, 7, 0)
			if err != nil {
				t.Fatalf("cipher %s class %d decrypt: %v", c, class, err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("cipher %s class %d round trip mismatch", c, class)
			}
		}
	}
}

// TestCiphertextIsNotPlaintext confirms the envelope body does not leak the input.
func TestCiphertextIsNotPlaintext(t *testing.T) {
	enc, _ := newEnc(t, CipherAES256GCM)
	plain := bytes.Repeat([]byte{0x11}, 256)
	env, err := enc.EncryptPage(plain, ClassVectorSegment, 1, 1)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(env, bytes.Repeat([]byte{0x11}, 16)) {
		t.Fatal("ciphertext contains a run of the plaintext byte")
	}
}

// TestNonceUniqueness confirms increasing LSNs on the same page yield distinct
// nonces (spec 23 section 18.4 nonce uniqueness).
func TestNonceUniqueness(t *testing.T) {
	enc, _ := newEnc(t, CipherAES256GCM)
	plain := bytes.Repeat([]byte{0x22}, 64)
	seen := make(map[string]bool)
	for lsn := uint64(1); lsn <= 1000; lsn++ {
		env, err := enc.EncryptPage(plain, ClassVectorSegment, 5, lsn)
		if err != nil {
			t.Fatalf("encrypt lsn %d: %v", lsn, err)
		}
		nonce := string(env[len(env)-EnvelopeOverhead : len(env)-16])
		if seen[nonce] {
			t.Fatalf("nonce repeated at lsn %d", lsn)
		}
		seen[nonce] = true
	}
}

// TestAADClassBinding confirms a page encrypted as one class fails to decrypt as
// another (spec 23 section 18.4 AAD binding).
func TestAADClassBinding(t *testing.T) {
	enc, _ := newEnc(t, CipherAES256GCM)
	plain := bytes.Repeat([]byte{0x33}, 64)
	env, err := enc.EncryptPage(plain, ClassVectorSegment, 9, 3)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = enc.DecryptPage(env, ClassHNSWGraph, 9, 3, 0)
	var ae ErrPageAuthFailed
	if !errors.As(err, &ae) {
		t.Fatalf("cross-class decrypt: want ErrPageAuthFailed, got %v", err)
	}
}

// TestTamperDetection confirms a flipped bit fails authentication (spec 23
// section 18.4 tamper detection).
func TestTamperDetection(t *testing.T) {
	enc, _ := newEnc(t, CipherAES256GCM)
	plain := bytes.Repeat([]byte{0x44}, 64)
	env, err := enc.EncryptPage(plain, ClassMetadata, 2, 2)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	env[10] ^= 0x01
	_, err = enc.DecryptPage(env, ClassMetadata, 2, 2, 0)
	var ae ErrPageAuthFailed
	if !errors.As(err, &ae) {
		t.Fatalf("tampered decrypt: want ErrPageAuthFailed, got %v", err)
	}
}

// TestWrongPassphrase confirms the verification tag rejects a wrong passphrase
// before any page is read (spec 23 section 18.4 wrong-key detection).
func TestWrongPassphrase(t *testing.T) {
	d, mk, err := NewDescriptor("correct-passphrase-1234", CipherAES256GCM)
	if err != nil {
		t.Fatalf("NewDescriptor: %v", err)
	}
	Zeroize(mk)
	_, err = OpenWithPassphrase(d, "wrong-passphrase-000000")
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: want ErrWrongPassphrase, got %v", err)
	}
}

// TestRawKeyPath exercises the KMS raw-key open path.
func TestRawKeyPath(t *testing.T) {
	raw := bytes.Repeat([]byte{0x55}, 32)
	d, mk, err := NewDescriptorRaw(raw, CipherAES256GCM)
	if err != nil {
		t.Fatalf("NewDescriptorRaw: %v", err)
	}
	Zeroize(mk)
	if d.KDF != KDFNone {
		t.Fatalf("raw descriptor KDF = %d, want KDFNone", d.KDF)
	}
	enc, err := OpenWithRawKey(d, raw)
	if err != nil {
		t.Fatalf("OpenWithRawKey: %v", err)
	}
	defer func() { _ = enc.Close() }()
	plain := bytes.Repeat([]byte{0x66}, 48)
	env, err := enc.EncryptPage(plain, ClassVectorSegment, 1, 1)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := enc.DecryptPage(env, ClassVectorSegment, 1, 1, 0)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("raw-key round trip failed: %v", err)
	}
}

// TestEpochRotation confirms a page written under epoch 0 still reads after the
// epoch bumps, and new pages use the new epoch (spec 23 section 4.3).
func TestEpochRotation(t *testing.T) {
	d, mk, err := NewDescriptor("rotation-passphrase-16", CipherAES256GCM)
	if err != nil {
		t.Fatalf("NewDescriptor: %v", err)
	}
	enc, err := OpenWithPassphrase(d, "rotation-passphrase-16")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = enc.Close() }()

	plain := bytes.Repeat([]byte{0x77}, 64)
	oldEnv, err := enc.EncryptPage(plain, ClassVectorSegment, 3, 1)
	if err != nil {
		t.Fatalf("encrypt epoch 0: %v", err)
	}

	// Bump to epoch 1.
	newDEK, err := DEK(mk, d.DBID, 1)
	if err != nil {
		t.Fatalf("DEK epoch 1: %v", err)
	}
	enc.AddDEK(1, newDEK)
	Zeroize(newDEK)
	Zeroize(mk)

	if enc.CurrentEpoch() != 1 {
		t.Fatalf("current epoch = %d, want 1", enc.CurrentEpoch())
	}
	// Old page still decrypts under epoch 0.
	got, err := enc.DecryptPage(oldEnv, ClassVectorSegment, 3, 1, 0)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("old-epoch page failed after rotation: %v", err)
	}
	// New page uses epoch 1.
	newEnv, err := enc.EncryptPage(plain, ClassVectorSegment, 4, 2)
	if err != nil {
		t.Fatalf("encrypt epoch 1: %v", err)
	}
	if _, err := enc.DecryptPage(newEnv, ClassVectorSegment, 4, 2, 1); err != nil {
		t.Fatalf("epoch-1 page failed to decrypt: %v", err)
	}
	// Releasing epoch 0 then reading an epoch-0 page fails with ErrMissingDEK.
	enc.ReleaseEpoch(0)
	_, err = enc.DecryptPage(oldEnv, ClassVectorSegment, 3, 1, 0)
	var md ErrMissingDEK
	if !errors.As(err, &md) {
		t.Fatalf("after ReleaseEpoch(0): want ErrMissingDEK, got %v", err)
	}
}

// TestDescriptorRoundTrip confirms the descriptor marshals and parses back.
func TestDescriptorRoundTrip(t *testing.T) {
	d, mk, err := NewDescriptor("descriptor-passphrase-1", CipherChaCha20Poly1305)
	if err != nil {
		t.Fatalf("NewDescriptor: %v", err)
	}
	Zeroize(mk)
	b := d.Marshal()
	if len(b) != DescriptorSize {
		t.Fatalf("marshal len = %d, want %d", len(b), DescriptorSize)
	}
	got, err := UnmarshalDescriptor(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.KDF != d.KDF || got.Cipher != d.Cipher || got.Epoch != d.Epoch {
		t.Fatalf("descriptor header mismatch: %+v vs %+v", got, d)
	}
	if got.Salt != d.Salt || got.DBID != d.DBID || got.VerificationTag != d.VerificationTag {
		t.Fatal("descriptor key material mismatch after round trip")
	}
	// The parsed descriptor opens with the same passphrase.
	if _, err := OpenWithPassphrase(got, "descriptor-passphrase-1"); err != nil {
		t.Fatalf("open parsed descriptor: %v", err)
	}
}

// TestNoCrypto confirms the no-op path is a pass through.
func TestNoCrypto(t *testing.T) {
	var nc NoCrypto
	if nc.Enabled() {
		t.Fatal("NoCrypto.Enabled = true")
	}
	plain := []byte("hello")
	env, _ := nc.EncryptPage(plain, ClassVectorSegment, 1, 1)
	if !bytes.Equal(env, plain) {
		t.Fatal("NoCrypto.EncryptPage altered the bytes")
	}
}
