package main

import (
	"context"
	"database/sql"
	"drag/pkg/crawler"
	"drag/pkg/search"
	_ "embed"
	"os"
	"path/filepath"
	"github.com/getlantern/systray"
    "runtime"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Embed the tray icon into the binary so the application can configure the
// system tray without depending on an external icon file at runtime.
//
//go:embed frontend/src/assets/images/letter-d.ico
var iconBytes []byte

type DirectoryState struct {
	IsValid   bool
	IsWatched bool
	IsPublic  bool
	IsIgnored bool
}

type FileDisplayInfo struct {
	FileName string `json:"fileName"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Status   string `json:"status"` // File lifecycle state exposed to the frontend.
	SyncedAt int64  `json:"syncedAt"`
}

// App holds the shared application services and runtime context used across the
// entire lifecycle of the desktop process.
type App struct {
	// ctx is the Wails application context, which lets the app call runtime
	// functions such as show, hide, or quit from any method on App.
	ctx     context.Context
	DB      *sql.DB
	Watcher *crawler.FileWatcher
	Walker  *crawler.FileWalker
	GC      *crawler.GarbageCollector
	RM      *crawler.RetryMachine
	Searcher *search.Searcher
}

// NewApp constructs the main application container and wires in the shared
// dependencies used by the UI, watcher, walker, retry machine, and cleanup jobs.
func NewApp(db *sql.DB, watcher *crawler.FileWatcher, walker *crawler.FileWalker, gc *crawler.GarbageCollector, rm *crawler.RetryMachine, searcher *search.Searcher) *App {
	return &App{
		DB:      db,
		Watcher: watcher,
		Walker:  walker,
		GC:      gc,
		RM:      rm,
		Searcher: searcher,
	}
}

// startup runs once when the Wails runtime finishes initializing the app.
// It stores the runtime context, starts the tray icon, and launches the
// background services that keep the crawler and maintenance loops running.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Launch the tray loop asynchronously so its event pump does not block the
	// rest of application startup.
    go func() {
        runtime.LockOSThread() // systray requires the same thread for working and breaks if go scheduler moves it to a different one, so we lock the goroutine to its current OS thread.
	    systray.Run(a.OnReady, a.OnExit)
    }()
    
	a.Watcher.SetContext(ctx)

    // Run the boot-time file walker to reconcile the database with the current
	//go a.Walker.RunBootSync()
    // Start the live filesystem watcher so the crawler can respond to changes.
	go a.Watcher.StartWatching()
    // Start the garbage collector and retry sweeper so they can perform periodic maintenance in the background.
	go a.GC.StartGarbageCollection()
	go a.RM.StartRetrySweeper()
	// Launch the tray loop asynchronously so its event pump does not block the
	// rest of application startup.
    go func() {
        runtime.LockOSThread() // systray requires the same thread for working and breaks if go scheduler moves it to a different one, so we lock the goroutine to its current OS thread.
	    systray.Run(a.OnReady, a.OnExit)
    }()
    
	a.Watcher.SetContext(ctx)

    // Run the boot-time file walker to reconcile the database with the current
	//go a.Walker.RunBootSync()
    // Start the live filesystem watcher so the crawler can respond to changes.
	go a.Watcher.StartWatching()
    // Start the garbage collector and retry sweeper so they can perform periodic maintenance in the background.
	go a.GC.StartGarbageCollection()
	go a.RM.StartRetrySweeper()
}


// OnReady is called after the tray subsystem is ready, which is when the icon,
// tooltip, and interactive menu items can safely be created.
func (a *App) OnReady() {
	// Apply the embedded tray icon so the process does not need to load image
	// assets from disk when it starts.
	systray.SetIcon(iconBytes)

	// Configure the tray title and hover tooltip to make the app recognizable
	// in the operating system's tray area.
	systray.SetTitle("Drag")
	systray.SetTooltip("Drag is running")

	// Build the tray menu that lets the user reopen the window or quit cleanly.
	mOpen := systray.AddMenuItem("Open Drag", "Show the search window")
	// Separate the primary action from the shutdown action for clarity.
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Completely shut down the background engine")

	// Listen for menu clicks on a background goroutine so the tray remains
	// responsive while the app handles open/quit commands.
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				// Show the main window when the user asks to open the app.
				wailsRuntime.WindowShow(a.ctx)

			case <-mQuit.ClickedCh:
				// Shut down the tray subsystem first, then ask Wails to exit.
				systray.Quit()
				wailsRuntime.Quit(a.ctx)
			}
		}
	}()
}

// GetDirectoryState validates a filesystem path and returns the app's current
// knowledge about that folder, including watch and visibility state.
// Wails exposes this to the frontend so the UI can inspect folder status.
func (a *App) GetDirectoryState(targetPath string) (DirectoryState, error) {
	targetPath = filepath.Clean(targetPath)

	// First confirm the path exists on disk and points to a directory.
	info, err := os.Stat(targetPath)
	if err != nil || !info.IsDir() {
		// Non-directories are treated as invalid folder targets.
		return DirectoryState{IsValid: false}, nil
	}

	// Start with a conservative default: the folder exists, but it may not yet
	// be watched or publicly shared.
	state := DirectoryState{
		IsValid:   true,
		IsWatched: false,
		IsPublic:  false,
		IsIgnored: false,
	}

	// Read persisted folder metadata to determine whether the app already knows
	// about this path and how it is currently classified.
	var status string
	var isPublicInt int

	err = a.DB.QueryRow(`SELECT status, is_public FROM folders WHERE path = ?`, targetPath).Scan(&status, &isPublicInt)

	// No row means the folder is valid on disk but not yet registered in the DB.
	if err == sql.ErrNoRows {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if status == "active" {
		state.IsWatched = true
		if isPublicInt == 1 {
			state.IsPublic = true
		}
	} else if status == "ignored" {
		state.IsIgnored = true
	}
	return state, nil
}

// SetFolderVisibility updates the public/private flag for a folder and,
// optionally, all descendant folders inside the same tree.
// Wails exposes this to the frontend as a folder configuration action.
func (a *App) SetFolderVisibility(targetPath string, isPublic bool, applyToSubfolders bool) error {
	targetPath = filepath.Clean(targetPath)

	// Convert the boolean into SQLite's integer representation.
	pubInt := 0
	if isPublic {
		pubInt = 1
	}

	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // Ensure we rollback if anything goes wrong before we commit

	// Update the selected folder record first.
	_, err = tx.Exec(`
		UPDATE folders 
		SET is_public = ?, updated_at = cast(strftime('%s', 'now') as int) 
		WHERE path = ?`,
		pubInt, targetPath)

	if err != nil {
		return err
	}

	// If requested, apply the same visibility state to every folder under the
	// chosen directory tree.
	if applyToSubfolders {
		// Build a SQL LIKE pattern that matches all descendants beneath the root.
		likePattern := targetPath + string(os.PathSeparator) + "%"

		_, err = tx.Exec(`
			UPDATE folders 
			SET is_public = ?, updated_at = cast(strftime('%s', 'now') as int) 
			WHERE path LIKE ?`,
			pubInt, likePattern)

		if err != nil {
			return err
		}
	}
	// Commit the transaction once both the parent and optional descendants have
	// been updated successfully.
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// SetFolderWatchStatus changes whether a folder tree is actively tracked by the
// crawler. Enabling watch starts live observation, while disabling watch marks
// the tree ignored, removes files, and unregisters watcher paths.
func (a *App) SetFolderWatchStatus(rootPath string, isWatched bool) error {
	rootPath = filepath.Clean(rootPath)

	// When enabling watch, reuse the existing walker so the folder tree is scanned
	// and registered through the normal startup/runtime discovery path.
	if isWatched {

		go a.Watcher.RunWalker(rootPath)

		return nil
	}

	// Build a descendant pattern so the database and watcher can target the full
	// subtree rooted at the requested folder.
	likePattern := rootPath + string(os.PathSeparator) + "%"

	// Start one transaction so the ignored-state update and file deletion happen
	// together or not at all.
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}

	// Rollback protects against partial updates if any step fails before commit.
	defer tx.Rollback()

	// Capture the current folder paths first so watcher removal can happen after
	// the database changes are safely committed.
	var pathsToUnwatch []string
	rows, err := tx.Query(`SELECT path FROM folders WHERE path = ? OR path LIKE ?`, rootPath, likePattern)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var subPath string
		if err := rows.Scan(&subPath); err == nil {
			pathsToUnwatch = append(pathsToUnwatch, subPath)
		} else {
			return err
		}
	}

	// Mark the selected folder and subfolders as ignored in the database.
	_, err = tx.Exec(`
		UPDATE folders 
		SET status = 'ignored', updated_at = cast(strftime('%s', 'now') as int) 
		WHERE path = ? OR path LIKE ?`,
		rootPath, likePattern)

	if err != nil {
		return err
	}

	// Remove files under the ignored subtree so the index no longer treats them
	// as active content sources.
	_, err = tx.Exec(`DELETE FROM files WHERE folder_path = ? OR folder_path LIKE ?`, rootPath, likePattern)
	if err != nil {
		return err
	}

	// Persist the DB changes before touching the OS watcher registry.
	if err := tx.Commit(); err != nil {
		return err
	}

	// After the database is safely updated, unregister each watched path from the
	// live watcher so future filesystem events are ignored.
	for _, p := range pathsToUnwatch {
		_ = a.Watcher.Watcher.Remove(p)
	}

	return nil
}

// GetFileInfo returns the stored metadata for a single file so the frontend can
// display its name, path, size, sync status, and last update time.
func (a *App) GetFileInfo(filePath string) (FileDisplayInfo, error) {
	filePath = filepath.Clean(filePath)
	var f FileDisplayInfo

	err := a.DB.QueryRow(`
		SELECT file_name, path, size, status, updated_at 
		FROM files 
		WHERE path = ?`,
		filePath).Scan(&f.FileName, &f.Path, &f.Size, &f.Status, &f.SyncedAt)

	if err != nil {
		return f, err
	}

	return f, nil
}

func (a *App) Search(query string, useHybrid bool) ([]search.LocalSearchResult, error) {
	var results []search.SearchResult
	var err error

	if useHybrid {
		results, err = a.Searcher.HybridSearch(a.ctx, query)
	} else {
		results, err = a.Searcher.SearchSemantic(a.ctx, query)
	}

	if err != nil {
		return nil, err
	}

	return a.Searcher.SearchLocal(results), nil
}

// OnExit performs final process shutdown cleanup when the application is about
// to terminate.
func (a *App) OnExit() {
	// Close the shared database handle so the connection pool is released cleanly.
	a.DB.Close()
}
