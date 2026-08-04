package platformapi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

// Encryptor is platformapi's OWN copy of the credential intake encrypt-on-write primitive —
// byte-for-byte the same shape as adminapi.Encryptor (internal/adminapi/crypto.go). Duplicated
// rather than imported: platformapi cannot import internal/adminapi (OD-7's package-isolation
// precedent, cited at Docs/history/phase-2-strip-shared-pool.md §8.6) — the two Domain A/B surfaces never
// share Go code, only the underlying KMS key material main.go loads once and hands to both
// constructors. Encrypt-ONLY: there is no Decrypt method here either, mirroring the same
// structural guarantee — even a compromised platform surface can write new credentials but never
// read one back.
//
// AES-256-GCM, 12-byte random nonce per encryption, 128-bit tag appended inside the ciphertext.
// AAD = account.id (raw 16 bytes) ‖ credential_key_id (utf8) binds the ciphertext to its row
// (module 2 OD-3, LOCKED-ADOPTED) — the exact encode contract internal/credential's Decryptor
// rebuilds on the send path, regardless of which surface (admin or platform) created the row.
type Encryptor struct {
	key   []byte
	keyID string
}

// NewEncryptor builds an Encryptor from raw key material + the current write-key alias.
func NewEncryptor(key []byte, keyID string) (*Encryptor, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("credential key must be 32 bytes for AES-256, got %d", len(key))
	}
	if keyID == "" {
		return nil, fmt.Errorf("credential_key_id (write-key alias) must be non-empty")
	}
	if _, err := cipher.NewGCM(mustBlock(key)); err != nil {
		return nil, fmt.Errorf("init gcm: %w", err)
	}
	return &Encryptor{key: append([]byte(nil), key...), keyID: keyID}, nil
}

// KeyID returns the current write-key alias stored in credential_key_id.
func (e *Encryptor) KeyID() string { return e.keyID }

// Encrypt seals plaintext under the current key with a FRESH random nonce and AAD =
// accountID(16 raw bytes) ‖ keyID. The plaintext is never retained; callers zero their own copy.
func (e *Encryptor) Encrypt(accountID [16]byte, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("nonce: %w", err)
	}
	aad := aadFor(accountID, e.keyID)
	ct := gcm.Seal(nil, nonce, plaintext, aad)
	return ct, nonce, nil
}

// aadFor builds the credential AAD = raw 16-byte account UUID ‖ utf8(credential_key_id). Shared
// encode contract with adminapi.aadFor / internal/credential.aadFor / the harness's reference
// implementation — each package keeps its own copy (OD-7), but the byte layout must never drift.
func aadFor(accountUUID [16]byte, keyID string) []byte {
	out := make([]byte, 0, 16+len(keyID))
	out = append(out, accountUUID[:]...)
	out = append(out, []byte(keyID)...)
	return out
}

func mustBlock(key []byte) cipher.Block {
	b, _ := aes.NewCipher(key)
	return b
}

// zero overwrites a byte slice (best-effort secret hygiene: plaintext lives in process memory
// only and is cleared right after encryption).
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
