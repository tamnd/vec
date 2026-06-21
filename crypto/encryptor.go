package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

// PageEncryptor is the Crypto implementation backed by a per-epoch DEK map (spec
// 23 section 14.1, 18.3). It is safe for concurrent use: reads and normal writes
// take a read lock to look up the current epoch and DEK; rotation takes the write
// lock. Per-page key derivation is stateless and runs without the lock.
type PageEncryptor struct {
	cipher Cipher
	dbID   [16]byte

	mu      sync.RWMutex
	deks    map[uint16][]byte
	current uint16
}

// EncryptorConfig is the input to NewPageEncryptor. DEKs maps each live epoch to
// its 32-byte key; Current is the epoch new writes use.
type EncryptorConfig struct {
	Cipher  Cipher
	DBID    [16]byte
	DEKs    map[uint16][]byte
	Current uint16
}

// NewPageEncryptor builds a PageEncryptor from a set of DEKs (spec 23 section
// 14.2). The caller derives the DEKs from the master key and supplies them here,
// so this constructor never sees the passphrase.
func NewPageEncryptor(cfg EncryptorConfig) (*PageEncryptor, error) {
	if cfg.Cipher != CipherAES256GCM && cfg.Cipher != CipherChaCha20Poly1305 {
		return nil, ErrUnknownCipher
	}
	if len(cfg.DEKs) == 0 {
		return nil, ErrNoDEK
	}
	if _, ok := cfg.DEKs[cfg.Current]; !ok {
		return nil, ErrMissingDEK{Epoch: cfg.Current}
	}
	deks := make(map[uint16][]byte, len(cfg.DEKs))
	for epoch, dek := range cfg.DEKs {
		k := make([]byte, len(dek))
		copy(k, dek)
		deks[epoch] = k
	}
	return &PageEncryptor{cipher: cfg.Cipher, dbID: cfg.DBID, deks: deks, current: cfg.Current}, nil
}

// Enabled reports true.
func (e *PageEncryptor) Enabled() bool { return true }

// CurrentEpoch returns the epoch the next EncryptPage call uses.
func (e *PageEncryptor) CurrentEpoch() uint16 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.current
}

// aead builds the AEAD for one page key under the configured cipher.
func (e *PageEncryptor) aead(pk []byte) (cipher.AEAD, error) {
	switch e.cipher {
	case CipherChaCha20Poly1305:
		return chacha20poly1305.New(pk)
	default:
		block, err := aes.NewCipher(pk)
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(block)
	}
}

// EncryptPage encrypts a plaintext page under the current epoch and returns the
// envelope: ciphertext, then the 12-byte nonce, then the 16-byte tag (spec 23
// section 2.3, 14.1).
func (e *PageEncryptor) EncryptPage(plaintext []byte, pageClass uint8, pageNo, lsn uint64) ([]byte, error) {
	e.mu.RLock()
	epoch := e.current
	dek := e.deks[epoch]
	e.mu.RUnlock()
	if dek == nil {
		return nil, ErrMissingDEK{Epoch: epoch}
	}

	pk, err := pageKey(dek, pageClass, pageNo)
	if err != nil {
		return nil, err
	}
	defer Zeroize(pk)
	nonce, err := deriveNonce(pk, pageNo, lsn)
	if err != nil {
		return nil, err
	}
	aead, err := e.aead(pk)
	if err != nil {
		return nil, err
	}
	aad := buildAAD(pageNo, pageClass, lsn, epoch)

	// Seal yields ciphertext||tag. Lay the envelope out as ciphertext||nonce||tag.
	sealed := aead.Seal(nil, nonce[:], plaintext, aad)
	body, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	out := make([]byte, 0, len(plaintext)+EnvelopeOverhead)
	out = append(out, body...)
	out = append(out, nonce[:]...)
	out = append(out, tag...)
	return out, nil
}

// DecryptPage authenticates and decrypts an envelope under the recorded epoch
// (spec 23 section 14.1). A failed tag returns ErrPageAuthFailed and never yields
// plaintext: the cause is a wrong key, a tampered page, or storage corruption.
func (e *PageEncryptor) DecryptPage(envelope []byte, pageClass uint8, pageNo, lsn uint64, epoch uint16) ([]byte, error) {
	if len(envelope) < EnvelopeOverhead {
		return nil, ErrPageTooShort
	}
	e.mu.RLock()
	dek := e.deks[epoch]
	e.mu.RUnlock()
	if dek == nil {
		return nil, ErrMissingDEK{Epoch: epoch}
	}

	n := len(envelope)
	body := envelope[:n-EnvelopeOverhead]
	nonce := envelope[n-EnvelopeOverhead : n-16]
	tag := envelope[n-16:]

	pk, err := pageKey(dek, pageClass, pageNo)
	if err != nil {
		return nil, err
	}
	defer Zeroize(pk)
	aead, err := e.aead(pk)
	if err != nil {
		return nil, err
	}
	aad := buildAAD(pageNo, pageClass, lsn, epoch)

	// Reassemble ciphertext||tag for Open.
	ct := make([]byte, 0, len(body)+16)
	ct = append(ct, body...)
	ct = append(ct, tag...)
	plaintext, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrPageAuthFailed{PageNo: pageNo, PageClass: pageClass, Epoch: epoch, Reason: "tag mismatch"}
	}
	return plaintext, nil
}

// AddDEK installs the DEK for an epoch and makes it current (spec 23 section 4.3,
// the epoch bump). Old DEKs stay in the map so old-epoch pages still read.
func (e *PageEncryptor) AddDEK(epoch uint16, dek []byte) {
	k := make([]byte, len(dek))
	copy(k, dek)
	e.mu.Lock()
	e.deks[epoch] = k
	e.current = epoch
	e.mu.Unlock()
}

// ReloadDEK re-installs a DEK for an old epoch without changing the current epoch
// (spec 23 section 17.2). It recovers from a released DEK that a still-present
// page needs.
func (e *PageEncryptor) ReloadDEK(epoch uint16, dek []byte) {
	k := make([]byte, len(dek))
	copy(k, dek)
	e.mu.Lock()
	e.deks[epoch] = k
	e.mu.Unlock()
}

// ReleaseEpoch drops the DEK for an old epoch and zeroizes it (spec 23 section
// 4.3). The caller releases an epoch only once no live page references it, after
// a RekeyVacuum.
func (e *PageEncryptor) ReleaseEpoch(epoch uint16) {
	e.mu.Lock()
	if k := e.deks[epoch]; k != nil {
		Zeroize(k)
		delete(e.deks, epoch)
	}
	e.mu.Unlock()
}

// Close zeroizes and drops every DEK (spec 23 section 18.2).
func (e *PageEncryptor) Close() error {
	e.mu.Lock()
	for epoch, k := range e.deks {
		Zeroize(k)
		delete(e.deks, epoch)
	}
	e.mu.Unlock()
	return nil
}
