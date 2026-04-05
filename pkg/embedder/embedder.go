package embedder

import (
	"drag/pkg/extractor"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

var ErrEmptyText = errors.New("Cannot embed empty text")

/**
	Note: 
	For local Ollama, simple sequential calls are fine for testing.
	In production it is better to use batch processing

*/

// type of the embedder instance
type Embedder struct {
	URL   string
	Model string
}

// function creates an instance of embeder
func NewEmbedder() *Embedder {
	return &Embedder{
		URL:   "http://localhost:11434/api/embeddings",
		Model: "all-minilm", 
	}
}

/**
	description: function to convert a given `text` to vector embedding

	params:
		text: string representing the text to be embedded

	return:
		embedding: list of 384 length, on successful embedding
		error: if there were any errors caused
*/
func (o *Embedder) Embed(text extractor.Chunk) ([]float32, error) {
	if text.Content == "" {
		return nil, ErrEmptyText
	}

	reqBody, _ := json.Marshal(map[string]string{
		"model":  o.Model,
		"prompt": text.Content,
	})

	resp, err := http.Post(o.URL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if len(result.Embedding) != 384 {
		return nil, fmt.Errorf("expected 384 dimensions, got %d", len(result.Embedding))
	}

	return result.Embedding, nil
}
	
/**
	description: function to embed a given vector of strings

	params:
		text[]: a list of strings representing the text to be embedded

	return:
		embedding[]: a list of vectors each of 384 length, on successful embedding
		error: if there were any errors caused
*/
func (o *Embedder) EmbedBatch(texts []extractor.Chunk) ([][]float32, error) {
	var batch [][]float32

	for _, t := range texts {
		vec, err := o.Embed(t)
		if err != nil {
			return nil, err
		}
		batch = append(batch, vec)
	}
	return batch, nil
}