package main

import "testing"

func TestNewApp(t *testing.T) {
	t.Parallel()

	if app := NewApp(); app == nil {
		t.Fatal("NewApp() returned nil")
	}
}
