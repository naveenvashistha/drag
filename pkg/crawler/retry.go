package crawler

import (
	"database/sql"
	"log"
	"time"
)

type RetryMachine struct {
	DB           *sql.DB
	ProcessQueue chan<- string
}

// RetryMachine coordinates a background recovery loop for files that were left
// in a pending state after a failed vectorization attempt.
//
// DB provides access to the file tracking tables, and ProcessQueue is the
// channel used to hand file paths back to the worker pipeline for another try.
// The channel is send-only here on purpose: this component only enqueues work
// and never consumes it.

// StartRetrySweeper is the long-running retry scheduler.
// It performs one immediate retry pass when the service starts, then repeats the
// same scan once every 24 hours so stale pending files are not forgotten.
func (rm *RetryMachine) StartRetrySweeper() {

	rm.runRetry()

	ticker := time.NewTicker(24 * time.Hour)

	for range ticker.C {
		rm.runRetry()
	}
}

func (rm *RetryMachine) runRetry() {
	// Query the file table for work that is still eligible for retry.
	// A file is considered retryable here only when it is still marked pending
	// and has a retry count between 1 and 2, which means the system has already
	// tried it before but has not yet exhausted the maximum retry budget.
	log.Println("Retry Sweeper: Scanning for pending files eligible for retry...")
	rows, err := rm.DB.Query(`
		SELECT path FROM files 
		WHERE status = 'pending' AND retry_count > 0 AND retry_count < 3
	`)
	if err != nil {
		log.Println("Retry Sweeper: Error querying pending files.")
		return
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err == nil {
			// Send the file path back to the worker queue so the normal processing
			// pipeline can attempt extraction, embedding, and persistence again.
			// The non-blocking select prevents this retry loop from stalling if the
			// queue is already full.
			select {
			case rm.ProcessQueue <- path:
				// The file was successfully re-queued for another pass.
			default:
				// If the queue has no free capacity, skip this file for now and log
				// the condition so the retry system remains responsive.
				log.Printf("Retry Sweeper: Worker queue is full, skipping retry for %s\n", path)
			}
			count++
		}
	}

	if count > 0 {
		// Emit a summary when at least one file was discovered and re-queued.
		log.Printf("Retry Sweeper: Queued %d pending files for another attempt.\n", count)
	}
	log.Println("Retry Sweeper: Completed one pass through pending files.")
}
