package hasher

import (
	"crypto/sha256"
	"fmt"
)

// CalculateHash generates a SHA256 hash of the input string
func CalculateHash(input string) string {
	hash := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", hash)
}
