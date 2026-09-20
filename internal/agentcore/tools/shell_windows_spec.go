package tools

import (
	"fmt"
	"path"
	"strings"
)

// windowsShellInvocation resolves the Windows shell without parsing a
// shell-shaped configuration string. MAESTRO_SHELL may name or point to a
// supported executable, but cannot smuggle extra command-line arguments.
// COMSPEC is the platform default; stock Windows falls back to cmd.exe.
func windowsShellInvocation(configured, comspec, command string) (string, []string, error) {
	shell := strings.TrimSpace(configured)
	if shell == "" {
		shell = strings.TrimSpace(comspec)
	}
	if shell == "" {
		shell = "cmd.exe"
	}
	if strings.ContainsRune(shell, '\x00') {
		return "", nil, fmt.Errorf("MAESTRO_SHELL contains a NUL byte")
	}

	// path.Base is used after normalizing separators so the resolver behaves
	// identically in host tests and in Windows builds.
	base := strings.ToLower(path.Base(strings.ReplaceAll(shell, `\`, "/")))
	switch base {
	case "cmd", "cmd.exe":
		return shell, []string{"/D", "/S", "/C", command}, nil
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return shell, []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command}, nil
	default:
		return "", nil, fmt.Errorf(
			"unsupported Windows shell %q; MAESTRO_SHELL must name cmd.exe, powershell.exe, or pwsh.exe",
			shell,
		)
	}
}
