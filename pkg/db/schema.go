package db

import (
	"database/sql"
	"log"
	"path/filepath"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

// InitDB opens (or creates) the local SQLite database, validates connectivity,
// configures connection-pool behavior, and applies the schema.
//
// The database file is stored in dataDir as drag.db. On success, the function
// returns the shared sql.DB handle used by the rest of the application.
func InitDB(dataDir string) (*sql.DB, error) {
	sqlite_vec.Auto()
	dbPath := filepath.Join(dataDir, "drag.db")
	// The DSN includes pragmas to set WAL mode (which lets you read concurrently alongside writing) for better concurrency, a busy timeout to reduce lock contention, and foreign key enforcement for data integrity.
	// The sqlite3 driver parses these pragmas from the DSN and applies them when opening the connection.
	dsn := "file:" + dbPath + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}

	// Verify the database is reachable before continuing startup.
	if err := db.Ping(); err != nil {
		return nil, err
	}

	// Constrain writes to a single open connection to reduce lock contention and
	// golang put the processes in sleep which are trying to access the locked database
	db.SetMaxOpenConns(1)

	// Keep one idle connection ready for reuse.
	db.SetMaxIdleConns(1)

	// Create/upgrade schema objects required by crawler and search pipelines.
	if err := createTables(db); err != nil {
		return nil, err
	}

	log.Println("Database initialized successfully at:", dbPath)
	return db, nil
}

// createTables defines the persistent schema for folder/file tracking, content
// chunk storage, full-text search, vector search, and cleanup/indexing triggers.
// It is safe to call repeatedly because all objects use IF NOT EXISTS guards.
func createTables(db *sql.DB) error {
	schema := `
		-- Stores tracked folder paths and visibility/status metadata.
	CREATE TABLE IF NOT EXISTS folders (
		id INTEGER PRIMARY KEY,
		path TEXT UNIQUE NOT NULL,
		status TEXT DEFAULT 'active',
    	updated_at INTEGER DEFAULT (cast(strftime('%s', 'now') as int)),
		is_public INTEGER DEFAULT 0 -- 0 = private, 1 = public
	);

	-- Stores deduplicated content identities keyed by hash.
	CREATE TABLE IF NOT EXISTS documents (
		hash TEXT PRIMARY KEY,
		created_at INTEGER DEFAULT (cast(strftime('%s', 'now') as int))
	);

	-- Maps physical files to metadata and optional content hash ownership.
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY,
		folder_path TEXT NOT NULL,
		path TEXT UNIQUE NOT NULL,      
		file_name TEXT NOT NULL,        
		
		-- Not unique: multiple files can reference the same document hash.
		file_hash TEXT,          
		
		-- Size + modified time support change/ghost detection heuristics.
		size INTEGER NOT NULL,
		last_modified INTEGER NOT NULL, 
		
		status TEXT DEFAULT 'pending',  
		retry_count INTEGER DEFAULT 0,
		updated_at INTEGER DEFAULT (cast(strftime('%s', 'now') as int)),
		
		FOREIGN KEY (folder_path) REFERENCES folders(path) ON DELETE CASCADE,
		FOREIGN KEY (file_hash) REFERENCES documents(hash)
	);

	-- Stores extracted text chunks linked to a deduplicated document hash.
	CREATE TABLE IF NOT EXISTS chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		document_hash TEXT NOT NULL, 
		chunk_index INTEGER NOT NULL,
		content TEXT NOT NULL,                
		FOREIGN KEY (document_hash) REFERENCES documents(hash) ON DELETE CASCADE
	);

	-- Full-text keyword search index for chunk content.
	CREATE VIRTUAL TABLE IF NOT EXISTS fts_chunks USING fts5(
		content,             
		chunk_id UNINDEXED   
	);

	-- Vector index used for semantic similarity search.
	CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
		chunk_id INTEGER PRIMARY KEY,   
		embedding float[384]            
	);

	-- Keep FTS rows synchronized with chunk inserts.
	CREATE TRIGGER IF NOT EXISTS sync_fts_insert 
	AFTER INSERT ON chunks
	BEGIN
		INSERT INTO fts_chunks(content, chunk_id) VALUES (new.content, new.id);
	END;

	-- Remove FTS rows when chunk rows are deleted.
	CREATE TRIGGER IF NOT EXISTS sync_fts_delete 
	AFTER DELETE ON chunks 
	BEGIN
		DELETE FROM fts_chunks WHERE chunk_id = old.id;
	END;

	-- Remove vector rows when chunk rows are deleted.
	CREATE TRIGGER IF NOT EXISTS cleanup_vec_chunks
	AFTER DELETE ON chunks
	BEGIN
		DELETE FROM vec_chunks WHERE chunk_id = old.id;
	END;

	-- Delete orphaned documents after file-row deletion when no remaining file
	-- references the same hash.
	CREATE TRIGGER IF NOT EXISTS cleanup_orphan_documents
	AFTER DELETE ON files
	BEGIN
		DELETE FROM documents 
		WHERE hash = OLD.file_hash 
		AND NOT EXISTS (
			SELECT 1 FROM files WHERE file_hash = OLD.file_hash
		);
	END;

	-- Delete old hash ownership on file-hash updates when it becomes orphaned.
	CREATE TRIGGER IF NOT EXISTS cleanup_orphan_documents_update
    AFTER UPDATE ON files
    BEGIN
        DELETE FROM documents 
        WHERE hash = OLD.file_hash 
        AND OLD.file_hash IS NOT NULL
        AND NOT EXISTS (
            SELECT 1 FROM files WHERE file_hash = OLD.file_hash
        );
    END;

	-- Supports retry and garbage-collection lookups by status/retry_count.
	CREATE INDEX IF NOT EXISTS idx_files_status_retry 
		ON files(status, retry_count);

	-- Supports stale-row cleanup queries by status and updated_at.
	CREATE INDEX IF NOT EXISTS idx_files_status_updated 
		ON files(status, updated_at);

	-- Supports ghost matching lookups by status + file metadata.
	CREATE INDEX IF NOT EXISTS idx_files_ghost_lookup 
		ON files(status, size, last_modified);

	-- Supports orphan-document checks keyed by file_hash.
	CREATE INDEX IF NOT EXISTS idx_files_hash 
		ON files(file_hash);

	-- Supports boot-time recovery scans by status + path + size + modified time.
	CREATE INDEX IF NOT EXISTS idx_files_boot_sync
    	ON files(status, path, size, last_modified);

	-- Supports folder visibility queries and cascading status updates.
	CREATE INDEX IF NOT EXISTS idx_folders_status
   		ON folders(status, path);
	`

	_, err := db.Exec(schema)
	return err
}
