package main

import (
	"archive/zip"  // Used to peek inside zip files safely
	"fmt"          // Used for formatting error messages
	"net/http"     // Contains the magic MIME-type detector
	"os"           // Used for interacting with the operating system (opening files)
	"unicode/utf8" // Used to check if unknown bytes are readable text
)

// ExtractText takes a file path, verifies its true internal format securely, 
// and routes it to the correct parser. 
// It returns the extracted text as a string, or an error if something goes wrong.
func ExtractText(filePath string) (string, error) {
	
	// -------------------------------------------------------------------
	// STEP 1: OPEN THE FILE
	// -------------------------------------------------------------------
	// We use os.Open to get a "handle" on the file so we can read its contents.
	file, err := os.Open(filePath)
	if err != nil {
		// %w is a special Go verb that "wraps" the original error, preserving its context
		return "", fmt.Errorf("extractor failed to open file: %w", err)
	}
	
	// 'defer' is a powerful Go keyword. It schedules the file.Close() command 
	// to run the absolute moment this function finishes, no matter what happens 
	// (even if it crashes or returns early). This prevents memory leaks.
	defer file.Close()

	// -------------------------------------------------------------------
	// STEP 2: READ THE "MAGIC NUMBERS" (FILE SIGNATURE)
	// -------------------------------------------------------------------
	// We create an empty byte slice (like an array) that holds exactly 512 bytes.
	// 512 bytes is the standard amount needed to reliably detect a file's true type.
	buffer := make([]byte, 512)
	
	// file.Read attempts to fill our buffer. 
	// 'n' tells us exactly how many bytes it actually managed to read.
	n, err := file.Read(buffer)
	
	// It's possible the file is smaller than 512 bytes (e.g., a tiny text file).
	// If so, Go throws an "EOF" (End of File) error. We want to ignore EOF errors
	// because reading a tiny file is perfectly fine, but we catch all other errors.
	if err != nil && err.Error() != "EOF" {
		return "", fmt.Errorf("extractor failed to read file signature: %w", err)
	}

	// -------------------------------------------------------------------
	// STEP 3: DETECT THE TRUE FILE TYPE
	// -------------------------------------------------------------------
	// If a file is only 10 bytes long, 'n' will be 10. 
	// The syntax 'buffer[:n]' slices the buffer, ignoring the 502 empty bytes at the end.
	// This prevents the detector from getting confused by empty space.
	validBuffer := buffer[:n]

	// This function looks at the raw bytes and returns the standard MIME type 
	// (e.g., "application/pdf" or "image/png"). It completely ignores the file's name.
	mimeType := http.DetectContentType(validBuffer)

	// -------------------------------------------------------------------
	// STEP 4: ROUTE THE FILE TO THE CORRECT PARSER
	// -------------------------------------------------------------------
	// A switch statement lets us check the mimeType against several possibilities.
	switch mimeType {
		
	case "application/pdf":
		// If it's truly a PDF, send it to the PDF parser.
		return parsePDF(filePath)
	
	case "application/zip":
		// FIX 1: The "Fake ZIP" Loophole. 
		// Modern Office files (.docx, .pptx) are actually zipped folders containing XML.
		// However, a hacker could rename a normal, malicious ZIP to "document.docx".
		// Instead of trusting the name, we pass it to a special function that peeks inside.
		return routeZipBasedDocs(filePath)

	case "text/plain; charset=utf-8":
		// This safely covers standard plain text formats like .txt, .md, and .csv.
		return parsePlainText(filePath)

	case "application/octet-stream":
		// FIX 3: The Markdown/CSV Quirk.
		// "octet-stream" is Go's way of saying "I have no idea what this binary data is."
		// Sometimes, perfectly valid Markdown or CSV files get labeled as this if they 
		// lack certain standard markers. 
		
		// Fallback check: Let's see if the unknown data is actually valid, readable text.
		if utf8.Valid(validBuffer) {
			return parsePlainText(filePath)
		}
		
		// 'fallthrough' is a Go keyword. If the bytes are NOT valid text, 
		// it forces the program to drop down into the 'default' case below and reject it.
		fallthrough 

	default:
		// If the file is a disguised executable (.exe), an image, or anything else 
		// we don't explicitly support, it ends up here and is safely rejected.
		return "", fmt.Errorf("extractor rejected file: unsupported or disguised MIME type detected (%s)", mimeType)
	}
}

// -----------------------------------------------------------------------
// HELPER FUNCTION: DEEP ZIP INSPECTION
// -----------------------------------------------------------------------
func routeZipBasedDocs(filePath string) (string, error) {
	// We use Go's built-in zip package to open the file as an archive.
	r, err := zip.OpenReader(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open zip archive: %w", err)
	}
	// Don't forget to close the zip reader when we're done!
	defer r.Close()

	// r.File is a list of all the files contained INSIDE the zip archive.
	// We loop through them one by one.
	for _, f := range r.File {
		// A genuine Microsoft Word file will ALWAYS have this specific file inside it.
		if f.Name == "word/document.xml" {
			return parseDocx(filePath)
			
		// A genuine PowerPoint file will ALWAYS have this specific file inside it.
		} else if f.Name == "ppt/presentation.xml" {
			return parsePptx(filePath)
		}
	}

	// If we loop through the whole zip and don't find those specific XML files,
	// it's an imposter (just a regular zip file renamed to .docx or .pptx).
	return "", fmt.Errorf("file is a zip archive, but lacks DOCX or PPTX internal structures")
}

// -----------------------------------------------------------------------
// DUMMY PARSERS (To be replaced with actual logic later)
// -----------------------------------------------------------------------

func parsePDF(path string) (string, error) {
	return "Extracted PDF content from " + path, nil
}

func parseDocx(path string) (string, error) {
	return "Extracted DOCX content from " + path, nil
}

func parsePptx(path string) (string, error) {
	return "Extracted PPTX content from " + path, nil
}

func parsePlainText(path string) (string, error) {
	// os.ReadFile is a quick way to read an entire file into memory at once.
	// We only do this here because we trust the upstream watcher checked the file size!
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read plain text file: %w", err)
	}
	
	// os.ReadFile returns a slice of bytes, so we convert it to a string before returning.
	return string(content), nil
}