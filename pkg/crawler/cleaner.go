package crawler

import (
	"database/sql"
	"log"
	"time"
)

type GarbageCollector struct {
	DB *sql.DB
}

// GarbageCollector is responsible for removing stale records that should no
// longer remain in the database. It works as a long-running maintenance task
// that periodically scans for rows marked missing and deletes them once they
// have been absent long enough to be considered permanently gone.

// StartGarbageCollection begins the background cleanup cycle.
// It performs one immediate pass so the application does not wait a full day
// before reclaiming old rows, then repeats the same maintenance work every
// 24 hours while the process remains alive.
func (gc *GarbageCollector) StartGarbageCollection() {
	log.Println("Garbage Collector activated.")

	// Run once right away so old records are handled as soon as the application
	// starts instead of waiting for the next scheduled cleanup window.
	gc.runCleanup()

	// Create a daily ticker so the cleanup job repeats automatically without any
	// manual intervention from the rest of the application.
	ticker := time.NewTicker(24 * time.Hour)
	for range ticker.C {
		gc.runCleanup()
	}
}

// runCleanup performs one transactional cleanup pass against SQLite.
// The function deletes old missing files and folders in a single database
// transaction so that the maintenance operation can be committed or rolled back
// as one consistent unit.
func (gc *GarbageCollector) runCleanup() {
	// Start a transaction before deleting anything so both table cleanups are
	// coordinated and do not leave the database half-updated if an error occurs.
	tx, err := gc.DB.Begin()
	if err != nil {
		log.Println("Garbage Collector transaction error:", err)
		return
	}
	// Always roll back on exit unless commit succeeds first; this is a safety net
	// that clears any partial changes if a later statement fails.
	defer tx.Rollback()

	// Delete file rows that have been marked missing for at least seven days.
	// The timestamp comparison uses SQLite's current time and stored unix epoch
	// values so the cleanup happens relative to the present moment.
	query1 := `
		DELETE FROM files 
		WHERE status = 'missing' 
		AND updated_at <= cast(strftime('%s', 'now', '-7 days') as int)
	`

	// Delete folder rows under the same retention rule so orphaned folder records
	// are cleaned up with the same timing policy as file records.
	query2 := `
		DELETE FROM folders
		WHERE status = 'missing'
		AND updated_at <= cast(strftime('%s', 'now', '-7 days') as int)
	`

	// Execute the file cleanup first and inspect the number of rows removed so we
	// can log useful maintenance information for operators.
	result, err := tx.Exec(query1)
	if err != nil {
		log.Println("Garbage Collector error:", err)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		log.Printf("Garbage Collector permanently deleted %d old missing files.\n", rowsAffected)
	}

	// Run the folder cleanup next using the same transaction so both categories of
	// stale data are handled together.
	result2, err := tx.Exec(query2)
	if err != nil {
		log.Println("Garbage Collector error:", err)
		return
	}

	rowsAffected2, _ := result2.RowsAffected()
	if rowsAffected2 > 0 {
		log.Printf("Garbage Collector permanently deleted %d old missing folders.\n", rowsAffected2)
	}

	// Commit only after both delete statements succeed. This makes the cleanup
	// durable and ensures the database reflects the completed maintenance pass.
	if err := tx.Commit(); err != nil {
		log.Println("Garbage Collector commit error:", err)
	}

	// Emit a completion message so the logs clearly show when a full cleanup cycle
	// has finished, even if no rows were removed during this pass.
	log.Println("Garbage Collector cycle completed.")
}
