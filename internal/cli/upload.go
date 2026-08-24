package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/patriksimms/dkwws/internal/config"
	"github.com/patriksimms/dkwws/internal/link"
	"github.com/patriksimms/dkwws/internal/store"
)

// DefaultMaxUploadSize is the client-side limit on a single upload.
const DefaultMaxUploadSize = 25 << 20 // 25 MiB

// maxKeyAttempts bounds the retry loop that runs when a conditional create
// loses a race. Losing twice in a row is already astronomically unlikely with
// 128 bits of entropy; a bound just stops a misbehaving backend from spinning.
const maxKeyAttempts = 5

func (a *App) upload(ctx context.Context, args []string) error {
	fs := a.newFlagSet("upload")
	asJSON := fs.Bool("json", false, "print a JSON object instead of the bare URL")
	contentType := fs.String("content-type", "", "override the content type guessed from the file extension")
	name := fs.String("filename", "", "override the filename shown in the share URL")
	maxSize := fs.Int64("max-size", DefaultMaxUploadSize, "reject files larger than this many bytes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("upload takes exactly one file")
	}
	path := fs.Arg(0)

	body, err := readUpload(path, *maxSize)
	if err != nil {
		return err
	}

	original := firstNonEmpty(*name, path)
	filename := link.SanitizeFilename(original)
	ct := *contentType
	if ct == "" {
		// Guess from the name the user gave us. Sanitising can strip the
		// extension off a name that was entirely non-ASCII, which would serve
		// an HTML plan as a download instead of rendering it.
		ct = link.ContentTypeFor(original)
	}
	sum := sha256.Sum256(body)

	s, cfg, err := a.openStore(true)
	if err != nil {
		return err
	}

	fmt.Fprintf(a.Stderr, "uploading %s (%d bytes) to %s\n", filename, len(body), cfg.Bucket)
	objectKey, err := putNewObject(ctx, s, body, ct)
	if err != nil {
		return err
	}

	now := a.now().UTC()
	rec := link.Record{
		Version:     link.RecordVersion,
		ObjectKey:   objectKey,
		Filename:    filename,
		ContentType: ct,
		Size:        int64(len(body)),
		SHA256:      hex.EncodeToString(sum[:]),
		CreatedAt:   now,
		ExpiresAt:   now.Add(link.DefaultTTL),
	}
	token, url, err := createLink(ctx, s, cfg.ViewerBaseURL, rec)
	if err != nil {
		return err
	}

	fmt.Fprintf(a.Stderr, "link expires %s\n", rec.ExpiresAt.Format("2006-01-02 15:04 MST"))
	return newResult(url, token, rec).write(a.Stdout, *asJSON)
}

// readUpload reads the file, refusing anything over the limit. The whole body
// is held in memory: uploads are capped at a size where that is cheaper than
// the seekable-stream dance SigV4 would otherwise need.
func readUpload(path string, maxSize int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; dkwws uploads a single self-contained file", path)
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit; raise -max-size to override", path, info.Size(), maxSize)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxSize {
		return nil, fmt.Errorf("%s grew past the %d byte limit while being read", path, maxSize)
	}
	return body, nil
}

// putNewObject stores the body under a fresh random key. Conditional creation
// guarantees an upload never overwrites an existing object, so two files with
// the same name stay independent.
func putNewObject(ctx context.Context, s *store.Store, body []byte, contentType string) (string, error) {
	for attempt := 0; attempt < maxKeyAttempts; attempt++ {
		id, err := link.NewObjectID()
		if err != nil {
			return "", err
		}
		key := link.ObjectKey(id)
		err = s.PutNew(ctx, key, body, contentType)
		if err == nil {
			return key, nil
		}
		if errors.Is(err, store.ErrAccessDenied) {
			return "", fmt.Errorf("the backend refused these credentials: %w; check %s, %s and that the key may write to %s",
				err, config.KeyAccessKeyID, config.KeySecretKey, config.KeyBucket)
		}
		if !errors.Is(err, store.ErrAlreadyExists) {
			return "", fmt.Errorf("upload object: %w", err)
		}
	}
	return "", fmt.Errorf("upload object: could not find a free object key in %d attempts", maxKeyAttempts)
}

// createLink writes a link record under a fresh token and returns its URL.
func createLink(ctx context.Context, s *store.Store, baseURL string, rec link.Record) (string, string, error) {
	for attempt := 0; attempt < maxKeyAttempts; attempt++ {
		token, err := link.NewToken()
		if err != nil {
			return "", "", err
		}
		err = s.PutLink(ctx, token, rec)
		if errors.Is(err, store.ErrAlreadyExists) {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("create link record: %w", err)
		}
		url, err := link.ShareURL(baseURL, token, rec.Filename)
		if err != nil {
			return "", "", err
		}
		return token, url, nil
	}
	return "", "", fmt.Errorf("create link record: could not find a free token in %d attempts", maxKeyAttempts)
}

func firstNonEmpty(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}
