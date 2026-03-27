package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExtractText routes the file to the appropriate parser strictly based on its extension.
// IMPORTANT: This function assumes that upstream validation (like a FileWatcher)
// has already verified the file is safe, not a disguised executable, and within size limits.
func ExtractText(filePath string) (string, error) {

	// -------------------------------------------------------------------
	// STEP 1: EXTRACT AND NORMALIZE THE EXTENSION
	// -------------------------------------------------------------------
	// We grab the extension from the file path and convert it to lowercase.
	// This ensures that files named "document.PDF" and "document.pdf"
	// are treated exactly the same way.
	ext := strings.ToLower(filepath.Ext(filePath))

	// -------------------------------------------------------------------
	// STEP 2: ROUTE BASED ON EXTENSION
	// -------------------------------------------------------------------
	// We strictly handle ONLY the extensions explicitly defined in our project scope.
	switch ext {

	case ".pdf":
		return parsePDF(filePath)

	case ".docx":
		return parseDocx(filePath)

	case ".pptx":
		return parsePptx(filePath)

	case ".txt", ".md":
		return parsePlainText(filePath)

	// -------------------------------------------------------------------
	// STEP 3: THE DEFAULT CATCH-ALL (DETAILED ERROR HANDLING)
	// -------------------------------------------------------------------
	default:
		// Edge Case: The file has no extension at all (e.g., "Makefile" or a corrupted name).
		if ext == "" {
			return "", fmt.Errorf(
				"extraction error: file '%s' has no extension. "+
					"The extractor requires a valid extension to determine the parsing strategy.",
				filePath,
			)
		}

		// The Catch-All: This is where images, videos, CSVs, and unknown formats land.
		// We provide a highly verbose error so future developers know exactly why it was rejected,
		// and what steps they need to take if they want to add support for it later.
		return "", fmt.Errorf(
			"extraction error: unsupported file extension '%s' for file '%s'. "+
				"Note: Media files (images/videos) and tabular data (like .csv) are intentionally "+
				"outside the current extraction scope. If '%s' should be supported, please add it "+
				"to the validExts whitelist in the FileWatcher AND implement a corresponding parser here.",
			ext, filePath, ext,
		)
	}
}

// -----------------------------------------------------------------------
// PARSER FUNCTIONS
// -----------------------------------------------------------------------

// parsePDF extracts text from standard PDF documents.
func parsePDF(path string) (string, error) {
	// TODO: Implement actual PDF extraction (e.g., using github.com/ledongthuc/pdf)
	return "Extracted PDF content from " + path, nil
}

// parseDocx extracts text from Microsoft Word (.docx) documents.
func parseDocx(path string) (string, error) {
	// TODO: Implement actual DOCX extraction (e.g., using baliance.com/gooxml)
	return "Extracted DOCX content from " + path, nil
}

// parsePptx extracts text from Microsoft PowerPoint (.pptx) presentations.
func parsePptx(path string) (string, error) {
	// TODO: Implement actual PPTX extraction
	return "Extracted PPTX content from " + path, nil
}

// parsePlainText reads the entire contents of standard text files (.txt, .md).
func parsePlainText(path string) (string, error) {
	// os.ReadFile loads the whole file into memory.
	// This is safe here because the upstream FileWatcher enforces a strict size limit.
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read plain text file '%s': %w", path, err)
	}
	return string(content), nil
}
