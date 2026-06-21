package vec

import (
	"context"
	"errors"

	"github.com/tamnd/vec/crypto"
)

// EncryptionInfo reports the at-rest encryption state of a database (spec 23 §3).
// For an unencrypted database Enabled is false and the other fields are zero.
type EncryptionInfo struct {
	Enabled bool
	Cipher  string // "AES-256-GCM" or "ChaCha20-Poly1305"
	KDF     string // "argon2id" or "raw-key"
	Epoch   uint16 // the current write epoch
}

// Encryption returns the encryption state (spec 23 §3). It never returns key
// material; the fields describe the configuration, not the secrets.
func (db *DB) Encryption() EncryptionInfo {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.desc == nil || db.crypt == nil || !db.crypt.Enabled() {
		return EncryptionInfo{}
	}
	kdf := "argon2id"
	if db.desc.KDF == crypto.KDFNone {
		kdf = "raw-key"
	}
	return EncryptionInfo{
		Enabled: true,
		Cipher:  db.desc.Cipher.String(),
		KDF:     kdf,
		Epoch:   db.crypt.CurrentEpoch(),
	}
}

// ChangePassphrase replaces the passphrase that unlocks the database (spec 23
// §9.1). The old passphrase is verified first; a wrong one returns
// ErrWrongPassphrase and changes nothing.
//
// In a persisted database this is an O(1) operation: the master key is wrapped by
// a key-encryption key derived from the passphrase, so only the wrapped-key
// envelope in the header is rewritten and no data page is touched. That envelope
// lives with the on-disk header, which lands with the pager wiring. This build is
// process-resident with no persisted header, so the call re-derives the key
// material under the new passphrase in memory after checking the old one.
func (db *DB) ChangePassphrase(ctx context.Context, oldPass, newPass Passphrase) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.desc == nil {
		return ErrNotEncrypted
	}
	if db.desc.KDF != crypto.KDFArgon2id {
		return ErrNotEncrypted // a raw-key database has no passphrase to change
	}
	if _, err := crypto.OpenWithPassphrase(db.desc, oldPass.Reveal()); err != nil {
		return mapCryptoErr(err)
	}
	desc, _, err := crypto.NewDescriptor(newPass.Reveal(), db.desc.Cipher)
	if err != nil {
		return mapCryptoErr(err)
	}
	enc, err := crypto.OpenWithPassphrase(desc, newPass.Reveal())
	if err != nil {
		return mapCryptoErr(err)
	}
	_ = db.crypt.Close()
	db.crypt = enc
	db.desc = desc
	return nil
}

// RotateDEK advances the data encryption key to a new epoch (spec 23 §9.2). New
// page writes use the new key; pages written under earlier epochs stay readable
// because their epoch is recorded in each page and the old key stays loaded. The
// supplied secret re-authenticates the caller and re-derives the master key, which
// the database does not keep in memory between calls.
//
// Rotation is lazy: existing pages are not rewritten here. RekeyVacuum forces a
// full re-encryption when an operator wants to retire an old epoch's key.
func (db *DB) RotateDEK(ctx context.Context, secret Passphrase) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.desc == nil {
		return ErrNotEncrypted
	}
	if db.desc.Epoch == ^uint16(0) {
		return mapCryptoErr(crypto.ErrEpochExhausted)
	}
	mk, err := db.masterKeyFor(secret)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(mk)
	next := db.desc.Epoch + 1
	dek, err := crypto.DEK(mk, db.desc.DBID, next)
	if err != nil {
		return mapCryptoErr(err)
	}
	defer crypto.Zeroize(dek)
	db.crypt.(*crypto.PageEncryptor).AddDEK(next, dek)
	db.desc.Epoch = next
	return nil
}

// RekeyVacuumStats reports the result of a full re-encryption pass (spec 23 §9.3).
type RekeyVacuumStats struct {
	PagesRewritten uint64
	OldEpoch       uint16
	NewEpoch       uint16
}

// RekeyVacuum rewrites every page under a fresh epoch and retires every older key
// (spec 23 §9.3). It is the heavy counterpart to RotateDEK: where rotation is lazy
// and leaves old-epoch pages in place, a vacuum re-encrypts the whole database so
// the old DEKs can be released.
//
// The rewrite walks the pager, which this process-resident build does not persist
// to yet, so there are no on-disk pages to rewrite. The method rotates the epoch
// and reports zero pages rewritten; it becomes a full pass once the pager writes
// encrypted pages to a file.
func (db *DB) RekeyVacuum(ctx context.Context, secret Passphrase) (RekeyVacuumStats, error) {
	if err := ctx.Err(); err != nil {
		return RekeyVacuumStats{}, ctxErr(err)
	}
	db.mu.RLock()
	old := uint16(0)
	if db.desc != nil {
		old = db.desc.Epoch
	}
	db.mu.RUnlock()
	if err := db.RotateDEK(ctx, secret); err != nil {
		return RekeyVacuumStats{}, err
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	return RekeyVacuumStats{PagesRewritten: 0, OldEpoch: old, NewEpoch: db.desc.Epoch}, nil
}

// masterKeyFor re-derives the master key from a supplied secret, verifying it
// against the descriptor. The caller holds db.mu. The returned key is the caller's
// to zeroize.
func (db *DB) masterKeyFor(secret Passphrase) ([]byte, error) {
	switch db.desc.KDF {
	case crypto.KDFArgon2id:
		mk, err := crypto.MasterKey(secret.Reveal(), db.desc.Argon2Params())
		if err != nil {
			return nil, mapCryptoErr(err)
		}
		if !crypto.VerifyMasterKey(mk, db.desc.VerificationTag) {
			crypto.Zeroize(mk)
			return nil, ErrWrongPassphrase
		}
		return mk, nil
	default:
		return nil, ErrNotEncrypted // raw-key rotation needs the KMS key, not a passphrase
	}
}

// mapCryptoErr maps a crypto-package error to the library's stable vocabulary.
func mapCryptoErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, crypto.ErrWrongPassphrase):
		return ErrWrongPassphrase
	default:
		return err
	}
}
