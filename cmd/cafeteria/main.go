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

	"cafeteria-management/internal/keychain"
	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/microsoft"
	"cafeteria-management/internal/server"
	"cafeteria-management/internal/store"
)

// Set at build time with -ldflags "-X main.version=...". The client and
// tenant IDs of the Microsoft 365 app registration are public identifiers
// (not secrets); both empty means Microsoft 365 is not available (test
// mode only).
var (
	version    = "dev"
	msClientID string
	msTenantID string
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	port := flag.Int("port", 8080, "port to listen on")
	dbPath := flag.String("db", "", "database file (default: in the user's app data folder)")
	clientID := flag.String("ms-client-id", msClientID, "Microsoft 365 application (client) ID (default: built in)")
	tenantID := flag.String("ms-tenant-id", msTenantID, "Microsoft 365 directory (tenant) ID (default: built in)")
	flag.Parse()

	if (*clientID == "") != (*tenantID == "") {
		return errors.New("ms-client-id and ms-tenant-id must be set together (or both left empty)")
	}

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

	// Test mode is always there. Microsoft 365 needs the app registration
	// IDs. The queue is owned here: started below, awaited before the
	// database closes.
	providers := map[string]mailer.Provider{
		mailer.ProviderTest: {Sender: mailer.TestSender{}, Interval: 500 * time.Millisecond},
	}
	var client *microsoft.Client // nil when not configured
	configured := *clientID != ""
	slog.Info("microsoft 365", "configured", configured)
	if configured {
		kc := keychain.New("CafeteriaManagement", keychain.OS{})
		client = microsoft.New(microsoft.Config{ClientID: *clientID, TenantID: *tenantID}, kc)
		if err := client.Load(); err != nil {
			// Not fatal: the app starts "not connected".
			slog.Error("microsoft 365 sign-in unreadable", "err", err)
		}
		// 2 s: about 30 emails per minute, the Exchange Online limit.
		providers[mailer.ProviderGraph] = mailer.Provider{Sender: client, Interval: 2 * time.Second}
	}
	q := mailer.NewQueue(st, mailer.Options{Providers: providers})
	// Before listening, so a batch created later is never taken for an
	// interrupted one. Nothing is sent again by it.
	if err := q.Recover(ctx); err != nil {
		return fmt.Errorf("recover interrupted sendings: %w", err)
	}

	// Assigned only when configured: a nil *microsoft.Client inside the
	// interface would not be nil for the server.
	var ms interface {
		Account() (microsoft.Account, bool)
		BeginSignIn(returnHost string) (string, error)
		FinishSignIn(ctx context.Context, state, code string) (string, error)
		SignOut() error
		Send(ctx context.Context, m mailer.Message) error
	}
	if client != nil {
		ms = client
	}
	srv, err := server.New(ctx, version, st, q, ms)
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
