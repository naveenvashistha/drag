package vectorize

import (
	"database/sql"
	"os"
	"path/filepath"
	"drag/pkg/extractor"
	"log"
)

type Vectorizer struct {
	DB *sql.DB
}

// Vectorize runs a file through the pipeline and saves it atomically using the Hash PK.
func (v *Vectorizer) Vectorize(folderPath string, filePath string, fileHash string, info os.FileInfo) error {
	log.Printf("Starting vector pipeline for: %s\n", filePath)

	// 1. EXTRACTION & CHUNKING
	textContent, err := extractor.ExtractText(filePath)
	if err != nil{
		return err
	}
	if len(textContent) == 0 {
		return nil
	}
	chunks := extractor.ChunkText(textContent, 1500, 200, fileHash) // These parameters can be tweaked for different chunk sizes and overlaps

	// 2. EMBEDDING
	embeddings, err := EmbedChunks(chunks)
	if err != nil {
		return err
	}

	// ==========================================
	// 3. ATOMIC DATABASE TRANSACTION
	// ==========================================
	tx, err := v.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// A. Insert the master document hash (IGNORE if it already exists)
	_, err = tx.Exec(`INSERT OR IGNORE INTO documents (hash) VALUES (?)`, fileHash)
	if err != nil {
		log.Println("Failed to insert document hash:", err)
		return err
	}

	// B. Prepare Statements for massive speed boost
	// We need RETURNING id on chunks to feed the sqlite-vec table
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

	// C. Insert every chunk and its vector
	for i, chunk := range chunks {
		var chunkID int
		
		// Insert Text Chunk
		err = chunkStmt.QueryRow(fileHash, i, chunk).Scan(&chunkID)
		if err != nil {
			log.Printf("Failed to insert chunk text %d: %v\n", i, err)
			return err 
		}

		// Insert Vector Embedding (Make sure embeddings[i] is formatted for sqlite-vec)
		_, err = vecStmt.Exec(chunkID, embeddings[i])
		if err != nil {
			log.Printf("Failed to insert vector %d: %v\n", i, err)
			return err
		}
	}

	// C. UPSERT The File Map (Now 100% Atomic with the Vectors)
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
		return dbErr // defer tx.Rollback() handles the cleanup
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		// If 0 rows were affected, it means the 'WHERE files.status = pending' lock failed.
		// The Watcher must have changed the status to 'missing' while we were running the AI model!
		// We return an error. The 'defer tx.Rollback()' at the top will automatically fire,
		// deleting the orphan chunks and vectors we just inserted, keeping the DB perfectly clean.
		log.Printf("Vectorization aborted: %s was altered by user during processing.", filepath.Base(filePath))
		return nil
	}

	// D. Commit to the hard drive
	if err := tx.Commit(); err != nil {
		return err
	}

	log.Printf("Successfully vectorized %d chunks for hash: %s\n", len(chunks), fileHash)
	return nil
}