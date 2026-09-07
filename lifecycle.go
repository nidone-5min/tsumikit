package main

import (
	"context"
	"fmt"

	"github.com/wailsapp/wails/v2/pkg/options"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const singleInstanceID = "d5793e0a-15e5-4d0e-942a-3b3979efbc7e"

type desktopLifecycle interface {
	Hide(context.Context)
	Show(context.Context)
	Quit(context.Context)
	ConfirmQuit(context.Context) (bool, error)
}

type wailsDesktopLifecycle struct{}

func (wailsDesktopLifecycle) Hide(ctx context.Context) {
	wailsruntime.Hide(ctx)
}

func (wailsDesktopLifecycle) Show(ctx context.Context) {
	wailsruntime.WindowUnminimise(ctx)
	wailsruntime.Show(ctx)
}

func (wailsDesktopLifecycle) Quit(ctx context.Context) {
	wailsruntime.Quit(ctx)
}

func (wailsDesktopLifecycle) ConfirmQuit(ctx context.Context) (bool, error) {
	const quitButton = "終了"
	result, err := wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{
		Type:          wailsruntime.QuestionDialog,
		Title:         "tsumikitを終了しますか？",
		Message:       "OBS Browser Sourceが接続中です。終了するとオーバーレイとの接続が切断されます。",
		Buttons:       []string{quitButton, "キャンセル"},
		DefaultButton: "キャンセル",
		CancelButton:  "キャンセル",
	})
	if err != nil {
		return false, err
	}
	return result == quitButton, nil
}

// beforeClose keeps the process resident only after the tray is ready. If tray
// initialisation failed, closing the window remains a reliable way to exit.
func (a *App) beforeClose(ctx context.Context) bool {
	a.lifecycleMu.Lock()
	quitting := a.quitting
	tray := a.tray
	a.lifecycleMu.Unlock()

	if quitting || tray == nil || !tray.Ready() {
		return false
	}
	a.lifecycle.Hide(ctx)
	return true
}

// showWindow is used by both the tray and the trusted single-instance signal.
// No arguments or working directory supplied by the second process are used.
func (a *App) showWindow() {
	a.lifecycleMu.Lock()
	ctx := a.ctx
	quitting := a.quitting
	a.lifecycleMu.Unlock()
	if ctx == nil || quitting {
		return
	}
	a.lifecycle.Show(ctx)
}

func (a *App) onSecondInstanceLaunch(_ options.SecondInstanceData) {
	a.showWindow()
}

func (a *App) requestQuit() {
	a.lifecycleMu.Lock()
	if a.ctx == nil || a.quitting || a.quitPrompting {
		a.lifecycleMu.Unlock()
		return
	}
	ctx := a.ctx
	a.quitPrompting = true
	a.lifecycleMu.Unlock()

	confirmed := true
	if a.overlayServer.Status().Connections > 0 {
		var err error
		confirmed, err = a.lifecycle.ConfirmQuit(ctx)
		if err != nil {
			confirmed = false
			a.overlayServer.setLastError(fmt.Errorf("終了確認を表示できませんでした: %w", err))
		}
	}

	a.lifecycleMu.Lock()
	a.quitPrompting = false
	if confirmed && a.ctx != nil && !a.quitting {
		a.quitting = true
		ctx = a.ctx
	} else {
		confirmed = false
	}
	a.lifecycleMu.Unlock()
	if confirmed {
		a.lifecycle.Quit(ctx)
	}
}

func (a *App) forceQuit() {
	a.lifecycleMu.Lock()
	if a.ctx == nil || a.quitting {
		a.lifecycleMu.Unlock()
		return
	}
	a.quitting = true
	ctx := a.ctx
	a.lifecycleMu.Unlock()
	a.lifecycle.Quit(ctx)
}
