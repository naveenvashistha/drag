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
	"strings"

	// Third-party library for PDF parsing
	"github.com/ledongthuc/pdf"
)

// -----------------------------------------------------------------------
// GLOBAL VARIABLES
// -----------------------------------------------------------------------

var (
	ErrEmptyContent = errors.New("extraction error: file contained no readable text")

	multipleNewlinesRegex = regexp.MustCompile(`\n{3,}`)
	multipleSpacesRegex   = regexp.MustCompile(`[ \t]+`)

	// pdfBrokenWordRegex repairs coordinate-based word tearing common in PDFs (e.g., "M\nain" -> "Main")
	pdfBrokenWordRegex = regexp.MustCompile(`([a-zA-Z])\n([a-zA-Z])`)

	// pdfHyphenRegex repairs hyphenated line breaks (e.g., "pipe-\nline" -> "pipeline")
	pdfHyphenRegex = regexp.MustCompile(`([a-zA-Z])-\n([a-zA-Z])`)
)

// -----------------------------------------------------------------------
// MAIN EXTRACTOR
// -----------------------------------------------------------------------

func ExtractText(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	var rawText string
	var parseErr error

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

	cleanedText := sanitizeText(rawText)

	if len(cleanedText) == 0 {
		return "", ErrEmptyContent
	}

	return cleanedText, nil
}

// -----------------------------------------------------------------------
// SANITIZATION LOGIC
// -----------------------------------------------------------------------

func sanitizeText(input string) string {
	cleaned := strings.ReplaceAll(input, "\x00", "")
	cleaned = strings.ToValidUTF8(cleaned, "")
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
	cleaned = multipleNewlinesRegex.ReplaceAllString(cleaned, "\n\n")
	cleaned = multipleSpacesRegex.ReplaceAllString(cleaned, " ")
	return strings.TrimSpace(cleaned)
}

// -----------------------------------------------------------------------
// FULLY IMPLEMENTED PARSERS
// -----------------------------------------------------------------------

// parsePlainText reads the entire contents of standard text files.
func parsePlainText(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// parsePDF extracts raw text from PDF documents using a lightweight library
// and aggressively resolves PDF-specific text formatting artifacts.
func parsePDF(path string) (string, error) {
	// Open the PDF file
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Extract the plain text from the reader
	var buf bytes.Buffer
	b, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	buf.ReadFrom(b)

	rawPDFText := buf.String()

	// 1. Resolve hyphenated line breaks ("pipe-\nline" -> "pipeline")
	rawPDFText = pdfHyphenRegex.ReplaceAllString(rawPDFText, "$1$2")

	// 2. Resolve coordinate-torn words ("k\naise" -> "kaise")
	rawPDFText = pdfBrokenWordRegex.ReplaceAllString(rawPDFText, "$1$2")

	// 3. Flatten remaining newlines into standard spaces
	rawPDFText = strings.ReplaceAll(rawPDFText, "\n", " ")

	return rawPDFText, nil
}

// parseDocx treats the .docx file as a ZIP archive, locates the document.xml,
// and extracts all text nodes.
func parseDocx(path string) (string, error) {
	// Open the docx as a zip file
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()

	// Search for the main document XML file inside the zip
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

// parsePptx treats the .pptx file as a ZIP archive, iterates through all
// slide XML files, and extracts their text nodes sequentially into a flat string.
func parsePptx(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()

	var textBuilder strings.Builder

	// Iterate through all files in the zip to find the slides
	for _, f := range r.File {
		// Slides are stored as ppt/slides/slide1.xml, ppt/slides/slide2.xml, etc.
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			rc, err := f.Open()
			if err != nil {
				continue // Skip broken slides rather than failing the whole document
			}

			slideText, _ := extractXMLText(rc)
			slideText = strings.TrimSpace(slideText)

			// Only append if the slide actually contained text (ignores blank slides)
			if len(slideText) > 0 {
				// Flatten any internal newlines within the slide's text boxes
				slideText = strings.ReplaceAll(slideText, "\n", " ")

				// Add a single space to separate this slide's text from the next
				textBuilder.WriteString(slideText + " ")
			}

			rc.Close()
		}
	}

	if textBuilder.Len() == 0 {
		return "", errors.New("no readable slides found in pptx")
	}

	return strings.TrimSpace(textBuilder.String()), nil
}

// -----------------------------------------------------------------------
// HELPER FUNCTIONS
// -----------------------------------------------------------------------

// extractXMLText is a high-performance, low-memory stream reader.
// Instead of loading a massive XML file into memory, it streams the file token
// by token, pulling out only the raw text strings hidden inside specific tags.
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
			// Both Word and PPTX use 't' for text nodes (<w:t> or <a:t>)
			if element.Name.Local == "t" {
				inTextTag = true
			}
		case xml.EndElement:
			if element.Name.Local == "t" {
				inTextTag = false
			} else if element.Name.Local == "p" {
				// Both Word and PPTX use 'p' for paragraphs (<w:p> or <a:p>)
				// Injecting a space here prevents paragraphs from mashing together,
				// while ensuring heavily formatted single words stay intact.
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
