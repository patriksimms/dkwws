package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

// Result is the structured description of an upload, printed with -json.
type Result struct {
	URL         string `json:"url"`
	Object      string `json:"object"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
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
