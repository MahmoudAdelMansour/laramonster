package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/dns"
	"laramonster/internal/elevate"
	"laramonster/internal/fetch"
	"laramonster/internal/paths"
	"laramonster/internal/php"
	"laramonster/internal/services"
	"laramonster/internal/sites"
	"laramonster/internal/trust"
	"laramonster/internal/ui"
)

// HeaderName must be sent with every API request. Browsers can't add custom
// headers cross-origin without a CORS preflight (which we never allow), so
// this blocks other websites from driving the API through localhost.
const HeaderName = "X-LaraMonster"

type apiFunc func(r *http.Request) (any, error)

// Handler serves the API under /api/ and the dashboard UI everywhere else.
func (rt *Runtime) Handler(stop func()) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, fn apiFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(HeaderName) == "" {
				http.Error(w, "missing "+HeaderName+" header", http.StatusForbidden)
				return
			}
			v, err := fn(r)
			w.Header().Set("Content-Type", "application/json")
			if errors.Is(err, errProRequired) {
				// Not an error to show: the UI explains Pro and offers sign-in.
				w.WriteHeader(http.StatusPaymentRequired)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "pro_required", "account": rt.acct.Status()})
				return
			}
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			if v == nil {
				v = rt.Status()
			}
			_ = json.NewEncoder(w).Encode(v)
		})
	}

	handle("GET /api/status", func(*http.Request) (any, error) { return nil, nil })
	handle("GET /api/health", func(*http.Request) (any, error) {
		if !rt.ready.Load() {
			return nil, errors.New("starting")
		}
		return map[string]bool{"ready": true}, nil
	})
	handle("POST /api/reload", func(*http.Request) (any, error) { return nil, rt.Reload() })
	handle("POST /api/stop", func(*http.Request) (any, error) {
		go stop()
		return map[string]bool{"ok": true}, nil
	})

	// --- sites ---
	handle("POST /api/sites/park", rt.mutating(func(b body) error { _, err := core.Park(b.str("path")); return err }))
	handle("POST /api/sites/forget", rt.mutating(func(b body) error { _, err := core.Forget(b.str("path")); return err }))
	handle("POST /api/sites/link", rt.mutating(func(b body) error { _, err := core.Link(b.str("name"), b.str("path")); return err }))
	handle("POST /api/sites/unlink", rt.mutating(func(b body) error { return core.Unlink(b.str("name")) }))
	handle("POST /api/sites/secure", rt.mutating(func(b body) error { return core.Secure(b.str("name"), b.bool("on")) }))
	handle("POST /api/sites/isolate", rt.mutating(func(b body) error { return core.Isolate(b.str("name"), b.str("version")) }))
	handle("POST /api/sites/open", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		_, list, err := core.Sites()
		if err != nil {
			return nil, err
		}
		s, ok := sites.Find(list, b.str("name"))
		if !ok {
			return nil, fmt.Errorf("no site named %q", b.str("name"))
		}
		switch b.str("target") {
		case "editor":
			err = core.OpenEditor(s.Path)
		case "folder":
			err = core.Open(s.Path)
		default:
			err = core.Open(s.URL)
		}
		return map[string]bool{"ok": err == nil}, err
	})
	// --- php ---
	handle("GET /api/php/available", func(*http.Request) (any, error) { return php.Available() })
	handle("POST /api/php/install", func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		v := php.Normalize(b.str("version"))
		title := "Install PHP " + v
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			rel, err := php.InstallVersionSteps(v, j.Bytes, j.Step)
			if err == nil {
				fmt.Fprintf(j, "installed PHP %s", rel.Full)
			}
			return err
		}, rt.reloadQuiet), nil
	})
	// Windows: PHP needs Microsoft's Visual C++ runtime. Its installer asks
	// for administrator rights itself.
	handle("POST /api/php/vcredist", func(*http.Request) (any, error) {
		if runtime.GOOS != "windows" {
			return nil, errors.New("only Windows needs the Visual C++ runtime")
		}
		title := "Install the Visual C++ runtime"
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			exe := filepath.Join(os.TempDir(), "laramonster-vc_redist.x64.exe")
			defer os.Remove(exe)
			fmt.Fprintln(j, "downloading "+php.VCRedistURL)
			if _, err := fetch.Download(php.VCRedistURL, exe, "", j.Bytes); err != nil {
				return fmt.Errorf("couldn't download the Visual C++ runtime: %w", err)
			}
			j.Set(-1, "Installing (Windows asks for permission)")
			script := "$p = Start-Process -FilePath '" + strings.ReplaceAll(exe, "'", "''") + "' -ArgumentList '/install','/passive','/norestart' -Wait -PassThru; exit $p.ExitCode"
			if err := elevate.RunTo(script, j); err != nil {
				return fmt.Errorf("the Visual C++ runtime wasn't installed: %w", err)
			}
			fmt.Fprintln(j, "Visual C++ runtime installed")
			return nil
		}, rt.reloadQuiet), nil
	})
	// php.ini settings: global, per version, per site.
	handle("GET /api/php/settings", func(r *http.Request) (any, error) {
		return core.ReadPHPSettings(r.URL.Query().Get("scope"))
	})
	handle("POST /api/php/settings", func(r *http.Request) (any, error) {
		var b struct {
			Scope  string             `json:"scope"`
			Values map[string]*string `json:"values"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil {
			return nil, err
		}
		if err := core.SetPHPSettings(b.Scope, b.Values); err != nil {
			return nil, err
		}
		// PHP-FPM reads settings when it starts: reload restarts the pools
		// whose settings changed.
		if err := rt.Reload(); err != nil {
			return nil, err
		}
		return core.ReadPHPSettings(b.Scope)
	})
	handle("POST /api/php/uninstall", rt.mutating(func(b body) error { return php.Uninstall(b.str("version")) }))
	handle("POST /api/php/default", rt.mutating(func(b body) error { _, err := core.SetDefaultPHP(b.str("version")); return err }))

	// --- services ---
	handle("POST /api/services/{name}/{action}", func(r *http.Request) (any, error) {
		d, ok := services.Get(r.PathValue("name"))
		if !ok {
			return nil, fmt.Errorf("unknown service %q", r.PathValue("name"))
		}
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		return rt.serviceAction(d, r.PathValue("action"), b)
	})

	// --- settings, dns, tls ---
	handle("POST /api/settings", func(r *http.Request) (any, error) {
		var s core.Settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&s); err != nil {
			return nil, err
		}
		if err := core.UpdateSettings(s); err != nil {
			return nil, err
		}
		return nil, rt.Reload()
	})
	handle("POST /api/dns/setup", func(*http.Request) (any, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		_, script, err := dns.SetupScript(cfg.TLD, cfg.DNSPort, core.HostNames(cfg.TLD, rt.Status().Sites))
		if err != nil {
			return nil, err
		}
		return nil, elevate.Run(script)
	})
	handle("POST /api/tls/trust", func(*http.Request) (any, error) {
		err := TrustCA()
		rt.invalidateTLS()
		return nil, err
	})

	// --- logs ---
	handle("GET /api/logs", func(r *http.Request) (any, error) {
		name := r.URL.Query().Get("name")
		if name == "" {
			return LogNames(), nil
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		return TailLog(name, n)
	})

	rt.registerExtra(handle, mux)
	rt.registerSetup(handle)
	rt.registerSiteSettings(handle)
	rt.registerPro(handle, mux)
	mux.Handle("/", ui.Handler())
	return mux
}

func (rt *Runtime) reloadQuiet() { _ = rt.Reload() }

// mutating wraps a config change: parse the body, apply, reload, return status.
func (rt *Runtime) mutating(fn func(body) error) apiFunc {
	return func(r *http.Request) (any, error) {
		b, err := readBody(r)
		if err != nil {
			return nil, err
		}
		if err := fn(b); err != nil {
			return nil, err
		}
		return nil, rt.Reload()
	}
}

func (rt *Runtime) serviceAction(d *services.Def, action string, b body) (any, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	sc := cfg.Service(d.Name, d.DefaultPort)
	switch action {
	case "start":
		if err := rt.svc.Start(d, sc.Port); err != nil {
			return nil, err
		}
	case "stop":
		rt.svc.Stop(d.Name)
	case "restart":
		rt.svc.Stop(d.Name)
		if err := rt.svc.Start(d, sc.Port); err != nil {
			return nil, err
		}
	case "enable":
		sc.Enabled = b.bool("on")
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	case "port":
		port := int(b.num("port"))
		if port < 1 || port > 65535 {
			return nil, errors.New("invalid port")
		}
		sc.Port = port
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		if rt.svc.Running(d.Name) {
			rt.svc.Stop(d.Name)
			if err := rt.svc.Start(d, port); err != nil {
				return nil, err
			}
		}
	case "install":
		title := "Install " + d.Title
		if rt.jobs.Busy(title) {
			return nil, errors.New(title + " is already running")
		}
		return rt.jobs.Run(title, func(j *core.Job) error {
			if err := rt.svc.Install(d, j.Bytes); err != nil {
				return err
			}
			fmt.Fprintf(j, "%s installed", d.Title)
			return nil
		}, nil), nil
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
	if d.Name == "mailpit" {
		_ = rt.Reload()
	}
	return nil, nil
}

// TrustCA installs the local CA into the system store (elevated) and the
// browser stores (as the current user).
func TrustCA() error {
	if !trust.Exists() {
		return errors.New("the local CA doesn't exist yet: start LaraMonster first")
	}
	exe, err := cliExecutable()
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" && !trust.Trusted() {
		if err := trust.InstallMacUser(); err != nil {
			return err
		}
	} else if os.Geteuid() != 0 && !trust.Trusted() {
		script := fmt.Sprintf("LARAMONSTER_HOME='%s' '%s' trust --system-only", paths.Root(), exe)
		if err := elevate.Run(script); err != nil {
			return fmt.Errorf("installing into the system store: %w", err)
		}
	}
	// Browser stores (Firefox, and Chrome on Linux) need certutil.
	if !trust.NeedsBrowserStores() {
		return nil
	}
	if err := trust.InstallBrowsers(); err != nil {
		if trust.BrowserStatus() == "missing-certutil" {
			return errors.New("the system trusts LaraMonster now, but browsers need certutil: install “Browser certificate tools” from Tools, then trust again")
		}
		return fmt.Errorf("browser certificate stores: %w", err)
	}
	return nil
}

// cliExecutable is the binary that understands `trust --system-only`: this
// one, or the CLI next to the desktop app (which also handles that flag).
func cliExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	name := "laramonster"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if cli := filepath.Join(filepath.Dir(exe), name); cli != exe {
		if _, err := os.Stat(cli); err == nil {
			return cli, nil
		}
	}
	return exe, nil
}

// LogNames lists available log files (without extension).
func LogNames() []string {
	entries, _ := os.ReadDir(paths.Logs())
	var out []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".log"); ok {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// TailLog returns the last n lines of a log.
func TailLog(name string, n int) (map[string]any, error) {
	if n <= 0 || n > 2000 {
		n = 300
	}
	if name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, errors.New("invalid log name")
	}
	f, err := os.Open(filepath.Join(paths.Logs(), name+".log"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Read at most the last 512 KB.
	if fi, err := f.Stat(); err == nil && fi.Size() > 512<<10 {
		_, _ = f.Seek(-512<<10, io.SeekEnd)
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return map[string]any{"name": name, "lines": lines}, nil
}

// body is a loosely-typed JSON request body.
type body map[string]any

func readBody(r *http.Request) (body, error) {
	b := body{}
	// Don't trust ContentLength: some clients (WebKitGTK) leave it at 0 even
	// with a body. An empty body just decodes to io.EOF.
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&b); err != nil && err != io.EOF {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	return b, nil
}

func (b body) str(k string) string { s, _ := b[k].(string); return strings.TrimSpace(s) }
func (b body) bool(k string) bool  { v, _ := b[k].(bool); return v }
func (b body) num(k string) float64 {
	switch v := b[k].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}
