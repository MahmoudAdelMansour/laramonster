package core

import (
	"fmt"
	"laramonster/internal/proc"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// userPath reads the user PATH from the registry (what new terminals get),
// without starting PowerShell.
func userPath() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("Path")
	return v, err
}

// replaceInUserPath swaps the directory old for new in the user PATH, if
// it is there. .NET writes it and tells running programs (Explorer, new
// terminals) the same way AddToPath does.
func replaceInUserPath(old, new string) error {
	p, err := userPath()
	if err != nil || !slices.ContainsFunc(filepath.SplitList(p), func(d string) bool { return strings.EqualFold(filepath.Clean(d), filepath.Clean(old)) }) {
		return nil
	}
	script := "$p=[Environment]::GetEnvironmentVariable('Path','User'); $n=($p -split ';' | ForEach-Object { if ($_.TrimEnd('\\') -ieq $env:LM_OLD) { $env:LM_NEW } else { $_ } }) -join ';'; [Environment]::SetEnvironmentVariable('Path', $n, 'User')"
	cmd := proc.Command("powershell", "-NoProfile", "-Command", script)
	cmd.Env = append(os.Environ(), "LM_OLD="+filepath.Clean(old), "LM_NEW="+new)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("couldn't update your PATH: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
