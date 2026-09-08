//go:build bindings

package main

// Binding generation must reach wails.Run without starting a native UI loop.
func prepareApplicationTray(*App) func() {
	return func() {}
}
