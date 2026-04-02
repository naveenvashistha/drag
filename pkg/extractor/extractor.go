package extractor

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	// Third-party library for PDF parsing
	"github.com/ledongthuc/pdf"
)

// -----------------------------------------------------------------------
// GLOBAL VARIABLES & COMPILED REGEX
// -----------------------------------------------------------------------

var (
	// ErrEmptyContent prevents the downstream ingestion of blank documents,
	// safeguarding the vector database from storing useless embeddings.
	ErrEmptyContent = errors.New("extraction error: file contained no readable text")

	// Whitespace Optimization
	// These patterns condense excessive gaps, optimizing the resulting string 
	// to conserve token usage during the AI embedding phase.
	multipleNewlinesRegex = regexp.MustCompile(`\n{3,}`)
	multipleSpacesRegex   = regexp.MustCompile(`[ \t]+`)

	// PDF Heuristics
	// PDFs rely on absolute coordinate positioning rather than semantic text flow.
	// These regex patterns reconstruct words severed by formatting artifacts.
	
	// pdfBrokenWordRegex repairs coordinate-based word tearing (e.g., "M\nain" -> "Main").
	pdfBrokenWordRegex = regexp.MustCompile(`([a-zA-Z])\n([a-zA-Z])`)

	// pdfHyphenRegex repairs hyphenated line breaks (e.g., "pipe-\nline" -> "pipeline").
	pdfHyphenRegex = regexp.MustCompile(`([a-zA-Z])-\n([a-zA-Z])`)
)

// -----------------------------------------------------------------------
// MAIN EXTRACTOR PIPELINE
// -----------------------------------------------------------------------

// ExtractText acts as the primary routing mechanism for the ingestion pipeline.
// It identifies the file type, delegates to the appropriate parsing algorithm,
// and enforces strict sanitization protocols before returning the raw string.
func ExtractText(filePath string) (string, error) {
	// Normalize the extension to lowercase to ensure reliable routing 
	// regardless of original file capitalization (e.g., ".PDF" -> ".pdf").
	ext := strings.ToLower(filepath.Ext(filePath))

	var rawText string
	var parseErr error

	// 1. File Type Routing
	// Enforces a strict whitelist of supported extensions.
	switch ext {
	case ".pdf":
		rawText, parseErr = parsePDF(filePath)
	case ".docx":
		rawText, parseErr = parseDocx(filePath)
	case ".pptx":
		rawText, parseErr = parsePptx(filePath)
	case ".txt", ".md":
		rawText, parseErr = parsePlainText(filePath)
	default:
		if ext == "" {
			return "", fmt.Errorf("extraction error: file '%s' has no extension", filepath.Base(filePath))
		}
		return "", fmt.Errorf(
			"extraction skipped for '%s': unsupported extension '%s'.\n"+
				"-> Allowed extensions are: .txt, .md, .pdf, .docx, .pptx.",
			filepath.Base(filePath), ext,
		)
	}

	if parseErr != nil {
		return "", fmt.Errorf("failed to parse %s file: %w", ext, parseErr)
	}

	// 2. Universal Data Hygiene
	cleanedText := sanitizeText(rawText)

	if len(cleanedText) == 0 {
		return "", ErrEmptyContent
	}

	return cleanedText, nil
}

// -----------------------------------------------------------------------
// SANITIZATION LOGIC
// -----------------------------------------------------------------------

// sanitizeText applies global hygiene rules to prevent database corruption
// and optimize the string for vectorization.
func sanitizeText(input string) string {
	// Strip null bytes (\x00) which can silently truncate C-based database inserts (e.g., SQLite).
	cleaned := strings.ReplaceAll(input, "\x00", "")
	
	// Strip invalid byte sequences to guarantee strict UTF-8 compliance.
	cleaned = strings.ToValidUTF8(cleaned, "")
	
	// Normalize legacy Mac (\r) and Windows (\r\n) line endings to standard Unix (\n).
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
	
	// Collapse massive whitespace gaps to conserve AI tokens.
	cleaned = multipleNewlinesRegex.ReplaceAllString(cleaned, "\n\n")
	cleaned = multipleSpacesRegex.ReplaceAllString(cleaned, " ")
	
	return strings.TrimSpace(cleaned)
}

// -----------------------------------------------------------------------
// FORMAT-SPECIFIC PARSERS
// -----------------------------------------------------------------------

// parsePlainText performs a direct memory load of standard text files.
// NOTE: This assumes an upstream ingestion layer (like a FileWatcher) 
// has already enforced strict file size limits to prevent memory exhaustion.
func parsePlainText(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// parsePDF extracts raw text from PDF binaries. It relies heavily on regex 
// heuristics to repair words torn apart by the PDF's coordinate-based rendering.
func parsePDF(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	b, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	buf.ReadFrom(b)

	rawPDFText := buf.String()

	// Silent Failure Detection
	// The underlying library cannot perform OCR. If the PDF is an image-based scan
	// or password-protected, it silently returns empty text. This traps that state.
	if len(strings.TrimSpace(rawPDFText)) == 0 {
		return "", errors.New("PDF extraction failed: file contains no readable text (it may be an image-based scan or password-protected)")
	}

	// Sequential heuristic application to reconstruct torn semantics.
	rawPDFText = pdfHyphenRegex.ReplaceAllString(rawPDFText, "$1$2")
	rawPDFText = pdfBrokenWordRegex.ReplaceAllString(rawPDFText, "$1$2")
	rawPDFText = strings.ReplaceAll(rawPDFText, "\n", " ")

	return rawPDFText, nil
}

// parseDocx extracts text from Word documents by treating the file as a ZIP archive
// and directly streaming the primary 'document.xml' file.
func parseDocx(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			return extractXMLText(rc)
		}
	}
	return "", errors.New("invalid docx format: word/document.xml not found")
}

// parsePptx extracts text from PowerPoint presentations. It mandates an internal
// sorting mechanism because standard ZIP archives do not guarantee chronological file order.
func parsePptx(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()

	// Intermediate structure to map slides to their chronological index.
	type slide struct {
		file  *zip.File
		index int
	}
	var slides []slide

	// 1. Locate and map all slide XML files.
	for _, f := range r.File {
		prefix := "ppt/slides/slide"
		suffix := ".xml"
		if strings.HasPrefix(f.Name, prefix) && strings.HasSuffix(f.Name, suffix) {
			// Extract the slide number from the filename to ensure correct chronological sorting.
			numStr := strings.TrimSuffix(strings.TrimPrefix(f.Name, prefix), suffix)
			num, err := strconv.Atoi(numStr)
			if err == nil {
				slides = append(slides, slide{file: f, index: num})
			}
		}
	}

	// 2. Sort the mapped slides numerically.
	sort.Slice(slides, func(i, j int) bool {
		return slides[i].index < slides[j].index
	})

	var textBuilder strings.Builder

	// 3. Sequentially extract and concatenate slide contents.
	for _, s := range slides {
		rc, err := s.file.Open()
		if err != nil {
			continue // Skip corrupted individual slides to salvage the presentation
		}

		slideText, _ := extractXMLText(rc)
		slideText = strings.TrimSpace(slideText)

		if len(slideText) > 0 {
			// Flatten internal formatting newlines to preserve token density.
			slideText = strings.ReplaceAll(slideText, "\n", " ")
			textBuilder.WriteString(slideText + " ")
		}

		rc.Close()
	}

	if textBuilder.Len() == 0 {
		return "", errors.New("no readable slides found in pptx")
	}

	return strings.TrimSpace(textBuilder.String()), nil
}

// -----------------------------------------------------------------------
// HELPER FUNCTIONS
// -----------------------------------------------------------------------

// extractXMLText utilizes a high-performance, low-memory stream decoder.
// It prevents application panic when parsing exceptionally large Office documents 
// by reading the XML token-by-token rather than loading the entire DOM into memory.
func extractXMLText(rc io.ReadCloser) (string, error) {
	decoder := xml.NewDecoder(rc)
	var textBuilder strings.Builder
	inTextTag := false

	for {
		t, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		switch element := t.(type) {
		case xml.StartElement:
			// Office Open XML utilizes the 't' tag for text content (<w:t> or <a:t>).
			if element.Name.Local == "t" {
				inTextTag = true
			}
		case xml.EndElement:
			if element.Name.Local == "t" {
				inTextTag = false
			} else if element.Name.Local == "p" {
				// Office Open XML utilizes the 'p' tag for paragraphs (<w:p> or <a:p>).
				// Injecting a space at the close of a paragraph prevents word collision
				// between separate paragraphs while preserving intra-word formatting splits.
				textBuilder.WriteString(" ")
			}
		case xml.CharData:
			if inTextTag {
				textBuilder.Write(element)
			}
		}
	}

	return textBuilder.String(), nil
}