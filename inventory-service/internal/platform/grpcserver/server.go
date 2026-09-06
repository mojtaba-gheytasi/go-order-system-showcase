package grpcserver

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type Config struct {
	Address string
	MaxConnectionIdle    time.Duration
	MaxConnectionAge     time.Duration
	MaxConnectionAgeSlop time.Duration
	UnaryInterceptors []grpc.UnaryServerInterceptor
}

type Server struct {
	address string
	server  *grpc.Server
}

func NewServer(config Config, register func(*grpc.Server)) *Server {
	server := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     config.MaxConnectionIdle,
			MaxConnectionAge:      config.MaxConnectionAge,
			MaxConnectionAgeGrace: config.MaxConnectionAgeSlop,
		}),
		grpc.ChainUnaryInterceptor(config.UnaryInterceptors...),
	)

	register(server)

	return &Server{address: config.Address, server: server}
}

func (s *Server) Start() error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.address, err)
	}

	return s.server.Serve(listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	stopped := make(chan struct{})
	go func() {
		s.server.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		s.server.Stop()

		return ctx.Err()
	}
}
