// Command laramonster-app is the LaraMonster desktop app: a native window
// with the dashboard and a tray icon. It runs the engine in-process unless a
// background LaraMonster (CLI or login service) is already running.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"laramonster/internal/ipc"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"laramonster/internal/account"
	"laramonster/internal/client"
	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/daemon"
	"laramonster/internal/paths"
	"laramonster/internal/selfupdate"
	"laramonster/internal/trust"
	"laramonster/internal/ui"
	"laramonster/internal/update"
)

//go:embed icons/appicon.png
var appIcon []byte

//go:embed icons/trayicon.png
var trayIcon []byte

func main() {
	// Elevated helper mode, used by "Trust certificate authority" when the
	// app is installed without the CLI next to it.
	if len(os.Args) == 3 && os.Args[1] == "trust" && os.Args[2] == "--system-only" {
		if err := trust.InstallSystem(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Started by an update: let the old version finish quitting first.
	selfupdate.UserAgent = update.UserAgent()
	selfupdate.WaitForPrevious(os.Args[1:])
	selfupdate.Cleanup()

	var (
		app        *application.App
		window     *application.WebviewWindow
		engineStop context.CancelFunc
		engineDone = make(chan struct{})
		ownsEngine bool
		quitting   bool
		showOnce   = func() {}
		// --background (login start): stay in the tray until opened.
		background = slices.Contains(os.Args[1:], "--background")
	)

	// Run the engine in-process if nothing else is running it.
	if !client.Running() {
		ownsEngine = true
		var ctx context.Context
		ctx, engineStop = context.WithCancel(context.Background())
		ready := make(chan struct{})
		go func() {
			if err := daemon.Run(ctx, func(*daemon.Runtime) { close(ready) }); err != nil {
				log.Printf("engine: %v", err)
			}
			// Mark the engine as finished before quitting: OnShutdown waits for it.
			close(engineDone)
			// The engine stopped (Stop button or signal): close the app too.
			if app != nil && !quitting {
				app.Quit()
			}
		}()
		select {
		case <-ready:
		case <-engineDone:
		case <-time.After(20 * time.Second):
		}
	} else {
		close(engineDone)
	}

	app = application.New(application.Options{
		Name:        "LaraMonster",
		Description: "Local PHP & Laravel development environment",
		Icon:        appIcon,
		Assets: application.AssetOptions{Handler: assetHandler(&app, func() { showOnce() }, func() {
			// Restart into the updated version. A separate engine (CLI or
			// login service) runs the old code: stop it, so the new app
			// starts the new engine (a login service restarts on the new files).
			if !ownsEngine {
				_, _ = client.Call(http.MethodPost, "/api/stop", nil)
			}
			quitting = true
			app.Quit()
		})},
		Mac:   application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
		Linux: application.LinuxOptions{ProgramName: "laramonster"},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: instanceID(),
			OnSecondInstanceLaunch: func(d application.SecondInstanceData) {
				if window != nil {
					// A laramonster:// link clicked while the app runs.
					if u := activateURL(d.Args); u != "" {
						window.SetURL(u)
					}
					window.Show()
					window.Focus()
				}
			},
		},
		DisableDefaultSignalHandler: ownsEngine, // the engine handles SIGINT/SIGTERM
		OnShutdown: func() {
			quitting = true
			if ownsEngine && engineStop != nil {
				engineStop()
				select {
				case <-engineDone:
				case <-time.After(30 * time.Second):
				}
			}
		},
	})

	// The window stays hidden until the UI has its first data, so it never
	// shows a blank page loading. The timer is a safety net.
	window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Hidden:           true,
		Name:             "main",
		Title:            "LaraMonster",
		Width:            1280,
		Height:           840,
		MinWidth:         900,
		MinHeight:        600,
		URL:              firstOr(activateURL(os.Args[1:]), "/"),
		BackgroundColour: application.NewRGB(0x07, 0x14, 0x31),
	})
	if !background {
		time.AfterFunc(4*time.Second, func() { showOnce() })
	}
	// Closing the window keeps LaraMonster running in the tray.
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if !quitting {
			e.Cancel()
			window.Hide()
		}
	})

	show := func() { window.Show(); window.Focus() }
	var shown sync.Once
	showOnce = func() {
		if !background {
			shown.Do(show)
		}
	}
	menu := app.Menu.New()
	menu.Add("Open LaraMonster").OnClick(func(*application.Context) { show() })
	menu.Add("New project…").OnClick(func(*application.Context) {
		window.SetURL("/#new")
		show()
	})
	menu.Add("Mail inbox").OnClick(func(*application.Context) {
		window.SetURL("/#mail")
		show()
	})
	menu.Add("LaraMonster Pro…").OnClick(func(*application.Context) { _ = core.Open(account.BaseURL() + "/laramonster") })
	menu.Add("Reload configuration").OnClick(func(*application.Context) { _, _ = client.Reload() })
	menu.AddSeparator()
	menu.Add("Open sites folder").OnClick(func(*application.Context) {
		if c, err := config.Load(); err == nil && len(c.Paths) > 0 {
			_ = core.Open(c.Paths[0])
		}
	})
	menu.Add("Open logs folder").OnClick(func(*application.Context) { _ = core.Open(paths.Logs()) })
	menu.AddSeparator()
	quitLabel := "Quit LaraMonster"
	if ownsEngine {
		quitLabel = "Stop engine and quit"
	}
	menu.Add(quitLabel).OnClick(func(*application.Context) { quitting = true; app.Quit() })

	tray := app.SystemTray.New()
	tray.SetIcon(trayIcon)
	tray.SetTooltip("LaraMonster")
	tray.SetMenu(menu)
	tray.OnClick(show)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// assetHandler serves the dashboard, proxies /api to the engine's socket,
// and adds desktop-only helpers under /app.
func assetHandler(app **application.App, ready, restart func()) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "http", Host: "laramonster"})
			r.Out.Host = "laramonster"
			ipc.Authorize(r.Out)
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return ipc.Dial(ctx) },
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "LaraMonster engine is not running"})
		},
	}
	dashboard := ui.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"), strings.HasPrefix(r.URL.Path, "/mail-part/"):
			// WebKitGTK sends request bodies without a Content-Length, and the
			// proxy drops any body whose length is 0. Buffer it to fix the length.
			if r.Body != nil && r.ContentLength <= 0 && r.Method != http.MethodGet {
				b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
			}
			proxy.ServeHTTP(w, r)
		case r.URL.Path == "/app/info":
			writeJSON(w, map[string]any{"desktop": true, "version": core.Version})
		case r.URL.Path == "/app/update" && r.Method == http.MethodGet:
			writeJSON(w, map[string]any{"state": selfupdate.Status(), "store_url": selfupdate.StoreURL})
		case r.URL.Path == "/app/update/install" && r.Method == http.MethodPost:
			// The release comes from monstersgate.com here, never from the page.
			info := update.Check()
			if !info.Available {
				info = update.Refresh() // the engine may have found it moments ago
			}
			if !info.Available {
				writeError(w, "LaraMonster is up to date")
				return
			}
			if err := selfupdate.Start(info.Latest, info.DownloadURL); err != nil {
				writeError(w, err.Error())
				return
			}
			writeJSON(w, map[string]any{"state": selfupdate.Status()})
		case r.URL.Path == "/app/update/restart" && r.Method == http.MethodPost:
			if selfupdate.Status().Phase != "ready" {
				writeError(w, "no update is ready to install")
				return
			}
			if err := selfupdate.Restart(os.Args[1:]); err != nil {
				writeError(w, "couldn't start the new version: "+err.Error())
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
			go func() { time.Sleep(300 * time.Millisecond); restart() }()
		case r.URL.Path == "/app/ready":
			ready()
			writeJSON(w, map[string]bool{"ok": true})
		case r.URL.Path == "/app/open-url":
			// Links leave the app for the system browser, never this window.
			if r.URL.Query().Get("url") == selfupdate.StoreURL {
				writeJSON(w, map[string]bool{"ok": core.Open(selfupdate.StoreURL) == nil})
				return
			}
			u, err := url.Parse(r.URL.Query().Get("url"))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				http.Error(w, "only http(s) links can be opened", http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]bool{"ok": core.Open(u.String()) == nil})
		case r.URL.Path == "/app/pick-folder":
			path, err := (*app).Dialog.OpenFile().
				CanChooseDirectories(true).
				CanChooseFiles(false).
				SetTitle("Choose a folder").
				PromptForSingleSelection()
			if err != nil {
				path = ""
			}
			writeJSON(w, map[string]string{"path": path})
		default:
			dashboard.ServeHTTP(w, r)
		}
	})
}

// instanceID allows one app per LaraMonster home: a second home
// (LARAMONSTER_HOME, used by tests and demos) is a separate LaraMonster.
func instanceID() string {
	id := "com.monstersgate.laramonster"
	if h := os.Getenv("LARAMONSTER_HOME"); h != "" {
		// A D-Bus name on Linux: each part must start with a letter.
		id += fmt.Sprintf(".home%x", sha256.Sum256([]byte(h)))[:16]
	}
	return id
}

// activateURL turns laramonster://activate?key=…&email=… (from a license
// email) into the Account page with the key filled in.
func activateURL(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "laramonster://") {
			continue
		}
		u, err := url.Parse(a)
		if err != nil || u.Host != "activate" {
			return "/#account"
		}
		q := url.Values{}
		for _, k := range []string{"key", "email"} {
			if v := u.Query().Get(k); v != "" {
				q.Set(k, v)
			}
		}
		return "/#account?" + q.Encode()
	}
	return ""
}

func firstOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func writeError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
