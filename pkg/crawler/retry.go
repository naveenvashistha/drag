package crawler

import (
	"database/sql"
	"time"
	"log"
)

type RetryMachine struct {
	DB *sql.DB
	ProcessQueue chan<- string
}

// StartRetrySweeper wakes up periodically to retry failed vectorizations.
func (rm *RetryMachine) StartRetrySweeper() {

	rm.runRetry()

	ticker := time.NewTicker(24 * time.Hour)

	for range ticker.C {
		rm.runRetry()
	}
}

func (rm *RetryMachine) runRetry(){
	// Find files that failed, but still have attempts left
	rows, err := rm.DB.Query(`
		SELECT path FROM files 
		WHERE status = 'pending' AND retry_count > 0 AND retry_count < 3
	`)
	if err == nil {
		return
	}

	var count int
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err == nil {
			// Drop it back into the worker queue for another attempt!
			rm.ProcessQueue <- path
			count++
		}
	}
	rows.Close()

	if count > 0 {
		log.Printf("Retry Sweeper: Queued %d pending files for another attempt.\n", count)
	}
}