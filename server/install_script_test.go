package server

import (
	"reflect"
	"testing"
)

func TestInstallScriptName(t *testing.T) {
	if got := installScriptName(false); got != "install.sh" {
		t.Errorf("linux script = %q", got)
	}
	if got := installScriptName(true); got != "install.ps1" {
		t.Errorf("windows script = %q", got)
	}
}

func TestInstallScriptContent(t *testing.T) {
	// Linux shells fail on carriage returns, they are removed as before.
	if got := string(installScriptContent(false, "echo a\r\necho b\r\n")); got != "echo a\necho b\n" {
		t.Errorf("linux content = %q", got)
	}

	// Windows PowerShell needs a byte order mark to read non-ASCII characters as UTF-8,
	// the script itself is left untouched.
	got := installScriptContent(true, "Write-Output 'é'\r\n")
	if want := "\xef\xbb\xbfWrite-Output 'é'\r\n"; string(got) != want {
		t.Errorf("windows content = %q, want %q", got, want)
	}
}

func TestInstallCommand(t *testing.T) {
	cases := []struct {
		name       string
		windows    bool
		entrypoint string
		want       []string
	}{
		{"linux", false, "bash", []string{"bash", "/mnt/install/install.sh"}},
		{"linux ash", false, "ash", []string{"ash", "/mnt/install/install.sh"}},
		{
			"windows powershell", true, "powershell.exe",
			[]string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\mnt\install\install.ps1`},
		},
		{
			"windows powershell without extension", true, "powershell",
			[]string{"powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\mnt\install\install.ps1`},
		},
		{
			"windows powershell 7 with a path", true, `C:\Program Files\PowerShell\7\pwsh.exe`,
			[]string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\mnt\install\install.ps1`},
		},
		// Any other entrypoint simply gets the script as its argument.
		{"windows other", true, "cmd.exe", []string{"cmd.exe", `C:\mnt\install\install.ps1`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := installCommand(c.windows, c.entrypoint); !reflect.DeepEqual(got, c.want) {
				t.Errorf("installCommand = %q, want %q", got, c.want)
			}
		})
	}
}
