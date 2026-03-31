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
	DB *sql.DB
	Watch *FileWatcher
}

// RunBootSync is the master initialization function. It runs exactly ONCE at startup.
// It perfectly reconciles the physical hard drive with the SQLite database.
func (w *FileWalker) RunBootSync() {
	log.Println("Starting Boot Synchronization...")

	// ==========================================
	// PHASE 1: THE ORPHAN SWEEP (DB -> Disk)
	// Find files that were DELETED while the app was closed.
	// ==========================================
	w.sweepMissingFiles()

	fileDBState := make(map[string]struct {
		Size    int64
		ModTime int64
	})

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

	folderDBState := make(map[string]struct{
		status string
		state bool
	})
	folders, ferr := w.DB.Query(`SELECT path, status FROM folders WHERE status != 'missing'`)
	if ferr == nil {
		for folders.Next() {
			var path string
			var status string
			folders.Scan(&path, &status)
			if status == "ignored" {
				folderDBState[path] = struct{
					status string
					state bool
				}{status, true}
			} else {
				folderDBState[path] = struct{
					status string
					state bool
				}{status, false}
			}
		}
		folders.Close()
	}

	// ==========================================
	// PHASE 2: THE DISCOVERY SWEEP (Disk -> DB)
	// Find files that were ADDED or MODIFIED while the app was closed.
	// ==========================================
	standardRoots := w.getStandardRoots()
	for _, root := range standardRoots {
		if _, err := os.Stat(root); err == nil {
			log.Printf("Boot Sync: Scanning standard drive %s\n", root)
			w.scanPhysicalDrive(root, fileDBState, folderDBState)
		}
	}

	for path := range folderDBState {
		// FIX 3: Explicitly read the live map to avoid stale state
		if _, err := os.Stat(path); err != nil {
			if !folderDBState[path].state {
				w.Watch.handleDelete(path, true) // Mark missing
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

	// 1. Always grab the Home Directory first (C:\Users\Name)
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
	}

	// 2. Ask the Operating System kernel for all mounted partitions
	// Passing 'false' tells the OS to ignore virtual/system partitions (like Docker or Snap loops)
	partitions, err := disk.Partitions(false)
	if err == nil {
		for _, p := range partitions {
			
			// We skip the primary C:\ or / root because scanning the entire 
			// Windows/System32 or Linux root filesystem will crash the app.
			if p.Mountpoint == "C:\\" || p.Mountpoint == "C:" || p.Mountpoint == "/" {
				continue
			}

			// Safely normalize the Windows path (Prevent "D:\\")
			mount := p.Mountpoint
			if !strings.HasSuffix(mount, "\\") {
				mount += "\\"
			}

			rootPtr, _ := windows.UTF16PtrFromString(mount)
            driveType := windows.GetDriveType(rootPtr)
            // 5. Compare the result against the official Windows constant for removable media
            if driveType == windows.DRIVE_FIXED{
				// Add the physical OS mount point (e.g., "D:\", "E:\", or "/Volumes/MyUSB")
				roots = append(roots, mount)   
            }
		}
	}

	return roots
}
// ─── Internal Reconciliation Methods ──────────────────────────────────────────

func (w *FileWalker) sweepMissingFiles() {
	// We only check files the database currently thinks are alive
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

		// If the file is physically gone, mark it missing safely
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
	}, folderDBState map[string]struct{
		status string
		state bool
	}) {

	// 2. Walk the physical hard drive
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if folderData, exists := folderDBState[path]; exists{
			if folderData.status == "ignored" {
				return filepath.SkipDir
			} else{
				folderDBState[path] = struct{
					status string
					state bool
				}{folderData.status, true}
			}
		}
		
		if err != nil {
			return nil // Silently skip unreadable paths (Permission Denied)
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		// A. THE GATEKEEPER
		// Uses the exact same filter you built for the Watcher!
		if !w.Watch.isValidTarget(path, info.Size(), d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir // Ignore blacklisted folders entirely
			}
			return nil
		}

		// B. DIRECTORY LOGIC
		if d.IsDir() {
			// 1. Add it to fsnotify so we watch it for live changes
			_ = w.Watch.Watcher.Add(path)

			// 2. Ensure it's active in the database (For the P2P tree)
			if _, ok := folderDBState[path]; !ok {
				w.DB.Exec(`
					INSERT INTO folders(path, status) VALUES (?, 'active')
					ON CONFLICT(path) DO UPDATE SET status = 'active', updated_at = cast(strftime('%s', 'now') as int)`, 
					path)
			}
			return nil
		}

		// C. FILE RECONCILIATION LOGIC
		fileData, exists := fileDBState[path]

		if !exists {
			// SCENARIO 1: Brand New File
			// Not in our database, created while the app was closed.
			w.Watch.ProcessQueue <- path
		} else if fileData.Size != info.Size() || fileData.ModTime != info.ModTime().Unix() {
			// SCENARIO 2: Modified File
			// Exists in DB, but the physical size or timestamp changed offline.
			w.Watch.ProcessQueue <- path
		}

		// SCENARIO 3: Perfect Match
		// If it exists and the stats match, we do absolutely nothing. It is fully synced.
		return nil
	})

	if err != nil {
		log.Printf("Error scanning physical drive at %s: %v\n", root, err)
	}
}