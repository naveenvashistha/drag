package crawler

import (
	"database/sql"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

type FileWalker struct {
	DB    *sql.DB
	Watch *FileWatcher
}

// FileWalker performs startup reconciliation between on-disk state and the
// persisted crawler state in SQLite.
//
// DB is the source of truth for previously indexed files/folders, and Watch
// provides the runtime watcher/queue hooks used to re-enter files into the
// normal processing pipeline.

// RunBootSync executes one full startup synchronization pass.
// It compares database records against the current filesystem, marks records
// missing when files disappeared while the app was offline, discovers files and
// folders that were added or changed during downtime, and repopulates watcher
// coverage so live monitoring resumes with a consistent baseline.
func (w *FileWalker) RunBootSync() {
	log.Println("Starting Boot Synchronization...")

	// Phase 1: verify currently active DB file rows against disk presence and mark
	// rows missing when files no longer exist on the filesystem.
	w.sweepMissingFiles()

	fileDBState := make(map[string]struct {
		Size    int64
		ModTime int64
	})

	// Load file paths, sizes, and modification times for all non-missing records into fileDBState so the walker can compare on-disk state against previously recorded metadata. This
	// allows the system to detect changes that happened while it was offline and
	// reprocess files that were modified or replaced since the last crawl.
	rows, err := w.DB.Query(`SELECT path, size, last_modified FROM files WHERE status != 'missing'`)
	if err == nil {
		for rows.Next() {
			var p string
			var s, m int64
			if err := rows.Scan(&p, &s, &m); err == nil {
				fileDBState[p] = struct {
					Size    int64
					ModTime int64
				}{s, m}
			}
		}
		rows.Close()
	}

	folderDBState := make(map[string]struct {
		status string
		state  bool
	})

	// Load folder paths and statuses for all non-missing records into folderDBState so the walker can track which folders are present in db either in active or ignored state.
	// we mark status of active folders as false initially and update it to true when we encounter the folder during walk.
	// we mark status of ignored folders as ignored because we want to adhere to user wishes to not crawl that folder, there is no record of anything related to that folder in the db except in folders table.
	folders, ferr := w.DB.Query(`SELECT path, status FROM folders WHERE status != 'missing'`)
	if ferr == nil {
		for folders.Next() {
			var path string
			var status string
			folders.Scan(&path, &status)
			if status == "ignored" {
				folderDBState[path] = struct {
					status string
					state  bool
				}{status, true}
			} else {
				folderDBState[path] = struct {
					status string
					state  bool
				}{status, false}
			}
		}
		folders.Close()
	}

	// Phase 2: walk selected roots on disk to discover additions/changes that the
	// live watcher could not observe while the application was not running.
	standardRoots := w.getStandardRoots()
	for _, root := range standardRoots {
		if _, err := os.Stat(root); err == nil {
			log.Printf("Boot Sync: Scanning standard drive %s\n", root)
			w.scanPhysicalDrive(root, fileDBState, folderDBState)
		}
	}

	for path := range folderDBState {
		// Re-check each folder saved in folderDBState with the disk and reconcile folder state:
		// mark missing when removed, delete ignored entries that no longer exist,
		// and recursively scan active folders whose status still remain false.
		if _, err := os.Stat(path); err != nil {
			if !folderDBState[path].state {
				w.Watch.handleDelete(path, true)
			} else if folderDBState[path].status == "ignored" {
				_, err := w.DB.Exec(`DELETE FROM folders WHERE path = ?`, path)
				if err != nil {
					log.Printf("Error deleting folder from DB: %v", err)
				} else {
					log.Printf("Deleted folder from DB: %s", path)
				}
			}
		} else if folderDBState[path].status == "active" && !folderDBState[path].state {
			w.scanPhysicalDrive(path, fileDBState, folderDBState)
		}
	}

	runtime.EventsEmit(w.Watch.ctx, "bootSyncComplete", nil)
	log.Println("Boot Synchronization complete. System is perfectly reconciled.")
}

func (w *FileWalker) getStandardRoots() []string {
	var roots []string

	// Always include the current user's home directory so common personal data
	// locations are scanned even if no extra drives are present.
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
	}

	// Query mounted partitions and limit results to real, user-relevant volumes.
	// The boolean flag excludes pseudo/system partitions that are not useful for
	// content indexing.
	partitions, err := disk.Partitions(false)
	if err == nil {
		for _, p := range partitions {

			// Skip primary root mount points to avoid scanning extremely large and
			// sensitive OS directories that are outside the intended crawl scope.
			if p.Mountpoint == "C:\\" || p.Mountpoint == "C:" || p.Mountpoint == "/" {
				continue
			}

			// Normalize mount formatting so drive paths consistently end with a
			// trailing backslash on Windows.
			mount := p.Mountpoint
			if !strings.HasSuffix(mount, "\\") {
				mount += "\\"
			}

			rootPtr, _ := windows.UTF16PtrFromString(mount)
			driveType := windows.GetDriveType(rootPtr)
			// Keep only fixed local drives so network/removable media does not create
			// unstable crawl roots.
			if driveType == windows.DRIVE_FIXED {
				// Add accepted mount points as candidate crawl roots.
				roots = append(roots, mount)
			}
		}
	}

	return roots
}

// sweepMissingFiles checks active file records and flags rows as missing when
// their paths no longer exist on disk.
// This protects the index from serving stale records for files deleted while
// the application was offline.

func (w *FileWalker) sweepMissingFiles() {
	// Load file paths that are currently considered present so only active records
	// participate in the existence check.
	rows, err := w.DB.Query(`SELECT path FROM files WHERE status != 'missing'`)
	if err != nil {
		return
	}
	defer rows.Close()

	var missingCount int
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			continue
		}

		// If the file is absent from disk, propagate the deletion path through the
		// watcher delete handler so related state is updated consistently.
		if _, err := os.Stat(path); os.IsNotExist(err) {
			w.Watch.handleDelete(path, false)
			missingCount++
		}
	}

	if missingCount > 0 {
		log.Printf("Boot Sync: Marked %d offline-deleted files as 'missing'.\n", missingCount)
	}
}

func (w *FileWalker) scanPhysicalDrive(root string, fileDBState map[string]struct {
	Size    int64
	ModTime int64
}, folderDBState map[string]struct {
	status string
	state  bool
}) {

	// Walk every entry under the provided root and reconcile each directory/file
	// against existing DB state.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission-denied and similar traversal errors are ignored so one
			// inaccessible subtree does not abort the full root scan.
			return nil
		}

		if folderData, exists := folderDBState[path]; exists {
			if folderData.status == "ignored" {
				// Entire ignored directories are skipped recursively to avoid indexing
				// their contents during startup reconciliation.
				return filepath.SkipDir
			} else {
				// Mark encountered tracked folders as present during this scan pass.
				folderDBState[path] = struct {
					status string
					state  bool
				}{folderData.status, true}
			}
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Apply the same allow/deny rules used by the live watcher so startup scan
		// and runtime monitoring index the same target set.
		if !w.Watch.isValidTarget(path, info.Size(), d.IsDir()) {
			if d.IsDir() {
				// Skip entire blocked directories to avoid descending into children.
				return filepath.SkipDir
			}
			return nil
		}

		// Directory reconciliation: ensure active watch registration and active
		// folder state in the database.
		if d.IsDir() {
			// Register this directory with fsnotify so future file system events are
			// captured after boot sync completes.
			_ = w.Watch.Watcher.Add(path)

			// Upsert folder metadata so newly discovered directories appear as active
			// in the folder table.
			if _, ok := folderDBState[path]; !ok {
				w.DB.Exec(`
					INSERT INTO folders(path, status) VALUES (?, 'active')
					ON CONFLICT(path) DO UPDATE SET status = 'active', updated_at = cast(strftime('%s', 'now') as int)`,
					path)
			}
			return nil
		}

		// File reconciliation: compare on-disk file stats with previously recorded
		// DB stats to determine whether processing is required.
		fileData, exists := fileDBState[path]

		if !exists || fileData.Size != info.Size() || fileData.ModTime != info.ModTime().Unix() {
			// New or changed files are queued for standard processing. If the queue
			// is currently full, persist a pending DB record so retry logic can pick
			// the file up later without losing the update signal.
			select {
			case w.Watch.ProcessQueue <- path:
				// Successfully queued for immediate processing.
			default:
				info, err := os.Stat(path)
				if err != nil {
					return nil
				}
				// Queue backpressure fallback: write pending row with reset hash/retry
				// metadata so asynchronous retry mechanisms can process it later.
				_, err = w.DB.Exec(`
					INSERT INTO files (folder_path, path, file_name, size, last_modified, status, retry_count) 
					VALUES (?, ?, ?, ?, ?, 'pending', 0)
					ON CONFLICT(path) DO UPDATE SET 
						size = excluded.size,
						last_modified = excluded.last_modified,
						file_hash = NULL, 
						status = 'pending',
						retry_count = 0,
						updated_at = cast(strftime('%s', 'now') as int)`,
					filepath.Dir(path), path, filepath.Base(path), info.Size(), info.ModTime().Unix())
				if err != nil {
					log.Printf("Failed to persist pending file: %s\n", filepath.Base(path))
					return nil
				}
			}
		}

		// Exact matches require no action; the existing indexed state already reflects
		// the current on-disk content.
		return nil
	})

	if err != nil {
		log.Printf("Error scanning physical drive at %s: %v\n", root, err)
	}
}
