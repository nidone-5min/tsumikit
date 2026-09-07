package main

import (
	"embed"
	"runtime"
	"sync"
	"sync/atomic"

	"fyne.io/systray"
)

//go:embed build/appicon.png build/windows/icon.ico
var trayAssets embed.FS

type trayReadiness interface {
	Ready() bool
}

// TrayController integrates systray with Wails' existing native event loop.
// Prepare must be called from the main OS thread before wails.Run.
type TrayController struct {
	show     func()
	quit     func()
	ready    atomic.Bool
	stopped  atomic.Bool
	done     chan struct{}
	stopOnce sync.Once
	end      func()
}

func NewTrayController(show, quit func()) *TrayController {
	return &TrayController{show: show, quit: quit, done: make(chan struct{})}
}

func (t *TrayController) Prepare() func() {
	start, end := systray.RunWithExternalLoop(t.onReady, func() {
		t.ready.Store(false)
	})
	t.end = end
	start()
	return t.Stop
}

func (t *TrayController) Ready() bool {
	return t.ready.Load()
}

func (t *TrayController) Stop() {
	t.stopOnce.Do(func() {
		t.stopped.Store(true)
		t.ready.Store(false)
		close(t.done)
		if t.end != nil {
			t.end()
		}
	})
}

func (t *TrayController) onReady() {
	if t.stopped.Load() {
		return
	}
	icon, err := trayIcon()
	if err != nil {
		return
	}
	systray.SetIcon(icon)
	systray.SetTooltip("tsumikit")
	systray.SetOnTapped(func() { go t.show() })

	showItem := systray.AddMenuItem("tsumikitを表示", "tsumikitのウィンドウを表示します")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("終了", "tsumikitを終了します")

	go t.waitForClick(showItem.ClickedCh, t.show)
	go t.waitForClick(quitItem.ClickedCh, t.quit)
	if !t.stopped.Load() {
		t.ready.Store(true)
	}
}

func (t *TrayController) waitForClick(clicked <-chan struct{}, action func()) {
	for {
		select {
		case _, ok := <-clicked:
			if !ok {
				return
			}
			action()
		case <-t.done:
			return
		}
	}
}

func trayIcon() ([]byte, error) {
	filename := "build/appicon.png"
	if runtime.GOOS == "windows" {
		filename = "build/windows/icon.ico"
	}
	return trayAssets.ReadFile(filename)
}
