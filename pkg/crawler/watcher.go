package crawler

import (
	"database/sql"
	"log"
	"context"
	"os"
	"drag/pkg/hasher"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"net/http"
	"time"
	"reflect"
	"drag/pkg/vectorize"
	"github.com/fsnotify/fsnotify"
)

// FileWatcher manages fsnotify and background background processing queues
type FileWatcher struct {
	DB           *sql.DB // Access to the SQLite database instance
	Watcher      *fsnotify.Watcher // 
	ProcessQueue chan string
	ctx          context.Context
	timers     map[string]*time.Timer
	timersLock sync.Mutex
}

func (w *FileWatcher) SetContext(ctx context.Context) {
	w.ctx = ctx
}

// NewFileWatcher initializes the watcher and a massive 10,000 file buffer queue
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

// Start begins the event loop and a multi-core worker pool
func (w *FileWatcher) StartWatching() {
	log.Println("Starting Watcher and Worker Pool...")

	// Start 4 concurrent workers to max out multi-core CPUs
	for i := 0; i < 4; i++ {
		go w.workerLoop()
	}

	// Main fsnotify event loop
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

// ─── Event Routing ────────────────────────────────────────────────────────────

func (w *FileWatcher) routeEvent(event fsnotify.Event) {

	// 2. Ask the OS if the path is a directory
	info, err := os.Stat(event.Name)
	isDir := (err == nil && info.IsDir())

	// 3. Directory Handling
	if isDir && w.isValidTarget(event.Name, info.Size(), true) {
		if event.Has(fsnotify.Create) {
			log.Println("New folder detected, watching:", event.Name)
			// Spawn walker to catch "Silent Children" pre-existing in this folder
			go w.RunWalker(event.Name) 
			return
		}
		if event.Has(fsnotify.Write) {
			return // Ignore directory metadata writes
		}
	}

	// 4. The Windows "OldName" Fast-Path Rename
	// If fsnotify gives us the exact old name, we bypass all heuristics.
	field := reflect.ValueOf(event).FieldByName("renamedFrom")
	oldName := ""
	if field.IsValid() {
		oldName = field.String()
	}
	if event.Has(fsnotify.Create) && oldName != "" {
		w.handleExactRename(oldName, event.Name, isDir)
		return
	}

	// 5. Normal File Events
	switch {
	case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
		w.debounce(event.Name)

	case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
		w.handleDelete(event.Name, isDir)
	}
}

// ─── Debounce & Status Updates ───────────────────────────────────────────────

func (w *FileWatcher) debounce(filePath string) {
	w.timersLock.Lock()
	defer w.timersLock.Unlock()

	if timer, exists := w.timers[filePath]; exists {
		timer.Stop()
	}

	w.timers[filePath] = time.AfterFunc(2*time.Second, func() {
		w.timersLock.Lock()
		delete(w.timers, filePath)
		w.timersLock.Unlock()

		select {
			case w.ProcessQueue <- filePath:
				// happy path, nothing extra needed
			default:
				info, err := os.Stat(filePath)
				if err != nil{
					return
				}
				// queue is full — persist so a retry job can recover it
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

func (w *FileWatcher) handleDelete(filePath string, isDir bool) {
	// Create the wildcard to catch all nested children (e.g., "C:\Taxes\%")
	childWildcard := filePath + string(filepath.Separator) + "%"
	now := time.Now().Unix()

	tx, err := w.DB.Begin()
	if err != nil {
		log.Println("Failed to start delete transaction:", err)
		return
	}
	defer tx.Rollback() // Safety net: undoes everything if we don't reach Commit()

	// 1. Mark all nested files as missing (Protects AI Vectors)
	if _, err := tx.Exec(`
		UPDATE files SET status = 'missing', updated_at = ?
		WHERE path = ? OR path LIKE ?`, now, filePath, childWildcard); err != nil {
		log.Println("Failed to update missing files:", err)
		return 
	}

	// 2. Mark the folder and all subfolders as missing (Preserves P2P Tree)
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

func (w *FileWatcher) handleExactRename(oldPath, newPath string, isDir bool) {
	if !isDir {
		// STANDARD FILE RENAME
		w.DB.Exec(`
			UPDATE files 
			SET path = ?, file_name = ?, updated_at = cast(strftime('%s', 'now') as int)
			WHERE path = ?`, newPath, filepath.Base(newPath), oldPath)
		log.Printf("Instant File Rename: %s -> %s\n", filepath.Base(oldPath), filepath.Base(newPath))
		return
	}

	// NESTED DIRECTORY RENAME
	oldWildcard := oldPath + string(filepath.Separator) + "%"
	now := time.Now().Unix()

	tx, err := w.DB.Begin()
	if err != nil {
		log.Println("Failed to start rename transaction:", err)
		return
	}
	defer tx.Rollback()
	// 1. Update The Map (files table)
	if _, err := tx.Exec(`
		UPDATE files 
		SET path = ? || substr(path, length(?) + 1),
		    updated_at = ?
		WHERE path = ? OR path LIKE ?`, 
		newPath, oldPath, now, oldPath, oldWildcard); err != nil {
		return
	}

	// 2. Update The P2P Tree (folders table)
	if _, err := tx.Exec(`
		UPDATE folders 
		SET path = ? || substr(path, length(?) + 1),
		    updated_at = ?,
		    status = 'active'
		WHERE path = ? OR path LIKE ?`, 
		newPath, oldPath, now, oldPath, oldWildcard); err != nil {
		return
	}

	// 3. Safely commit the structural change
	if err := tx.Commit(); err == nil {
		log.Printf("Transactional Nested Folder Rename: %s -> %s\n", oldPath, newPath)
		if w.ctx != nil {
			runtime.EventsEmit(w.ctx, "folder-tree-updated")
		}
	}
}

// ─── Background Workers & Deduplication ─────────────────────────────────────

func (w *FileWatcher) workerLoop() {
	for filePath := range w.ProcessQueue {
		w.processFileFast(filePath)
	}
}

func (w *FileWatcher) processFileFast(newPath string) {
	info, err := os.Stat(newPath)
	if err != nil || info.IsDir() {
		return
	}

	// Filter out bad extensions and files that are too large
	if !w.isValidTarget(newPath, info.Size(), false) {
		return
	}

	if !w.verifyTrueFileType(newPath) {
		log.Printf("Spoofed file type detected and rejected: %s\n", newPath)
		return
	}

	// 1. THE CIRCUIT BREAKER & CURRENT STATE
	var currentStatus string
	var currentHash sql.NullString // Safely catch NULLs
	
	err = w.DB.QueryRow(`
		SELECT status, file_hash FROM files WHERE path = ?
	`, newPath).Scan(&currentStatus, &currentHash)

	fileSize := info.Size()
	modTime := info.ModTime().Unix()
	folderPath := filepath.Dir(newPath)
	// THE HASH TIE-BREAKER
	actualHash, hErr := hasher.CalculateHash(newPath)
	if hErr != nil {
		log.Printf("Failed to calculate hash for %s: %v\n", newPath, hErr)
		return
	}

	// ==========================================
	// 2. THE "UNCHANGED CONTENT" BYPASS
	// ==========================================
	// If the text hasn't changed, save your CPU and API limits!
	if err == nil && currentStatus != "missing" && currentHash.Valid && currentHash.String == actualHash {
		// Just update the metadata so the Walker knows it's fresh
		w.DB.Exec(`
			UPDATE files 
			SET size = ?, last_modified = ?, updated_at = cast(strftime('%s', 'now') as int)
			WHERE path = ?`, fileSize, modTime, newPath)
			
		log.Printf("Content unchanged, updated metadata only for: %s\n", filepath.Base(newPath))
		return 
	}

	// If the file is already marked as missing but still in queue then just skip it.
	if err == nil && currentStatus == "missing" {
		return
	}


	vec := &vectorize.Vectorizer{DB: w.DB}

	// 1. Ask SQLite for any missing files with this exact size & timestamp
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
		var c struct{ ID int; Hash sql.NullString }
		rows.Scan(&c.ID, &c.Hash)
		candidates = append(candidates, c)
	}
	defer rows.Close()

	var vecErr error
	// 2. The Routing Logic
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
			return // If we can't even map it, we must abort.
		}

		vecErr = vec.Vectorize(folderPath, newPath, actualHash, info)
	} else {
		
		for _, ghost := range candidates {
			if ghost.Hash.Valid && ghost.Hash.String == actualHash {
				w.claimGhost(newPath, actualHash, ghost.ID, info)
				return
			}
		}
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
			return // If we can't even map it, we must abort.
		}

		vecErr = vec.Vectorize(folderPath, newPath, actualHash, info)
	}

	if vecErr != nil{
		// FAILURE: Keep it pending, bump the retry count so a background job can try again later.
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

func (w *FileWatcher) claimGhost(newPath string, actualHash string, ghostID int, info os.FileInfo) {
	tx, err := w.DB.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	folderPath := filepath.Dir(newPath)

	// 1. THE STEAL: Upsert the new/updated file to claim the hash
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

	// 2. THE KILL: Delete the missing ghost row
	_, err = tx.Exec(`DELETE FROM files WHERE id = ?`, ghostID)
	if err != nil {
		return
	}

	if err := tx.Commit(); err == nil {
		log.Printf("Successfully claimed ghost vectors for: %s\n", filepath.Base(newPath))
	}
}

// ─── Helper Filters ───────────────────────────────────────────────────────────

// isValidTarget acts as the universal gatekeeper for the watcher and the workers.
// It returns true ONLY if the directory is safe to watch, or the file is safe to vectorize.
func (w *FileWatcher) isValidTarget(targetPath string, size int64, isDir bool) bool {
	base := strings.ToLower(filepath.Base(targetPath))

	// 1. HIDDEN CHECK FIRST (Applies to both files and folders)
    if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "~$") {
        return false
    }

	// 1. DIRECTORY BLACKHOLES
	// These are massive, auto-generated, or system folders that will crash your 
	// watcher or pollute your AI database with useless code/cache fragments.
	ignoredDirs := []string{
    // --- Your existing entries ---
		"node_modules", ".git", ".next", "venv", ".venv",
		"__pycache__", ".idea", ".vscode", "build", "dist",
		"vendor", "coverage", ".cache", "tmp", "temp",

		// --- Windows System (will crash watcher or flood index) ---
		"windows", "system32", "syswow64", "winsxs",
		"program files", "program files (x86)",
		"$recycle.bin", "system volume information",
		"recovery", "perflogs",

		// --- AppData subfolders (roaming/local caches are enormous) ---
		"appdata",     // or be more surgical: watch only AppData\Documents subtrees

		// --- More dev tooling you likely missed ---
		"target",      // Maven / Rust cargo output
		".gradle",
		".m2",         // Maven local repo
		"out",         // IntelliJ output
		".dart_tool",
		".pub-cache",
		".pytest_cache",
		".mypy_cache",
		".tox",
		"site-packages",
		"__mocks__",
		"obj",         // .NET build artifacts
		"packages",    // NuGet / Flutter
		"bin",         // compiled binaries folder (common in .NET/Java projects)
	}

	// Reject if the target itself is a blacklisted directory
	if isDir {
		for _, dir := range ignoredDirs {
			if base == dir {
				return false
			}
		}
		return true // If it's a directory and not blacklisted, we watch it.
	}

	// 2. TEMP & HIDDEN FILE FILTER
	// Ignore OS hidden files, browser downloads, and Office lock files
	if strings.HasPrefix(base, ".") || 
	   strings.HasPrefix(base, "~$") || 
	   strings.HasSuffix(base, ".tmp") || 
	   strings.HasSuffix(base, ".crdownload") || 
	   strings.HasPrefix(base, ".~lock.") || 
	   strings.HasSuffix(base, ".part") ||       // Firefox partial downloads
	   strings.HasSuffix(base, ".download") ||   // Safari partial downloads  
	   strings.HasSuffix(base, ".opdownload") ||  // Opera partial downloads
	   strings.HasSuffix(base, ".~tmp") ||
	   strings.HasSuffix(base, ".bak") ||        // backup files (usually auto-generated)
	   strings.HasSuffix(base, ".log") ||  // log files (unless you want them) 
	   base == "desktop.ini" || base == "thumbs.db" || base == "ntuser.dat"{
	   return false
	}

	// 3. STRICT EXTENSION WHITELIST
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

	// 4. HARD SIZE LIMIT (100 MB)
	const maxBytes = 100 * 1024 * 1024
	const minBytes = 50                  // 50 bytes floor — skip empty/stub files
	if size > maxBytes || size < minBytes {
		return false
	}

	return true
}

// RunWalker recursively scans a newly discovered directory during runtime.
// It is designed to be fired off instantly in the background: go w.RunWalker(newPath)
func (w *FileWatcher) RunWalker(root string) {
	log.Printf("Walker starting scan on new directory: %s\n", root)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// 1. Handle OS-level access errors (e.g., Windows "Permission Denied")
		if err != nil {
			return nil // Silently skip unreadable paths without crashing the walker
		}

		// 2. Extract stats for our Gatekeeper
		info, err := d.Info()
		if err != nil {
			return nil
		}

		// 3. THE GATEKEEPER (The ultimate CPU saver)
		if !w.isValidTarget(path, info.Size(), d.IsDir()) {
			if d.IsDir() {
				// 🛑 CRITICAL: Returning filepath.SkipDir tells Go to completely 
				// ignore this folder and everything inside it. This single line is
				// what stops "node_modules" from freezing your entire application.
				return filepath.SkipDir
			}
			return nil // Just skip the invalid file
		}

		// 4. Processing Logic
		if d.IsDir() {
			// --- FOLDER LOGIC (P2P Tree & Watcher) ---
			
			// A. Tell fsnotify to listen to this nested folder.
			// (Windows requires explicit registration for every sub-directory).
			_ = w.Watcher.Add(path)

			// B. UPSERT into the P2P folders table.
			// If it's a resurrected folder, this flips it from 'missing' to 'active'.
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
			// --- FILE LOGIC (AI Vectors) ---
			
			// Dump the valid file into the 10,000-capacity worker channel.
			// The background workers will pick it up, compute the xxHash, 
			// and seamlessly deduplicate or vectorize it.
			select {
				case w.ProcessQueue <- path:
					// happy path, nothing extra needed
				default:
					// queue is full — persist so a retry job can recover it
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
			"path": root,
			"error": err.Error(),
		})
	} else {
		log.Printf("Walker finished scanning: %s\n", root)
		runtime.EventsEmit(w.ctx, "folder-tree-updated", root)
	}
}

// verifyTrueFileType reads the first 512 bytes to check the Magic Number
func (w *FileWatcher) verifyTrueFileType(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	// Read the first 512 bytes required for MIME detection
	buffer := make([]byte, 512)
	_, err = file.Read(buffer)
	if err != nil {
		return false // Empty or unreadable files are rejected
	}

	// Detect the true MIME type
	mimeType := http.DetectContentType(buffer)

	// Since we only allow specific extensions, we verify their true MIME counterparts
	// Note: .docx and .pptx are essentially zipped XML files, so their true type is zip.
	switch mimeType {
	case "application/pdf":
		return true 
	case "application/zip": // Covers .docx, .pptx
		return true
	case "text/plain; charset=utf-8": // Covers .txt, .md, csv, etc.
		return true
	default:
		// If it's a renamed MP4, EXE, or JPEG, it hits the default and gets rejected!
		return false
	}
}