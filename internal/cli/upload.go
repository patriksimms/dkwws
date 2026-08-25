package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/patriksimms/dkwws/internal/config"
	"github.com/patriksimms/dkwws/internal/link"
	"github.com/patriksimms/dkwws/internal/store"
)

// DefaultMaxUploadSize is the client-side limit on a single upload.
const DefaultMaxUploadSize = 25 << 20 // 25 MiB

// maxKeyAttempts bounds the retry loop that runs when a conditional create
// loses a race. Losing twice in a row is already astronomically unlikely with
// 160 bits of entropy; a bound just stops a misbehaving backend from spinning.
const maxKeyAttempts = 5

// verifyTimeout bounds the unauthenticated check that the upload really is
// publicly readable.
const verifyTimeout = 15 * time.Second

func (a *App) upload(ctx context.Context, args []string) error {
	fs := a.newFlagSet("upload")
	asJSON := fs.Bool("json", false, "print a JSON object instead of the bare URL")
	contentType := fs.String("content-type", "", "override the content type guessed from the file extension")
	name := fs.String("filename", "", "override the filename shown in the URL")
	maxSize := fs.Int64("max-size", DefaultMaxUploadSize, "reject files larger than this many bytes")
	skipVerify := fs.Bool("no-verify", false, "skip the check that the uploaded file is publicly readable")
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
		//
		// Nothing sits in front of the bucket to correct this later: whatever
		// is stored here is what the browser sees.
		ct = link.ContentTypeFor(original)
	}
	sum := sha256.Sum256(body)

	s, cfg, err := a.openStore()
	if err != nil {
		return err
	}
	base, err := cfg.PublicBase()
	if err != nil {
		return err
	}

	fmt.Fprintf(a.Stderr, "uploading %s (%d bytes) to %s\n", filename, len(body), cfg.Bucket)
	key, err := putNewObject(ctx, s, cfg.Bucket, filename, body, ct)
	if err != nil {
		return err
	}
	url, err := link.PublicURL(base, key)
	if err != nil {
		return err
	}

	if !*skipVerify {
		if err := verifyPublic(ctx, url); err != nil {
			return err
		}
	}

	return Result{
		URL:         url,
		Object:      key,
		Filename:    filename,
		ContentType: ct,
		Size:        int64(len(body)),
		SHA256:      hex.EncodeToString(sum[:]),
	}.write(a.Stdout, *asJSON)
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
func putNewObject(ctx context.Context, s *store.Store, bucket, filename string, body []byte, contentType string) (string, error) {
	for attempt := 0; attempt < maxKeyAttempts; attempt++ {
		id, err := link.NewObjectID()
		if err != nil {
			return "", err
		}
		key := link.ObjectKey(id, filename)
		err = s.PutNew(ctx, key, body, contentType)
		if err == nil {
			return key, nil
		}
		if errors.Is(err, store.ErrAccessDenied) {
			return "", fmt.Errorf("the backend refused these credentials: %w; check %s, %s and that the key may write to %s under %s",
				err, config.KeyAccessKeyID, config.KeySecretKey, bucket, link.Prefix)
		}
		if !errors.Is(err, store.ErrAlreadyExists) {
			return "", fmt.Errorf("upload object: %w", err)
		}
	}
	return "", fmt.Errorf("upload object: could not find a free object key in %d attempts", maxKeyAttempts)
}

// verifyPublic fetches the new URL with no credentials at all, which is the
// only way to know the bucket policy actually publishes the prefix. Without
// this, a missing policy surfaces as a colleague getting a 403 from a link
// that dkwws reported as a success.
func verifyPublic(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// A bare client: no credentials, no ambient auth, exactly what a stranger
	// opening the link would send.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("the file was uploaded to %s but the URL could not be checked: %w; retry with -no-verify to skip this check", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("the file was uploaded but %s is not publicly readable (HTTP %d); "+
			"grant unauthenticated s3:GetObject on %s* in the bucket policy, then the same URL will work",
			url, resp.StatusCode, link.Prefix)
	}
	return fmt.Errorf("the file was uploaded but %s answered HTTP %d", url, resp.StatusCode)
}

func firstNonEmpty(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}
