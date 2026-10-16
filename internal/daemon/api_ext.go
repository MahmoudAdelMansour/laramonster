package daemon

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/elevate"
	"laramonster/internal/fetch"
	"laramonster/internal/php"
)

var extName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)

// registerExtensions adds PHP → Settings → Extensions: what each PHP
// version has, turning extensions on and off, and installing popular ones.
func (rt *Runtime) registerExtensions(handle func(string, apiFunc)) {
	find := func(version string) (php.Install, error) {
		inst, ok := php.Find(php.Discover(), version)
		if !ok {
			return inst, fmt.Errorf("PHP %s isn't installed", version)
		}
		return inst, nil
	}
	handle("GET /api/php/extensions", func(r *http.Request) (any, error) {
		inst, err := find(r.URL.Query().Get("version"))
		if err != nil {
			return nil, err
		}
		cfg, _ := config.Load()
		return php.Extensions(inst, cfg.PHPExtensions[inst.Version])
	})
	handle("POST /api/php/extensions/toggle", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		inst, err := find(b.str("version"))
		if err != nil {
			return nil, err
		}
		name := strings.ToLower(b.str("name"))
		if !extName.MatchString(name) {
			return nil, errors.New("that isn't an extension name")
		}
		if err := setExtension(inst, name, b.bool("on")); err != nil {
			return nil, err
		}
		if err := rt.Reload(); err != nil {
			return nil, err
		}
		cfg, _ := config.Load()
		return php.Extensions(freshInstall(inst), cfg.PHPExtensions[inst.Version])
	})
	handle("POST /api/php/extensions/install", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		inst, err := find(b.str("version"))
		if err != nil {
			return nil, err
		}
		name := strings.ToLower(b.str("name"))
		if !extName.MatchString(name) {
			return nil, errors.New("that isn't an extension name")
		}
		title := fmt.Sprintf("Add %s to PHP %s", name, inst.Version)
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			switch php.KindOf(inst) {
			case php.KindWindows:
				if err := installWindowsExt(inst, name, j); err != nil {
					return err
				}
			case php.KindSystem:
				mgr, pkg, err := php.Package(inst, name)
				if err != nil {
					return err
				}
				cmd, admin := php.InstallCommand(mgr, pkg)
				j.Set(-1, "Installing "+pkg+" with "+mgr)
				fmt.Fprintf(j, "$ %s\n", cmd)
				if admin {
					err = elevate.RunTo(cmd, j)
				} else {
					err = runShell(cmd, j)
				}
				if err != nil {
					return fmt.Errorf("%s couldn't install %s: %w", mgr, pkg, err)
				}
			default:
				return errors.New("LaraMonster's PHP for this system can't load extra extensions")
			}
			// Installed: make sure it's on (pacman, for one, installs it off).
			fresh := freshInstall(inst)
			if !fresh.HasExtension(name) {
				if err := setExtension(fresh, name, true); err != nil {
					return err
				}
			}
			j.Set(1, name+" is on for PHP "+inst.Version)
			return rt.Reload()
		}, nil), nil
	})
}

// freshInstall probes an install again (its extensions changed).
func freshInstall(inst php.Install) php.Install {
	if f, ok := php.Find(php.Discover(), inst.Version); ok {
		return f
	}
	return inst
}

// setExtension turns an extension on or off for a PHP version: in its
// php.ini on Windows, in LaraMonster's config for a system PHP.
func setExtension(inst php.Install, name string, on bool) error {
	switch php.KindOf(inst) {
	case php.KindStatic:
		return errors.New("LaraMonster's PHP has its extensions built in: they can't be turned on or off")
	case php.KindWindows:
		if err := php.SetIniExtension(filepath.Join(inst.IniDir, "php.ini"), name, on); err != nil {
			return err
		}
	default:
		if err := core.Mutate(func(c *config.Config) error {
			m := c.PHPExtensions[inst.Version]
			if m == nil {
				m = map[string]bool{}
			}
			if on {
				m[name] = true
			} else {
				delete(m, name)
			}
			if len(m) == 0 {
				delete(c.PHPExtensions, inst.Version)
			} else {
				c.PHPExtensions[inst.Version] = m
			}
			return nil
		}); err != nil {
			return err
		}
	}
	// Xdebug slows every request down: on for the version, but idle
	// (mode=off) until a site turns step debugging on in the Debugger.
	if name == "xdebug" && on {
		cfg, _ := config.Load()
		if _, set := cfg.PHPSettings[inst.Version]["xdebug.mode"]; !set {
			off := "off"
			return core.SetPHPSettings(inst.Version, map[string]*string{"xdebug.mode": &off})
		}
	}
	return nil
}

// installWindowsExt downloads an extension's DLL into a Windows PHP's ext
// folder: PECL's build, or Xdebug's from xdebug.org.
func installWindowsExt(inst php.Install, name string, j *core.Job) error {
	j.Set(-1, "Finding "+name+" for PHP "+inst.Version)
	url, err := php.WindowsDownload(name, inst.Version, func(u string) (string, error) {
		var s string
		return s, fetch.GetText(u, &s)
	})
	if err != nil {
		return err
	}
	dir := filepath.Dir(inst.FPM)
	extDir := filepath.Join(dir, "ext")
	tmp, err := os.MkdirTemp(dir, ".ext-"+name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, filepath.Base(url))
	j.Set(-1, "Downloading "+filepath.Base(url))
	if _, err := fetch.Download(url, file, "", j.Bytes); err != nil {
		return err
	}
	dll := filepath.Join(extDir, "php_"+name+".dll")
	if strings.HasSuffix(file, ".dll") {
		return copyFile(file, dll)
	}
	unz := filepath.Join(tmp, "unzipped")
	if err := fetch.Unzip(file, unz); err != nil {
		return err
	}
	entries, _ := os.ReadDir(unz)
	found := false
	for _, e := range entries {
		n := e.Name()
		switch {
		case strings.EqualFold(n, "php_"+name+".dll"):
			found = true
			if err := copyFile(filepath.Join(unz, n), dll); err != nil {
				return err
			}
		case strings.HasSuffix(strings.ToLower(n), ".dll"):
			// Libraries the extension needs go next to php.exe.
			_ = copyFile(filepath.Join(unz, n), filepath.Join(dir, n))
		}
	}
	if !found {
		return fmt.Errorf("the download doesn't contain php_%s.dll", name)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("couldn't write %s (is PHP running from it? stop the sites using it first): %w", filepath.Base(dst), err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// runShell runs a command without administrator rights (Homebrew).
func runShell(script string, out io.Writer) error {
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}
