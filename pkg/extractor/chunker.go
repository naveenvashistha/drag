package extractor

// -----------------------------------------------------------------------
// DATA STRUCTURES
// -----------------------------------------------------------------------

// Chunk represents a single text segment structured for vector embedding.
// The fields align with standard database schemas for downstream ingestion.
type Chunk struct {
	DocumentHash string
	Index        int
	Content      string
}

// -----------------------------------------------------------------------
// MAIN CHUNKER
// -----------------------------------------------------------------------

// ChunkText partitions a continuous string into overlapping, semantically intact blocks.
// It implements a sliding window algorithm with word-boundary snapping to ensure
// contextual continuity and prevent word truncation across chunks.
func ChunkText(text string, chunkSize int, chunkOverlap int, documentHash string) []Chunk {
	var chunks []Chunk

	// 1. Parameter Initialization and Validation
	// Default to 1500 characters if an invalid chunk size is provided.
	// This size is optimized for standard token limits in modern embedding models.
	if chunkSize <= 0 {
		chunkSize = 1500
	}

	// Ensure chunk overlap is strictly less than the chunk size to maintain forward progression.
	// Defaults to 200 characters (approximately 13% of the default chunk size).
	if chunkOverlap <= 0 || chunkOverlap >= chunkSize {
		chunkOverlap = 200
	}

	// 2. UTF-8 Encoding Handling
	// Slicing native Go strings operates on bytes, which can sever multi-byte characters
	// resulting in invalid UTF-8 sequences. Converting the text to a slice of runes
	// ensures safe slicing at precise character boundaries.
	runes := []rune(text)
	totalLength := len(runes)

	if totalLength == 0 {
		return chunks
	}

	// 3. Sliding Window Execution
	i := 0          // Tracks the starting index of the current chunk window
	chunkIndex := 0 // Tracks the sequential order of the chunks

	for i < totalLength {
		// Determine the theoretical maximum end index for the current chunk.
		end := i + chunkSize

		if end >= totalLength {
			// Snap to the document's end if the window exceeds the remaining text length.
			end = totalLength
		} else {
			// End-Boundary Semantic Snapping
			// Adjusts the end index backward to the nearest whitespace to prevent word truncation.

			// Limit the backward search to 150 characters. This prevents excessive truncation
			// when parsing continuous strings lacking whitespace, such as long URLs or encoded data.
			searchLimit := end - 150
			if searchLimit < i {
				searchLimit = i
			}

			for j := end; j > searchLimit; j-- {
				if runes[j] == ' ' || runes[j] == '\n' {
					end = j
					break
				}
			}
		}

		// Instantiate the chunk and append it to the collection.
		chunks = append(chunks, Chunk{
			DocumentHash: documentHash,
			Index:        chunkIndex,
			Content:      string(runes[i:end]), // Convert the rune slice back to a standard string
		})

		chunkIndex++

		// Terminate the loop if the end of the document has been reached.
		if end == totalLength {
			break
		}

		// Start-Boundary Overlap Calculation
		// Determine the starting index for the subsequent chunk by applying the overlap offset.
		nextStart := end - chunkOverlap

		// Start-Boundary Semantic Snapping
		foundSpace := false
		for searchIdx := nextStart; searchIdx > i; searchIdx-- {
			if runes[searchIdx-1] == ' ' || runes[searchIdx-1] == '\n' {
				nextStart = searchIdx
				foundSpace = true
				break
			}
		}

		// Forward Progression Guarantee
		// If no space was found, we MUST force the window forward to prevent infinite loops.
		if !foundSpace {
			nextStart = end - chunkOverlap
		}

		// Absolute fallback to guarantee the loop advances by at least 1 character
		if nextStart <= i {
			nextStart = i + 1
		}

		// Advance the window index for the next iteration.
		i = nextStart
	}

	return chunks
}