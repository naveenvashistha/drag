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
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, err
	}

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
	-- Force SQLite to respect FOREIGN KEY constraints
	PRAGMA foreign_keys = ON;

	CREATE TABLE IF NOT EXISTS folders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT UNIQUE NOT NULL
	);

	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		folder_id INTEGER NOT NULL,
		path TEXT UNIQUE NOT NULL,      
		file_name TEXT NOT NULL,        
		file_hash TEXT UNIQUE,          
		last_modified INTEGER NOT NULL, 
		status TEXT DEFAULT 'pending',  
		retry_count INTEGER DEFAULT 0,
		FOREIGN KEY (folder_id) REFERENCES folders(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS chunks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_id INTEGER NOT NULL,
		chunk_index INTEGER NOT NULL,
		content TEXT NOT NULL,                
		FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE
	);

	-- 1. FTS5 Index (Keyword Search)
	CREATE VIRTUAL TABLE IF NOT EXISTS fts_chunks USING fts5(
		content,             
		chunk_id UNINDEXED   
	);

	-- 2. vec0 Index (Semantic Search)
	CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
		chunk_id INTEGER PRIMARY KEY,   
		embedding float[384]            
	);

	-- 3. Triggers to keep FTS and Vectors synced with chunks
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
	`

	_, err := db.Exec(schema)
	return err
}