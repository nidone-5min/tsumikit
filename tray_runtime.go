//go:build !bindings

package main

func prepareApplicationTray(app *App) func() {
	tray := NewTrayController(app.showWindow, app.requestQuit)
	app.setTray(tray)
	return tray.Prepare()
}
