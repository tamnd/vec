package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"

	"golang.org/x/crypto/argon2"
)

// The key hierarchy is four levels deep (spec 23 section 3.1):
//
//	passphrase --Argon2id--> master key (MK, 32 bytes, memory only)
//	MK --HKDF(salt=dbID, info=epoch)--> DEK (per epoch, memory only)
//	DEK --HKDF(info=class||pageNo)--> page key (PK)
//	PK --HKDF(info=lsn||pageNo)--> nonce (12 bytes)
//
// Only the master-key derivation inputs (the Argon2id salt and parameters, or a
// flag that a raw key was supplied) live on disk, in the cleartext header
// descriptor. Everything below the master key is derived in process and never
// stored.

// Argon2Params holds the Argon2id cost parameters (spec 23 section 3.2). They are
// stored verbatim in the header descriptor so the file opens on any hardware
// without prior knowledge of the configuration.
type Argon2Params struct {
	Time    uint32 // passes, default 3
	Memory  uint32 // KiB, default 65536 (64 MiB)
	Threads uint8  // lanes, default 4
	Salt    []byte // 32 random bytes generated at create time
}

// DefaultArgon2Params returns the OWASP-recommended baseline (t=3, m=64MiB, p=4).
// The caller supplies the salt.
func DefaultArgon2Params(salt []byte) Argon2Params {
	return Argon2Params{Time: 3, Memory: 64 * 1024, Threads: 4, Salt: salt}
}

// MasterKey derives the 32-byte master key from a passphrase with Argon2id (spec
// 23 section 3.2). A raw 32-byte key supplied by a KMS skips this step entirely;
// see MasterKeyFromRaw.
func MasterKey(passphrase string, p Argon2Params) ([]byte, error) {
	if len(p.Salt) != 32 {
		return nil, ErrInvalidSalt
	}
	if p.Time == 0 || p.Memory == 0 || p.Threads == 0 {
		return nil, ErrInvalidArgon2Params
	}
	return argon2.IDKey([]byte(passphrase), p.Salt, p.Time, p.Memory, p.Threads, 32), nil
}

// MasterKeyFromRaw accepts a raw 32-byte key from a KMS or keyfile (spec 23
// section 3.2). No KDF is applied; the bytes become the master key directly.
func MasterKeyFromRaw(key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKeyLength
	}
	out := make([]byte, 32)
	copy(out, key)
	return out, nil
}

// DEK derives the database encryption key for an epoch (spec 23 section 3.3). The
// dbID binds the DEK to one database, so a DEK from database A cannot authenticate
// pages from database B even under a shared master key.
func DEK(masterKey []byte, dbID [16]byte, epoch uint16) ([]byte, error) {
	info := append([]byte("vec-dek-v1-epoch-"), byte(epoch), byte(epoch>>8))
	return hkdf.Key(sha256.New, masterKey, dbID[:], string(info), 32)
}

// pageKey derives the per-page AEAD key with domain separation by page class and
// page number (spec 23 section 3.4). Separate subkeys per class give defense in
// depth against a hypothetical related-key weakness across classes.
func pageKey(dek []byte, pageClass uint8, pageNo uint64) ([]byte, error) {
	var info [13]byte
	info[0], info[1], info[2], info[3] = 'v', 'p', 'k', '-'
	info[4] = pageClass
	binary.LittleEndian.PutUint64(info[5:], pageNo)
	return hkdf.Key(sha256.New, dek, nil, string(info[:]), 32)
}

// deriveNonce derives the 12-byte AEAD nonce from the page key, page number, and
// LSN (spec 23 section 2.4). Because the WAL gives every write to a page a
// strictly greater LSN, the (pageNo, lsn) pair is unique for the life of the
// database, so the nonce never repeats under one key.
func deriveNonce(pk []byte, pageNo, lsn uint64) ([12]byte, error) {
	var ctx [21]byte
	ctx[0], ctx[1], ctx[2], ctx[3] = 'v', 'n', 'c', '-'
	binary.LittleEndian.PutUint64(ctx[4:], lsn)
	binary.LittleEndian.PutUint64(ctx[12:], pageNo)
	out, err := hkdf.Key(sha256.New, pk, nil, string(ctx[:]), 12)
	if err != nil {
		return [12]byte{}, err
	}
	var n [12]byte
	copy(n[:], out)
	return n, nil
}

// buildAAD assembles the additional authenticated data: page number, page class,
// LSN, and epoch (spec 23 section 14.1). The page class defeats a cut-and-paste
// attack across classes; the epoch ties a ciphertext to the DEK it was written
// under so a replay under a newer DEK fails authentication.
func buildAAD(pageNo uint64, pageClass uint8, lsn uint64, epoch uint16) []byte {
	var aad [19]byte
	binary.LittleEndian.PutUint64(aad[0:], pageNo)
	aad[8] = pageClass
	binary.LittleEndian.PutUint64(aad[9:], lsn)
	binary.LittleEndian.PutUint16(aad[17:], epoch)
	return aad[:]
}

// verificationConstant is the 16-byte known plaintext the verification tag
// authenticates: the ASCII "vec-verify-v1" zero-padded to 16 bytes (spec 23
// section 3.5).
var verificationConstant = []byte("vec-verify-v1\x00\x00\x00")

// VerificationTag produces the GCM tag over the known constant under the master
// key and a fixed zero nonce (spec 23 section 3.5). On open, the implementation
// recomputes this tag from the derived master key and compares it before reading
// any data page, so a wrong passphrase fails cleanly instead of returning garbage.
func VerificationTag(masterKey []byte) ([16]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return [16]byte{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return [16]byte{}, err
	}
	var nonce [12]byte
	sealed := gcm.Seal(nil, nonce[:], verificationConstant, nil)
	var tag [16]byte
	copy(tag[:], sealed[len(sealed)-16:])
	return tag, nil
}

// VerifyMasterKey reports whether masterKey reproduces the stored verification
// tag, using a constant-time comparison (spec 23 section 14.2).
func VerifyMasterKey(masterKey []byte, stored [16]byte) bool {
	tag, err := VerificationTag(masterKey)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(tag[:], stored[:]) == 1
}

// Zeroize overwrites a key slice with zeros (spec 23 section 18.2). Go does not
// guarantee the garbage collector zeroes reclaimed memory, so key material is
// wiped explicitly when it is released. This is the portable defense; the off-GC
// mmap arena in section 18.2 is a further hardening left for the pager wiring.
func Zeroize(b []byte) {
	clear(b)
}
