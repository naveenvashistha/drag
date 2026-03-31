package main

import (
	"context"
	_ "embed"
    "path/filepath"
    "os"
    "drag/pkg/crawler"
    "database/sql"
	"github.com/getlantern/systray"
    "github.com/wailsapp/wails/v2/pkg/runtime"
)

// this directive puts the icon file as a byte slice in iconBytes variable. This allows us to set the system tray icon without needing to read from disk at runtime.
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
	Status   string `json:"status"` // 'active', 'pending', 'failed'
	SyncedAt int64  `json:"syncedAt"`
}

// App struct holds the context and any other state we want to maintain across our app's lifecycle
type App struct {
    // ctx is the Wails application context, which allows us to interact with the runtime (like showing/hiding windows, quitting the app, etc.) from anywhere in our App struct.
	ctx     context.Context
	DB      *sql.DB
	Watcher *crawler.FileWatcher
	Walker  *crawler.FileWalker
	GC      *crawler.GarbageCollector
	RM      *crawler.RetryMachine
}

// NewApp creates a new App application struct
func NewApp(db *sql.DB, watcher *crawler.FileWatcher, walker *crawler.FileWalker, gc *crawler.GarbageCollector, rm *crawler.RetryMachine) *App {
	return &App{
		DB: db,
		Watcher: watcher,
		Walker: walker,
		GC: gc,
		RM: rm,
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Start the system tray in a separate goroutine so it doesn't block the main thread as it starts its own infinite loop to keep the tray alive. The OnReady and OnExit methods will be called by the systray library when appropriate.
	go systray.Run(a.OnReady, a.OnExit)

    a.Watcher.SetContext(ctx)

    go a.Walker.RunBootSync()
	go a.Watcher.StartWatching()
	go a.GC.StartGarbageCollection()
	go a.RM.StartRetrySweeper()
}


// OnReady is called when the systray is ready to be used. This is where we set up our tray icon, tooltip, and menu items.
func (a *App) OnReady() {
    // Set the tray icon using the embedded iconBytes. This avoids the need to read an icon file from disk at runtime, which can simplify deployment and avoid file path issues.
    systray.SetIcon(iconBytes) 
    
    // Set the title and tooltip for the tray icon. The title is often not shown on all platforms, but the tooltip will show when the user hovers over the icon.
    systray.SetTitle("Drag")
    systray.SetTooltip("Drag is running")

    // Create the menu buttons
    mOpen := systray.AddMenuItem("Open Drag", "Show the search window")
    systray.AddSeparator() // Adds a horizontal line
    mQuit := systray.AddMenuItem("Quit", "Completely shut down the background engine")

    // Listen for button clicks in a background loop
    go func() {
        for {
            select {
            case <-mOpen.ClickedCh:
                // User clicked "Open": Tell Wails to show the window!
                runtime.WindowShow(a.ctx)
                
            case <-mQuit.ClickedCh:
                // User clicked "Quit": We actually kill the app now.
                systray.Quit()          // Kill the tray
                runtime.Quit(a.ctx)     // Kill Wails
            }
        }
    }()
}

// IsValidDirectory checks if the provided path is a valid directory on the filesystem.
// this will be used by frontend to validate user directory address input
// GetDirectoryState checks if a folder exists and retrieves its current DB settings.
// Wails exports this as: GetDirectoryState(targetPath: string): Promise<DirectoryState>
func (a *App) GetDirectoryState(targetPath string) (DirectoryState, error) {
	targetPath = filepath.Clean(targetPath)

	// 1. Check the Operating System (Physical Reality)
	info, err := os.Stat(targetPath)
	if err != nil || !info.IsDir() {
		// If it doesn't exist or is a file, return immediately
		return DirectoryState{IsValid: false}, nil
	}

	// 2. Set the default state for a valid folder that hasn't been watched yet
	state := DirectoryState{
		IsValid:   true,
		IsWatched: false,
		IsPublic:  false,
        IsIgnored: false,
	}

	// 3. Check the Database (App Memory)
	var status string
	var isPublicInt int

	err = a.DB.QueryRow(`SELECT status, is_public FROM folders WHERE path = ?`, targetPath).Scan(&status, &isPublicInt)
	

	// If err is nil, the folder exists in our database!
	if err != nil{
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

// SetFolderVisibility changes a folder to public (true) or private (false).
// Wails will export this as: SetFolderVisibility(targetPath: string, isPublic: boolean, applyToSubfolders: boolean): Promise<void>
func (a *App) SetFolderVisibility(targetPath string, isPublic bool, applyToSubfolders bool) error {
	targetPath = filepath.Clean(targetPath)

	// SQLite doesn't use true/false booleans, it uses 1 and 0
	pubInt := 0
	if isPublic {
		pubInt = 1
	}

    tx, err := a.DB.Begin()
    if err != nil{
        return err
    }
    defer tx.Rollback() // Ensure we rollback if anything goes wrong before we commit

	// 1. Update the parent folder
	_, err = tx.Exec(`
		UPDATE folders 
		SET is_public = ?, updated_at = cast(strftime('%s', 'now') as int) 
		WHERE path = ?`, 
		pubInt, targetPath)
		
	if err != nil {
		return err
	}

	// 2. Cascade to all subfolders if requested
	if applyToSubfolders {
		// Example: If target is "D:\Code", pattern becomes "D:\Code\%"
		// This mathematically targets ONLY files inside this specific directory tree.
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
    // 3. Commit the transaction to save changes
    if err := tx.Commit(); err != nil {
        return err
    }
	return nil
}

// SetFolderWatchStatus adds or removes a folder and all its subfolders from the watch list.
func (a *App) SetFolderWatchStatus(rootPath string, isWatched bool) error {
	rootPath = filepath.Clean(rootPath)

	// ==========================================
	// SCENARIO 1: WATCH THE FOLDER
	// ==========================================
	if isWatched {
		
		// Brilliant refactor: We just trigger your existing Walker!
		// Because it uses a Goroutine internally or dumps to the queue, 
		// it is non-blocking and highly efficient.
		go a.Watcher.RunWalker(rootPath)
		
		return nil
	}

	// ==========================================
	// SCENARIO 2: UNWATCH THE FOLDER (Ignore State)
	// ==========================================
	
	// We need to match the parent folder AND any deeply nested subfolders
	likePattern := rootPath + string(os.PathSeparator) + "%"

	// 1. GATHER PATHS FIRST (Do not remove from OS yet!)
	var pathsToUnwatch []string
	rows, err := a.DB.Query(`SELECT path FROM folders WHERE path = ? OR path LIKE ?`, rootPath, likePattern)
    if err != nil {
        return err
    }
    defer rows.Close()
    for rows.Next() {
        var subPath string
        if err := rows.Scan(&subPath); err == nil {
            pathsToUnwatch = append(pathsToUnwatch, subPath)
        } else{
            return err
        }
    }

	// 2. START THE ATOMIC TRANSACTION
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	
	// Safety Net: If the function panics or returns early before Commit(), 
	// Rollback() executes automatically and undoes any partial changes.
	defer tx.Rollback() 

	// 3. UPDATE FOLDERS TO 'IGNORED'
	_, err = tx.Exec(`
		UPDATE folders 
		SET status = 'ignored', updated_at = cast(strftime('%s', 'now') as int) 
		WHERE path = ? OR path LIKE ?`, 
		rootPath, likePattern)

	if err != nil {
		return err
	}

	// 4. THE PURGE: DELETE THE FILES
	_, err = tx.Exec(`DELETE FROM files WHERE folder_path = ? OR folder_path LIKE ?`, rootPath, likePattern)
	if err != nil {
		return err
	}

	// 5. COMMIT THE TRANSACTION (Lock it in)
	if err := tx.Commit(); err != nil {
		return err
	}

	// 6. REMOVE FROM OS WATCHER (Only happens if the DB transaction was 100% successful)
	for _, p := range pathsToUnwatch {
		_ = a.Watcher.Watcher.Remove(p)
	}

	return nil
}

// GetFileInfo retrieves the database record for a specific file.
// Wails exports this as: GetFileInfo(filePath string) Promise<FileDisplayInfo>
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

// onExit runs right before the app finally dies
func (a *App) OnExit() {
    // Clean up your database connections and network ports here later!
}
