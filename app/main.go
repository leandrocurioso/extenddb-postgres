// clients-api: a client-domain service that talks to ExtendDB through the
// standard AWS SDK for Go v2 DynamoDB client.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/curioso/extenddb-postgres/app/internal/clients"
)

type settings struct {
	Endpoint   string
	Region     string
	CAFile     string
	Table      string
	Cell       string
	Listen     string
	Simulate   time.Duration
	MaxRetries int
}

func loadSettings() settings {
	sim, err := time.ParseDuration(env("SIMULATE_INTERVAL", "15s"))
	if err != nil {
		sim = 15 * time.Second
	}
	retries, err := strconv.Atoi(env("AWS_MAX_ATTEMPTS", "8"))
	if err != nil {
		retries = 8
	}
	return settings{
		Endpoint:   env("EXTENDDB_ENDPOINT", "https://extenddb:18443"),
		Region:     env("AWS_REGION", "us-east-1"),
		CAFile:     env("EXTENDDB_CA_FILE", "/etc/extenddb-ca/cert.pem"),
		Table:      env("TABLE_NAME", "Clients"),
		Cell:       env("CELL_ID", "local"),
		Listen:     env("LISTEN_ADDR", ":8080"),
		Simulate:   sim,
		MaxRetries: retries,
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := loadSettings()
	log = log.With("cell", s.Cell)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := newDynamoClient(ctx, s)
	if err != nil {
		log.Error("aws config", "err", err)
		os.Exit(1)
	}
	repo := clients.NewRepository(db, s.Table, log)
	svc := clients.NewService(repo, s.Cell)

	var ready atomic.Bool
	go func() {
		// ExtendDB may still be starting; keep trying until the table is usable.
		for {
			err := repo.EnsureTable(ctx)
			if err == nil {
				ready.Store(true)
				log.Info("table ready", "table", s.Table, "endpoint", s.Endpoint)
				if s.Simulate > 0 {
					go simulate(ctx, svc, s, log)
				}
				return
			}
			log.Warn("table not ready yet", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()

	srv := &http.Server{Addr: s.Listen, Handler: routes(svc, &ready, log), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("listening", "addr", s.Listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "err", err)
		os.Exit(1)
	}
}

// newDynamoClient builds a stock DynamoDB client pointed at ExtendDB. The only
// differences from talking to AWS are the endpoint and the trusted CA
// (ExtendDB serves TLS with a self-signed certificate). Credentials come from
// the standard AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY variables and are
// SigV4-verified by ExtendDB.
func newDynamoClient(ctx context.Context, s settings) (*dynamodb.Client, error) {
	pem, err := os.ReadFile(s.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in %s", s.CAFile)
	}
	httpClient := awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	})

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(s.Region),
		config.WithHTTPClient(httpClient),
		config.WithRetryMaxAttempts(s.MaxRetries),
	)
	if err != nil {
		return nil, err
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(s.Endpoint)
	}), nil
}

func routes(svc *clients.Service, ready *atomic.Bool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "table not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Register one client: PutItem (profile) + BatchWriteItem (details).
	mux.HandleFunc("POST /clients", func(w http.ResponseWriter, r *http.Request) {
		var c clients.Client
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		created, err := svc.Register(r.Context(), &c)
		if err != nil {
			writeDomainError(w, log, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	})

	// Bulk import: BatchWriteItem for profiles and details.
	mux.HandleFunc("POST /clients/batch", func(w http.ResponseWriter, r *http.Request) {
		var cs []*clients.Client
		if err := json.NewDecoder(r.Body).Decode(&cs); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		created, err := svc.Import(r.Context(), cs)
		if err != nil {
			writeDomainError(w, log, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	})

	mux.HandleFunc("GET /clients/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, err := svc.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeDomainError(w, log, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})

	mux.HandleFunc("GET /clients", func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("email")
		if email == "" {
			writeError(w, http.StatusBadRequest, errors.New("query parameter email is required"))
			return
		}
		c, err := svc.FindByEmail(r.Context(), email)
		if err != nil {
			writeDomainError(w, log, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})

	return mux
}

func writeDomainError(w http.ResponseWriter, log *slog.Logger, err error) {
	var invalid *clients.ValidationError
	switch {
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, clients.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, clients.ErrClientExists):
		writeError(w, http.StatusConflict, err)
	default:
		log.Error("request failed", "err", err)
		writeError(w, http.StatusBadGateway, err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
