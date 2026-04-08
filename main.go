package main

import (
	"drag/pkg/crawler"
	"drag/pkg/db"
	"drag/pkg/system"
	"drag/pkg/search"
	"drag/pkg/embedder"
	"embed"
	"log"
	"os"
	"path/filepath"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// Embed the built frontend into the Go binary so the desktop app can serve its
// UI directly from memory instead of depending on external files at runtime.
// This makes distribution simpler, avoids missing-file issues, and keeps the
// packaged application self-contained.
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {

	// Register the application for automatic startup so it can resume running
	// in the background after the operating system starts.
	system.EnableAutoStart()

	// Locate the user-specific configuration directory, which is where the app
	// stores its writable data on the current machine.
	dataDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal("Failed to get config dir:", err)
	}

	// Keep all Drag data inside a dedicated subfolder so app files stay grouped
	// together and do not mix with other configuration data.
	appDataDir := filepath.Join(dataDir, "Drag")
	os.MkdirAll(appDataDir, 0755)

	// Open the SQLite database and initialize the schema used by the crawler,
	// vector search, retry logic, and cleanup jobs.
	db, db_err := db.InitDB(appDataDir)
	if db_err != nil {
		log.Fatal("Failed to initialize database:", db_err)
	}

	// Create the filesystem watcher responsible for receiving live file events.
	watcher, watchErr := crawler.NewFileWatcher(db)
	if watchErr != nil {
		log.Fatal("Failed to initialize folder watcher:", watchErr)
	}
	// Ensure the underlying fsnotify watcher is closed when the process exits.
	defer watcher.Watcher.Close()

	// GarbageCollector performs periodic cleanup of stale missing records.
	gc := &crawler.GarbageCollector{DB: db}

	// RetryMachine periodically re-queues failed pending files.
	rm := &crawler.RetryMachine{DB: db, ProcessQueue: watcher.ProcessQueue}

	// FileWalker performs boot-time reconciliation between disk state and DB state.
	walker := &crawler.FileWalker{DB: db, Watch: watcher}

	// Initialize the embedder and searcher
	emb := embedder.NewEmbedder()  // however you initialize yours
	searcher := search.NewSearcher(db, emb)

	// Build the application controller that wires together the database,
	// watcher, walker, retry machine, and garbage collector.
	app := NewApp(db, watcher, walker, gc, rm, searcher)

	// Launch the Wails desktop shell with the embedded frontend and the runtime
	// options that control startup behavior and window presentation.
	err = wails.Run(&options.App{
		Title:  "drag",
		Width:  1024,
		Height: 768,

		// Serve the embedded frontend bundle directly from the binary so the UI
		// loads without needing a separate static file server.
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// Use a dark background so the desktop shell starts with a consistent
		// appearance even before the frontend finishes rendering.
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		// Start the app hidden because Drag is intended to live in the tray and
		// be shown only when the user explicitly opens it.
		StartHidden: true,
		// Run application startup initialization after the shell is ready.
		OnStartup: app.startup,
		// Hide the window instead of terminating so background services remain
		// available until the user quits from the tray menu.
		HideWindowOnClose: true,
		// Expose structs and methods to the frontend so the UI can call into Go code.
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
