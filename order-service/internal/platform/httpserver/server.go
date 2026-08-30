package httpserver

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type Config struct {
	Address string
	ReadHeaderTimeout time.Duration
	ReadTimeout time.Duration
	WriteTimeout time.Duration
	IdleTimeout time.Duration
}

type Server struct {
	server *http.Server
}

func NewServer(config Config, handler http.Handler) *Server {
	return &Server{
		server: &http.Server{
			Addr:              config.Address,
			Handler:           handler,
			ReadHeaderTimeout: config.ReadHeaderTimeout,
			ReadTimeout:       config.ReadTimeout,
			WriteTimeout:      config.WriteTimeout,
			IdleTimeout:       config.IdleTimeout,
		},
	}
}

func (s *Server) Start() error {
	err := s.server.ListenAndServe()

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

func (s *Server) Shotdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}