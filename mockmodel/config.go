package mockmodel

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"time"
)

const (
	// DefaultListen binds loopback only. Exposing the mock on another
	// interface is an explicit choice (the --listen flag).
	DefaultListen = "127.0.0.1:8081"
	// DefaultMaxBodyBytes bounds the request body.
	DefaultMaxBodyBytes int64 = 1 << 20
	// DefaultCompletionTokens is the simulated completion length.
	DefaultCompletionTokens = 16

	// MaxLatency bounds simulated latency (and therefore how long a
	// connection can be held); it is below the server write timeout.
	MaxLatency = 30 * time.Second
	// MaxCompletionTokens bounds the simulated completion length, and with it
	// the memory and time one request can cost.
	MaxCompletionTokens int64 = 10000
	// maxBodyLimit bounds the configurable request body size.
	maxBodyLimit int64 = 16 << 20
)

// Config holds the mock's behavior. The zero value is not valid; start from
// DefaultConfig. Token counts are SIMULATED values, not tokenizer output.
type Config struct {
	// Listen is the address the server binds.
	Listen string
	// MaxBodyBytes bounds a request body.
	MaxBodyBytes int64
	// Seed makes responses and failures differ between deployments while
	// staying deterministic for one seed.
	Seed int64
	// PromptTokens is the simulated prompt token count. Zero means derive a
	// simulated count from the request (whitespace-separated words).
	PromptTokens int64
	// CompletionTokens is the simulated completion length, capped by the
	// request's max_tokens.
	CompletionTokens int64
	// Latency is the simulated processing time added to every valid request.
	Latency time.Duration
	// ErrorRate is the fraction of requests that deterministically fail with
	// a 500, in [0, 1]. 0 never fails, 1 always fails.
	ErrorRate float64
}

// DefaultConfig returns the loopback-only default configuration.
func DefaultConfig() Config {
	return Config{
		Listen:           DefaultListen,
		MaxBodyBytes:     DefaultMaxBodyBytes,
		Seed:             1,
		CompletionTokens: DefaultCompletionTokens,
	}
}

// Validate checks every range. It is called before the server starts, so a bad
// value is a startup error, never a surprise at request time.
func (c Config) Validate() error {
	var errs []error
	host, port, err := net.SplitHostPort(c.Listen)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("listen %q is not host:port: %v", c.Listen, err))
	default:
		if p, perr := strconv.Atoi(port); perr != nil || p < 0 || p > 65535 {
			errs = append(errs, fmt.Errorf("listen port %q must be a number between 0 and 65535", port))
		}
		_ = host // an empty or non-loopback host is allowed: it is an explicit operator choice
	}
	if c.MaxBodyBytes < 1 || c.MaxBodyBytes > maxBodyLimit {
		errs = append(errs, fmt.Errorf("max body bytes %d must be between 1 and %d", c.MaxBodyBytes, maxBodyLimit))
	}
	if c.PromptTokens < 0 || c.PromptTokens > maxSafeInteger {
		errs = append(errs, fmt.Errorf("prompt-tokens %d must be between 0 and %d (0 derives it from the request)", c.PromptTokens, maxSafeInteger))
	}
	if c.CompletionTokens < 0 || c.CompletionTokens > MaxCompletionTokens {
		errs = append(errs, fmt.Errorf("completion-tokens %d must be between 0 and %d", c.CompletionTokens, MaxCompletionTokens))
	}
	if c.Latency < 0 || c.Latency > MaxLatency {
		errs = append(errs, fmt.Errorf("latency %s must be between 0 and %s", c.Latency, MaxLatency))
	}
	if math.IsNaN(c.ErrorRate) || c.ErrorRate < 0 || c.ErrorRate > 1 {
		errs = append(errs, fmt.Errorf("error-rate %v must be between 0 and 1", c.ErrorRate))
	}
	return errors.Join(errs...)
}

// ParseFlags builds a Config from command-line arguments (without the program
// name) and validates it. Usage and errors go to out. flag.ErrHelp is returned
// unchanged for -h/--help.
func ParseFlags(args []string, out io.Writer) (Config, error) {
	cfg := DefaultConfig()
	fs := flag.NewFlagSet("mockmodel", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "address to listen on; the default is loopback only, anything else is an explicit choice")
	fs.DurationVar(&cfg.Latency, "latency", cfg.Latency, "simulated latency added to every valid request, 0 to "+MaxLatency.String())
	fs.Int64Var(&cfg.PromptTokens, "prompt-tokens", cfg.PromptTokens, "simulated prompt token count; 0 derives it from the request's words")
	fs.Int64Var(&cfg.CompletionTokens, "completion-tokens", cfg.CompletionTokens, "simulated completion length in tokens (capped by max_tokens), 0 to "+strconv.FormatInt(MaxCompletionTokens, 10))
	fs.Float64Var(&cfg.ErrorRate, "error-rate", cfg.ErrorRate, "fraction of requests that fail deterministically with 500, 0 to 1")
	fs.Int64Var(&cfg.Seed, "seed", cfg.Seed, "seed for responses and failure selection")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}
