package embedder

import (
	
)

type Embedder struct {
	model string
}

func NewEmbedder(model string) *Embedder {
	return &Embedder{model: model}
}

func (e *Embedder) Embed(text string) ([]float32, error) {
	// TODO: Implement embedding logic
	return nil, nil
}