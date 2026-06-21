// Package crypto implements vec's encryption at rest: the page-level AEAD
// envelope, the key hierarchy (master key, per-epoch DEK, per-page key,
// per-write nonce), key rotation, and the verification tag that detects a wrong
// passphrase before any data page is read. It is spec 23 sections 2 through 5,
// 14, 17, and 18.
//
// The package has no dependency on the pager or the rest of the engine. The
// pager reaches it through the Crypto interface, so an unencrypted database
// carries no crypto code on its hot path: the pager checks Enabled once and
// takes the no-op branch.
package crypto

// Page classes tag every encrypted page so a ciphertext from one class cannot be
// relocated into another (spec 23 section 2.2). The tag goes into the AAD, which
// makes the ciphertext of a vector segment page cryptographically distinct from a
// graph page even when the plaintext is identical.
const (
	ClassVectorSegment uint8 = 0x01
	ClassHNSWGraph     uint8 = 0x02
	ClassIVFDiskANN    uint8 = 0x03
	ClassMetadata      uint8 = 0x04
	ClassCatalog       uint8 = 0x05
	ClassFreelist      uint8 = 0x06
	ClassOverflow      uint8 = 0x07
)

// Cipher selects the AEAD construction recorded in the header descriptor (spec 23
// section 3.5). AES-256-GCM is the default and uses hardware AES on all modern
// x86-64 and arm64 hardware. ChaCha20-Poly1305 is the constant-time software
// fallback for hardware without AES acceleration.
type Cipher uint8

const (
	CipherAES256GCM        Cipher = 0x01
	CipherChaCha20Poly1305 Cipher = 0x02
)

// String renders the cipher id as the token used in the header descriptor and the
// configuration view.
func (c Cipher) String() string {
	switch c {
	case CipherAES256GCM:
		return "aes256gcm"
	case CipherChaCha20Poly1305:
		return "chacha20poly1305"
	default:
		return "unknown"
	}
}

// EnvelopeOverhead is the number of bytes the AEAD envelope adds to a page: a
// 12-byte nonce and a 16-byte authentication tag (spec 23 section 2.3). A page of
// size P holds P-28 bytes of plaintext when encryption is on.
const EnvelopeOverhead = 28

// Crypto is the seam the pager calls to encrypt and decrypt pages (spec 23
// section 18.1). An implementation must be safe for concurrent use.
type Crypto interface {
	// Enabled reports whether encryption is on. The pager checks this on every
	// page read and write; when it is false the pager takes the no-op path.
	Enabled() bool

	// EncryptPage encrypts a plaintext page and returns the ciphertext envelope,
	// which is len(plaintext)+EnvelopeOverhead bytes.
	EncryptPage(plaintext []byte, pageClass uint8, pageNo, lsn uint64) ([]byte, error)

	// DecryptPage authenticates and decrypts an envelope. epoch is the key epoch
	// recorded in the page or WAL frame header.
	DecryptPage(envelope []byte, pageClass uint8, pageNo, lsn uint64, epoch uint16) ([]byte, error)

	// CurrentEpoch returns the epoch the next EncryptPage call will use.
	CurrentEpoch() uint16

	// Close releases in-memory key material.
	Close() error
}

// NoCrypto is the Crypto for an unencrypted database. Every method is a pass
// through, so the pager's fast path stays free of crypto work.
type NoCrypto struct{}

// Enabled reports false.
func (NoCrypto) Enabled() bool { return false }

// EncryptPage returns the plaintext unchanged.
func (NoCrypto) EncryptPage(p []byte, _ uint8, _, _ uint64) ([]byte, error) { return p, nil }

// DecryptPage returns the envelope unchanged.
func (NoCrypto) DecryptPage(e []byte, _ uint8, _, _ uint64, _ uint16) ([]byte, error) {
	return e, nil
}

// CurrentEpoch returns 0.
func (NoCrypto) CurrentEpoch() uint16 { return 0 }

// Close does nothing.
func (NoCrypto) Close() error { return nil }
