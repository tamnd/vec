---
title: "Encryption at rest"
description: "Encrypt a .vec file with a passphrase or a raw key, change the secret, and rotate the data key."
weight: 50
---

vec encrypts the database file page by page.
With encryption on, every data page is sealed before it touches disk and opened on the way back in, so a stolen `.vec` file is ciphertext.
The hot path of an unencrypted database pays nothing for this: with no secret configured, there is no crypto on the read or write path at all.

## Open with a passphrase

Pass `WithPassphrase` at open time.
The master key is derived from the passphrase with Argon2id, so opening the same database later needs the same passphrase, and a wrong one fails before any data page is read:

```go
db, err := vec.Open("secret.vec", vec.WithPassphrase(vec.Passphrase("correct horse battery staple")))
if err != nil {
	// vec.ErrWrongPassphrase if the passphrase does not match.
	log.Fatal(err)
}
defer db.Close()
```

`Passphrase` redacts itself in logs, JSON, and `%v` formatting, so a stray log line does not leak the secret.

## Open with a raw key

When an external KMS or HSM manages keys, supply a 32-byte key directly with `WithEncryptionKey`.
The key is used as the master key with no passphrase KDF:

```go
key := vec.EncryptionKey(rawBytesFromKMS) // 32 bytes
db, err := vec.Open("secret.vec", vec.WithEncryptionKey(key))
```

## Choosing a cipher

A newly created encrypted database defaults to AES-256-GCM.
On a host without AES hardware acceleration, ChaCha20-Poly1305 is the faster choice:

```go
db, err := vec.Open("secret.vec",
	vec.WithPassphrase(pass),
	vec.WithCipher(crypto.CipherChaCha20Poly1305),
)
```

The cipher is recorded in the file header, so opening an existing encrypted database uses its stored cipher and ignores `WithCipher`.

## Inspect the state

`Encryption` reports the configuration without ever returning key material:

```go
info := db.Encryption()
fmt.Println(info.Enabled, info.Cipher, info.KDF) // true AES-256-GCM argon2id
```

## Change the passphrase

`ChangePassphrase` re-wraps the data key under a new passphrase.
It rewraps the key, not the whole file, so it is cheap regardless of database size:

```go
err := db.ChangePassphrase(ctx,
	vec.Passphrase("old secret"),
	vec.Passphrase("new secret"),
)
```

## Rotate the data key

`RotateDEK` issues a fresh data-encryption key for new writes, advancing the write epoch.
`RekeyVacuum` goes further: it rewrites every page under the new key, so old ciphertext no longer exists in the file:

```go
// New writes use a fresh key; existing pages stay under the old one.
err := db.RotateDEK(ctx, vec.Passphrase("secret"))

// Rewrite every page under the current key, leaving no old ciphertext.
stats, err := db.RekeyVacuum(ctx, vec.Passphrase("secret"))
```

Use `RotateDEK` for routine rotation and `RekeyVacuum` when policy requires the old key's ciphertext to be gone from the file.

## Serving an encrypted database

The [server](/guides/serving-over-the-network/) opens the file the same way: configure the passphrase where you build the server, and it serves the decrypted view over the network while the file on disk stays encrypted.
