//go:build windows

package config

// UseOpenat2 always returns false on Windows as the openat2 syscall is
// Linux-specific (kernel 5.6+).
func UseOpenat2() bool {
	return false
}
