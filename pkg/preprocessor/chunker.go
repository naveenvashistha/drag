package preprocessor

import (
	"fmt"
)

// Chunker handles splitting data into chunks
type Chunker struct {
	ChunkSize int
}

// NewChunker creates a new Chunker instance
func NewChunker(chunkSize int) *Chunker {
	return &Chunker{
		ChunkSize: chunkSize,
	}
}

// Chunk splits input data into chunks of specified size
func (c *Chunker) Chunk(data []byte) ([][]byte, error) {
	if c.ChunkSize <= 0 {
		return nil, fmt.Errorf("chunk size must be positive")
	}

	var chunks [][]byte
	for i := 0; i < len(data); i += c.ChunkSize {
		end := i + c.ChunkSize
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, data[i:end])
	}

	return chunks, nil
}
