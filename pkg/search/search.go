package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"fmt"
	"drag/pkg/embedder"
)

// SearchResult represents a single matched chunk from the database
type SearchResult struct {
	ChunkID       int
	FileName      string
	FilePath      string
	Content       string
	CombinedScore float64
}

type Searcher struct {
	DB  *sql.DB
	Emb embedder.Embedder
}

func NewSearcher(db *sql.DB, emb embedder.Embedder) *Searcher {
	return &Searcher{
		DB:  db,
		Emb: emb,
	}
}

// HybridSearch performs both FTS5 keyword search and vec0 semantic search.
func (s *Searcher) HybridSearch(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	// generate the Vector Embedding for the Query
	queryVector, err := s.Emb.EmbedText(query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	// Convert the []float32 vector to a JSON string array.
	// sqlite-vec natively understands JSON arrays as vector inputs, 
	// which safely bypasses complex Go-to-C blob conversions.
	vecJSON, err := json.Marshal(queryVector)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal vector: %w", err)
	}

	// Sanitize the FTS query (prevent SQL syntax errors from weird characters)
	ftsQuery := sanitizeFTSQuery(query)

	// the Hybrid Search SQL Query (Reciprocal Rank Fusion)
	sqlQuery := `
	WITH 
	-- CTE 1: Semantic Search (Vibes) using vec0
	semantic_search AS (
		SELECT 
			chunk_id, 
			distance,
			row_number() OVER (ORDER BY distance ASC) as rank_vec
		FROM vec_chunks
		WHERE embedding MATCH ? AND k = 20
	),
	
	-- CTE 2: Keyword Search (Facts) using FTS5
	keyword_search AS (
		SELECT 
			chunk_id, 
			rank as fts_score,
			row_number() OVER (ORDER BY rank ASC) as rank_fts -- FTS5 rank is ascending (lower is better)
		FROM fts_chunks
		WHERE content MATCH ?
		LIMIT 20
	)
	
	-- CTE 3: The Fusion
	SELECT 
		c.id,
		f.file_name,
		f.path,
		c.content,
		-- RRF Formula: 1 / (60 + rank)
		(COALESCE(1.0 / (60 + s.rank_vec), 0.0) + 
		 COALESCE(1.0 / (60 + k.rank_fts), 0.0)) as combined_score
	FROM chunks c
	JOIN files f ON c.file_id = f.id
	LEFT JOIN semantic_search s ON c.id = s.chunk_id
	LEFT JOIN keyword_search k ON c.id = k.chunk_id
	WHERE s.chunk_id IS NOT NULL OR k.chunk_id IS NOT NULL
	ORDER BY combined_score DESC
	LIMIT ?;
	`

	// execute the Query
	rows, err := s.DB.QueryContext(ctx, sqlQuery, string(vecJSON), ftsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("hybrid search query failed: %w", err)
	}
	defer rows.Close()

	// parse the Results
	var results []SearchResult
	for rows.Next() {
		var res SearchResult
		if err := rows.Scan(&res.ChunkID, &res.FileName, &res.FilePath, &res.Content, &res.CombinedScore); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		results = append(results, res)
	}

	return results, nil
}

// sanitizeFTSQuery ensures the user's input doesn't break SQLite's FTS5 parser
// by stripping quotes and forcing an OR/AND approach if necessary.
func sanitizeFTSQuery(query string) string {
	// A simple approach: remove double quotes and format as an implicit phrase search
	clean := strings.ReplaceAll(query, "\"", "")
	// Wrapping in quotes forces FTS to treat it as a phrase, avoiding syntax errors
	return fmt.Sprintf("\"%s\"", clean)
}