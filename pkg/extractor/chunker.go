package extractor

// Chunk represents a single slice of text.
// This struct perfectly matches the 'chunks' table schema provided by your project head
// (document_hash, chunk_index, content) so the database orchestrator can insert it directly.
type Chunk struct {
	DocumentHash string
	Index        int
	Content      string
}

// ChunkText takes a continuous string of sanitized text and slices it into
// overlapping, semantically safe blocks.
// - chunkSize: the max characters per chunk (e.g., 1000).
// - chunkOverlap: how many characters should overlap with the previous chunk (e.g., 200).
func ChunkText(text string, chunkSize int, chunkOverlap int, documentHash string) []Chunk {
	var chunks []Chunk

	// =========================================================
	// STEP 1: GUARDRAILS & PARAMETER SAFETY
	// =========================================================

	// Prevent infinite loops or panics from zero/negative chunk sizes
	if chunkSize <= 0 {
		chunkSize = 1000 // Fallback to a safe default
	}
	// Overlap must always be strictly less than the chunk size
	if chunkOverlap >= chunkSize {
		chunkOverlap = chunkSize / 2
	}

	// =========================================================
	// STEP 2: RUNE CONVERSION (THE UTF-8 SHIELD)
	// =========================================================

	// CRITICAL: Convert the string to a slice of 'rune' instead of 'byte'.
	// Slicing a raw string in Go slices by bytes. If you slice exactly halfway
	// through a 4-byte character (like an emoji 🚀 or an accented letter é),
	// it creates a fatal invalid UTF-8 sequence. A 'rune' represents one complete
	// character, ensuring we only slice cleanly between valid characters.
	runes := []rune(text)
	totalLength := len(runes)

	// Edge case: If the document had no text (e.g., image-only PDF), return the empty array.
	if totalLength == 0 {
		return chunks
	}

	// =========================================================
	// STEP 3: THE DYNAMIC SLIDING WINDOW
	// =========================================================

	i := 0          // The starting index of the current chunk
	chunkIndex := 0 // The sequential ID for the database (0, 1, 2...)

	for i < totalLength {

		// Calculate the theoretical hard limit for this chunk
		end := i + chunkSize

		if end >= totalLength {
			// If our window overshoots the text, snap it to the absolute end
			end = totalLength
		} else {
			// --- WORD BOUNDARY SNAPPING (SEMANTIC SAFETY) ---
			// We don't want to cut the word "exaggeration" in half.
			// So, we start at our hard limit and walk backwards looking for a space.

			// We limit the backward search to 50 characters. If we encounter a
			// massive 100-character block of gibberish with no spaces, we don't
			// want to accidentally delete it all.
			searchLimit := end - 50
			if searchLimit < i {
				searchLimit = i // Never search backwards past the start of the chunk
			}

			// Walk backwards from 'end'
			for j := end; j > searchLimit; j-- {
				// If we find a space or a newline, snap the boundary exactly to it!
				if runes[j] == ' ' || runes[j] == '\n' {
					end = j
					break
				}
			}
		}

		// Package the chunk data and append it to our array
		chunks = append(chunks, Chunk{
			DocumentHash: documentHash,
			Index:        chunkIndex,
			Content:      string(runes[i:end]), // Convert runes back to a standard string
		})

		chunkIndex++

		// If our window's end has reached the end of the text, we are done
		if end == totalLength {
			break
		}

		// --- SLIDE THE WINDOW FORWARD ---
		// Calculate the next starting position. We subtract the desired overlap
		// from the *actual* snapped end of the previous chunk.
		nextStart := end - chunkOverlap

		// Safety net: If the overlap was massive and the word snapping pushed our
		// end backwards, 'nextStart' might accidentally slide us backwards, causing
		// an infinite loop. This ensures we ALWAYS make forward progress.
		if nextStart <= i {
			i = i + 1
		} else {
			i = nextStart
		}
	}

	return chunks
}
