// Package id generates sandbox identifiers and API keys.
package id

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// Sandbox returns an id of the form sbx_<16 hex chars>.
func Sandbox() string {
	return "sbx_" + hex.EncodeToString(random(8))
}

// Key returns a new API key plus the sha256 hex digest and a non-secret prefix.
// The plaintext is shown once; only the digest is stored.
func Key() (plaintext, hash, prefix string) {
	// 20 bytes is 160 bits and encodes to 32 base32 characters with no padding.
	raw := random(20)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	plaintext = "sbx_" + strings.ToLower(enc)
	hash = Hash(plaintext)
	prefix = plaintext
	if len(prefix) > 12 {
		prefix = plaintext[:12]
	}
	return plaintext, hash, prefix
}

// RecordID is a stable id for a stored API key row.
func RecordID() string {
	return "key_" + hex.EncodeToString(random(8))
}

// Hash is the sha256 hex digest of an API key.
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return b
}
