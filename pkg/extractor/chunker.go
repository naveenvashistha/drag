package extractor

// -----------------------------------------------------------------------
// DATA STRUCTURES
// -----------------------------------------------------------------------

// Chunk represents a single text segment ready for vector embedding.
// This struct aligns with the database schema to allow seamless insertion
// by the downstream orchestrator.
type Chunk struct {
	DocumentHash string
	Index        int
	Content      string
}

// -----------------------------------------------------------------------
// MAIN CHUNKER
// -----------------------------------------------------------------------

// ChunkText slices a continuous string into overlapping, semantically safe blocks.
// It uses a sliding window approach with word-boundary snapping to ensure
// context is preserved and words are not severed across chunks.
func ChunkText(text string, chunkSize int, chunkOverlap int, documentHash string) []Chunk {
	var chunks []Chunk

	// 1. Parameter Safety Guardrails
	// Prevent infinite loops or panics caused by invalid external configurations.
	if chunkSize <= 0 {
		chunkSize = 1000 // Fallback to a safe default
	}
	// Overlap must always be strictly less than the chunk size to ensure forward movement.
	if chunkOverlap >= chunkSize {
		chunkOverlap = chunkSize / 2
	}

	// 2. UTF-8 Protection (Rune Conversion)
	// Slicing a raw string in Go slices by bytes, which can fatally sever multi-byte
	// characters (like emojis or accented letters) resulting in invalid UTF-8.
	// Converting to a slice of 'rune' ensures we only slice cleanly between whole characters.
	runes := []rune(text)
	totalLength := len(runes)

	// Edge case: If the document contains no text, return the empty array immediately.
	if totalLength == 0 {
		return chunks
	}

	// 3. The Sliding Window Algorithm
	i := 0          // The starting index of the current chunk
	chunkIndex := 0 // The sequential ID for database ordering

	for i < totalLength {
		// Calculate the theoretical hard limit for the end of this chunk
		end := i + chunkSize

		// Snap to the absolute end if our window overshoots the remaining text
		if end >= totalLength {
			end = totalLength
		} else {
			// --- END-OF-CHUNK SEMANTIC SNAPPING ---
			// To prevent severing words (e.g., cutting "database" into "data" and "base"),
			// we walk backward from the hard limit to find the nearest space or newline.

			// Limit the backward search to 50 characters to prevent accidentally
			// deleting massive blocks of text if the document lacks spaces.
			searchLimit := end - 50
			if searchLimit < i {
				searchLimit = i // Never search backward past the start of the current chunk
			}

			for j := end; j > searchLimit; j-- {
				if runes[j] == ' ' || runes[j] == '\n' {
					end = j
					break
				}
			}
		}

		// Package the finalized chunk data
		chunks = append(chunks, Chunk{
			DocumentHash: documentHash,
			Index:        chunkIndex,
			Content:      string(runes[i:end]), // Convert safely back to a string
		})

		chunkIndex++

		// If the window has reached the end of the document, chunking is complete
		if end == totalLength {
			break
		}

		// --- START-OF-CHUNK SEMANTIC SNAPPING (OVERLAP) ---
		// Calculate the starting position for the next chunk by subtracting the overlap
		// from the snapped end of the current chunk.
		nextStart := end - chunkOverlap

		// Safety net: Ensure the window always makes forward progress.
		if nextStart <= i {
			nextStart = i + 1
		}

		// To prevent the next chunk from starting in the middle of a word,
		// we walk backward from nextStart until we find the space immediately preceding a word.
		for nextStart > i {
			if runes[nextStart-1] == ' ' || runes[nextStart-1] == '\n' {
				break
			}
			nextStart--
		}

		// Slide the window forward
		i = nextStart
	}

	return chunks
}
