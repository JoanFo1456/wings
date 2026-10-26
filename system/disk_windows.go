//go:build windows

package system

import (
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v3/disk"
)

// getDiskForPath finds the volume where the given path is stored
func getDiskForPath(path string, partitions []disk.PartitionStat) (string, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	vol := strings.TrimRight(filepath.VolumeName(abs), `\/`)

	for _, part := range partitions {
		if strings.EqualFold(strings.TrimRight(part.Mountpoint, `\/`), vol) {
			return part.Device, part.Mountpoint, nil
		}
	}

	return "", "", nil // No error, but couldn't find the disk
}
