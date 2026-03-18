package system

import (
    "log"
    "os"
    "github.com/emersion/go-autostart"
)

// this package puts our .exe shortcut in the startup folder on windows
// but when we use "wails dev" command, wails create a temporary .exe which gets deleted after we stop the dev server
// and since the shortcut points to that temporary .exe and isEnable() only looks at the name of file in startup folder (which is still there), it becomes a broken shortcut and does nothing on startup
// thats why we are not checking if its already enabled, and just enabling it every time which will update the shortcut to point to the new .exe created by wails on every dev run. This is not ideal but works for now. We can improve this later by different ways

// EnableAutoStart registers the app to run on boot
func EnableAutoStart() {
    // 1. Find where our .exe is currently located on the hard drive
    executablePath, err := os.Executable()
    if err != nil {
        log.Println("Could not find executable path:", err)
        return
    }

    // 2. Define the Autostart entry
    app := &autostart.App{
        Name:        "Drag",
        DisplayName: "Drag",
        Exec:        []string{executablePath},
    }

    // Dont use it right now
    // 3. Check if it's already enabled to avoid redundant OS writes
    // if app.IsEnabled() {
    //     log.Println("Autostart is already enabled.")
    //     return
    // }

    // 4. Tell the OS to run it on boot
    log.Println("Enabling Autostart...")
    if err := app.Enable(); err != nil {
        log.Println("Failed to enable autostart:", err)
    }
}