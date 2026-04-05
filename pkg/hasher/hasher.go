package hasher

import (
	"crypto/sha256"
	"encoding/hex"
	
	"io"
	"os"

	// xxhash library, reference: https://pkg.go.dev/github.com/cespare/xxhash
	"github.com/cespare/xxhash/v2"
)

// CalculateHash generates a SHA256 hash of the input string
func CalculateHash(input string) (string, error) {
	hash := sha256.Sum256([]byte(input))
	return hashToString(hash), nil
}

func hashToString(hash [32]byte) string {
	return hex.EncodeToString(hash[:])
}

/*
	params:
		filePath: path of the file as saved in the filesystem

	return: 
		a numeric hash of type uint64
		ex: 6925293637973706096
*/
func hashFile(filePath string) (uint64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	// initialise the hasher
	hasher := xxhash.New()

	// Copy function copies the file from the source to destination (which is hasher here)
	if _, err := io.Copy(hasher, file); err != nil {
		return 0, err
	}

	// Sum64() returns the numeric hash directly
	return hasher.Sum64(), nil
}
