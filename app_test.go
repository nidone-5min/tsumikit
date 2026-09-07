package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options"
)

type fakeTray struct {
	ready bool
}

func (t fakeTray) Ready() bool {
	return t.ready
}

type fakeDesktopLifecycle struct {
	mu           sync.Mutex
	hideCalls    int
	showCalls    int
	quitCalls    int
	confirmCalls int
	confirm      bool
	confirmErr   error
}

type blockingDesktopLifecycle struct {
	fakeDesktopLifecycle
	entered chan struct{}
	release chan struct{}
}

func (f *blockingDesktopLifecycle) ConfirmQuit(context.Context) (bool, error) {
	f.mu.Lock()
	f.confirmCalls++
	f.mu.Unlock()
	close(f.entered)
	<-f.release
	return false, nil
}

func (f *fakeDesktopLifecycle) Hide(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hideCalls++
}

func (f *fakeDesktopLifecycle) Show(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.showCalls++
}

func (f *fakeDesktopLifecycle) Quit(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quitCalls++
}

func (f *fakeDesktopLifecycle) ConfirmQuit(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmCalls++
	return f.confirm, f.confirmErr
}

func newLifecycleTestApp(lifecycle desktopLifecycle) *App {
	app := NewApp()
	app.ctx = context.Background()
	app.lifecycle = lifecycle
	return app
}

func TestNewApp(t *testing.T) {
	t.Parallel()

	if app := NewApp(); app == nil {
		t.Fatal("NewApp() returned nil")
	}
}

func TestStartOverlayServerRejectsUnsafePorts(t *testing.T) {
	t.Parallel()

	app := NewApp()
	for _, port := range []int{-1, 0, 80, 65536} {
		if err := app.StartOverlayServer(port); err == nil {
			t.Errorf("StartOverlayServer(%d) returned nil error", port)
		}
	}
}

func TestBeforeCloseHidesWindowWhenTrayIsReady(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeDesktopLifecycle{}
	app := newLifecycleTestApp(lifecycle)
	app.setTray(fakeTray{ready: true})

	if prevent := app.beforeClose(context.Background()); !prevent {
		t.Fatal("beforeClose() did not prevent close with a ready tray")
	}
	if lifecycle.hideCalls != 1 {
		t.Fatalf("Hide() calls = %d, want 1", lifecycle.hideCalls)
	}
}

func TestBeforeCloseExitsWhenTrayIsUnavailable(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeDesktopLifecycle{}
	app := newLifecycleTestApp(lifecycle)
	app.setTray(fakeTray{ready: false})

	if prevent := app.beforeClose(context.Background()); prevent {
		t.Fatal("beforeClose() prevented close without a ready tray")
	}
	if lifecycle.hideCalls != 0 {
		t.Fatalf("Hide() calls = %d, want 0", lifecycle.hideCalls)
	}
}

func TestSecondInstanceShowsExistingWindowAndDiscardsInput(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeDesktopLifecycle{}
	app := newLifecycleTestApp(lifecycle)
	app.onSecondInstanceLaunch(options.SecondInstanceData{
		Args:             []string{"--untrusted", "../../secret"},
		WorkingDirectory: "/untrusted/path",
	})

	if lifecycle.showCalls != 1 {
		t.Fatalf("Show() calls = %d, want 1", lifecycle.showCalls)
	}
}

func TestRequestQuitWithoutConnectionsQuitsImmediately(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeDesktopLifecycle{}
	app := newLifecycleTestApp(lifecycle)
	app.requestQuit()

	if lifecycle.confirmCalls != 0 {
		t.Fatalf("ConfirmQuit() calls = %d, want 0", lifecycle.confirmCalls)
	}
	if lifecycle.quitCalls != 1 {
		t.Fatalf("Quit() calls = %d, want 1", lifecycle.quitCalls)
	}
	if prevent := app.beforeClose(context.Background()); prevent {
		t.Fatal("beforeClose() prevented an explicit quit")
	}
}

func TestRequestQuitWithConnectionCanBeCancelled(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeDesktopLifecycle{confirm: false}
	app := newLifecycleTestApp(lifecycle)
	app.overlayServer.clients[&overlayClient{}] = struct{}{}
	app.requestQuit()

	if lifecycle.confirmCalls != 1 {
		t.Fatalf("ConfirmQuit() calls = %d, want 1", lifecycle.confirmCalls)
	}
	if lifecycle.quitCalls != 0 {
		t.Fatalf("Quit() calls = %d, want 0", lifecycle.quitCalls)
	}
	if app.quitting {
		t.Fatal("requestQuit() marked app as quitting after cancellation")
	}
}

func TestRequestQuitWithConnectionCanBeConfirmed(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeDesktopLifecycle{confirm: true}
	app := newLifecycleTestApp(lifecycle)
	app.overlayServer.clients[&overlayClient{}] = struct{}{}
	app.requestQuit()

	if lifecycle.confirmCalls != 1 {
		t.Fatalf("ConfirmQuit() calls = %d, want 1", lifecycle.confirmCalls)
	}
	if lifecycle.quitCalls != 1 {
		t.Fatalf("Quit() calls = %d, want 1", lifecycle.quitCalls)
	}
	if !app.quitting {
		t.Fatal("requestQuit() did not mark app as quitting")
	}
}

func TestRequestQuitKeepsRunningWhenConfirmationFails(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeDesktopLifecycle{confirm: true, confirmErr: errors.New("dialog unavailable")}
	app := newLifecycleTestApp(lifecycle)
	app.overlayServer.clients[&overlayClient{}] = struct{}{}
	app.requestQuit()

	if lifecycle.quitCalls != 0 {
		t.Fatalf("Quit() calls = %d, want 0", lifecycle.quitCalls)
	}
	if got := app.GetOverlayStatus().Error; !strings.Contains(got, "終了確認を表示できませんでした") {
		t.Fatalf("status error = %q, want a safe confirmation error", got)
	}
}

func TestRequestQuitSerialisesConfirmationDialogs(t *testing.T) {
	t.Parallel()

	lifecycle := &blockingDesktopLifecycle{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	app := newLifecycleTestApp(lifecycle)
	app.overlayServer.clients[&overlayClient{}] = struct{}{}
	done := make(chan struct{})
	go func() {
		app.requestQuit()
		close(done)
	}()
	<-lifecycle.entered

	app.requestQuit()
	close(lifecycle.release)
	<-done

	if lifecycle.confirmCalls != 1 {
		t.Fatalf("ConfirmQuit() calls = %d, want 1", lifecycle.confirmCalls)
	}
}

func TestForceQuitBypassesConnectionConfirmation(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeDesktopLifecycle{}
	app := newLifecycleTestApp(lifecycle)
	app.overlayServer.clients[&overlayClient{}] = struct{}{}
	app.forceQuit()

	if lifecycle.confirmCalls != 0 {
		t.Fatalf("ConfirmQuit() calls = %d, want 0", lifecycle.confirmCalls)
	}
	if lifecycle.quitCalls != 1 {
		t.Fatalf("Quit() calls = %d, want 1", lifecycle.quitCalls)
	}
	if prevent := app.beforeClose(context.Background()); prevent {
		t.Fatal("beforeClose() prevented a signal-triggered quit")
	}
}

func TestTrayIconIsEmbedded(t *testing.T) {
	t.Parallel()

	icon, err := trayIcon()
	if err != nil {
		t.Fatalf("trayIcon(): %v", err)
	}
	if len(icon) == 0 {
		t.Fatal("trayIcon() returned empty data")
	}
}
