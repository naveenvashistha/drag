package hasher

import (
	"crypto/sha256"
	"encoding/hex"
)

// CalculateHash generates a SHA256 hash of the input string
func CalculateHash(input string) (string, error) {
	hash := sha256.Sum256([]byte(input))
	return hashToString(hash), nil
}

func hashToString(hash [32]byte) string {
	return hex.EncodeToString(hash[:])
}
