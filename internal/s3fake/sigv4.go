package s3fake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// This is a deliberately independent implementation of SigV4 verification. It
// shares no code with the signer under test, so a test that passes here is
// evidence that dkwws produces signatures a real S3 backend would accept.

const algorithm = "AWS4-HMAC-SHA256"

var authRe = regexp.MustCompile(
	`^AWS4-HMAC-SHA256 Credential=([^/]+)/([^,]+), ?SignedHeaders=([^,]+), ?Signature=([0-9a-f]+)$`)

type signature struct {
	accessKeyID   string
	scope         string // date/region/service/aws4_request
	signedHeaders []string
	signature     string
}

func parseAuthorization(header string) (signature, error) {
	m := authRe.FindStringSubmatch(strings.TrimSpace(header))
	if m == nil {
		return signature{}, fmt.Errorf("malformed Authorization header")
	}
	return signature{
		accessKeyID:   m[1],
		scope:         m[2],
		signedHeaders: strings.Split(m[3], ";"),
		signature:     m[4],
	}, nil
}

// verify recomputes the request signature from the secret key and compares it
// with the one the client sent.
//
// The credential scope is checked against the region and service the server
// expects rather than simply trusted. Deriving the signing key from whatever
// the client claimed would accept any region, and a client that sent the wrong
// one would pass here while a real backend rejected it.
func (s signature) verify(r *http.Request, secretKey, region, payloadHash string) error {
	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		return fmt.Errorf("missing X-Amz-Date")
	}
	parts := strings.Split(s.scope, "/")
	if len(parts) != 4 || parts[3] != "aws4_request" {
		return fmt.Errorf("malformed credential scope %q", s.scope)
	}
	if !strings.HasPrefix(amzDate, parts[0]) {
		return fmt.Errorf("credential scope date %q does not match X-Amz-Date %q", parts[0], amzDate)
	}
	if parts[1] != region {
		return fmt.Errorf("request signed for region %q, want %q", parts[1], region)
	}
	if parts[2] != "s3" {
		return fmt.Errorf("request signed for service %q, want s3", parts[2])
	}

	canonicalRequest := strings.Join([]string{
		r.Method,
		canonicalURI(r),
		canonicalQuery(r.URL),
		canonicalHeaders(r, s.signedHeaders),
		strings.Join(s.signedHeaders, ";"),
		payloadHash,
	}, "\n")

	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		s.scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	key := []byte("AWS4" + secretKey)
	for _, part := range parts {
		key = hmacSHA256(key, part)
	}
	want := hex.EncodeToString(hmacSHA256(key, stringToSign))
	if !hmac.Equal([]byte(want), []byte(s.signature)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// canonicalURI returns the path as it went over the wire. S3 signs the
// already-escaped path without escaping it a second time.
func canonicalURI(r *http.Request) string {
	path := r.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

func canonicalQuery(u *url.URL) string {
	values := u.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), values[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalHeaders renders exactly the headers the client claims to have
// signed. Anything a proxy or the Go transport appended afterwards is
// therefore correctly excluded.
func canonicalHeaders(r *http.Request, signed []string) string {
	var b strings.Builder
	for _, name := range signed {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(stripExcessSpaces(headerValue(r, name)))
		b.WriteByte('\n')
	}
	return b.String()
}

func headerValue(r *http.Request, name string) string {
	switch name {
	case "host":
		return r.Host
	case "content-length":
		return strconv.FormatInt(r.ContentLength, 10)
	}
	return strings.Join(r.Header.Values(name), ",")
}

func stripExcessSpaces(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
