package php

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"laramonster/internal/paths"
)

// Broken is a PHP version LaraMonster installed that doesn't start, so it's
// missing from Discover. The PHP page shows why instead of hiding it.
type Broken struct {
	Version string `json:"version"`
	Reason  string `json:"reason"`
	// VCRedist: Windows can't start PHP because the Microsoft Visual C++
	// runtime it's built against isn't installed.
	VCRedist bool `json:"vc_redist,omitempty"`
}

// VCRedistURL is Microsoft's permanent link to the x64 Visual C++ runtime
// that windows.php.net's VS17 builds need.
const VCRedistURL = "https://aka.ms/vs/17/release/vc_redist.x64.exe"

// FindBroken checks every managed install that isn't in working.
func FindBroken(working []Install) []Broken {
	dirs, _ := filepath.Glob(filepath.Join(paths.PHP(), "*"))
	var out []Broken
	for _, dir := range dirs {
		v := filepath.Base(dir)
		if strings.HasPrefix(v, ".") {
			continue // an install in progress
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		if i, ok := Find(working, v); ok && i.Managed {
			continue
		}
		out = append(out, diagnose(v, dir))
	}
	return out
}

// hasVCRuntime reports whether the Visual C++ runtime's DLLs are in System32.
func hasVCRuntime() bool {
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	for _, dll := range []string{"vcruntime140.dll", "vcruntime140_1.dll"} {
		if _, err := os.Stat(filepath.Join(sys, dll)); err != nil {
			return false
		}
	}
	return true
}

// STATUS_DLL_NOT_FOUND: Windows couldn't load a DLL the program needs.
const dllNotFound = 0xC0000135

func diagnose(version, dir string) Broken {
	b := Broken{Version: version}
	name := "php-fpm"
	if runtime.GOOS == "windows" {
		name = "php-cgi.exe"
	}
	bin := filepath.Join(dir, name)
	if _, err := os.Stat(bin); err != nil {
		b.Reason = "the download is incomplete (" + name + " is missing): reinstall it"
		return b
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-v").CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil && versionRe.Match(out):
		b.Reason = "it starts now: reload LaraMonster to use it"
	case ctx.Err() != nil:
		b.Reason = fmt.Sprintf("it didn't answer within %s (antivirus scanning it?): try again in a minute", probeTimeout)
	case errors.As(err, &ee) && runtime.GOOS == "windows" && uint32(ee.ExitCode()) == dllNotFound:
		// PHP's own php8.dll (php8ts.dll) sits next to php-cgi.exe; the
		// Visual C++ runtime's vcruntime140.dll lives in System32.
		if own, _ := filepath.Glob(filepath.Join(dir, "php*.dll")); len(own) == 0 {
			b.Reason = "the download is incomplete (php8.dll is missing): reinstall it"
		} else if hasVCRuntime() {
			b.Reason = "Windows can't start PHP: a DLL it needs is missing (the Visual C++ runtime may be damaged: reinstall it)"
			b.VCRedist = true
		} else {
			b.VCRedist = true
			b.Reason = "Windows can't start PHP: the Microsoft Visual C++ runtime (2015-2022, x64) isn't installed"
		}
	case err != nil:
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		if msg == "" {
			msg = err.Error()
		}
		b.Reason = "it doesn't start: " + msg
	default:
		b.Reason = "it doesn't report a version: reinstall it"
	}
	return b
}
