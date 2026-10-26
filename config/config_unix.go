//go:build !windows

package config

func defaultLocation() string {
	return "/etc/pelican/config.yml"
}

// applyPlatformDefaults adjusts the defaults for the platform, the defaults
// declared on the structs are the ones for Unix systems.
func applyPlatformDefaults(c *Configuration) {}
