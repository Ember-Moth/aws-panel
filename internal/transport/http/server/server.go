package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/network/standard"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/herta-network/aws-panel/internal/transport/http/middleware"
	"github.com/herta-network/aws-panel/internal/transport/http/routes"
)

type Service struct {
	engine   *server.Hertz
	listener net.Listener
	done     chan error
}

func New(cfg config.HTTP, deps routes.Dependencies, logger *slog.Logger) (*Service, error) {
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return nil, errors.New("cannot bind HTTP listener; check address and port availability")
	}
	hlog.SetLevel(hlog.LevelWarn)
	h := server.New(
		server.WithListener(listener),
		server.WithTransport(standard.NewTransporter),
		server.WithDisablePrintRoute(true),
		server.WithReadTimeout(10*time.Second),
		server.WithIdleTimeout(60*time.Second),
		server.WithExitWaitTime(cfg.ShutdownTimeout),
		server.WithMaxRequestBodySize(1<<20),
	)
	h.Use(middleware.Request(logger))
	routes.Register(h, deps)
	return &Service{engine: h, listener: listener, done: make(chan error, 1)}, nil
}

func (s *Service) Start(ctx context.Context) error {
	go func() { s.done <- s.engine.Run() }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.engine.IsRunning() {
			return nil
		}
		select {
		case err := <-s.done:
			if err == nil {
				return errors.New("HTTP server stopped during startup")
			}
			return err
		case <-ctx.Done():
			_ = s.listener.Close()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) Done() <-chan error { return s.done }

func (s *Service) Shutdown(ctx context.Context) error {
	if s.engine.IsRunning() {
		return s.engine.Shutdown(ctx)
	}
	return s.listener.Close()
}
