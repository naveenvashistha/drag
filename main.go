package main

import (
	"drag/pkg/db"
	"drag/pkg/system"
	"embed"
	"log"
	"drag/pkg/crawler"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// embed the frontend/dist directory into the binary itself. This allows us to serve the frontend assets directly from memory without needing to read from disk, which can simplify deployment and improve performance.
//go:embed all:frontend/dist
var assets embed.FS

func main() {

	// Enable autostart for the application
	system.EnableAutoStart()

	// Initialize the database and get a connection pool. We can pass this db connection pool
	db, db_err := db.InitDB("D:/Naveen/drag/drag/pkg/db")

	if db_err != nil {
		log.Fatal("Failed to initialize database:", db_err)
	}
	defer db.Close() // Ensure the database connection is closed when the main function exits

	watcher, watchErr := crawler.NewFileWatcher(db)
	if watchErr != nil {
		log.Fatal("Failed to initialize folder watcher:", watchErr)
	}
	defer watcher.Watcher.Close()

	gc := crawler.GarbageCollector{DB: db}
	
	rm := crawler.RetryMachine{DB: db, ProcessQueue: watcher.ProcessQueue}

	walker := crawler.FileWalker{DB: db, Watch: watcher}

	// Create an instance of the app structure
	app := NewApp(db, watcher, &walker, &gc, &rm)

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "drag", // The title of the application
		Width:  1024, // The initial width of the application window
		Height: 768, // The initial height of the application window

		// The AssetServer option tells Wails to serve the embedded frontend assets using the provided embed.FS. This allows us to load our React frontend directly from the binary without needing to read from disk.
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1}, // A nice dark blue background color for our app
		StartHidden: true, // Start the app hidden since we want it to run in the system tray and only show the window when the user clicks "Open Drag" from the tray menu
		OnStartup: app.startup, // The startup function will be called when the app starts, 
		HideWindowOnClose: true, // When the user clicks the close button on the window, we want to hide it instead of quitting the app, since we want it to keep running in the system tray. The user can quit the app from the tray menu.
		// Bind the app struct to the Wails runtime, which allows us to call methods on our App struct from the frontend (like showing/hiding the window when the user clicks tray menu items). We can add more structs to this Bind array later as we build out more functionality and want to expose more methods to the frontend.
		Bind: []interface{}{ 
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
