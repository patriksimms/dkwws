package link

import (
	"fmt"
	"net/url"
	"strings"
)

// PathPrefix is the viewer route under which share links are served.
const PathPrefix = "/s/"

// ShareURL builds the public URL for a link record hosted by the viewer at
// base.
func ShareURL(base, token, filename string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", fmt.Errorf("parse viewer base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("viewer base URL must be absolute, got %q", base)
	}
	u.Path = strings.TrimRight(u.Path, "/") + PathPrefix + token + "/" + filename
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// TokenFromShareURL extracts the token from a share URL. It also accepts a
// bare token so that `dkwws renew` works with either form.
func TokenFromShareURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if ValidToken(s) {
		return s, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("not a share URL or token: %q", s)
	}
	// Strip everything up to and including the last /s/ segment so that a
	// viewer mounted under a path prefix still works.
	idx := strings.LastIndex(u.Path, PathPrefix)
	if idx < 0 {
		return "", fmt.Errorf("no %s segment in %q", strings.Trim(PathPrefix, "/"), s)
	}
	rest := u.Path[idx+len(PathPrefix):]
	token, _, _ := strings.Cut(rest, "/")
	if !ValidToken(token) {
		return "", fmt.Errorf("malformed share-link token in %q", s)
	}
	return token, nil
}
