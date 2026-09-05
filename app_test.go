package main

import "testing"

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
