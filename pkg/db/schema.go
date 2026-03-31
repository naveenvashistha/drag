package db

import (
	// Standard library packages for database/sql, file path manipulation, and logging
	"database/sql"
	"path/filepath"
	"log"
	// The database/sql driver wrapper for SQLite, which in turn uses the ncruces driver under the hood. This is necessary to register the "sqlite3" driver name with database/sql.
	_ "github.com/ncruces/go-sqlite3/driver"
	// This package contains wasm bindings for sqlite-vec extension, which provides the vec0 virtual table for vector search. By importing it with a blank identifier
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
)

// InitDB creates or connects to the local SQLite file and runs the schema
// function takes the data directory path as an argument, which is where the SQLite database file will be stored. and outputs pointer to the sql.DB connection pool and any error encountered during the process.
func InitDB(dataDir string) (*sql.DB, error) {
	dbPath := filepath.Join(dataDir, "drag.db")
	dsn := "file:" + dbPath + "?_journal_mode=WAL&_busy_timeout=5000&_fk=ON"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, err
	}

	// 3. THE GO QUEUE MANAGER
	// This forces all 4 of your background workers to share exactly 1 connection.
	// When Worker A is writing, Workers B, C, and D are safely paused by Go 
	// until the connection is free. No errors, no skipped files.
	db.SetMaxOpenConns(1)
	
	// (Optional but good practice) Keep the connection alive
	db.SetMaxIdleConns(1)

	// Execute our schema
	if err := createTables(db); err != nil {
		return nil, err
	}

	log.Println("Database initialized successfully at:", dbPath)
	return db, nil
}

// createTables initializes the database schema, including tables for folders, files, chunks, and the necessary FTS5 and vec0 virtual tables for search functionality.
// It also sets up triggers to keep the FTS and vector tables in sync with the main chunks table.
// function takes a pointer to the sql.DB connection pool and returns any error encountered during the execution of the schema creation.
func createTables(db *sql.DB) error {
	schema := `
		-- 1. FOLDERS: The Watch Roots
	CREATE TABLE IF NOT EXISTS folders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT UNIQUE NOT NULL
		status TEXT DEFAULT 'active',
    	updated_at INTEGER DEFAULT (cast(strftime('%s', 'now') as int))
		is_public INTEGER DEFAULT 0, -- 0 for private folders, 1 for public folders
	);

	-- 2. DOCUMENTS: "The Brain" (Stores unique content states)
	-- We use the xxHash as the Primary Key here.
	CREATE TABLE IF NOT EXISTS documents (
		hash TEXT PRIMARY KEY,
		created_at INTEGER DEFAULT (cast(strftime('%s', 'now') as int))
	);

	-- 3. FILES: "The Map" (Stores physical file locations)
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		folder_path TEXT NOT NULL,
		path TEXT UNIQUE NOT NULL,      
		file_name TEXT NOT NULL,        
		
		-- AI LINK: Removed UNIQUE. Multiple files can now point to the same hash.
		file_hash TEXT,          
		
		-- HEURISTIC DATA: Added size for instant rename/move detection
		size INTEGER NOT NULL,
		last_modified INTEGER NOT NULL, 
		
		status TEXT DEFAULT 'pending',  
		retry_count INTEGER DEFAULT 0,
		updated_at INTEGER DEFAULT (cast(strftime('%s', 'now') as int)),
		
		FOREIGN KEY (folder_path) REFERENCES folders(path) ON DELETE CASCADE,
		FOREIGN KEY (file_hash) REFERENCES documents(hash)
	);

	-- 4. CHUNKS: Now linked to the Document Hash, not the File ID
	CREATE TABLE IF NOT EXISTS chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		document_hash TEXT NOT NULL, 
		chunk_index INTEGER NOT NULL,
		content TEXT NOT NULL,                
		FOREIGN KEY (document_hash) REFERENCES documents(hash) ON DELETE CASCADE
	);

	-- 5. FTS5 Index (Keyword Search) - Unchanged
	CREATE VIRTUAL TABLE IF NOT EXISTS fts_chunks USING fts5(
		content,             
		chunk_id UNINDEXED   
	);

	-- 6. vec0 Index (Semantic Search) - Unchanged
	CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
		chunk_id INTEGER PRIMARY KEY,   
		embedding float[384]            
	);

	-- 7. Triggers - Unchanged
	CREATE TRIGGER IF NOT EXISTS sync_fts_insert 
	AFTER INSERT ON chunks 
	BEGIN
		INSERT INTO fts_chunks(chunk_id, content) VALUES (new.id, new.content);
	END;

	CREATE TRIGGER IF NOT EXISTS sync_fts_delete 
	AFTER DELETE ON chunks 
	BEGIN
		DELETE FROM fts_chunks WHERE chunk_id = old.id;
	END;

	CREATE TRIGGER IF NOT EXISTS cleanup_vec_chunks
	AFTER DELETE ON chunks
	BEGIN
		DELETE FROM vec_chunks WHERE chunk_id = old.id;
	END;

	CREATE TRIGGER IF NOT EXISTS cleanup_orphan_documents
	AFTER DELETE ON files
	BEGIN
		-- This deletes the document ONLY IF no other files are using this hash
		DELETE FROM documents 
		WHERE hash = OLD.file_hash 
		AND NOT EXISTS (
			SELECT 1 FROM files WHERE file_hash = OLD.file_hash
		);
	END;

	CREATE TRIGGER IF NOT EXISTS cleanup_orphan_documents_update
    AFTER UPDATE ON files
    BEGIN
        -- Deletes the old document ONLY IF no other files are still using it
        DELETE FROM documents 
        WHERE hash = OLD.file_hash 
        AND OLD.file_hash IS NOT NULL
        AND NOT EXISTS (
            SELECT 1 FROM files WHERE file_hash = OLD.file_hash
        );
    END;
	`

	_, err := db.Exec(schema)
	return err
}