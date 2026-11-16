// Command cafeteria starts the local web app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/server"
	"cafeteria-management/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	port := flag.Int("port", 8080, "port to listen on")
	dbPath := flag.String("db", "", "database file (default: in the user's app data folder)")
	flag.Parse()

	if *dbPath == "" {
		p, err := store.DefaultPath()
		if err != nil {
			return err
		}
		*dbPath = p
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	slog.Info("database opened", "path", *dbPath)

	// Test mode: the only sender until a real provider is added. The queue
	// is owned here: started below, awaited before the database closes.
	q := mailer.NewQueue(st, mailer.TestSender{}, mailer.Options{
		Provider: mailer.ProviderTest,
		Interval: 500 * time.Millisecond,
	})
	// Before listening, so a batch created later is never taken for an
	// interrupted one. Nothing is sent again by it.
	if err := q.Recover(ctx); err != nil {
		return fmt.Errorf("recover interrupted sendings: %w", err)
	}

	srv, err := server.New(version, st, q)
	if err != nil {
		return err
	}

	queueDone := make(chan struct{})
	go func() {
		defer close(queueDone)
		q.Run(ctx)
	}()
	// Runs before st.Close: the queue stops (the email in flight is finished
	// and recorded) before the database closes, also on a server error.
	defer func() {
		stop()
		<-queueDone
	}()

	// Listen on localhost only: the app must never be reachable from the network.
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server started", "url", "http://"+addr, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
