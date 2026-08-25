// Package cli implements the dkwws command-line interface.
//
// Normal output on stdout is exactly the resulting URL, or a single JSON
// object with -json. Everything else — progress, warnings, errors — goes to
// stderr, so an agent can consume stdout verbatim.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/patriksimms/dkwws/internal/config"
	"github.com/patriksimms/dkwws/internal/store"
)

// Version is set at build time with -ldflags.
var Version = "dev"

// App holds the streams the commands write to, so tests can capture them.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	// Now overrides the clock used to stamp link expiries.
	Now func() time.Time
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

const usage = `dkwws uploads a self-contained file and prints a link to it.

Usage:
  dkwws upload [flags] <file>   upload a file and print its URL
  dkwws version                 print the version

Configuration is read from the environment, falling back to a KEY=value file
at ${XDG_CONFIG_HOME:-~/.config}/dkwws/config with mode 0600:

  DKWWS_S3_ENDPOINT           https endpoint of the S3-compatible backend
  DKWWS_S3_REGION             signing region (default us-east-1)
  DKWWS_S3_BUCKET             the bucket to upload into
  DKWWS_S3_ACCESS_KEY_ID      this machine's access key
  DKWWS_S3_SECRET_ACCESS_KEY  this machine's secret key
  DKWWS_S3_PATH_STYLE         path-style addressing (default true)
  DKWWS_PUBLIC_BASE_URL       where the bucket is publicly served, if that is
                              not the endpoint itself
`

// Run executes one command and returns the process exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.Stderr, usage)
		return 2
	}
	var err error
	switch cmd := args[0]; cmd {
	case "upload":
		err = a.upload(ctx, args[1:])
	case "version", "--version", "-version":
		fmt.Fprintln(a.Stdout, Version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(a.Stderr, usage)
		return 0
	default:
		fmt.Fprintf(a.Stderr, "dkwws: unknown command %q\n\n", cmd)
		fmt.Fprint(a.Stderr, usage)
		return 2
	}
	if err != nil {
		if err == flag.ErrHelp {
			return 2
		}
		fmt.Fprintf(a.Stderr, "dkwws: %v\n", err)
		return 1
	}
	return 0
}

// newFlagSet returns a flag set that reports errors on stderr rather than
// exiting the process, so Run stays in control of the exit code.
func (a *App) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	return fs
}

// openStore loads configuration and connects to the backend.
func (a *App) openStore() (*store.Store, config.Config, error) {
	cfg, src, err := config.Load()
	if err != nil {
		return nil, cfg, err
	}
	if err := cfg.Validate(); err != nil {
		if src.FilePath != "" {
			return nil, cfg, fmt.Errorf("%w (config file: %s)", err, src.FilePath)
		}
		return nil, cfg, err
	}
	s, err := store.New(cfg)
	if err != nil {
		return nil, cfg, err
	}
	return s, cfg, nil
}
