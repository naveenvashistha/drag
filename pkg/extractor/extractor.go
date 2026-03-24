package extractor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// --- Configuration & Custom Errors ---

const (
	// MaxFileSize prevents Out-Of-Memory (OOM) panics. Set to 50MB.
	MaxFileSize = 50 * 1024 * 1024
)

var (
	// Standardized errors allow the caller (the database orchestrator)
	// to handle specific failure cases gracefully.
	ErrFileNotFound      = errors.New("file does not exist")
	ErrIsDirectory       = errors.New("path is a directory, not a file")
	ErrFileTooLarge      = errors.New("file exceeds maximum allowed size")
	ErrUnsupportedFormat = errors.New("unsupported file format")
	ErrEmptyContent      = errors.New("no text could be extracted")

	// Pre-compile regular expressions for performance during sanitization
	multipleNewlinesRegex = regexp.MustCompile(`\n{3,}`)
	multipleSpacesRegex   = regexp.MustCompile(`[ \t]+`)
)

// --- Main Extractor Function ---

// ExtractText takes a physical file path, safely validates the file, detects
// its true format via magic bytes, routes it to the correct parser, and
// returns cleaned, raw text ready for the chunker.
func ExtractText(filePath string) (string, error) {

	// =========================================================
	// PHASE 1: Pre-flight Checks (Safety First)
	// =========================================================

	// 1. Get file metadata without loading the file into memory
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrFileNotFound
		}
		return "", fmt.Errorf("failed to stat file: %w", err)
	}

	// 2. Reject directories immediately
	if fileInfo.IsDir() {
		return "", ErrIsDirectory
	}

	// 3. Enforce file size limits
	if fileInfo.Size() > MaxFileSize {
		return "", ErrFileTooLarge
	}

	// 4. Open the file safely
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close() // Ensures the file is closed no matter how the function exits

	// =========================================================
	// PHASE 2: True Type Detection (Magic Bytes)
	// =========================================================

	// Read the first 512 bytes required by http.DetectContentType
	buffer := make([]byte, 512)
	bytesRead, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read file header: %w", err)
	}

	// Detect true format.
	mimeType := http.DetectContentType(buffer[:bytesRead])

	// Reset the file pointer back to the beginning so parsers can read from the start
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to reset file pointer: %w", err)
	}

	// =========================================================
	// PHASE 3: Routing & Extraction
	// =========================================================

	var rawText string
	var extractErr error

	// Route based on the detected MIME type
	switch {
	case strings.HasPrefix(mimeType, "text/plain"):
		ext := strings.ToLower(filepath.Ext(filePath))

		// ALLOWLIST: Only accept these specific plain text formats
		validTextExts := map[string]bool{
			".txt": true,
			".md":  true,
			// You can add ".csv" back here in the future if you ever build a tabular pipeline!
		}

		if !validTextExts[ext] {
			return "", fmt.Errorf("%w: plain text format '%s' is not supported", ErrUnsupportedFormat, ext)
		}

		rawText, extractErr = extractPlainText(file)

	case mimeType == "application/pdf":
		rawText, extractErr = extractPDF(file)

	// Docx files are zip archives containing XML.
	case mimeType == "application/zip" || mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		rawText, extractErr = extractDOCX(file)

	default:
		// Reject unsupported formats (including tabular data/CSVs)
		return "", fmt.Errorf("%w: %s", ErrUnsupportedFormat, mimeType)
	}

	if extractErr != nil {
		return "", fmt.Errorf("extraction failed: %w", extractErr)
	}

	// =========================================================
	// PHASE 4: Sanitization
	// =========================================================

	cleanedText := sanitizeText(rawText)

	// Final check: did the extraction yield anything useful?
	if len(cleanedText) == 0 {
		return "", ErrEmptyContent
	}

	return cleanedText, nil
}

// --- Sanitization Logic ---

// sanitizeText cleans the extracted text to ensure it doesn't break
// vector embeddings or database C-bindings.
func sanitizeText(input string) string {
	// 1. Strip null bytes (\x00). These are fatal to C-bindings like sqlite-vec.
	cleaned := strings.ReplaceAll(input, "\x00", "")

	// 2. Ensure valid UTF-8. Drops invalid byte sequences.
	cleaned = strings.ToValidUTF8(cleaned, "")

	// 3. Normalize line endings (Windows/Mac to Unix).
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")

	// 4. Collapse excessive vertical whitespace (turns 3+ newlines into exactly 2).
	cleaned = multipleNewlinesRegex.ReplaceAllString(cleaned, "\n\n")

	// 5. Collapse excessive horizontal whitespace.
	cleaned = multipleSpacesRegex.ReplaceAllString(cleaned, " ")

	// 6. Trim leading/trailing whitespace from the document.
	return strings.TrimSpace(cleaned)
}

// --- Stub Functions (To be implemented with specific libraries later) ---

func extractPlainText(file *os.File) (string, error) {
	// For plain text, we can just read the whole file into a string
	// (Since we already enforced a 50MB max file size in Phase 1)
	bytes, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func extractPDF(file *os.File) (string, error) {
	// TODO: Implement PDF parsing (e.g., using github.com/ledongthuc/pdf)
	return "Mock PDF content", nil
}

func extractDOCX(file *os.File) (string, error) {
	// TODO: Implement DOCX parsing
	return "Mock DOCX content", nil
}
