package crypto

import (
	"errors"
	"fmt"
)

// Key-setup errors. These surface at open time or at key-rotation time, before
// any page is processed.
var (
	ErrInvalidSalt         = errors.New("vec/crypto: salt must be 32 bytes")
	ErrInvalidArgon2Params = errors.New("vec/crypto: argon2id time, memory, and threads must be non-zero")
	ErrInvalidKeyLength    = errors.New("vec/crypto: raw key must be 32 bytes")
	ErrUnknownCipher       = errors.New("vec/crypto: unknown cipher")
	ErrNoDEK               = errors.New("vec/crypto: no DEK supplied")
	ErrWrongPassphrase     = errors.New("vec/crypto: wrong passphrase")
	ErrPageTooShort        = errors.New("vec/crypto: page shorter than the AEAD envelope")
	ErrEpochExhausted      = errors.New("vec/crypto: key epoch exhausted; run RekeyVacuum to reset")
)

// ErrPageAuthFailed is returned when a page fails AEAD authentication (spec 23
// section 17.1). The cause is a wrong key, a tampered page, or storage
// corruption. The reader returns this rather than plaintext, and the pager must
// not cache a page that failed.
type ErrPageAuthFailed struct {
	PageNo    uint64
	PageClass uint8
	Epoch     uint16
	Reason    string
}

func (e ErrPageAuthFailed) Error() string {
	return fmt.Sprintf(
		"vec/crypto: page %d (class %d, epoch %d) failed AEAD authentication: %s; "+
			"possible causes: wrong key, tampered page, storage corruption",
		e.PageNo, e.PageClass, e.Epoch, e.Reason,
	)
}

// ErrMissingDEK is returned when a page references an epoch whose DEK is no longer
// in memory (spec 23 section 17.2). Recovery is to reload the DEK for that epoch
// from the master key.
type ErrMissingDEK struct {
	Epoch uint16
}

func (e ErrMissingDEK) Error() string {
	return fmt.Sprintf(
		"vec/crypto: no DEK available for epoch %d; was the old DEK released before RekeyVacuum completed?",
		e.Epoch,
	)
}
