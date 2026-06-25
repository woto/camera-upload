// Command server runs the video upload microservice: an embedded tusd handler
// for resumable uploads, a management API for status/progress, and an embedded
// Uppy client.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/woto/camera-upload/internal/config"
	"github.com/woto/camera-upload/internal/process"
	"github.com/woto/camera-upload/internal/server"
	"github.com/woto/camera-upload/internal/store"
	"github.com/woto/camera-upload/internal/tus"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /health endpoint and exit (used by Docker HEALTHCHECK)")
	port := flag.String("port", "", "HTTP port to listen on (overrides the PORT env var)")
	flag.Parse()

	// A -port flag overrides the PORT env var for both the server and the
	// healthcheck probe, which read it through the environment.
	if *port != "" {
		_ = os.Setenv("PORT", *port)
	}

	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// runHealthcheck performs a request to the local /health endpoint and returns a
// process exit code. It is invoked by the container's HEALTHCHECK so the image
// needs no extra tools like curl.
func runHealthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}

	st := store.New(cfg.DataDir)

	tusHandler, err := tus.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Consume completed uploads for post-processing (ffprobe + thumbnail).
	proc := process.New(st, cfg.Thumbnails, log)
	go proc.Run(ctx, tusHandler.CompleteUploads)

	// Periodically purge stale incomplete uploads.
	go cleanupLoop(ctx, st, cfg.IncompleteTTL, log)

	handler := server.New(cfg, st, tusHandler.HTTP, log)

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,
		// No write/read timeouts: uploads of multi-GB files run for a long time.
		ReadHeaderTimeout: 30 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "data_dir", cfg.DataDir, "base_path", cfg.BasePath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func cleanupLoop(ctx context.Context, st *store.Store, ttl time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := st.CleanupIncomplete(ttl); err != nil {
				log.Error("cleanup failed", "err", err)
			} else if n > 0 {
				log.Info("cleaned up incomplete uploads", "count", n)
			}
		}
	}
}
