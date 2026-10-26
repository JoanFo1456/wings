//go:build windows

package config

import (
	"os"
	"path/filepath"
)

// programData returns the directory Wings keeps its configuration and data in.
func programData() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "Pelican")
}

func defaultLocation() string {
	return filepath.Join(programData(), "config.yml")
}

// applyPlatformDefaults replaces the Unix defaults declared on the structs with
// their Windows equivalents, everything lives below %ProgramData%\Pelican.
func applyPlatformDefaults(c *Configuration) {
	root := programData()
	c.System.RootDirectory = root
	c.System.LogDirectory = filepath.Join(root, "logs")
	c.System.Data = filepath.Join(root, "volumes")
	c.System.ArchiveDirectory = filepath.Join(root, "archives")
	c.System.BackupDirectory = filepath.Join(root, "backups")
	c.System.TmpDirectory = filepath.Join(root, "tmp")
	c.System.User.Passwd.Directory = filepath.Join(root, "passwd")
	c.System.MachineID.Directory = filepath.Join(root, "machine-id")
}
