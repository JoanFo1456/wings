//go:build !windows

package cmd

// selectDockerEngine is only needed on Windows, where Docker Desktop provides
// separate Windows and Linux engines.
func selectDockerEngine() {}
