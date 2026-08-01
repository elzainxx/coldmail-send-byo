package platformapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// newUUIDv4 generates a random RFC 4122 v4 UUID application-side, mirroring adminapi's own
// newUUIDv4 (module 2 OD-4) — the id must exist before Encrypt so the credential AAD is bound to
// the exact row in a single INSERT. Duplicated per package (OD-7 isolation), not imported.
func newUUIDv4() (string, [16]byte, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", b, fmt.Errorf("uuid rand: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b), b, nil
}

// uuidToBytes parses a canonical UUID string into its 16 raw bytes.
func uuidToBytes(s string) ([16]byte, error) {
	var out [16]byte
	if !uuidRe.MatchString(s) {
		return out, fmt.Errorf("not a uuid: %q", s)
	}
	clean := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	raw, err := hex.DecodeString(clean)
	if err != nil || len(raw) != 16 {
		return out, fmt.Errorf("uuid decode: %q", s)
	}
	copy(out[:], raw)
	return out, nil
}

func formatUUID(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
