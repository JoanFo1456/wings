package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBinaryNameFor(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"linux", "amd64", "wings_linux_amd64"},
		{"linux", "arm64", "wings_linux_arm64"},
		{"windows", "amd64", "wings_windows_amd64.exe"},
		{"windows", "arm64", "wings_windows_arm64.exe"},
		{"windows", "386", ""},
	}
	for _, c := range cases {
		if got := binaryNameFor(c.goos, c.goarch); got != c.want {
			t.Errorf("binaryNameFor(%s, %s) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestVerifyChecksumWindowsAsset(t *testing.T) {
	dir := t.TempDir()
	content := []byte("not really an executable")
	bin := filepath.Join(dir, "wings_windows_amd64.exe")
	if err := os.WriteFile(bin, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(content))
	sums := fmt.Sprintf("%s  wings_linux_amd64\n%s  wings_windows_amd64.exe\n%s  wings_windows_arm64.exe\n",
		"0000000000000000000000000000000000000000000000000000000000000000", sum,
		"1111111111111111111111111111111111111111111111111111111111111111")
	sumsPath := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(sumsPath, []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := verifyChecksum(bin, sumsPath, "wings_windows_amd64.exe"); err != nil {
		t.Errorf("valid checksum rejected: %v", err)
	}
	if err := verifyChecksum(bin, sumsPath, "wings_windows_arm64.exe"); err == nil {
		t.Error("a binary with the wrong checksum was accepted")
	}
}
