package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"laramonster/internal/fetch"
	"laramonster/internal/paths"
)

const composerURL = "https://getcomposer.org/download/latest-stable/composer.phar"

func ComposerPhar() string { return filepath.Join(paths.Bin(), "composer.phar") }

// EnsureComposer downloads composer.phar once.
func EnsureComposer(progress fetch.Progress) error {
	if _, err := os.Stat(ComposerPhar()); err == nil {
		return nil
	}
	if err := os.MkdirAll(paths.Bin(), 0o755); err != nil {
		return err
	}
	var sum string
	if err := fetch.GetText(composerURL+".sha256", &sum); err != nil {
		return err
	}
	tmp := ComposerPhar() + ".tmp"
	if _, err := fetch.Download(composerURL, tmp, strings.Fields(sum)[0], progress); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, ComposerPhar())
}

// PHPCommand builds a command that runs PHP (for dir's site) with args.
func PHPCommand(dir string, args ...string) (*exec.Cmd, error) {
	inst, err := PHPFor(dir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(inst.CLI, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), inst.Env()...)
	return cmd, nil
}

// ComposerCommand builds a command that runs Composer with the right PHP.
func ComposerCommand(dir string, args ...string) (*exec.Cmd, error) {
	if err := EnsureComposer(nil); err != nil {
		return nil, fmt.Errorf("downloading composer: %w", err)
	}
	return PHPCommand(dir, append([]string{ComposerPhar()}, args...)...)
}

// NodeDir is where LaraMonster installs Node.js.
func NodeDir() string { return filepath.Join(paths.Root(), "tools", "node") }

// ManagedNode returns the node binary LaraMonster installed, if any.
func ManagedNode() string {
	pattern := filepath.Join(NodeDir(), "*", "bin", "node")
	if runtime.GOOS == "windows" {
		pattern = filepath.Join(NodeDir(), "*", "node.exe")
	}
	m, _ := filepath.Glob(pattern)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

// NodeBinDir is the directory to put on PATH for node/npm/npx ("" if none).
func NodeBinDir() string {
	if p := ManagedNode(); p != "" {
		return filepath.Dir(p)
	}
	if p, err := exec.LookPath("node"); err == nil {
		return filepath.Dir(p)
	}
	return ""
}

// CLIPath finds the laramonster command-line binary: this process when it is
// the CLI, else next to this binary (the desktop app), else on PATH. It
// returns "" when only the desktop app is installed.
func CLIPath() string {
	name := "laramonster"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		if filepath.Base(self) == name {
			return self
		}
		if p := filepath.Join(filepath.Dir(self), name); isFile(p) {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil && filepath.Dir(p) != paths.Bin() {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return real
		}
		return p
	}
	return ""
}

// ShimsInstalled reports whether the php shim exists in the bin dir.
func ShimsInstalled() bool {
	name := "php"
	if runtime.GOOS == "windows" {
		name += ".cmd"
	}
	return isFile(filepath.Join(paths.Bin(), name))
}

// InstallShims writes laramonster, php and composer wrappers into
// LaraMonster's bin dir. Put that directory on PATH and `php` follows each
// site's PHP version.
func InstallShims() (string, error) {
	exe := CLIPath()
	if exe == "" {
		return "", errors.New("the laramonster command-line tool is not installed next to the app")
	}
	dir := paths.Bin()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, tool := range []string{"laramonster", "php", "composer"} {
		arg := " " + tool
		if tool == "laramonster" {
			arg = ""
		}
		var name, body string
		if runtime.GOOS == "windows" {
			name, body = tool+".cmd", fmt.Sprintf("@echo off\r\n\"%s\"%s %%*\r\n", exe, arg)
		} else {
			name, body = tool, fmt.Sprintf("#!/bin/sh\nexec '%s'%s \"$@\"\n", exe, arg)
		}
		// Don't shadow a laramonster that is already on PATH.
		if tool == "laramonster" && filepath.Dir(exe) != dir {
			if p, err := exec.LookPath("laramonster"); err == nil && filepath.Dir(p) != dir {
				continue
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			return "", err
		}
	}
	// node, npm and npx point straight at LaraMonster's Node.js when installed.
	if node := ManagedNode(); node != "" {
		bin := filepath.Dir(node)
		for _, tool := range []string{"node", "npm", "npx"} {
			var name, body string
			if runtime.GOOS == "windows" {
				name, body = tool+".cmd", fmt.Sprintf("@echo off\r\n\"%s\" %%*\r\n", filepath.Join(bin, tool+map[bool]string{true: ".exe", false: ".cmd"}[tool == "node"]))
			} else {
				name, body = tool, fmt.Sprintf("#!/bin/sh\nPATH='%s':\"$PATH\" exec '%s' \"$@\"\n", bin, filepath.Join(bin, tool))
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

// PathConfigured reports whether the bin dir is on PATH now, or will be in
// new terminals (shell profile or user PATH edited).
func PathConfigured() bool {
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(d) == paths.Bin() {
			return true
		}
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("powershell", "-NoProfile", "-Command", "[Environment]::GetEnvironmentVariable('Path','User')").Output()
		return err == nil && slices.Contains(filepath.SplitList(strings.TrimSpace(string(out))), paths.Bin())
	}
	home, _ := os.UserHomeDir()
	for _, rc := range []string{".bashrc", ".bash_profile", ".zshrc", ".profile", filepath.Join(".config", "fish", "conf.d", "laramonster.fish")} {
		if b, err := os.ReadFile(filepath.Join(home, rc)); err == nil && strings.Contains(string(b), paths.Bin()) {
			return true
		}
	}
	return false
}

// AddToPath puts the bin dir on PATH for new terminals: the shell profiles
// on unix, the user PATH on Windows.
func AddToPath() ([]string, error) {
	if runtime.GOOS == "windows" {
		if PathConfigured() {
			return nil, nil
		}
		// Read and write the user PATH as-is (setx would truncate it).
		script := "$p=[Environment]::GetEnvironmentVariable('Path','User'); if ($p) { $p=$p.TrimEnd(';')+';' }; [Environment]::SetEnvironmentVariable('Path', $p+$env:LM_BIN, 'User')"
		cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
		cmd.Env = append(os.Environ(), "LM_BIN="+paths.Bin())
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("couldn't update your PATH: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return []string{"user PATH"}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	line := fmt.Sprintf("export PATH=\"%s:$PATH\" # LaraMonster", paths.Bin())
	var changed []string
	// The profiles that exist, plus the user's own shell's, created when
	// missing (a new Mac has no ~/.zshrc).
	own := shellProfile()
	for _, rc := range []string{".bashrc", ".bash_profile", ".zshrc", ".profile"} {
		p := filepath.Join(home, rc)
		b, err := os.ReadFile(p)
		if (err != nil && rc != own) || strings.Contains(string(b), "# LaraMonster") {
			continue
		}
		if err := appendLine(p, line); err != nil {
			return changed, err
		}
		changed = append(changed, p)
	}
	if fish := filepath.Join(home, ".config", "fish", "conf.d"); dirExists(fish) {
		p := filepath.Join(fish, "laramonster.fish")
		if err := os.WriteFile(p, []byte("fish_add_path -g "+paths.Bin()+"\n"), 0o644); err == nil {
			changed = append(changed, p)
		}
	}
	return changed, nil
}

// shellProfile is the startup file of the user's shell, relative to home.
func shellProfile() string {
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return ".zshrc"
	case "bash":
		if runtime.GOOS == "darwin" {
			return ".bash_profile" // Terminal opens login shells
		}
		return ".bashrc"
	case "", "sh":
		if runtime.GOOS == "darwin" {
			return ".zshrc" // the default shell since macOS 10.15
		}
	}
	return ".profile"
}

// appendLine adds a line to a file, creating it when missing.
func appendLine(p, line string) error {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "\n%s\n", line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
