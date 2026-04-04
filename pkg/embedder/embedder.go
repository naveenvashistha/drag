package embedder

import (
	"drag/pkg/extractor"
)

type Embedder struct {
	model string
}

func NewEmbedder(model string) *Embedder {
	return &Embedder{model: model}
}

func Embed(chunks []extractor.Chunk) ([]float32, error) {
	// TODO: Implement embedding logic
	return nil, nil
}