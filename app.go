package main

import (
	"context"
	"fmt"
)

const defaultOverlayPort = 18500

// App is the backend exposed to the Wails frontend.
type App struct {
	ctx           context.Context
	overlayServer *OverlayServer
}

// NewApp creates the application backend.
func NewApp() *App {
	return &App{overlayServer: NewOverlayServer()}
}

// startup stores the Wails context for future application services.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if err := a.overlayServer.Start(defaultOverlayPort); err != nil {
		a.overlayServer.setLastError(err)
	}
}

// shutdown releases resources owned by the application.
func (a *App) shutdown(ctx context.Context) {
	if err := a.overlayServer.Stop(ctx); err != nil {
		a.overlayServer.setLastError(err)
	}
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
