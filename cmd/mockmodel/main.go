// Command mockmodel runs the deterministic local mock chat model. It is a test
// backend: no authentication, loopback by default, simulated token counts.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/MenaceHecker/AgentPlane/mockmodel"
)

func main() {
	cfg, err := mockmodel.ParseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mockmodel: %v\n", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mockmodel: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg mockmodel.Config) error {
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", cfg.Listen, err)
	}
	_, _ = fmt.Fprintf(os.Stdout, "mockmodel listening on %s\n", ln.Addr())
	return mockmodel.Serve(ctx, mockmodel.NewHTTPServer(cfg, mockmodel.NewHandler(cfg)), ln)
}
