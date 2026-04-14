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

type LocalSearchResult struct {
    FilePath      string  `json:"filePath"`
    FileName      string  `json:"fileName"`
}

func NewSearcher(db *sql.DB, emb embedder.Embedder) *Searcher {
	return &Searcher{
		DB:  db,
		Emb: emb,
	}
}
// core semantic search — returns full results with content
func (s *Searcher) SearchSemantic(ctx context.Context, query string) ([]SearchResult, error) {
	queryVector, err := s.Emb.EmbedText(query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	vecJSON, err := json.Marshal(queryVector)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal vector: %w", err)
	}

	sqlQuery := `
	SELECT 
		c.id,
		f.file_name,
		f.path,
		c.content,
		v.distance
	FROM vec_chunks v
	JOIN chunks c ON v.chunk_id = c.id
	JOIN files f ON c.document_hash = f.file_hash
	WHERE v.embedding MATCH ? AND k = 50
	ORDER BY v.distance ASC
	`

	rows, err := s.DB.QueryContext(ctx, sqlQuery, string(vecJSON))
	if err != nil {
		return nil, fmt.Errorf("semantic search query failed: %w", err)
	}
	defer rows.Close()

	const distanceThreshold = 0.8

	var results []SearchResult
	for rows.Next() {
		var res SearchResult
		var distance float64
		if err := rows.Scan(&res.ChunkID, &res.FileName, &res.FilePath, &res.Content, &distance); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		if distance > distanceThreshold {
			break
		}
		res.CombinedScore = 1.0 - distance
		results = append(results, res)
	}
	return results, nil
}

// deduplication wrapper — unique files only, best chunk score per file
func (s *Searcher) SearchLocal(results []SearchResult) []LocalSearchResult {
	seen := make(map[string]bool)
	var local []LocalSearchResult

	for _, r := range results {
		if seen[r.FilePath] {
			continue
		}
		seen[r.FilePath] = true
		local = append(local, LocalSearchResult{
			FilePath:      r.FilePath,
			FileName:      r.FileName,
		})
	}
	return local
}


// HybridSearch performs both FTS5 keyword search and vec0 semantic search.
func (s *Searcher) HybridSearch(ctx context.Context, query string) ([]SearchResult, error) {
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
		WHERE embedding MATCH ? AND k = 50
	),
	
	-- CTE 2: Keyword Search (Facts) using FTS5
	keyword_search AS (
		SELECT 
			chunk_id, 
			rank as fts_score,
			row_number() OVER (ORDER BY rank ASC) as rank_fts -- FTS5 rank is ascending (lower is better)
		FROM fts_chunks
		WHERE content MATCH ?
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
	JOIN files f ON c.document_hash = f.file_hash
	LEFT JOIN semantic_search s ON c.id = s.chunk_id
	LEFT JOIN keyword_search k ON c.id = k.chunk_id
	WHERE s.chunk_id IS NOT NULL OR k.chunk_id IS NOT NULL
	ORDER BY combined_score DESC
	`

	// execute the Query
	rows, err := s.DB.QueryContext(ctx, sqlQuery, string(vecJSON), ftsQuery)
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