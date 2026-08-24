package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patriksimms/dkwws/internal/cli"
	"github.com/patriksimms/dkwws/internal/config"
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
	viewerBase     = "https://dkwws.example.com"
	htmlBody       = "<!doctype html><title>plan</title><p>ship it"
)

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type harness struct {
	fake   *s3fake.Server
	app    *cli.App
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	clock  time.Time
}

// newHarness runs the CLI exactly as a user would: configuration comes from
// the environment, the AWS SDK signs every request and the fake backend
// verifies those signatures independently.
func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := s3fake.New("dkwws")
	fake.AddCredential(uploaderKey, uploaderSecret, s3fake.UploaderPermissions)
	fake.AddCredential(viewerKey, viewerSecret, s3fake.ViewerPermissions)
	t.Cleanup(fake.Close)

	h := &harness{
		fake:   fake,
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		clock:  now,
	}
	h.app = &cli.App{Stdout: h.stdout, Stderr: h.stderr, Now: func() time.Time { return h.clock }}

	// Point the loader at an empty temp file so the developer's own config
	// cannot reach the test.
	t.Setenv(config.KeyConfigFile, filepath.Join(t.TempDir(), "config"))
	t.Setenv(config.KeyEndpoint, fake.URL)
	t.Setenv(config.KeyRegion, config.DefaultRegion)
	t.Setenv(config.KeyBucket, "dkwws")
	t.Setenv(config.KeyAccessKeyID, uploaderKey)
	t.Setenv(config.KeySecretKey, uploaderSecret)
	t.Setenv(config.KeyPathStyle, "true")
	t.Setenv(config.KeyViewerBaseURL, viewerBase)
	return h
}

func (h *harness) run(t *testing.T, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	return h.app.Run(t.Context(), args)
}

// mustRun runs a command that is expected to succeed and returns stdout.
func (h *harness) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	if code := h.run(t, args...); code != 0 {
		t.Fatalf("dkwws %s exited %d\nstderr: %s", strings.Join(args, " "), code, h.stderr)
	}
	return h.stdout.String()
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// serveViewer starts a viewer with read-only credentials against the same
// bucket, so a share URL can actually be fetched in a test.
func (h *harness) serveViewer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := store.New(h.fake.Config(viewerKey, viewerSecret))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(viewer.New(s, viewer.Options{Now: func() time.Time { return h.clock }}))
	t.Cleanup(srv.Close)
	return srv
}

// An authenticated machine uploads a file and gets a working share URL, with
// nothing but that URL on stdout.
func TestUploadPrintsOnlyTheURL(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "plan.html", htmlBody)

	stdout := h.mustRun(t, "upload", path)
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout has %d lines, want exactly the URL:\n%s", len(lines), stdout)
	}
	token, err := link.TokenFromShareURL(lines[0])
	if err != nil {
		t.Fatalf("stdout is not a share URL: %v", err)
	}
	if want := viewerBase + "/s/" + token + "/plan.html"; lines[0] != want {
		t.Errorf("URL = %q, want %q", lines[0], want)
	}
	if h.stderr.Len() == 0 {
		t.Error("progress output should go to stderr")
	}
	if got := h.fake.PutCount("objects/"); got != 1 {
		t.Errorf("object uploads = %d, want 1", got)
	}
}

func TestUploadedLinkResolvesThroughTheViewer(t *testing.T) {
	h := newHarness(t)
	srv := h.serveViewer(t)
	path := writeFile(t, "plan.html", htmlBody)

	url := strings.TrimSpace(h.mustRun(t, "upload", path))
	token, err := link.TokenFromShareURL(url)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := srv.Client().Get(srv.URL + "/s/" + token + "/plan.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("viewer status = %d, want 200", resp.StatusCode)
	}
	body := &bytes.Buffer{}
	body.ReadFrom(resp.Body)
	if body.String() != htmlBody {
		t.Errorf("served body = %q, want %q", body, htmlBody)
	}
	if got := resp.Header.Get("Content-Type"); got != link.HTMLContentType {
		t.Errorf("Content-Type = %q, want %q", got, link.HTMLContentType)
	}
}

func TestUploadJSONOutput(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "plan.html", htmlBody)

	var result cli.Result
	if err := json.Unmarshal([]byte(h.mustRun(t, "upload", "-json", path)), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	sum := sha256.Sum256([]byte(htmlBody))
	if result.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %q, want the hash of the file", result.SHA256)
	}
	if result.Size != int64(len(htmlBody)) {
		t.Errorf("size = %d, want %d", result.Size, len(htmlBody))
	}
	if result.ContentType != link.HTMLContentType {
		t.Errorf("content_type = %q", result.ContentType)
	}
	if !strings.HasPrefix(result.Object, "objects/") {
		t.Errorf("object = %q, want an objects/ reference", result.Object)
	}
	if !result.ExpiresAt.Equal(now.Add(link.DefaultTTL)) {
		t.Errorf("expires_at = %v, want %v", result.ExpiresAt, now.Add(link.DefaultTTL))
	}
	if result.URL == "" || result.Token == "" {
		t.Errorf("result = %+v, want a url and token", result)
	}
}

// Two uploads that happen to share a filename must stay independent.
func TestSameFilenameUploadsDoNotCollide(t *testing.T) {
	h := newHarness(t)
	first := writeFile(t, "plan.html", "<p>first")
	second := writeFile(t, "plan.html", "<p>second")

	var results []cli.Result
	for _, path := range []string{first, second} {
		var r cli.Result
		if err := json.Unmarshal([]byte(h.mustRun(t, "upload", "-json", path)), &r); err != nil {
			t.Fatal(err)
		}
		results = append(results, r)
	}

	if results[0].Object == results[1].Object {
		t.Fatalf("both uploads landed on %s", results[0].Object)
	}
	if results[0].URL == results[1].URL {
		t.Fatal("both uploads produced the same URL")
	}
	for i, want := range []string{"<p>first", "<p>second"} {
		body, ok := h.fake.Object(results[i].Object)
		if !ok {
			t.Fatalf("%s is missing", results[i].Object)
		}
		if string(body) != want {
			t.Errorf("%s = %q, want %q", results[i].Object, body, want)
		}
	}
}

// Sanitising a name that is entirely non-ASCII can strip the extension, so the
// content type has to be guessed from the name the user actually gave.
func TestNonASCIIHTMLNameStillRendersAsHTML(t *testing.T) {
	h := newHarness(t)
	srv := h.serveViewer(t)
	path := writeFile(t, "plan.html", htmlBody)

	var result cli.Result
	if err := json.Unmarshal([]byte(h.mustRun(t, "upload", "-json", "-filename", "计划.html", path)), &result); err != nil {
		t.Fatal(err)
	}
	if result.ContentType != link.HTMLContentType {
		t.Errorf("content_type = %q, want %q", result.ContentType, link.HTMLContentType)
	}

	resp, err := srv.Client().Get(srv.URL + "/s/" + result.Token + "/" + result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("viewer status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != link.HTMLContentType {
		t.Errorf("Content-Type = %q, want %q", got, link.HTMLContentType)
	}
}

func TestUploadRejectsBadCredentials(t *testing.T) {
	h := newHarness(t)
	t.Setenv(config.KeySecretKey, "wrong-secret")
	path := writeFile(t, "plan.html", htmlBody)

	if code := h.run(t, "upload", path); code == 0 {
		t.Fatal("upload succeeded with a bad secret key")
	}
	if h.stdout.Len() != 0 {
		t.Errorf("failed upload wrote %q to stdout", h.stdout)
	}
	if len(h.fake.Keys("objects/")) != 0 {
		t.Errorf("failed upload stored %v", h.fake.Keys("objects/"))
	}
	// "not found" would send an operator looking in the wrong place.
	if stderr := h.stderr.String(); !strings.Contains(stderr, "credentials") {
		t.Errorf("stderr should blame the credentials, got: %s", stderr)
	}
}

func TestUploadReportsMissingConfiguration(t *testing.T) {
	h := newHarness(t)
	os.Unsetenv(config.KeyBucket)
	path := writeFile(t, "plan.html", htmlBody)

	if code := h.run(t, "upload", path); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(h.stderr.String(), config.KeyBucket) {
		t.Errorf("stderr should name the missing key, got: %s", h.stderr)
	}
}

func TestUploadRejectsOversizedFile(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "big.html", strings.Repeat("x", 2048))

	if code := h.run(t, "upload", "-max-size", "1024", path); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(h.stderr.String(), "limit") {
		t.Errorf("stderr should explain the limit, got: %s", h.stderr)
	}
	if len(h.fake.Keys("objects/")) != 0 {
		t.Error("an oversized file was uploaded anyway")
	}
}

func TestUploadRejectsDirectory(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "upload", t.TempDir()); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// Renewing an expired URL issues a fresh token and expiry against the same
// stored object, without uploading the file again.
func TestRenewExpiredLink(t *testing.T) {
	h := newHarness(t)
	srv := h.serveViewer(t)
	path := writeFile(t, "plan.html", htmlBody)

	var original cli.Result
	if err := json.Unmarshal([]byte(h.mustRun(t, "upload", "-json", path)), &original); err != nil {
		t.Fatal(err)
	}

	// Move past the expiry and confirm the link really stopped working.
	h.clock = now.Add(link.DefaultTTL).Add(time.Hour)
	resp, err := srv.Client().Get(srv.URL + "/s/" + original.Token + "/plan.html")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 410 {
		t.Fatalf("expired link status = %d, want 410", resp.StatusCode)
	}

	var renewed cli.Result
	if err := json.Unmarshal([]byte(h.mustRun(t, "renew", "-json", original.URL)), &renewed); err != nil {
		t.Fatal(err)
	}
	if renewed.Token == original.Token {
		t.Error("renew reused the old token")
	}
	if renewed.Object != original.Object {
		t.Errorf("renew pointed at %s, want the original %s", renewed.Object, original.Object)
	}
	if !renewed.ExpiresAt.Equal(h.clock.Add(link.DefaultTTL)) {
		t.Errorf("expires_at = %v, want %v", renewed.ExpiresAt, h.clock.Add(link.DefaultTTL))
	}
	if renewed.SHA256 != original.SHA256 || renewed.Size != original.Size {
		t.Errorf("renew changed the file metadata: %+v vs %+v", renewed, original)
	}
	if got := h.fake.PutCount("objects/"); got != 1 {
		t.Errorf("object uploads = %d, want the file to be stored exactly once", got)
	}

	resp, err = srv.Client().Get(srv.URL + "/s/" + renewed.Token + "/plan.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("renewed link status = %d, want 200", resp.StatusCode)
	}

	// The old link stays dead; renewing issues an additional link rather than
	// resurrecting the previous one.
	resp2, err := srv.Client().Get(srv.URL + "/s/" + original.Token + "/plan.html")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 410 {
		t.Errorf("old link status = %d, want it to stay 410", resp2.StatusCode)
	}
}

func TestRenewAcceptsBareToken(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "plan.html", htmlBody)

	var original cli.Result
	if err := json.Unmarshal([]byte(h.mustRun(t, "upload", "-json", path)), &original); err != nil {
		t.Fatal(err)
	}
	url := strings.TrimSpace(h.mustRun(t, "renew", original.Token))
	if url == original.URL {
		t.Error("renew returned the original URL")
	}
}

func TestRenewUnknownLink(t *testing.T) {
	h := newHarness(t)
	token, err := link.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if code := h.run(t, "renew", token); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("failed renew wrote %q to stdout", h.stdout)
	}
}

// The uploader credentials in this harness carry the documented policy, which
// grants no read access to objects/. Renewal must therefore stay inside
// links/ and never touch the uploaded file.
func TestRenewNeedsNoAccessToTheUploadedFile(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "plan.html", htmlBody)

	var original cli.Result
	if err := json.Unmarshal([]byte(h.mustRun(t, "upload", "-json", path)), &original); err != nil {
		t.Fatal(err)
	}
	if code := h.run(t, "renew", original.Token); code != 0 {
		t.Fatalf("renew exited %d\nstderr: %s", code, h.stderr)
	}
}

func TestRenewRejectsJunkArgument(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "renew", "https://dkwws.example.com/not-a-link"); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestUnknownCommandAndUsage(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "frobnicate"); code != 2 {
		t.Errorf("unknown command exit code = %d, want 2", code)
	}
	if code := h.run(t); code != 2 {
		t.Errorf("no arguments exit code = %d, want 2", code)
	}
	if code := h.run(t, "version"); code != 0 {
		t.Errorf("version exit code = %d, want 0", code)
	}
}
