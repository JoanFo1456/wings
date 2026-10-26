package server

import (
	"path"
	"strings"

	"github.com/pelican/wings/environment"
)

const (
	linuxInstallScript   = "install.sh"
	windowsInstallScript = "install.ps1"
)

// installScriptName returns the name the installation script is saved under. A
// PowerShell script has to end in .ps1 to be run as a script file.
func installScriptName(windows bool) string {
	if windows {
		return windowsInstallScript
	}
	return linuxInstallScript
}

// installScriptContent returns the bytes of the installation script file. Linux
// shells fail on carriage returns, so they are removed. Windows PowerShell reads a
// script without a byte order mark in the ANSI code page and garbles any other
// character, so Windows scripts are saved as UTF-8 with a byte order mark.
func installScriptContent(windows bool, script string) []byte {
	if windows {
		return append([]byte("\xef\xbb\xbf"), script...)
	}
	return []byte(strings.ReplaceAll(script, "\r\n", "\n"))
}

// installCommand returns the command that runs the installation script in the
// installation container. The script is mounted in /mnt/install, which on the
// Windows engine is C:\mnt\install.
func installCommand(windows bool, entrypoint string) []string {
	script := "/mnt/install/" + installScriptName(windows)
	if !windows {
		return []string{entrypoint, script}
	}

	script = environment.WindowsContainerPath(script)
	switch strings.TrimSuffix(strings.ToLower(path.Base(strings.ReplaceAll(entrypoint, `\`, "/"))), ".exe") {
	case "powershell", "pwsh":
		// Scripts of an egg are administrator controlled and the container is thrown away
		// afterwards, so the execution policy of the image must not get in the way.
		return []string{entrypoint, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script}
	default:
		return []string{entrypoint, script}
	}
}
