package main

import (
	"embed"
	"log"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var trayIcon []byte

func init() {
	application.RegisterEvent[map[string]any]("im:event")
	application.RegisterEvent[any]("im:connection")
}

func main() {
	notifier := notifications.New()
	chat := NewChatService(notifier)

	app := application.New(application.Options{
		Name:        "rym",
		Description: "rym desktop client",
		Services: []application.Service{
			application.NewService(chat),
			application.NewService(notifier),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
		},
		Linux: application.LinuxOptions{
			DisableQuitOnLastWindowClosed: true,
			ProgramName:                   "rym",
		},
	})

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "rym",
		Name:   "main",
		Width:  1180,
		Height: 760,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(18, 20, 28),
		URL:              "/",
	})

	var quitting atomic.Bool

	// 关闭窗口 = 收进托盘，不退出进程；托盘「退出」时不再拦截。
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if quitting.Load() {
			return
		}
		e.Cancel()
		window.Hide()
	})

	// 系统托盘：点击切换显示/隐藏，菜单提供显示、隐藏、退出。
	tray := app.SystemTray.New()
	tray.SetIcon(trayIcon)
	tray.SetTooltip("rym")
	menu := application.NewMenu()
	menu.Add("显示 rym").OnClick(func(*application.Context) {
		window.Show()
		window.Focus()
	})
	menu.Add("隐藏到托盘").OnClick(func(*application.Context) {
		window.Hide()
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		quitting.Store(true)
		app.Quit()
	})
	tray.SetMenu(menu)
	tray.OnClick(func() {
		if window.IsVisible() {
			window.Hide()
			return
		}
		window.Show()
		window.Focus()
	})
	tray.OnRightClick(func() {
		tray.OpenMenu()
	})

	// 点击通知回到主窗口。
	notifier.OnNotificationResponse(func(_ notifications.NotificationResult) {
		window.Show()
		window.Focus()
	})
	// macOS 需要授权；Windows/Linux 为 no-op。
	_, _ = notifier.RequestNotificationAuthorization()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
