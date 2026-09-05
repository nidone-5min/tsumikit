package main

import "context"

// App is the backend exposed to the Wails frontend.
type App struct {
	ctx context.Context
}

// NewApp creates the application backend.
func NewApp() *App {
	return &App{}
}

// startup stores the Wails context for future application services.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}
