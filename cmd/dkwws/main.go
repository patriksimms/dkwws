// Command dkwws uploads a self-contained file to S3-compatible object storage
// and prints a share link that expires after 30 days.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/patriksimms/dkwws/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := &cli.App{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(app.Run(ctx, os.Args[1:]))
}
