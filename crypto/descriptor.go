package crypto

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

// KDF identifies how the master key is produced (spec 23 section 3.5). A raw key
// from a KMS skips the KDF; a passphrase runs Argon2id.
type KDF uint8

const (
	KDFNone     KDF = 0x00 // raw 32-byte key, no KDF
	KDFArgon2id KDF = 0x01
)

// DescriptorSize is the byte length of the cleartext encryption descriptor (spec
// 23 section 3.5). The descriptor's own field offsets run to byte 77 (kdf 1,
// cipher 1, epoch 2, argon2 time 4, memory 4, threads 1, salt 32, db id 16,
// verification tag 16), so the serialized form is 77 bytes. Its placement inside
// the 100-byte file header, and any trailing reserved bytes, settle when the
// header and pager wiring land.
const DescriptorSize = 77

// Descriptor is the cleartext key-setup block. It holds everything needed to
// derive and verify the master key before any data page is read, and nothing that
// reveals user data (spec 23 section 2.2, 3.5).
type Descriptor struct {
	KDF             KDF
	Cipher          Cipher
	Epoch           uint16
	Argon2Time      uint32
	Argon2Memory    uint32 // KiB
	Argon2Threads   uint8
	Salt            [32]byte
	DBID            [16]byte
	VerificationTag [16]byte
}

// NewDescriptor builds a descriptor for a fresh encrypted database (spec 23
// section 3.5). It generates a random salt and database id, derives the master
// key, and records the verification tag. The returned master key is the caller's
// to derive DEKs from and then zeroize.
func NewDescriptor(passphrase string, c Cipher) (*Descriptor, []byte, error) {
	if c != CipherAES256GCM && c != CipherChaCha20Poly1305 {
		return nil, nil, ErrUnknownCipher
	}
	var salt [32]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, nil, err
	}
	var dbID [16]byte
	if _, err := rand.Read(dbID[:]); err != nil {
		return nil, nil, err
	}
	params := DefaultArgon2Params(salt[:])
	mk, err := MasterKey(passphrase, params)
	if err != nil {
		return nil, nil, err
	}
	tag, err := VerificationTag(mk)
	if err != nil {
		Zeroize(mk)
		return nil, nil, err
	}
	d := &Descriptor{
		KDF:             KDFArgon2id,
		Cipher:          c,
		Epoch:           0,
		Argon2Time:      params.Time,
		Argon2Memory:    params.Memory,
		Argon2Threads:   params.Threads,
		Salt:            salt,
		DBID:            dbID,
		VerificationTag: tag,
	}
	return d, mk, nil
}

// NewDescriptorRaw builds a descriptor for a database opened with a raw KMS key
// (spec 23 section 3.2). No Argon2id parameters are recorded; the salt is unused.
func NewDescriptorRaw(rawKey []byte, c Cipher) (*Descriptor, []byte, error) {
	if c != CipherAES256GCM && c != CipherChaCha20Poly1305 {
		return nil, nil, ErrUnknownCipher
	}
	mk, err := MasterKeyFromRaw(rawKey)
	if err != nil {
		return nil, nil, err
	}
	var dbID [16]byte
	if _, err := rand.Read(dbID[:]); err != nil {
		Zeroize(mk)
		return nil, nil, err
	}
	tag, err := VerificationTag(mk)
	if err != nil {
		Zeroize(mk)
		return nil, nil, err
	}
	return &Descriptor{KDF: KDFNone, Cipher: c, Epoch: 0, DBID: dbID, VerificationTag: tag}, mk, nil
}

// Argon2Params reconstructs the Argon2id parameters from the descriptor.
func (d *Descriptor) Argon2Params() Argon2Params {
	return Argon2Params{
		Time:    d.Argon2Time,
		Memory:  d.Argon2Memory,
		Threads: d.Argon2Threads,
		Salt:    d.Salt[:],
	}
}

// Marshal serializes the descriptor to its fixed on-disk form (spec 23 section
// 3.5). The layout matches the header descriptor offsets exactly.
func (d *Descriptor) Marshal() []byte {
	b := make([]byte, DescriptorSize)
	b[0] = byte(d.KDF)
	b[1] = byte(d.Cipher)
	binary.LittleEndian.PutUint16(b[2:], d.Epoch)
	binary.LittleEndian.PutUint32(b[4:], d.Argon2Time)
	binary.LittleEndian.PutUint32(b[8:], d.Argon2Memory)
	b[12] = d.Argon2Threads
	copy(b[13:45], d.Salt[:])
	copy(b[45:61], d.DBID[:])
	copy(b[61:77], d.VerificationTag[:])
	return b
}

// UnmarshalDescriptor parses a descriptor read from the file header.
func UnmarshalDescriptor(b []byte) (*Descriptor, error) {
	if len(b) < DescriptorSize {
		return nil, errors.New("vec/crypto: descriptor too short")
	}
	d := &Descriptor{
		KDF:           KDF(b[0]),
		Cipher:        Cipher(b[1]),
		Epoch:         binary.LittleEndian.Uint16(b[2:]),
		Argon2Time:    binary.LittleEndian.Uint32(b[4:]),
		Argon2Memory:  binary.LittleEndian.Uint32(b[8:]),
		Argon2Threads: b[12],
	}
	copy(d.Salt[:], b[13:45])
	copy(d.DBID[:], b[45:61])
	copy(d.VerificationTag[:], b[61:77])
	return d, nil
}

// OpenWithPassphrase derives the master key from the passphrase, verifies it
// against the descriptor's tag, derives every live DEK, and returns a ready
// PageEncryptor (spec 23 section 14.2). The master key is zeroized before return.
func OpenWithPassphrase(d *Descriptor, passphrase string) (*PageEncryptor, error) {
	if d.KDF != KDFArgon2id {
		return nil, errors.New("vec/crypto: descriptor was not created with a passphrase")
	}
	mk, err := MasterKey(passphrase, d.Argon2Params())
	if err != nil {
		return nil, err
	}
	defer Zeroize(mk)
	return openWithMasterKey(d, mk)
}

// OpenWithRawKey verifies a raw KMS key against the descriptor and returns a ready
// PageEncryptor (spec 23 section 3.2).
func OpenWithRawKey(d *Descriptor, rawKey []byte) (*PageEncryptor, error) {
	mk, err := MasterKeyFromRaw(rawKey)
	if err != nil {
		return nil, err
	}
	defer Zeroize(mk)
	return openWithMasterKey(d, mk)
}

func openWithMasterKey(d *Descriptor, mk []byte) (*PageEncryptor, error) {
	if !VerifyMasterKey(mk, d.VerificationTag) {
		return nil, ErrWrongPassphrase
	}
	deks := make(map[uint16][]byte, int(d.Epoch)+1)
	for epoch := uint16(0); epoch <= d.Epoch; epoch++ {
		dek, err := DEK(mk, d.DBID, epoch)
		if err != nil {
			for _, k := range deks {
				Zeroize(k)
			}
			return nil, err
		}
		deks[epoch] = dek
	}
	enc, err := NewPageEncryptor(EncryptorConfig{
		Cipher:  d.Cipher,
		DBID:    d.DBID,
		DEKs:    deks,
		Current: d.Epoch,
	})
	for _, k := range deks {
		Zeroize(k)
	}
	return enc, err
}
