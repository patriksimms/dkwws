// Package link defines share-link tokens, object identifiers and the link
// records that connect a share link to a stored object.
package link

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"mime"
	"path"
	"strings"
	"time"
)

const (
	// TokenBytes is the amount of entropy in a share-link token. 20 bytes is
	// 160 bits, comfortably above the 128-bit minimum required for links that
	// are only protected by being unguessable.
	TokenBytes = 20
	// ObjectIDBytes is the amount of entropy in an object identifier. Object
	// ids are never public, but they must not collide across uploads.
	ObjectIDBytes = 16

	// DefaultTTL is how long a freshly created share link stays valid.
	DefaultTTL = 30 * 24 * time.Hour

	// RecordVersion is the schema version written into every link record.
	RecordVersion = 1

	// DefaultContentType is used when a file extension carries no useful hint.
	DefaultContentType = "application/octet-stream"
	// HTMLContentType is used for .html and .htm uploads so browsers render
	// them inline instead of downloading them.
	HTMLContentType = "text/html; charset=utf-8"
)

// encoding produces lowercase, unpadded base32. The alphabet is restricted to
// characters that survive being copied out of a terminal, pasted into a chat
// message and typed back by hand.
var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// tokenLen and objectIDLen are the encoded lengths of a token and an object id.
var (
	tokenLen    = encoding.EncodedLen(TokenBytes)
	objectIDLen = encoding.EncodedLen(ObjectIDBytes)
)

func randomID(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return strings.ToLower(encoding.EncodeToString(buf)), nil
}

// NewToken returns a fresh share-link token.
func NewToken() (string, error) { return randomID(TokenBytes) }

// NewObjectID returns a fresh object identifier.
func NewObjectID() (string, error) { return randomID(ObjectIDBytes) }

func validID(s string, want int) bool {
	if len(s) != want {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '2' && c <= '7') {
			continue
		}
		return false
	}
	return true
}

// ValidToken reports whether s is shaped like a token this tool issued. It is
// a cheap filter that lets the viewer reject junk before touching storage.
func ValidToken(s string) bool { return validID(s, tokenLen) }

// ValidObjectID reports whether s is shaped like an object id this tool issued.
func ValidObjectID(s string) bool { return validID(s, objectIDLen) }

// ObjectKey returns the storage key holding the bytes of an uploaded file.
func ObjectKey(id string) string { return "objects/" + id }

// LinkKey returns the storage key holding a link record.
func LinkKey(token string) string { return "links/" + token + ".json" }

// Record is the private JSON document stored at LinkKey. It points at an
// uploaded object and carries everything the viewer needs to serve it.
type Record struct {
	Version     int       `json:"version"`
	ObjectKey   string    `json:"object_key"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Expired reports whether the link has passed its expiry at time now.
func (r Record) Expired(now time.Time) bool { return !now.Before(r.ExpiresAt) }

// Validate checks that a record read back from storage is usable. A record
// that fails validation is treated exactly like a missing one.
func (r Record) Validate() error {
	if r.Version != RecordVersion {
		return fmt.Errorf("unsupported link record version %d", r.Version)
	}
	id, ok := strings.CutPrefix(r.ObjectKey, "objects/")
	if !ok || !ValidObjectID(id) {
		return fmt.Errorf("link record has malformed object key")
	}
	if r.Filename == "" || r.Filename != SanitizeFilename(r.Filename) {
		return fmt.Errorf("link record has malformed filename")
	}
	if r.ExpiresAt.IsZero() {
		return fmt.Errorf("link record has no expiry")
	}
	return nil
}

// SanitizeFilename reduces an arbitrary path to a single URL path segment that
// is safe to put in a link and to echo back in a Content-Disposition header.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	if name == "." || name == "/" || name == ".." {
		return "file"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "file"
	}
	const maxLen = 100
	if len(out) > maxLen {
		ext := path.Ext(out)
		if len(ext) > 16 {
			ext = ""
		}
		out = out[:maxLen-len(ext)] + ext
	}
	return out
}

// ContentTypeFor guesses a content type from a filename. HTML is pinned to a
// UTF-8 charset so self-contained plans render correctly in the browser.
func ContentTypeFor(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".html", ".htm":
		return HTMLContentType
	}
	if ct := mime.TypeByExtension(strings.ToLower(path.Ext(filename))); ct != "" {
		return ct
	}
	return DefaultContentType
}
