package crawler

import (
	"context"
	"database/sql"
	"drag/pkg/hasher"
	"drag/pkg/vectorize"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// FileWatcher coordinates real-time filesystem monitoring, event routing, and
// asynchronous file processing.
//
// DB stores file/folder metadata and indexing state, Watcher delivers low-level
// fsnotify events, ProcessQueue buffers file paths for worker processing, and
// timers provides per-path debounce control so bursty write events do not cause
// duplicate vectorization work.
type FileWatcher struct {
	DB           *sql.DB
	Watcher      *fsnotify.Watcher
	ProcessQueue chan string
	ctx          context.Context
	timers       map[string]*time.Timer
	timersLock   sync.Mutex
}

func (w *FileWatcher) SetContext(ctx context.Context) {
	w.ctx = ctx
}

// NewFileWatcher creates the fsnotify instance and allocates an in-memory queue
// large enough to absorb high event bursts without blocking the producer side.
func NewFileWatcher(db *sql.DB) (*FileWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &FileWatcher{
		DB:           db,
		Watcher:      watcher,
		ProcessQueue: make(chan string, 10000),
		timers:       make(map[string]*time.Timer),
	}, nil
}

// StartWatching launches worker goroutines and enters the main fsnotify loop.
// Workers consume queued file paths concurrently, while this loop continuously
// routes filesystem events and publishes critical watcher failures to the UI.
func (w *FileWatcher) StartWatching() {
	log.Println("Starting Watcher and Worker Pool...")

	// Start multiple workers so hashing/vectorization can progress in parallel.
	for i := 0; i < 4; i++ {
		go w.workerLoop()
	}

	// Consume fsnotify event and error channels for the lifetime of the watcher.
	for {
		select {
		case event, ok := <-w.Watcher.Events:
			if !ok {
				return
			}
			w.routeEvent(event)

		case err, ok := <-w.Watcher.Errors:
			if !ok {
				return
			}
			log.Println("fsnotify error:", err)
			runtime.EventsEmit(w.ctx, "system-alert", map[string]string{
				"title":   "File System Error",
				"message": err.Error(),
				"level":   "critical",
			})
		}
	}
}

// routeEvent normalizes raw fsnotify events into higher-level actions:
// debounce candidate file writes, process deletions, and handle rename updates.

func (w *FileWatcher) routeEvent(event fsnotify.Event) {

	// Stat the path to determine whether this event currently refers to a directory.
	info, err := os.Stat(event.Name)
	isDir := (err == nil && info.IsDir())

	// Directory create events trigger a targeted recursive scan so files that were
	// created before watch registration are still discovered. Directory write
	// metadata updates are ignored to avoid unnecessary work.
	if isDir && w.isValidTarget(event.Name, info.Size(), true) {
		if event.Has(fsnotify.Create) {
			log.Println("New folder detected, watching:", event.Name)
			go w.RunWalker(event.Name)
			return
		}
		if event.Has(fsnotify.Write) {
			return
		}
	}

	// On platforms where fsnotify provides the old rename path on RENAME event, prefer exact rename
	// handling to preserve file/folder identity without remove + create heuristics. if oldname not provided, route to delete handler and let worker logic attempt to recover with a ghost claim if it reappears as CREATE event later.
	field := reflect.ValueOf(event).FieldByName("renamedFrom")
	oldName := ""
	if field.IsValid() {
		oldName = field.String()
	}
	if event.Has(fsnotify.Create) && oldName != "" {
		w.handleExactRename(oldName, event.Name, isDir)
		return
	}

	// Route remaining events to debounce or delete handlers.
	switch {
	case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
		w.debounce(event.Name)

	case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
		w.handleDelete(event.Name, isDir)
	}
}

// debounce coalesces rapid updates for the same file into a single queued task.
// This avoids repeated processing when editors write files in multiple bursts.

func (w *FileWatcher) debounce(filePath string) {
	w.timersLock.Lock()
	defer w.timersLock.Unlock()

	if timer, exists := w.timers[filePath]; exists {
		timer.Stop()
	}

	// Set a new timer for this path that triggers after a short delay. If another event
	// arrives for the same path before the timer fires, the previous timer is
	// stopped and replaced, effectively resetting the debounce window with each new event.
	w.timers[filePath] = time.AfterFunc(2*time.Second, func() {
		// lock the timers map again to safely remove the timer for this path now that the
		// debounce window has passed and the file is being processed.
		w.timersLock.Lock()
		delete(w.timers, filePath)
		w.timersLock.Unlock()

		select {
		case w.ProcessQueue <- filePath:
			// Successfully queued for normal worker processing.
		default:
			info, err := os.Stat(filePath)
			if err != nil {
				return
			}
			// If the queue is saturated, persist the file as pending so retry
			// logic can process it later instead of dropping the change signal.
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
				filepath.Dir(filePath), filePath, filepath.Base(filePath), info.Size(), info.ModTime().Unix())
			if err != nil {
				log.Printf("Failed to persist pending file: %s\n", filepath.Base(filePath))
				return
			}
		}
	})
}

// handleDelete marks removed paths as missing rather than hard-deleting rows.
// This preserves vector/folder relationships for reconciliation and ghost-claim
// flows while still reflecting that the content is currently absent on disk.
// this also saves resources by not immediately purging vectors that may be re-created shortly after
// the delete event, which is common when editors do safe saves by writing to a temp file and renaming over the original or cut/paste operations
// The missing status lets the system identify these cases and preserve vectors when possible instead of deleting and re-embedding unchanged content.
func (w *FileWatcher) handleDelete(filePath string, isDir bool) {
	// Build a wildcard that matches descendants under the deleted root path.
	childWildcard := filePath + string(filepath.Separator) + "%"
	now := time.Now().Unix()

	tx, err := w.DB.Begin()
	if err != nil {
		log.Println("Failed to start delete transaction:", err)
		return
	}
	// Rollback is a safety net for any early return before commit.
	defer tx.Rollback()

	// Mark all files in the folder and its subfolders as missing so downstream logic can reconcile later.
	if _, err := tx.Exec(`
		UPDATE files SET status = 'missing', updated_at = ?
		WHERE path = ? OR path LIKE ?`, now, filePath, childWildcard); err != nil {
		log.Println("Failed to update missing files:", err)
		return
	}

	// For directory deletions, mark folder and subfolders rows missing as well.
	if isDir {
		if _, err := tx.Exec(`
			UPDATE folders SET status = 'missing', updated_at = ?
			WHERE path = ? OR path LIKE ?`, now, filePath, childWildcard); err != nil {
			log.Println("Failed to update missing folders:", err)
			return
		}
	}
	if err := tx.Commit(); err == nil {
		log.Println("Safely soft-deleted path and its children:", filePath)
		if w.ctx != nil {
			runtime.EventsEmit(w.ctx, "folder-tree-updated")
		}
	}
}

// handleExactRename updates DB paths directly when both old and new names are
// available from the watcher event payload.
func (w *FileWatcher) handleExactRename(oldPath, newPath string, isDir bool) {
	if !isDir {
		// File rename: update one file row path/name in place.
		w.DB.Exec(`
			UPDATE files 
			SET path = ?, file_name = ?, updated_at = cast(strftime('%s', 'now') as int)
			WHERE path = ?`, newPath, filepath.Base(newPath), oldPath)
		log.Printf("Instant File Rename: %s -> %s\n", filepath.Base(oldPath), filepath.Base(newPath))
		return
	}

	// Directory rename: update root and descendant paths transactionally.
	oldWildcard := oldPath + string(filepath.Separator) + "%"
	now := time.Now().Unix()

	tx, err := w.DB.Begin()
	if err != nil {
		log.Println("Failed to start rename transaction:", err)
		return
	}
	defer tx.Rollback()

	// Rewrite descendant file paths to preserve relative suffixes under new root.
	if _, err := tx.Exec(`
		UPDATE files 
		SET path = ? || substr(path, length(?) + 1),
		    updated_at = ?
		WHERE path = ? OR path LIKE ?`,
		newPath, oldPath, now, oldPath, oldWildcard); err != nil {
		return
	}

	// Rewrite folder tree paths and reactivate rows under the new location.
	if _, err := tx.Exec(`
		UPDATE folders 
		SET path = ? || substr(path, length(?) + 1),
		    updated_at = ?,
		    status = 'active'
		WHERE path = ? OR path LIKE ?`,
		newPath, oldPath, now, oldPath, oldWildcard); err != nil {
		return
	}

	// Persist the rename mapping only if all updates succeeded.
	if err := tx.Commit(); err == nil {
		log.Printf("Transactional Nested Folder Rename: %s -> %s\n", oldPath, newPath)
		if w.ctx != nil {
			runtime.EventsEmit(w.ctx, "folder-tree-updated")
		}
	}
}

// workerLoop continuously consumes queued file paths and runs the fast-path
// processing pipeline for each entry.

func (w *FileWatcher) workerLoop() {
	for filePath := range w.ProcessQueue {
		w.processFileFast(filePath)
	}
}

// processFileFast validates, deduplicates, and vectorizes a file path using a
// sequence of cheap checks before expensive embedding work.
func (w *FileWatcher) processFileFast(newPath string) {
	info, err := os.Stat(newPath)
	if err != nil || info.IsDir() {
		return
	}

	// Reject unsupported, hidden, or size-out-of-range targets early.
	if !w.isValidTarget(newPath, info.Size(), false) {
		return
	}

	if !w.verifyTrueFileType(newPath) {
		log.Printf("Spoofed file type detected and rejected: %s\n", newPath)
		return
	}

	// Load current DB status/hash so we can short-circuit unchanged content.
	var currentStatus string
	var currentHash sql.NullString

	err = w.DB.QueryRow(`
		SELECT status, file_hash FROM files WHERE path = ?
	`, newPath).Scan(&currentStatus, &currentHash)

	fileSize := info.Size()
	modTime := info.ModTime().Unix()
	folderPath := filepath.Dir(newPath)

	// Hash current bytes to identify content-level equality independent of path.
	actualHash, hErr := hasher.CalculateHash(newPath)
	if hErr != nil {
		log.Printf("Failed to calculate hash for %s: %v\n", newPath, hErr)
		return
	}

	// If the file content hash is unchanged and row is still active, refresh only
	// metadata and skip vectorization entirely.
	if err == nil && currentStatus != "missing" && currentHash.Valid && currentHash.String == actualHash {
		// Keep timestamps/size fresh for reconciliation logic.
		w.DB.Exec(`
			UPDATE files 
			SET size = ?, last_modified = ?, updated_at = cast(strftime('%s', 'now') as int)
			WHERE path = ?`, fileSize, modTime, newPath)

		log.Printf("Content unchanged, updated metadata only for: %s\n", filepath.Base(newPath))
		return
	}

	// Ignore stale queue items for files already marked missing.
	if err == nil && currentStatus == "missing" {
		return
	}

	vec := &vectorize.Vectorizer{DB: w.DB}

	// Search for missing-file candidates with matching size and timestamp same as the incoming file has; these
	// are potential "ghost" records that may reuse existing vectors by hash.
	rows, dbErr := w.DB.Query(`
		SELECT id, file_hash FROM files 
		WHERE status = 'missing' AND size = ? AND last_modified = ?`, fileSize, modTime)

	if dbErr != nil {
		log.Println("DB Query Error:", dbErr)
		return
	}

	var candidates []struct {
		ID   int
		Hash sql.NullString
	}
	for rows.Next() {
		var c struct {
			ID   int
			Hash sql.NullString
		}
		rows.Scan(&c.ID, &c.Hash)
		candidates = append(candidates, c)
	}
	defer rows.Close()

	var vecErr error

	// If no candidates exist means this is a new/changed file and not a deleted one, register as pending row in db and vectorize as a new/changed
	// file. Otherwise attempt ghost-claim by hash before falling back to full vectorization.
	if len(candidates) == 0 {
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
			folderPath, newPath, filepath.Base(newPath), info.Size(), info.ModTime().Unix())

		if err != nil {
			log.Println("Failed to register pending file:", err)
			return
		}

		vecErr = vec.Vectorize(folderPath, newPath, actualHash, info)
	} else {
		// check if any candidates has same hash as incoming file's has which means the file is likely a true rename (for system that dont provide oldname field) or re-creation (either a deleted file respawned or a new/changed file has same content and size and timestamp as one of the missing file (very rare))
		// and we can claim the existing vectors instead of re-embedding
		for _, ghost := range candidates {
			if ghost.Hash.Valid && ghost.Hash.String == actualHash {
				w.claimGhost(newPath, actualHash, ghost.ID, info)
				return
			}
		}
		// if no candidate has same hash, mark the file as pending and fall back to full vectorization
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
			folderPath, newPath, filepath.Base(newPath), info.Size(), info.ModTime().Unix())

		if err != nil {
			log.Println("Failed to register pending file:", err)
			return
		}

		vecErr = vec.Vectorize(folderPath, newPath, actualHash, info)
	}

	if vecErr != nil {
		// Keep failed items retryable and promote to failed after retry budget is
		// exhausted.
		log.Printf("Vectorization failed for %s: %v\n", newPath, vecErr)
		w.DB.Exec(`
			UPDATE files 
			SET 
				retry_count = retry_count + 1, 
				status = CASE 
					WHEN retry_count >= 2 THEN 'failed' 
					ELSE 'pending' 
				END,
				updated_at = cast(strftime('%s', 'now') as int)
			WHERE path = ?`, newPath)
	}
}

// claimGhost reattaches a newly observed file path to vectors already owned by a
// missing-file record with the same content hash, then removes the ghost row.
func (w *FileWatcher) claimGhost(newPath string, actualHash string, ghostID int, info os.FileInfo) {
	tx, err := w.DB.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	folderPath := filepath.Dir(newPath)

	// Upsert the destination file row and claim the known content hash.
	_, err = tx.Exec(`
		INSERT INTO files (folder_path, path, file_name, file_hash, size, last_modified, status) 
		VALUES (?, ?, ?, ?, ?, ?, 'active')
		ON CONFLICT(path) DO UPDATE SET 
			file_hash = excluded.file_hash, 
			size = excluded.size,
			last_modified = excluded.last_modified, 
			status = 'active',
			updated_at = cast(strftime('%s', 'now') as int)`,
		folderPath, newPath, filepath.Base(newPath), actualHash, info.Size(), info.ModTime().Unix())

	if err != nil {
		log.Println("Failed to claim hash for file:", err)
		return
	}

	// Remove the old missing record now that ownership has been transferred.
	_, err = tx.Exec(`DELETE FROM files WHERE id = ?`, ghostID)
	if err != nil {
		return
	}

	if err := tx.Commit(); err == nil {
		log.Printf("Successfully claimed ghost vectors for: %s\n", filepath.Base(newPath))
	}
}

// isValidTarget is the shared filter used by watcher, walker, and workers.
// It approves directories that are safe to traverse/watch and files that meet
// naming, extension, and size constraints for indexing.
func (w *FileWatcher) isValidTarget(targetPath string, size int64, isDir bool) bool {
	base := strings.ToLower(filepath.Base(targetPath))

	// Fast reject hidden/lock-prefixed names across both files and folders.
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "~$") {
		return false
	}

	// Directory denylist for heavy/system/generated locations that should not be
	// traversed or watched.
	ignoredDirs := []string{
		"node_modules", ".git", ".next", "venv", ".venv",
		"__pycache__", ".idea", ".vscode", "build", "dist",
		"vendor", "coverage", ".cache", "tmp", "temp",

		// Windows/system-heavy locations.
		"windows", "system32", "syswow64", "winsxs",
		"program files", "program files (x86)",
		"$recycle.bin", "system volume information",
		"recovery", "perflogs",

		// Large application cache trees.
		"appdata",

		// Common build/test/tooling output trees.
		"target",
		".gradle",
		".m2",
		"out",
		".dart_tool",
		".pub-cache",
		".pytest_cache",
		".mypy_cache",
		".tox",
		"site-packages",
		"__mocks__",
		"obj",
		"packages",
		"bin",
	}

	// Directory mode: allow only if basename is not denylisted.
	if isDir {
		for _, dir := range ignoredDirs {
			if base == dir {
				return false
			}
		}
		return true
	}

	// File mode: reject temporary, partial-download, lock, and noisy OS files.
	if strings.HasPrefix(base, ".") ||
		strings.HasPrefix(base, "~$") ||
		strings.HasSuffix(base, ".tmp") ||
		strings.HasSuffix(base, ".crdownload") ||
		strings.HasPrefix(base, ".~lock.") ||
		strings.HasSuffix(base, ".part") ||
		strings.HasSuffix(base, ".download") ||
		strings.HasSuffix(base, ".opdownload") ||
		strings.HasSuffix(base, ".~tmp") ||
		strings.HasSuffix(base, ".bak") ||
		strings.HasSuffix(base, ".log") ||
		base == "desktop.ini" || base == "thumbs.db" || base == "ntuser.dat" {
		return false
	}

	// Restrict indexed file types to the known supported extension set.
	ext := filepath.Ext(base)
	validExts := map[string]bool{
		".txt":  true,
		".md":   true,
		".pdf":  true,
		".docx": true,
		".pptx": true,
	}

	if !validExts[ext] {
		return false
	}

	// Enforce hard bounds to avoid tiny noise files and very large files.
	const maxBytes = 100 * 1024 * 1024
	const minBytes = 50
	if size > maxBytes || size < minBytes {
		return false
	}

	return true
}

// RunWalker recursively scans a newly created directory while the app is
// running, registering nested folders and queueing eligible files for workers.
func (w *FileWatcher) RunWalker(root string) {
	log.Printf("Walker starting scan on new directory: %s\n", root)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// Ignore unreadable paths so one permission error does not stop the walk.
		if err != nil {
			return nil
		}

		// Gather file metadata for filtering and DB upserts.
		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Apply shared filter rules and prune denied subtrees efficiently.
		if !w.isValidTarget(path, info.Size(), d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Directory path: register watcher and upsert folder row as active.
		if d.IsDir() {
			_ = w.Watcher.Add(path)

			_, dbErr := w.DB.Exec(`
				INSERT INTO folders(path, status) VALUES (?, 'active')
				ON CONFLICT(path) DO UPDATE SET 
					status = 'active', 
					updated_at = cast(strftime('%s', 'now') as int)`,
				path)

			if dbErr != nil {
				log.Printf("Walker failed to insert folder %q: %v\n", path, dbErr)
			}

		} else {
			// File path: enqueue for worker processing; if queue is full, persist
			// pending state so retry flow can process later.
			select {
			case w.ProcessQueue <- path:
				// Successfully queued.
			default:
				_, err := w.DB.Exec(`
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
				}
			}
		}

		return nil
	})

	if err != nil {
		log.Printf("Walker encountered a critical error scanning %s: %v\n", root, err)
		runtime.EventsEmit(w.ctx, "folder-scan-error", map[string]string{
			"path":  root,
			"error": err.Error(),
		})
	} else {
		log.Printf("Walker finished scanning: %s\n", root)
		runtime.EventsEmit(w.ctx, "folder-tree-updated", root)
	}
}

// verifyTrueFileType validates content signatures using MIME sniffing so files
// with spoofed extensions can be rejected.
func (w *FileWatcher) verifyTrueFileType(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	// Read enough bytes for net/http content type detection.
	buffer := make([]byte, 512)
	_, err = file.Read(buffer)
	if err != nil {
		return false
	}

	// Detect MIME from file signature bytes.
	mimeType := http.DetectContentType(buffer)

	// Permit MIME types that correspond to currently supported extensions.
	switch mimeType {
	case "application/pdf":
		return true
	case "application/zip":
		return true
	case "text/plain; charset=utf-8":
		return true
	default:
		// Any non-whitelisted content signature is rejected.
		return false
	}
}
