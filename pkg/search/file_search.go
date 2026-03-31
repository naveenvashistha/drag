package search

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileSearch searches for files matching a pattern in a directory
type FileSearch struct {
	RootPath string
	Pattern  string
}

// NewFileSearch creates a new FileSearch instance
func NewFileSearch(rootPath, pattern string) *FileSearch {
	return &FileSearch{
		RootPath: rootPath,
		Pattern:  pattern,
	}
}

// Search recursively searches for files matching the pattern
func (fs *FileSearch) Search() ([]string, error) {
	var results []string

	err := filepath.Walk(fs.RootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			matched, err := filepath.Match(fs.Pattern, filepath.Base(path))
			if err != nil {
				return err
			}
			if matched {
				results = append(results, path)
			}
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	return results, nil
}
