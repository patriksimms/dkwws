package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/patriksimms/dkwws/internal/link"
)

// Result is the structured description of a share link, printed with -json.
type Result struct {
	URL         string    `json:"url"`
	Token       string    `json:"token"`
	Object      string    `json:"object"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func newResult(url, token string, rec link.Record) Result {
	return Result{
		URL:         url,
		Token:       token,
		Object:      rec.ObjectKey,
		Filename:    rec.Filename,
		ContentType: rec.ContentType,
		Size:        rec.Size,
		SHA256:      rec.SHA256,
		CreatedAt:   rec.CreatedAt,
		ExpiresAt:   rec.ExpiresAt,
	}
}

// write prints the result: the bare URL by default, or one JSON object.
func (r Result) write(w io.Writer, asJSON bool) error {
	if !asJSON {
		_, err := fmt.Fprintln(w, r.URL)
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
