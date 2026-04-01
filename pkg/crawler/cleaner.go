package crawler

import (
	"database/sql"
	"log"
	"time"
)

type GarbageCollector struct {
	DB *sql.DB
}

// Start runs the cleanup loop in the background
func (gc *GarbageCollector) StartGarbageCollection() {
	log.Println("Garbage Collector activated.")

	// 1. Run immediately on app startup
	gc.runCleanup()

	// 2. Set a ticker to run once every 24 hours while the app stays open
	ticker := time.NewTicker(24 * time.Hour)
	for range ticker.C {
		gc.runCleanup()
	}
}

// runCleanup executes the SQLite deletion
func (gc *GarbageCollector) runCleanup() {
	// SQLite's datetime() function handles the 30-day math automatically
	tx, err := gc.DB.Begin()
	if err != nil {
		log.Println("Garbage Collector transaction error:", err)
		return
	}
	defer tx.Rollback() // Ensure we rollback if anything goes wrong
	query1 := `
		DELETE FROM files 
		WHERE status = 'missing' 
		AND updated_at <= cast(strftime('%s', 'now', '-7 days') as int)
	`
	query2 := `
		DELETE FROM folders
		WHERE status = 'missing'
		AND updated_at <= cast(strftime('%s', 'now', '-7 days') as int)
	`
	result, err := tx.Exec(query1)
	if err != nil {
		log.Println("Garbage Collector error:", err)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		log.Printf("Garbage Collector permanently deleted %d old missing files.\n", rowsAffected)
	}

	result2, err := tx.Exec(query2)
	if err != nil {
		log.Println("Garbage Collector error:", err)
		return
	}

	rowsAffected2, _ := result2.RowsAffected()
	if rowsAffected2 > 0 {
		log.Printf("Garbage Collector permanently deleted %d old missing folders.\n", rowsAffected2)
	}

	if err := tx.Commit(); err != nil {
		log.Println("Garbage Collector commit error:", err)
	}
	log.Println("Garbage Collector cycle completed.")
}