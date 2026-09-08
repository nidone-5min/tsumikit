package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const defaultOverlayPort = 18500

// App is the backend exposed to the Wails frontend.
type App struct {
	lifecycleMu   sync.Mutex
	ctx           context.Context
	overlayServer *OverlayServer
	youtube       *YouTubeService
	twitch        *TwitchService
	lifecycle     desktopLifecycle
	tray          trayReadiness
	stopSignals   context.CancelFunc
	quitPrompting bool
	quitting      bool
}

// NewApp creates the application backend.
func NewApp() *App {
	app := &App{
		overlayServer: NewOverlayServer(),
		lifecycle:     wailsDesktopLifecycle{},
	}
	app.youtube = newYouTubeService(youtubeServiceDependencies{
		openURL: func(ctx context.Context, url string) {
			wailsruntime.BrowserOpenURL(ctx, url)
		},
	})
	app.twitch = newTwitchService(twitchServiceDependencies{
		openURL: func(ctx context.Context, url string) {
			wailsruntime.BrowserOpenURL(ctx, url)
		},
	})
	return app
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
	a.youtube.Shutdown()
	a.twitch.Shutdown()

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

// GetYouTubeStatus reports the in-memory OAuth and live chat connection state.
func (a *App) GetYouTubeStatus() YouTubeStatus {
	return a.youtube.Status()
}

// BeginYouTubeAuth starts Google Desktop OAuth in the system browser.
func (a *App) BeginYouTubeAuth(clientID string) error {
	a.lifecycleMu.Lock()
	ctx := a.ctx
	a.lifecycleMu.Unlock()
	if ctx == nil {
		return fmt.Errorf("アプリの起動完了後にGoogle認証を開始してください")
	}
	return a.youtube.BeginAuth(ctx, clientID)
}

// ConnectYouTube resolves the active live chat and starts streamList reception.
func (a *App) ConnectYouTube(streamURL string) error {
	a.lifecycleMu.Lock()
	ctx := a.ctx
	a.lifecycleMu.Unlock()
	if ctx == nil {
		return fmt.Errorf("アプリの起動完了後にYouTubeへ接続してください")
	}
	return a.youtube.Connect(ctx, streamURL)
}

// DisconnectYouTube stops chat reception while preserving the session token.
func (a *App) DisconnectYouTube() {
	a.youtube.Disconnect()
}

// SignOutYouTube clears the session-only OAuth token and stops reception.
func (a *App) SignOutYouTube() {
	a.youtube.SignOut()
}

// GetTwitchStatus reports the in-memory Device Code and EventSub state.
func (a *App) GetTwitchStatus() TwitchStatus {
	return a.twitch.Status()
}

// BeginTwitchAuth starts Twitch Device Code Grant in the system browser.
func (a *App) BeginTwitchAuth(clientID string) error {
	a.lifecycleMu.Lock()
	ctx := a.ctx
	a.lifecycleMu.Unlock()
	if ctx == nil {
		return fmt.Errorf("アプリの起動完了後にTwitch認証を開始してください")
	}
	return a.twitch.BeginAuth(ctx, clientID)
}

// ConnectTwitch resolves a channel URL and starts EventSub reception.
func (a *App) ConnectTwitch(streamURL string) error {
	a.lifecycleMu.Lock()
	ctx := a.ctx
	a.lifecycleMu.Unlock()
	if ctx == nil {
		return fmt.Errorf("アプリの起動完了後にTwitchへ接続してください")
	}
	return a.twitch.Connect(ctx, streamURL)
}

// DisconnectTwitch stops EventSub while preserving the session token.
func (a *App) DisconnectTwitch() {
	a.twitch.Disconnect()
}

// SignOutTwitch clears the session-only OAuth token and stops EventSub.
func (a *App) SignOutTwitch() {
	a.twitch.SignOut()
}
