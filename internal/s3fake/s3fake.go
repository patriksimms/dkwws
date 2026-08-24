// Package s3fake is an in-process S3-compatible server covering exactly the
// contract dkwws depends on: SigV4-signed PutObject, GetObject and HeadObject
// with path-style addressing and conditional creation via If-None-Match.
//
// It exists so the delivery path can be tested end to end — real config
// loading, real AWS SDK, real signatures, real HTTP — without a container
// runtime. It also models the restricted credential scope: listing, deleting
// and every other operation are refused for all credentials.
package s3fake

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Permissions describes which key prefixes one set of credentials may read
// and write. Modelling the prefixes rather than a blanket read/write flag lets
// tests use the same narrow policy the deployment documentation grants.
type Permissions struct {
	Read  []string
	Write []string
}

// UploaderPermissions and ViewerPermissions mirror the two credential scopes
// described in the README: an uploader writes objects and link records and
// reads link records back for renewal; a viewer only reads.
var (
	UploaderPermissions = Permissions{
		Read:  []string{"links/"},
		Write: []string{"objects/", "links/"},
	}
	ViewerPermissions = Permissions{
		Read: []string{"links/", "objects/"},
	}
)

func allows(prefixes []string, key string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

type credential struct {
	secretKey string
	perms     Permissions
}

type object struct {
	body        []byte
	contentType string
	modified    time.Time
}

// Server is a running fake backend. Close it when the test ends.
type Server struct {
	*httptest.Server

	bucket string

	mu       sync.Mutex
	creds    map[string]credential
	objects  map[string]object
	putCount map[string]int
	now      time.Time
}

// New starts a fake backend serving one bucket.
func New(bucket string) *Server {
	s := &Server{
		bucket:   bucket,
		creds:    map[string]credential{},
		objects:  map[string]object{},
		putCount: map[string]int{},
		now:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// AddCredential registers an access key and what it is allowed to do.
func (s *Server) AddCredential(accessKeyID, secretKey string, perms Permissions) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[accessKeyID] = credential{secretKey: secretKey, perms: perms}
}

// PutCount reports how many successful PutObject calls landed on keys with the
// given prefix. Tests use it to prove that renewing a link does not re-upload
// the file.
func (s *Server) PutCount(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for key, count := range s.putCount {
		if strings.HasPrefix(key, prefix) {
			n += count
		}
	}
	return n
}

// Keys returns the stored keys with the given prefix, for assertions.
func (s *Server) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys
}

// Object returns a stored object's bytes.
func (s *Server) Object(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, ok := s.objects[key]
	return obj.body, ok
}

// Delete removes an object out of band, so tests can simulate a link whose
// file is gone.
func (s *Server) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "IncompleteBody", err.Error())
		return
	}

	perms, ok := s.authenticate(w, r, body)
	if !ok {
		return
	}

	bucket, key, ok := splitPath(r.URL.Path)
	if !ok || bucket != s.bucket {
		// Bucket-level operations, including listing, are never permitted.
		writeError(w, r, http.StatusForbidden, "AccessDenied", "not permitted on this bucket")
		return
	}

	switch r.Method {
	case http.MethodPut:
		if !allows(perms.Write, key) {
			writeError(w, r, http.StatusForbidden, "AccessDenied", "credentials may not write "+key)
			return
		}
		s.put(w, r, key, body)
	case http.MethodGet, http.MethodHead:
		if !allows(perms.Read, key) {
			writeError(w, r, http.StatusForbidden, "AccessDenied", "credentials may not read "+key)
			return
		}
		s.get(w, r, key)
	default:
		// Deleting, tagging, policy and multipart operations are outside the
		// contract and outside the credential scope.
		writeError(w, r, http.StatusForbidden, "AccessDenied", "operation not permitted")
	}
}

// authenticate verifies the SigV4 signature and returns the caller's
// permissions.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, body []byte) (Permissions, bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		writeError(w, r, http.StatusForbidden, "AccessDenied",
			"anonymous access is not permitted")
		return Permissions{}, false
	}
	sig, err := parseAuthorization(auth)
	if err != nil {
		writeError(w, r, http.StatusForbidden, "AuthorizationHeaderMalformed", err.Error())
		return Permissions{}, false
	}

	s.mu.Lock()
	cred, known := s.creds[sig.accessKeyID]
	s.mu.Unlock()
	if !known {
		writeError(w, r, http.StatusForbidden, "InvalidAccessKeyId",
			"the access key id does not exist")
		return Permissions{}, false
	}

	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		writeError(w, r, http.StatusBadRequest, "InvalidRequest",
			"missing x-amz-content-sha256")
		return Permissions{}, false
	}
	if !strings.HasPrefix(payloadHash, "STREAMING-") && payloadHash != "UNSIGNED-PAYLOAD" {
		if got := hexSHA256(body); got != payloadHash {
			writeError(w, r, http.StatusBadRequest, "XAmzContentSHA256Mismatch",
				"the declared payload hash does not match the body")
			return Permissions{}, false
		}
	}
	if err := sig.verify(r, cred.secretKey, payloadHash); err != nil {
		writeError(w, r, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return Permissions{}, false
	}
	return cred.perms, true
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.objects[key]; exists && r.Header.Get("If-None-Match") == "*" {
		writeError(w, r, http.StatusPreconditionFailed, "PreconditionFailed",
			"at least one of the preconditions you specified did not hold")
		return
	}
	s.objects[key] = object{
		body:        body,
		contentType: r.Header.Get("Content-Type"),
		modified:    s.now,
	}
	s.putCount[key]++
	w.Header().Set("ETag", `"`+hexSHA256(body)+`"`)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, key string) {
	s.mu.Lock()
	obj, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		writeError(w, r, http.StatusNotFound, "NoSuchKey", "the specified key does not exist")
		return
	}
	if obj.contentType != "" {
		w.Header().Set("Content-Type", obj.contentType)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
	w.Header().Set("ETag", `"`+hexSHA256(obj.body)+`"`)
	w.Header().Set("Last-Modified", obj.modified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		w.Write(obj.body)
	}
}

// splitPath splits a path-style request path into bucket and key.
func splitPath(p string) (bucket, key string, ok bool) {
	bucket, key, ok = strings.Cut(strings.TrimPrefix(p, "/"), "/")
	if bucket == "" || key == "" {
		return "", "", false
	}
	return bucket, key, true
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`+
		`<Error><Code>%s</Code><Message>%s</Message><Resource>%s</Resource><RequestId>fake</RequestId></Error>`,
		code, message, r.URL.Path)
}
