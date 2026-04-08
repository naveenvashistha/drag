package hasher

import (
	"io"
	"os"
	"fmt"
	// xxhash library, reference: https://pkg.go.dev/github.com/cespare/xxhash
	"github.com/cespare/xxhash/v2"
)

/*
	params:
		filePath: path of the file as saved in the filesystem

	return: 
		a numeric hash of type uint64
		ex: 6925293637973706096
*/
func HashFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// initialise the hasher
	hasher := xxhash.New()

	// Copy function copies the file from the source to destination (which is hasher here)
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	// Sum64() returns the numeric hash directly but we convert it to a hex string for better readability and storage efficiency in the database.
	return fmt.Sprintf("%016x", hasher.Sum64()), nil
}
