/**
	Command to test: go test -v ./pkg/embedder

	Before testing change the type of the parameter in the onnx_embedder.go function
	from 
		func (o *ONNXEmbedder) Embed(texts []extractor.Chunk)
	to
		func (o *ONNXEmbedder) Embed(texts []string)
*/

package embedder

import (
	"fmt"
	"testing"
)

func TestNewONNXEmbedder(t *testing.T) {
	e, err := NewONNXEmbedder()
	if err != nil {
		t.Fatalf("failed to create embedder: %v", err)
	}
	defer e.Destroy()

	t.Log("embedder created successfully")
}

func TestEmbed(t *testing.T) {
	e, err := NewONNXEmbedder()
	if err != nil {
		t.Fatalf("failed to create embedder: %v", err)
	}
	defer e.Destroy()

	embedding, err := e.Embed("hello world")
	if err != nil {
		t.Fatalf("failed to embed text: %v", err)
	}

	// Check dimensions
	if len(embedding) != 384 {
		t.Errorf("expected 384 dimensions, got %d", len(embedding))
	}

	// Check normalization — magnitude should be ~1.0
	var magnitude float32
	for _, v := range embedding {
		magnitude += v * v
	}
	if magnitude < 0.99 || magnitude > 1.01 {
		t.Errorf("embedding not normalized, magnitude^2 = %f", magnitude)
	}

	fmt.Printf("first 5 values: %v\n", embedding[:5])
}

func TestEmbedEmpty(t *testing.T) {
	e, err := NewONNXEmbedder()
	if err != nil {
		t.Fatalf("failed to create embedder: %v", err)
	}
	defer e.Destroy()

	_, err = e.Embed("")
	if err == nil {
		t.Error("expected error for empty text, got nil")
	}
}

func TestEmbedDeterministic(t *testing.T) {
	e, err := NewONNXEmbedder()
	if err != nil {
		t.Fatalf("failed to create embedder: %v", err)
	}
	defer e.Destroy()

	text := "the quick brown fox"
	a, err := e.Embed(text)
	if err != nil {
		t.Fatalf("first embed failed: %v", err)
	}
	b, err := e.Embed(text)
	if err != nil {
		t.Fatalf("second embed failed: %v", err)
	}

	// Same input must always produce same output
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("embeddings differ at index %d: %f vs %f", i, a[i], b[i])
		}
	}
}

func TestEmbedSemanticSimilarity(t *testing.T) {
	e, err := NewONNXEmbedder()
	if err != nil {
		t.Fatalf("failed to create embedder: %v", err)
	}
	defer e.Destroy()

	query, err := e.Embed("how do I reset my password")
	if err != nil {
		t.Fatalf("failed to embed query: %v", err)
	}

	similar, err := e.Embed("I forgot my password, how can I recover it")
	if err != nil {
		t.Fatalf("failed to embed similar: %v", err)
	}

	unrelated, err := e.Embed("the weather is nice today")
	if err != nil {
		t.Fatalf("failed to embed unrelated: %v", err)
	}

	similarScore := cosineSimilarity(query, similar)
	unrelatedScore := cosineSimilarity(query, unrelated)

	fmt.Printf("similar score:   %.4f\n", similarScore)
	fmt.Printf("unrelated score: %.4f\n", unrelatedScore)

	if similarScore <= unrelatedScore {
		t.Errorf("expected similar > unrelated, got %.4f <= %.4f", similarScore, unrelatedScore)
	}
}

func TestEmbedBatch(t *testing.T) {
	e, err := NewONNXEmbedder()
	if err != nil {
		t.Fatalf("failed to create embedder: %v", err)
	}
	defer e.Destroy()

	texts := []string{
		"first document",
		"second document",
		"third document",
	}

	embeddings, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("batch embed failed: %v", err)
	}

	if len(embeddings) != len(texts) {
		t.Errorf("expected %d embeddings, got %d", len(texts), len(embeddings))
	}

	for i, emb := range embeddings {
		if len(emb) != 384 {
			t.Errorf("embedding %d: expected 384 dims, got %d", i, len(emb))
		}
	}
}

// cosineSimilarity computes dot product of two normalized vectors
// Since embeddings are already normalized, dot product == cosine similarity
func cosineSimilarity(a, b []float32) float32 {
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}