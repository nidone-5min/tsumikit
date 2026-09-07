package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

const defaultOverlayPort = 18500

// App is the backend exposed to the Wails frontend.
type App struct {
	lifecycleMu   sync.Mutex
	ctx           context.Context
	overlayServer *OverlayServer
	lifecycle     desktopLifecycle
	tray          trayReadiness
	stopSignals   context.CancelFunc
	quitPrompting bool
	quitting      bool
}

// NewApp creates the application backend.
func NewApp() *App {
	return &App{
		overlayServer: NewOverlayServer(),
		lifecycle:     wailsDesktopLifecycle{},
	}
}

// startup stores the Wails context for future application services.
func (a *App) startup(ctx context.Context) {
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	a.lifecycleMu.Lock()
	a.ctx = ctx
	a.stopSignals = stopSignals
	a.lifecycleMu.Unlock()
	go func() {
		<-signalCtx.Done()
		a.forceQuit()
	}()
	if err := a.overlayServer.Start(defaultOverlayPort); err != nil {
		a.overlayServer.setLastError(err)
	}
}

// shutdown releases resources owned by the application.
func (a *App) shutdown(ctx context.Context) {
	a.lifecycleMu.Lock()
	a.quitting = true
	a.ctx = nil
	stopSignals := a.stopSignals
	a.stopSignals = nil
	a.lifecycleMu.Unlock()
	if stopSignals != nil {
		stopSignals()
	}

	if err := a.overlayServer.Stop(ctx); err != nil {
		a.overlayServer.setLastError(err)
	}
}

func (a *App) setTray(tray trayReadiness) {
	a.lifecycleMu.Lock()
	a.tray = tray
	a.lifecycleMu.Unlock()
}

// GetOverlayStatus reports the current local overlay server state.
func (a *App) GetOverlayStatus() OverlayStatus {
	return a.overlayServer.Status()
}

// StartOverlayServer starts the local overlay server on port.
func (a *App) StartOverlayServer(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("ポート番号は1024から65535の範囲で指定してください")
	}
	return a.overlayServer.Start(port)
}

// StopOverlayServer stops the local overlay server.
func (a *App) StopOverlayServer() error {
	return a.overlayServer.Stop(context.Background())
}

// SendTestEvent broadcasts a test message to connected Browser Sources.
func (a *App) SendTestEvent(message string) error {
	return a.overlayServer.SendTestEvent(message)
}
