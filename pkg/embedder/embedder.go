package embedder

import (
	"drag/pkg/extractor"
	"fmt"
	"os"
)

type Embedder interface {
	EmbedText(text string) ([]float32, error)
	Embed(texts []extractor.Chunk) ([][]float32, error)
	Destroy()
}

func NewEmbedder() (Embedder, error) {
	mode := os.Getenv("EMBEDDER_MODE")
	if mode == "" {
		mode = "ollama" // default to ollama in dev
	}

	switch mode {
		case "ollama":
			return NewOllamaEmbedder(), nil
		case "onnx":
			return NewONNXEmbedder()
		default:
			return nil, fmt.Errorf("unknown embedder mode: %s", mode)
	}
}