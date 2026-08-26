package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patriksimms/dkwws/internal/cli"
	"github.com/patriksimms/dkwws/internal/config"
	"github.com/patriksimms/dkwws/internal/link"
	"github.com/patriksimms/dkwws/internal/s3fake"
)

const (
	uploaderKey    = "AKIAUPLOADER"
	uploaderSecret = "uploader-secret"
	htmlBody       = "<!doctype html><title>plan</title><p>ship it"
)

type harness struct {
	fake   *s3fake.Server
	app    *cli.App
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

// newHarness runs the CLI exactly as a user would: configuration comes from
// the environment, the AWS SDK signs every request and the fake backend
// verifies those signatures independently.
func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := s3fake.New("dkwws")
	fake.AddCredential(uploaderKey, uploaderSecret, s3fake.UploaderPermissions)
	t.Cleanup(fake.Close)

	h := &harness{fake: fake, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	h.app = &cli.App{Stdout: h.stdout, Stderr: h.stderr}

	t.Setenv(config.KeyEndpoint, fake.URL)
	t.Setenv(config.KeyRegion, config.DefaultRegion)
	t.Setenv(config.KeyBucket, "dkwws")
	t.Setenv(config.KeyAccessKeyID, uploaderKey)
	t.Setenv(config.KeySecretKey, uploaderSecret)
	t.Setenv(config.KeyPathStyle, "true")
	t.Setenv(config.KeyPublicBaseURL, "")
	os.Unsetenv(config.KeyPublicBaseURL)
	return h
}

func (h *harness) run(t *testing.T, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	return h.app.Run(t.Context(), args)
}

func (h *harness) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	if code := h.run(t, args...); code != 0 {
		t.Fatalf("dkwws %s exited %d\nstderr: %s", strings.Join(args, " "), code, h.stderr)
	}
	return h.stdout.String()
}

func (h *harness) uploadJSON(t *testing.T, args ...string) cli.Result {
	t.Helper()
	var r cli.Result
	if err := json.Unmarshal([]byte(h.mustRun(t, append([]string{"upload", "-json"}, args...)...)), &r); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	return r
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// get fetches a URL with no credentials at all, as a stranger would.
func get(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// An authenticated machine uploads a file and gets a working URL, with nothing
// but that URL on stdout.
func TestUploadPrintsOnlyTheURL(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "plan.html", htmlBody)

	stdout := h.mustRun(t, "upload", path)
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout has %d lines, want exactly the URL:\n%s", len(lines), stdout)
	}
	if !strings.HasPrefix(lines[0], h.fake.URL+"/dkwws/public/") {
		t.Errorf("URL = %q, want it under the bucket's public prefix", lines[0])
	}
	if !strings.HasSuffix(lines[0], "/plan.html") {
		t.Errorf("URL = %q, want it to end in the filename", lines[0])
	}
	if h.stderr.Len() == 0 {
		t.Error("progress output should go to stderr")
	}
}

// Anyone holding the URL can open the file without credentials, and an HTML
// upload comes back as HTML so the browser renders it.
func TestAnyoneCanOpenTheURLAndHTMLRendersInline(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "plan.html", htmlBody)

	url := strings.TrimSpace(h.mustRun(t, "upload", path))
	resp := get(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous GET = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != link.HTMLContentType {
		t.Errorf("Content-Type = %q, want %q", got, link.HTMLContentType)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != htmlBody {
		t.Errorf("body = %q, want %q", body, htmlBody)
	}
}

func TestUploadJSONOutput(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "plan.html", htmlBody)

	result := h.uploadJSON(t, path)
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
	if !strings.HasPrefix(result.Object, link.Prefix) {
		t.Errorf("object = %q, want it under %q", result.Object, link.Prefix)
	}
	if result.URL == "" || result.Filename != "plan.html" {
		t.Errorf("result = %+v", result)
	}
}

// Two uploads that happen to share a filename must stay independent.
func TestSameFilenameUploadsDoNotCollide(t *testing.T) {
	h := newHarness(t)
	first := writeFile(t, "plan.html", "<p>first")
	second := writeFile(t, "plan.html", "<p>second")

	a := h.uploadJSON(t, first)
	b := h.uploadJSON(t, second)

	if a.Object == b.Object {
		t.Fatalf("both uploads landed on %s", a.Object)
	}
	if a.URL == b.URL {
		t.Fatal("both uploads produced the same URL")
	}
	for i, want := range []string{"<p>first", "<p>second"} {
		result := []cli.Result{a, b}[i]
		body, ok := h.fake.Object(result.Object)
		if !ok {
			t.Fatalf("%s is missing", result.Object)
		}
		if string(body) != want {
			t.Errorf("%s = %q, want %q", result.Object, body, want)
		}
	}
}

// The random id is the only thing protecting the file, so it has to be in
// every URL and never repeat.
func TestEveryUploadGetsAFreshUnguessableID(t *testing.T) {
	h := newHarness(t)
	path := writeFile(t, "plan.html", htmlBody)

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		result := h.uploadJSON(t, path)
		id := strings.Split(strings.TrimPrefix(result.Object, link.Prefix), "/")[0]
		if !link.ValidObjectID(id) {
			t.Fatalf("object %q does not carry a valid id", result.Object)
		}
		if seen[id] {
			t.Fatalf("id %q was reused", id)
		}
		seen[id] = true
	}
}

// An unauthenticated caller can read a published file but nothing else.
func TestAnonymousAccessIsLimitedToThePublicPrefix(t *testing.T) {
	h := newHarness(t)
	result := h.uploadJSON(t, writeFile(t, "plan.html", htmlBody))

	if resp := get(t, h.fake.URL+"/dkwws/"+result.Object); resp.StatusCode != http.StatusOK {
		t.Errorf("published object = %d, want 200", resp.StatusCode)
	}
	// Listing the bucket, and reading anything outside the prefix.
	for _, path := range []string{"/dkwws", "/dkwws/", "/dkwws?list-type=2", "/dkwws/private/secret.txt"} {
		if resp := get(t, h.fake.URL+path); resp.StatusCode == http.StatusOK {
			t.Errorf("anonymous GET %s returned 200", path)
		}
	}
	// Uploading without credentials.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		h.fake.URL+"/dkwws/public/abc/evil.html", strings.NewReader("<p>evil"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("an unauthenticated caller was allowed to upload")
	}
}

// A missing bucket policy is the main way this design fails, and it must not
// look like success.
func TestUploadFailsWhenThePrefixIsNotPublic(t *testing.T) {
	h := newHarness(t)
	h.fake.SetPublicPrefix("nothing-is-public/")
	path := writeFile(t, "plan.html", htmlBody)

	if code := h.run(t, "upload", path); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("a URL that does not work was printed: %q", h.stdout)
	}
	stderr := h.stderr.String()
	if !strings.Contains(stderr, "not publicly readable") || !strings.Contains(stderr, "bucket policy") {
		t.Errorf("stderr should explain the bucket policy, got: %s", stderr)
	}
	// The object is stored either way; only the link is unusable.
	if len(h.fake.Keys(link.Prefix)) != 1 {
		t.Errorf("objects stored = %v, want the upload to have happened", h.fake.Keys(link.Prefix))
	}
}

func TestNoVerifySkipsTheCheck(t *testing.T) {
	h := newHarness(t)
	h.fake.SetPublicPrefix("nothing-is-public/")

	if code := h.run(t, "upload", "-no-verify", writeFile(t, "plan.html", htmlBody)); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, h.stderr)
	}
	if h.stdout.Len() == 0 {
		t.Error("-no-verify should still print the URL")
	}
}

// Sanitising a name that is entirely non-ASCII can strip the extension, so the
// content type has to be guessed from the name the user actually gave.
func TestNonASCIIHTMLNameStillRendersAsHTML(t *testing.T) {
	h := newHarness(t)
	result := h.uploadJSON(t, "-filename", "计划.html", writeFile(t, "plan.html", htmlBody))

	if result.ContentType != link.HTMLContentType {
		t.Errorf("content_type = %q, want %q", result.ContentType, link.HTMLContentType)
	}
	if got := get(t, result.URL).Header.Get("Content-Type"); got != link.HTMLContentType {
		t.Errorf("served Content-Type = %q, want %q", got, link.HTMLContentType)
	}
}

func TestPublicBaseURLOverridesTheEndpoint(t *testing.T) {
	h := newHarness(t)
	t.Setenv(config.KeyPublicBaseURL, "https://files.example.com")

	result := h.uploadJSON(t, "-no-verify", writeFile(t, "plan.html", htmlBody))
	if want := "https://files.example.com/" + result.Object; result.URL != want {
		t.Errorf("URL = %q, want %q", result.URL, want)
	}
}

func TestUploadRejectsBadCredentials(t *testing.T) {
	h := newHarness(t)
	t.Setenv(config.KeySecretKey, "wrong-secret")

	if code := h.run(t, "upload", writeFile(t, "plan.html", htmlBody)); code == 0 {
		t.Fatal("upload succeeded with a bad secret key")
	}
	if h.stdout.Len() != 0 {
		t.Errorf("failed upload wrote %q to stdout", h.stdout)
	}
	if len(h.fake.Keys(link.Prefix)) != 0 {
		t.Errorf("failed upload stored %v", h.fake.Keys(link.Prefix))
	}
	// "not found" would send an operator looking in the wrong place.
	if stderr := h.stderr.String(); !strings.Contains(stderr, "credentials") {
		t.Errorf("stderr should blame the credentials, got: %s", stderr)
	}
}

func TestUploadReportsMissingConfiguration(t *testing.T) {
	h := newHarness(t)
	os.Unsetenv(config.KeyBucket)

	if code := h.run(t, "upload", writeFile(t, "plan.html", htmlBody)); code != 1 {
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
	if len(h.fake.Keys(link.Prefix)) != 0 {
		t.Error("an oversized file was uploaded anyway")
	}
}

func TestUploadRejectsDirectory(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "upload", t.TempDir()); code != 1 {
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
