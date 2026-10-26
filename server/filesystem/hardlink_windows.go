//go:build windows

package filesystem

import "os"

// hardLinkID always reports false on Windows, hard links are rare there and
// the file index is not available from the information returned by a stat.
func hardLinkID(info os.FileInfo) (uint64, bool) {
	return 0, false
}
