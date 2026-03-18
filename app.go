package main

import (
	"context"
	_ "embed"
	"github.com/getlantern/systray"
    "github.com/wailsapp/wails/v2/pkg/runtime"
)

// this directive puts the icon file as a byte slice in iconBytes variable. This allows us to set the system tray icon without needing to read from disk at runtime.
//go:embed frontend/src/assets/images/letter-d.ico
var iconBytes []byte

// App struct holds the context and any other state we want to maintain across our app's lifecycle
type App struct {
    // ctx is the Wails application context, which allows us to interact with the runtime (like showing/hiding windows, quitting the app, etc.) from anywhere in our App struct.
	ctx context.Context
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Start the system tray in a separate goroutine so it doesn't block the main thread as it starts its own infinite loop to keep the tray alive. The OnReady and OnExit methods will be called by the systray library when appropriate.
	go systray.Run(a.OnReady, a.OnExit)
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

// onExit runs right before the app finally dies
func (a *App) OnExit() {
    // Clean up your database connections and network ports here later!
}
