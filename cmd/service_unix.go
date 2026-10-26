//go:build !windows

package cmd

// runAsWindowsService is only needed on Windows, where Wings can run as a
// service. It never takes over on other platforms.
func runAsWindowsService(func() error) bool { return false }
