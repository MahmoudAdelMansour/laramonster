package daemon

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"laramonster/internal/autostart"
	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/paths"
)

// SetupStatus feeds the first-run welcome screen.
type SetupStatus struct {
	OS        string `json:"os"`
	Folder    string `json:"folder"`    // suggested projects folder
	Parked    bool   `json:"parked"`    // a projects folder is already parked
	CLI       string `json:"cli"`       // laramonster CLI path, "" if missing
	Terminal  bool   `json:"terminal"`  // shims written and bin dir on PATH
	Bin       string `json:"bin"`       // the shims' directory
	Autostart bool   `json:"autostart"` // starts at login
}

func setupStatus() SetupStatus {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	st := SetupStatus{
		OS:        runtime.GOOS,
		Folder:    suggestedFolder(cfg),
		Parked:    len(cfg.Paths) > 0,
		CLI:       core.CLIPath(),
		Terminal:  core.ShimsInstalled() && core.PathConfigured(),
		Bin:       paths.Bin(),
		Autostart: autostart.Enabled(),
	}
	return st
}

// suggestedFolder is the parked folder, else the first common projects
// folder that exists, else ~/Code (created when chosen).
func suggestedFolder(cfg *config.Config) string {
	if len(cfg.Paths) > 0 {
		return cfg.Paths[0]
	}
	home, _ := os.UserHomeDir()
	for _, n := range []string{"Code", "code", "Projects", "projects", "Sites", "dev", "src"} {
		if fi, err := os.Stat(filepath.Join(home, n)); err == nil && fi.IsDir() {
			return filepath.Join(home, n)
		}
	}
	return filepath.Join(home, "Code")
}

// registerSetup adds the endpoints behind the welcome screen.
func (rt *Runtime) registerSetup(handle func(string, apiFunc)) {
	handle("GET /api/setup", func(*http.Request) (any, error) { return setupStatus(), nil })

	// Park the projects folder, creating it if needed.
	handle("POST /api/setup/folder", rt.mutating(func(b body) error {
		dir := b.str("path")
		if dir == "" {
			return errors.New("choose a folder")
		}
		if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[1:])
		}
		if !filepath.IsAbs(dir) {
			return errors.New("use a full path, like " + suggestedFolder(config.Default()))
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if slices.Contains(cfg.Paths, filepath.Clean(dir)) {
			return nil
		}
		_, err = core.Park(dir)
		return err
	}))

	// php, composer and laramonster in every new terminal.
	handle("POST /api/setup/terminal", func(*http.Request) (any, error) {
		if _, err := core.InstallShims(); err != nil {
			return nil, err
		}
		if _, err := core.AddToPath(); err != nil {
			return nil, err
		}
		return setupStatus(), nil
	})

	// Start at login. The engine already runs, so the registration only
	// takes effect at the next login and never starts a second engine.
	handle("POST /api/setup/autostart", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		switch {
		case !b.bool("on"):
			err = autostart.Disable(false) // the app may be using this engine
		case core.CLIPath() != "":
			err = autostart.EnableEngine(core.CLIPath(), false)
		default:
			// Only the desktop app is installed: start it in the tray.
			var exe string
			if exe, err = os.Executable(); err == nil {
				err = autostart.EnableApp(exe)
			}
		}
		if err != nil {
			return nil, err
		}
		return setupStatus(), nil
	})

	handle("POST /api/setup/done", rt.mutating(func(body) error {
		return core.Mutate(func(c *config.Config) error { c.Onboarded = true; return nil })
	}))
}
