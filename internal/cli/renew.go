package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/patriksimms/dkwws/internal/link"
	"github.com/patriksimms/dkwws/internal/store"
)

func (a *App) renew(ctx context.Context, args []string) error {
	fs := a.newFlagSet("renew")
	asJSON := fs.Bool("json", false, "print a JSON object instead of the bare URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("renew takes exactly one share URL or token")
	}
	oldToken, err := link.TokenFromShareURL(fs.Arg(0))
	if err != nil {
		return err
	}

	s, cfg, err := a.openStore(true)
	if err != nil {
		return err
	}

	// The old record is read regardless of its expiry: renewing an expired
	// link is the whole point of this command.
	rec, err := s.GetLink(ctx, oldToken)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no link record for that URL; it was never issued by this bucket, or the record was removed")
	}
	if err != nil {
		return fmt.Errorf("read link record: %w", err)
	}

	now := a.now().UTC()
	renewed := rec
	renewed.CreatedAt = now
	renewed.ExpiresAt = now.Add(link.DefaultTTL)

	fmt.Fprintf(a.Stderr, "renewing link for %s (%s)\n", renewed.Filename, renewed.ObjectKey)
	token, url, err := createLink(ctx, s, cfg.ViewerBaseURL, renewed)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stderr, "link expires %s\n", renewed.ExpiresAt.Format("2006-01-02 15:04 MST"))
	return newResult(url, token, renewed).write(a.Stdout, *asJSON)
}
