package viewer_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/patriksimms/dkwws/internal/link"
	"github.com/patriksimms/dkwws/internal/s3fake"
	"github.com/patriksimms/dkwws/internal/store"
	"github.com/patriksimms/dkwws/internal/viewer"
)

const (
	uploaderKey    = "AKIAUPLOADER"
	uploaderSecret = "uploader-secret"
	viewerKey      = "AKIAVIEWER"
	viewerSecret   = "viewer-secret"
	htmlBody       = "<!doctype html><title>plan</title><p>ship it"
)

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type fixture struct {
	fake   *s3fake.Server
	server *httptest.Server
	client *http.Client
	clock  time.Time
}

// newFixture wires a viewer with read-only credentials to a fake backend and
// seeds one uploaded object.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	fake := s3fake.New("dkwws")
	fake.AddCredential(uploaderKey, uploaderSecret, s3fake.UploaderPermissions)
	fake.AddCredential(viewerKey, viewerSecret, s3fake.ViewerPermissions)
	t.Cleanup(fake.Close)

	f := &fixture{fake: fake, clock: now}

	readOnly, err := store.New(fake.Config(viewerKey, viewerSecret))
	if err != nil {
		t.Fatal(err)
	}
	handler := viewer.New(readOnly, viewer.Options{Now: func() time.Time { return f.clock }})
	f.server = httptest.NewServer(handler)
	t.Cleanup(f.server.Close)

	f.client = &http.Client{
		// Redirects are asserted on directly rather than followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return f
}

// seed writes an object and a link record with the uploader's credentials.
func (f *fixture) seed(t *testing.T, rec link.Record, body string) string {
	t.Helper()
	writer, err := store.New(f.fake.Config(uploaderKey, uploaderSecret))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.PutNew(t.Context(), rec.ObjectKey, []byte(body), rec.ContentType); err != nil {
		t.Fatal(err)
	}
	token, err := link.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.PutLink(t.Context(), token, rec); err != nil {
		t.Fatal(err)
	}
	return token
}

func (f *fixture) seedHTML(t *testing.T) string {
	t.Helper()
	id, err := link.NewObjectID()
	if err != nil {
		t.Fatal(err)
	}
	return f.seed(t, link.Record{
		Version:     link.RecordVersion,
		ObjectKey:   link.ObjectKey(id),
		Filename:    "plan.html",
		ContentType: link.HTMLContentType,
		Size:        int64(len(htmlBody)),
		CreatedAt:   now,
		ExpiresAt:   now.Add(link.DefaultTTL),
	}, htmlBody)
}

func (f *fixture) do(t *testing.T, method, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestValidHTMLLinkRendersInline(t *testing.T) {
	f := newFixture(t)
	token := f.seedHTML(t)

	resp := f.do(t, http.MethodGet, "/s/"+token+"/plan.html")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != link.HTMLContentType {
		t.Errorf("Content-Type = %q, want %q", got, link.HTMLContentType)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Errorf("Content-Disposition = %q, want it to start with inline", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != htmlBody {
		t.Errorf("body = %q, want %q", body, htmlBody)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(htmlBody)) {
		t.Errorf("Content-Length = %q, want %d", got, len(htmlBody))
	}
}

func TestResponseIsNotIndexableAndNotCachedPastExpiry(t *testing.T) {
	f := newFixture(t)
	token := f.seedHTML(t)

	resp := f.do(t, http.MethodGet, "/s/"+token+"/plan.html")
	if got := resp.Header.Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag = %q", got)
	}
	cacheControl := resp.Header.Get("Cache-Control")
	if !strings.Contains(cacheControl, "private") {
		t.Errorf("Cache-Control = %q, want it to be private", cacheControl)
	}
	maxAge := maxAgeOf(t, cacheControl)
	if maxAge > int(viewer.MaxCacheAge.Seconds()) {
		t.Errorf("max-age = %d, want at most %d", maxAge, int(viewer.MaxCacheAge.Seconds()))
	}
	expires, err := http.ParseTime(resp.Header.Get("Expires"))
	if err != nil {
		t.Fatalf("Expires header: %v", err)
	}
	if !expires.Equal(now.Add(link.DefaultTTL)) {
		t.Errorf("Expires = %v, want the link expiry %v", expires, now.Add(link.DefaultTTL))
	}
}

// Close to expiry the cache lifetime has to shrink, otherwise a browser could
// keep serving the document after the link stopped working.
func TestCacheLifetimeShrinksNearExpiry(t *testing.T) {
	f := newFixture(t)
	token := f.seedHTML(t)
	f.clock = now.Add(link.DefaultTTL).Add(-30 * time.Second)

	resp := f.do(t, http.MethodGet, "/s/"+token+"/plan.html")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if maxAge := maxAgeOf(t, resp.Header.Get("Cache-Control")); maxAge > 30 {
		t.Errorf("max-age = %d, want at most the 30s left on the link", maxAge)
	}
}

func TestExpiredLinkIsGone(t *testing.T) {
	f := newFixture(t)
	token := f.seedHTML(t)
	f.clock = now.Add(link.DefaultTTL)

	resp := f.do(t, http.MethodGet, "/s/"+token+"/plan.html")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	// The file itself must survive its link, so the link can be renewed.
	keys := f.fake.Keys("objects/")
	if len(keys) != 1 {
		t.Errorf("objects in bucket = %v, want the upload to still be there", keys)
	}
}

func TestLinkWorksUpToTheLastMoment(t *testing.T) {
	f := newFixture(t)
	token := f.seedHTML(t)
	f.clock = now.Add(link.DefaultTTL).Add(-time.Nanosecond)

	if resp := f.do(t, http.MethodGet, "/s/"+token+"/plan.html"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 just before expiry", resp.StatusCode)
	}
}

// Unknown and malformed tokens must be indistinguishable, so nobody can probe
// the bucket through the viewer.
func TestRejectedTokensLookIdentical(t *testing.T) {
	f := newFixture(t)
	f.seedHTML(t)

	unknown, err := link.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, path := range []string{
		"/s/" + unknown + "/plan.html",
		"/s/tooshort/plan.html",
		"/s/" + strings.ToUpper(unknown) + "/plan.html",
		"/s/objects%2Fabc/plan.html",
	} {
		resp := f.do(t, http.MethodGet, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		bodies = append(bodies, string(body))
	}
	for _, body := range bodies[1:] {
		if body != bodies[0] {
			t.Errorf("rejection bodies differ: %q vs %q", bodies[0], body)
		}
	}
}

func TestWrongFilenameRedirectsToCanonicalURL(t *testing.T) {
	f := newFixture(t)
	token := f.seedHTML(t)

	for _, path := range []string{"/s/" + token + "/other.html", "/s/" + token} {
		resp := f.do(t, http.MethodGet, path)
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("GET %s status = %d, want 302", path, resp.StatusCode)
		}
		if got, want := resp.Header.Get("Location"), "/s/"+token+"/plan.html"; got != want {
			t.Errorf("Location = %q, want %q", got, want)
		}
	}
}

func TestHeadReturnsMetadataWithoutBody(t *testing.T) {
	f := newFixture(t)
	token := f.seedHTML(t)

	resp := f.do(t, http.MethodHead, "/s/"+token+"/plan.html")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != link.HTMLContentType {
		t.Errorf("Content-Type = %q", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("HEAD returned a body of %d bytes", len(body))
	}
}

// A link record whose object has gone must not turn into a 500.
func TestMissingObjectBehindValidLink(t *testing.T) {
	f := newFixture(t)
	token := f.seedHTML(t)
	for _, key := range f.fake.Keys("objects/") {
		f.fake.Delete(key)
	}

	if resp := f.do(t, http.MethodGet, "/s/"+token+"/plan.html"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHealthAndRobots(t *testing.T) {
	f := newFixture(t)

	if resp := f.do(t, http.MethodGet, "/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", resp.StatusCode)
	}
	resp := f.do(t, http.MethodGet, "/robots.txt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/robots.txt status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Disallow: /") {
		t.Errorf("robots.txt = %q, want it to disallow crawling", body)
	}
}

func TestRootIsNotFound(t *testing.T) {
	f := newFixture(t)
	if resp := f.do(t, http.MethodGet, "/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/ status = %d, want 404", resp.StatusCode)
	}
}

func maxAgeOf(t *testing.T, cacheControl string) int {
	t.Helper()
	for _, part := range strings.Split(cacheControl, ",") {
		value, ok := strings.CutPrefix(strings.TrimSpace(part), "max-age=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("max-age in %q: %v", cacheControl, err)
		}
		return n
	}
	t.Fatalf("no max-age in %q", cacheControl)
	return 0
}
