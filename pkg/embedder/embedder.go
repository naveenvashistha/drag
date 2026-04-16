package embedder

import (
	"drag/pkg/extractor"
)

type Embedder interface {
	EmbedText(text string) ([]float32, error)
	Embed(texts []extractor.Chunk) ([][]float32, error)
	Destroy()
}

func NewEmbedder() (Embedder, error) {
	return NewONNXEmbedder()
}