package preprocessor

import (
	"strings"
)

// ExtractText extracts and cleans text from input
func ExtractText(input string) string {
	// Remove extra whitespace
	text := strings.TrimSpace(input)

	// Replace multiple spaces with single space
	text = strings.Join(strings.Fields(text), " ")

	return text
}

// ExtractLines splits text into individual lines
func ExtractLines(input string) []string {
	return strings.Split(input, "\n")
}

// ExtractWords extracts all words from text
func ExtractWords(input string) []string {
	return strings.Fields(input)
}
