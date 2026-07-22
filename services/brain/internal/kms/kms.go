// Package kms is the Brain's credential-decryption boundary. The Brain is the SOLE
// decryptor (CLAUDE.md Rule 6): only this process loads the key. In Phase 0 the "KMS" is a
// local dev key file (prod is a real cloud KMS whose Decrypt principal is the Brain role
// only — the IAM-separation precondition, verified by deployment sign-off, not code).
//
// The box has no equivalent package and no key material — enforced structurally (no KMS SDK
// in the box service) and by container mounts (a harness test asserts the box cannot read
// the key file).
package kms

import (
	"fmt"
	"os"
)

// Decryptor holds the dev key material in-process. Real Decrypt is Phase 2 — Phase 0 only
// proves the key path is Brain-only and never logged.
type Decryptor struct {
	key []byte
}

// LoadDev reads the dev key file. It requires a non-empty key and never logs its contents.
func LoadDev(path string) (*Decryptor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dev kms key %q: %w", path, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("dev kms key %q is empty", path)
	}
	return &Decryptor{key: b}, nil
}

// KeyLen reports the key length for a startup log line WITHOUT exposing key material.
func (d *Decryptor) KeyLen() int {
	if d == nil {
		return 0
	}
	return len(d.key)
}
