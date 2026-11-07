package main

import (
	"embed"
	"fmt"
	"log"
	"os"

	"ehang.io/nps/lib/install"
	"ehang.io/nps/lib/version"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var trayIcon []byte

func main() {
	if plan := install.UpdatePlanArgument(os.Args[1:]); plan != "" {
		os.Exit(install.RunUpdateHelper(plan))
	}
	if len(os.Args) > 1 && os.Args[1] == "-version" {
		fmt.Printf("Version: %s\n", version.VERSION)
		return
	}
	app := application.New(application.Options{
		Name: "NPS 客户端",
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Linux: application.LinuxOptions{
			DisableQuitOnLastWindowClosed: true,
		},
	})
	if len(trayIcon) > 0 {
		app.SetIcon(trayIcon)
	}

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "NPS 客户端",
		Width:            1000,
		Height:           600,
		MinWidth:         1000,
		MinHeight:        600,
		BackgroundColour: application.NewRGB(27, 38, 54),
	})

	// 点击关闭按钮时隐藏窗口而不是退出（托盘应用常驻）
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if isQuitting() {
			return
		}
		event.Cancel()
		window.Hide()
	})

	app.RegisterService(application.NewService(NewApp(app, window)))

	if runErr := app.Run(); runErr != nil {
		log.Fatal(runErr)
	}
}
