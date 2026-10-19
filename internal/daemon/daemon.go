// Package daemon is the long-running process: it owns Caddy, the PHP pools,
// services and DNS, and serves the API + dashboard on a unix socket.
package daemon

import (
	"context"
	"errors"
	"io"
	"laramonster/internal/ipc"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"laramonster/internal/client"
	"laramonster/internal/config"
	"laramonster/internal/core"
	"laramonster/internal/paths"
	"laramonster/internal/tools"
)

// Run starts the daemon and blocks until ctx is cancelled, a signal arrives,
// or the API receives /stop. ready (optional) is called once it is serving.
func Run(ctx context.Context, ready func(*Runtime)) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	if client.Running() {
		return errors.New("LaraMonster is already running")
	}

	if f, err := os.OpenFile(filepath.Join(paths.Logs(), "laramonster.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		defer f.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, f))
		defer log.SetOutput(os.Stderr)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt := NewRuntime()

	// The API endpoint must exist before Caddy proxies the dashboard to it.
	ln, token, err := ipc.Listen()
	if err != nil {
		return err
	}
	defer ipc.Remove()
	// Runs before the socket is removed (defers are LIFO): clients that wait
	// for the socket to disappear then know the ports are free too.
	defer rt.Shutdown()
	srv := &http.Server{Handler: ipc.Guard(token, rt.Handler(stop))}
	go srv.Serve(ln)

	if err := rt.Reload(); err != nil {
		var busy *PortsBusyError
		if !errors.As(err, &busy) {
			return err
		}
		// Stay up: the dashboard shows who holds the ports and how to fix it.
		log.Print("warning: ", err)
	}
	rt.ready.Store(true)
	rt.StartEnabledServices()
	go tools.States() // warm the tools cache for the dashboard
	go rt.mon.run(ctx.Done())
	go rt.licenseLoop(ctx.Done())

	if cfg, err := config.Load(); err == nil && cfg.BrowserDashboard {
		log.Printf("LaraMonster is running: dashboard at %s", core.DashboardURL(cfg))
	} else {
		log.Print("LaraMonster is running")
	}
	if ready != nil {
		ready(rt)
	}
	<-ctx.Done()
	log.Print("stopping LaraMonster")
	// Don't wait forever on requests stuck in a password prompt.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
	}
	return nil
}
