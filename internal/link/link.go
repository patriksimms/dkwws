// Package link builds the storage key and public URL for an uploaded file.
//
// The key carries all the secrecy: a link is unguessable because the object id
// in its path is, not because anything checks who is asking.
package link

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"
)

const (
	// ObjectIDBytes is the amount of entropy in an object id. At 20 bytes it
	// is 160 bits, comfortably above the 128-bit minimum. This is the only
	// thing standing between a stranger and the file, so it is generous.
	ObjectIDBytes = 20

	// Prefix is the publicly readable prefix every upload lands under. The
	// rest of the bucket stays private.
	Prefix = "public/"

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

// objectIDLen is the encoded length of an object id.
var objectIDLen = encoding.EncodedLen(ObjectIDBytes)

// NewObjectID returns a fresh object identifier.
func NewObjectID() (string, error) {
	buf := make([]byte, ObjectIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return strings.ToLower(encoding.EncodeToString(buf)), nil
}

// ValidObjectID reports whether s is shaped like an object id this tool
// issued.
func ValidObjectID(s string) bool {
	if len(s) != objectIDLen {
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

// ObjectKey returns the storage key for an upload. The random id sits in the
// path rather than the filename, so the URL still ends in a real name and two
// uploads of the same filename cannot collide.
func ObjectKey(id, filename string) string {
	return Prefix + id + "/" + filename
}

// PublicURL returns the address the object is served at.
func PublicURL(base, key string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", fmt.Errorf("parse public base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("public base URL must be absolute, got %q", base)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + key
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// SanitizeFilename reduces an arbitrary path to a single URL path segment that
// is safe to put in a storage key and in a link.
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
		// The prefix cannot become empty: out was already trimmed, so it does
		// not start with a separator, and maxLen leaves room for it.
		out = strings.Trim(out[:maxLen-len(ext)], "-.") + ext
	}
	return out
}

// ContentTypeFor guesses a content type from a filename. HTML is pinned to a
// UTF-8 charset so self-contained plans render correctly in the browser.
//
// The stored content type is the only thing that decides how a browser treats
// the file, because nothing sits in front of the bucket to correct it later.
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
