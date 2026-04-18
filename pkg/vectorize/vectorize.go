package vectorize

import (
	"database/sql"
	"drag/pkg/extractor"
	"drag/pkg/embedder"
	"log"
	"os"
	"path/filepath"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

type Vectorizer struct {
	DB *sql.DB
	Embedder *embedder.ONNXEmbedder
}

// Vectorize processes one file end-to-end: it extracts readable text, breaks that
// text into overlapping chunks, generates an embedding for each chunk, and then
// stores everything inside one database transaction so the file is either fully
// indexed or not indexed at all.
func (v *Vectorizer) Vectorize(folderPath string, filePath string, fileHash string, info os.FileInfo) error {
	log.Printf("Starting vector pipeline for: %s\n", filePath)

	// Step 1: extract the file contents and split them into search-friendly pieces.
	// The extractor is responsible for turning the file into plain text, and the
	// chunker breaks that text into smaller windows so embeddings stay within the
	// model's practical input limits while still preserving nearby context.
	textContent, err := extractor.ExtractText(filePath)
	log.Printf("length of text content of file %s: %d\n", filepath.Base(filePath), len(textContent))
	if err != nil {
		return err
	}
	if len(textContent) == 0 {
		log.Printf("File %s has no readable content\n", filepath.Base(filePath))
		return nil
	}
	// The chunk size controls how much text is placed into each embedding request.
	// The overlap keeps a small amount of repeated context between neighboring
	// chunks so important sentences that cross a boundary are still discoverable.
	chunks := extractor.ChunkText(textContent, 800, 100, fileHash)
	log.Printf("Generated %d chunks for file %s\n", len(chunks), filepath.Base(filePath))

	// Step 2: open a transaction so every database write for this file happens as
	// one atomic unit. If anything fails after this point, the deferred rollback
	// removes all partially written rows and keeps the database consistent.
	tx, err := v.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Record the document hash first. This acts as the stable identity for the
	// source file, and INSERT OR IGNORE avoids duplicate rows when the same file is
	// encountered again.
	_, err = tx.Exec(`INSERT OR IGNORE INTO documents (hash) VALUES (?)`, fileHash)
	if err != nil {
		log.Println("Failed to insert document hash:", err)
		return err
	}

	// Prepare the chunk and vector insert statements once so the database can reuse
	// the parsed SQL for every row. This is much faster than rebuilding the query
	// on each loop iteration, especially for documents with many chunks.
	// The chunks table returns the generated chunk id because the vector table stores
	// embeddings against that id rather than duplicating the text again.
	chunkStmt, err := tx.Prepare(`
		INSERT INTO chunks (document_hash, chunk_index, content) 
		VALUES (?, ?, ?) RETURNING id`)
	if err != nil {
		return err
	}
	defer chunkStmt.Close()

	vecStmt, err := tx.Prepare(`
		INSERT INTO vec_chunks (chunk_id, embedding) 
		VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer vecStmt.Close()

	// Process embeddings and inserts in batches to prevent memory exhaustion
	batchSize := 1000
	for i := 0; i < len(chunks); i += batchSize {
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}

		batchChunks := chunks[i:end]

		// Embed only the current batch
		batchEmbeddings, err := v.Embedder.Embed(batchChunks)
		if err != nil {
			log.Println("Failed to embed batch:", err)
			return err
		}

		// Insert only the current batch
		for j, chunk := range batchChunks {
			var chunkID int
			
			// i+j gives the correct overall chunk_index for the document
			err = chunkStmt.QueryRow(fileHash, i+j, chunk.Content).Scan(&chunkID)
			if err != nil {
				log.Printf("Failed to insert chunk text %d: %v\n", i+j, err)
				return err
			}

			vectorBytes, err := sqlite_vec.SerializeFloat32(batchEmbeddings[j])
			if err != nil {
				log.Printf("Failed to serialize vector %d: %v\n", i+j, err)
				return err
			}

			_, err = vecStmt.Exec(chunkID, vectorBytes)
			if err != nil {
				log.Printf("Failed to insert vector %d: %v\n", i+j, err)
				return err
			}
		}
	}

	// Upsert the file record itself so the crawler can track where the file lives,
	// how large it is, when it was last modified, and whether it is currently active.
	// The conflict rule lets an existing pending row be refreshed, while preventing
	// accidental overwrites when another part of the system has already marked it
	// as handled or missing.
	result, dbErr := tx.Exec(`
		INSERT INTO files (folder_path, path, file_name, file_hash, size, last_modified, status) 
		VALUES (?, ?, ?, ?, ?, ?, 'active')
		ON CONFLICT(path) DO UPDATE SET 
			file_hash = excluded.file_hash, size = excluded.size,
			last_modified = excluded.last_modified, status = 'active',
			updated_at = cast(strftime('%s', 'now') as int)
			WHERE files.status = 'pending'`,
		folderPath, filePath, filepath.Base(filePath), fileHash, info.Size(), info.ModTime().Unix())

	if dbErr != nil {
		// The deferred rollback will undo every insert in this transaction, so a
		// single failure does not leave behind orphan chunks or vector rows.
		return dbErr
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		// A zero-row update means the file record no longer matched the expected
		// pending state, usually because another watcher changed the status while
		// this file was being processed. In that case we abort and let rollback
		// remove the chunk and vector rows we inserted moments earlier.
		log.Printf("Vectorization aborted: %s was altered by user during processing.", filepath.Base(filePath))
		return nil
	}

	// Once every insert succeeds, commit the transaction so the database writes
	// become visible together. Until this point, nothing is permanent.
	if err := tx.Commit(); err != nil {
		return err
	}

	log.Printf("Successfully vectorized %d chunks for file %s: %s\n", len(chunks), filepath.Base(filePath), fileHash)
	return nil
}
