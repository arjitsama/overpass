package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/arjitsama/overpass/internal/bus"
	"github.com/arjitsama/overpass/internal/config"
	"github.com/arjitsama/overpass/internal/errs"
)

const (
	maxBodyBytes    = 1 << 20
	shutdownTimeout = 5 * time.Second
)

type agent struct {
	cfg config.Config
	bus *bus.Bus
	log *slog.Logger
	tls *tls.Config
}

func newAgent(cfg config.Config, log *slog.Logger) (*agent, error) {
	cert, err := loadOrGenerateCert(cfg, log)
	if err != nil {
		return nil, err
	}
	return &agent{
		cfg: cfg,
		bus: bus.New(bus.DefaultBacklog, bus.DefaultMaxSubs),
		log: log,
		tls: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}},
	}, nil
}

func (a *agent) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.health)
	mux.Handle("/events", a.bus.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		errs.Write(w, http.StatusNotFound, errs.NotFound, "no route "+r.URL.Path)
	})
	return a.recoverMW(limitBody(mux))
}

func (a *agent) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		errs.Write(w, http.StatusMethodNotAllowed, errs.MethodNotAllowed, "use GET")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok", "role": a.cfg.Role, "host": a.cfg.Host,
	})
}

// recoverMW turns a handler panic into a named internal error.
func (a *agent) recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &trackingWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			a.log.Error("handler panic", "path", r.URL.Path, "panic", v)
			if !tw.wrote {
				errs.Write(w, http.StatusInternalServerError, errs.Internal, "internal error")
			}
		}()
		next.ServeHTTP(tw, r)
	})
}

// trackingWriter records whether the response has started, so a late panic
// does not splice a JSON error into a body already on the wire.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush and SetWriteDeadline.
func (t *trackingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// serve runs HTTPS on ln until ctx is done, then shuts down cleanly.
func (a *agent) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           a.routes(),
		TLSConfig:         a.tls,
		ReadHeaderTimeout: 5 * time.Second,
		// No ReadTimeout or WriteTimeout: either would cut long-lived SSE
		// streams. Bodies are capped by limitBody instead.
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 16 << 10,
		ErrorLog:       slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ServeTLS(ln, "", "") }()
	a.publish("agent_started", "ok", "")
	a.log.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		a.bus.Close()
		return err
	case <-ctx.Done():
	}
	a.publish("agent_stopping", "ok", "signal")
	a.bus.Close() // ends open SSE streams so Shutdown does not wait on them
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		a.log.Warn("graceful shutdown timed out; closing connections", "err", err)
		_ = srv.Close()
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	a.log.Info("stopped")
	return nil
}

func (a *agent) publish(kind, result, reason string) {
	_, err := a.bus.Publish(bus.Event{
		Agent: a.cfg.Host, Kind: kind, Subject: a.cfg.Role, Result: result, Reason: reason,
	})
	if err != nil {
		a.log.Error("publish", "kind", kind, "err", err)
	}
}
