package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/course"
	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/infra"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")

	flag.Parse()

	if err := run(*addr); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	connections, err := infra.Open(ctx)
	if err != nil {
		return err
	}
	defer connections.Close()

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	service := course.NewService(
		course.NewRepository(connections.Postgres),
		course.WithProgressCache(course.NewRedisCache(connections.Redis, 5*time.Minute)),
	)
	server := &http.Server{
		Handler:           course.NewHandler(service),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	// This startup line also lets local tooling discover an OS-assigned port with -addr 127.0.0.1:0.
	fmt.Printf("LISTEN_ADDR=%s\n", listener.Addr())

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}

		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			return err
		}

		return nil
	}
}
