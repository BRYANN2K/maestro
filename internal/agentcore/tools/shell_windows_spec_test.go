package tools

import (
	"reflect"
	"strings"
	"testing"
)

func TestWindowsShellInvocation(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		comspec    string
		wantShell  string
		wantArgs   []string
	}{
		{
			name:      "stock Windows default",
			wantShell: "cmd.exe",
			wantArgs:  []string{"/D", "/S", "/C", "echo hello"},
		},
		{
			name:      "COMSPEC path",
			comspec:   `C:\Windows\System32\cmd.exe`,
			wantShell: `C:\Windows\System32\cmd.exe`,
			wantArgs:  []string{"/D", "/S", "/C", "echo hello"},
		},
		{
			name:       "explicit PowerShell path wins",
			configured: `C:\Program Files\PowerShell\7\pwsh.exe`,
			comspec:    `C:\Windows\System32\cmd.exe`,
			wantShell:  `C:\Program Files\PowerShell\7\pwsh.exe`,
			wantArgs:   []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "echo hello"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shell, args, err := windowsShellInvocation(test.configured, test.comspec, "echo hello")
			if err != nil {
				t.Fatal(err)
			}
			if shell != test.wantShell || !reflect.DeepEqual(args, test.wantArgs) {
				t.Fatalf("invocation = %q %#v, want %q %#v", shell, args, test.wantShell, test.wantArgs)
			}
		})
	}
}

func TestWindowsShellInvocationRejectsArgumentsAndUnknownShells(t *testing.T) {
	for _, shell := range []string{
		`cmd.exe /Q`,
		`C:\tools\busybox.exe`,
		`pwsh.exe -EncodedCommand`,
		"cmd.exe\x00ignored",
	} {
		_, _, err := windowsShellInvocation(shell, "", "echo hello")
		if err == nil {
			t.Fatalf("windowsShellInvocation(%q) returned no error", shell)
		}
		if strings.Contains(err.Error(), "echo hello") {
			t.Fatalf("error reflected command contents: %v", err)
		}
	}
}
