package main // Note: Change this to 'package extractor' if placing in a specific pkg/extractor folder

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// -----------------------------------------------------------------------
// GLOBAL VARIABLES & REGEX COMPILATION
// -----------------------------------------------------------------------
// We pre-compile these regular expressions when the application starts.
// Compiling regex inside a function that runs on every file would cause massive performance lag.
var (
	// Matches 3 or more consecutive newlines to squash giant blank pages
	multipleNewlinesRegex = regexp.MustCompile(`\n{3,}`)
	// Matches sequences of spaces or tabs to squash massive horizontal gaps
	multipleSpacesRegex = regexp.MustCompile(`[ \t]+`)
)

// -----------------------------------------------------------------------
// MAIN EXTRACTOR FUNCTION
// -----------------------------------------------------------------------

// ExtractText takes a physical file path and routes it to the appropriate text parser.
//
// ARCHITECTURAL NOTE:
// This function operates on a "Trust but Verify" model. It assumes that upstream
// validation (e.g., FileWatcher) has already confirmed the file exists, is under
// the 100MB size limit, and is not a hidden/system file.
func ExtractText(filePath string) (string, error) {
	// 1. NORMALIZE EXTENSION
	// Extract the extension and force lowercase (e.g., "Report.PDF" -> ".pdf").
	// This prevents the switch statement from failing due to weird capitalization.
	ext := strings.ToLower(filepath.Ext(filePath))

	var rawText string
	var parseErr error

	// 2. ROUTING
	// Strictly handle ONLY the extensions explicitly defined in our project scope.
	switch ext {
	case ".pdf":
		rawText, parseErr = parsePDF(filePath)
	case ".docx":
		rawText, parseErr = parseDocx(filePath)
	case ".pptx":
		rawText, parseErr = parsePptx(filePath)
	case ".txt", ".md":
		rawText, parseErr = parsePlainText(filePath)

	// 3. THE CATCH-ALL REJECTION
	// If a file slips past the FileWatcher (e.g., .csv, .mp4, .jpg), it lands here.
	default:
		// Edge Case: The file has no extension at all (e.g., "Makefile").
		if ext == "" {
			return "", fmt.Errorf(
				"extraction error: file '%s' has no extension. "+
					"The extractor requires a valid extension to determine the parsing strategy.",
				filePath,
			)
		}

		// Verbose error to help future developers debug unsupported file drops.
		return "", fmt.Errorf(
			"extraction error: unsupported file extension '%s' for file '%s'. "+
				"Note: Media files (images/videos) and tabular data (like .csv) are intentionally "+
				"outside the current extraction scope. If '%s' should be supported, please add it "+
				"to the validExts whitelist in the FileWatcher AND implement a corresponding parser here.",
			ext, filePath, ext,
		)
	}

	// If the specific parser failed (e.g., corrupted PDF), bubble the error up
	if parseErr != nil {
		return "", fmt.Errorf("failed to parse %s file: %w", ext, parseErr)
	}

	// 4. SANITIZATION (The Janitor)
	// Extracted text from documents is often incredibly messy. We must clean it
	// before it reaches the chunker or the vector database.
	cleanedText := sanitizeText(rawText)

	// Ensure we aren't returning an empty string if a document was purely images
	// or empty space that got scrubbed away.
	if len(cleanedText) == 0 {
		return "", fmt.Errorf("extraction error: file '%s' contained no readable text", filePath)
	}

	return cleanedText, nil
}

// -----------------------------------------------------------------------
// SANITIZATION LOGIC
// -----------------------------------------------------------------------

// sanitizeText scrubs raw parsed text to ensure it is safe for vector embeddings
// and SQLite C-bindings.
func sanitizeText(input string) string {
	// CRITICAL: Strip null bytes (\x00). C-bindings (like sqlite-vec) interpret
	// null bytes as the absolute end of a string. If left in, they will silently
	// truncate the document in the database.
	cleaned := strings.ReplaceAll(input, "\x00", "")

	// Ensure the string is valid UTF-8, dropping any broken byte sequences.
	cleaned = strings.ToValidUTF8(cleaned, "")

	// Standardize line endings from Windows (\r\n) or old Mac (\r) to Unix (\n).
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")

	// Condense massive blocks of empty space (saves AI embedding tokens).
	cleaned = multipleNewlinesRegex.ReplaceAllString(cleaned, "\n\n")
	cleaned = multipleSpacesRegex.ReplaceAllString(cleaned, " ")

	// Remove trailing/leading whitespace from the very ends of the document.
	return strings.TrimSpace(cleaned)
}

// -----------------------------------------------------------------------
// PARSER STUB FUNCTIONS
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
	// os.ReadFile loads the entire file into memory at once.
	// This is safe to use here because the upstream FileWatcher strictly
	// prevents files over 100MB from reaching this function.
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read plain text file '%s': %w", path, err)
	}
	return string(content), nil
}
