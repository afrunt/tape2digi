package main

import (
	"embed"
	"os"
	"path/filepath"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/afrunt/tape2digi/internal/appdata"
)

//go:embed all:frontend/dist
var assets embed.FS

// The window icon on Linux; macOS and Windows take it from the app bundle.
//
//go:embed build/appicon.png
var icon []byte

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	app := NewApp()

	width, height := 1100, 760
	if w := app.settings.Load().Window; w != nil {
		width, height = w.W, w.H
	}
	opts := &options.App{
		Title:     "tape2digi",
		Width:     width,
		Height:    height,
		MinWidth:  860,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: coverHandler{app},
		},
		// Islands Dark window gap colour (gray-30).
		BackgroundColour: &options.RGBA{R: 0x26, G: 0x28, B: 0x2C, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		Menu:             app.buildMenu(),
		Bind:             []interface{}{app},
		// Setting Linux options replaces Wails' defaults, so the GPU policy it
		// would otherwise pick is kept explicitly.
		Linux: &linux.Options{Icon: icon, WebviewGpuPolicy: linux.WebviewGpuPolicyNever},
	}
	if goruntime.GOOS == "windows" {
		if dir, err := appdata.Default(); err == nil {
			opts.Windows = &windows.Options{WebviewUserDataPath: filepath.Join(dir.Path, "webview")}
			_ = os.MkdirAll(dir.Path, 0o700)
		}
	}

	if err := wails.Run(opts); err != nil {
		println("Error:", err.Error())
	}
}
