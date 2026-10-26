//go:build unix

package system

import (
	"github.com/shirou/gopsutil/v3/disk"
	"golang.org/x/sys/unix"
)

// getDiskForPath finds the mountpoint where the given path is stored
func getDiskForPath(path string, partitions []disk.PartitionStat) (string, string, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return "", "", err
	}

	for _, part := range partitions {
		var pStat unix.Statfs_t
		if err := unix.Statfs(part.Mountpoint, &pStat); err != nil {
			continue
		}
		if stat.Fsid == pStat.Fsid {
			return part.Device, part.Mountpoint, nil
		}
	}

	return "", "", nil // No error, but couldn't find the disk
}
