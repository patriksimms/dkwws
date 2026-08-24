package store_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/patriksimms/dkwws/internal/link"
	"github.com/patriksimms/dkwws/internal/s3fake"
	"github.com/patriksimms/dkwws/internal/store"
)

const (
	uploaderKey    = "AKIAUPLOADER"
	uploaderSecret = "uploader-secret"
	viewerKey      = "AKIAVIEWER"
	viewerSecret   = "viewer-secret"
)

func newFake(t *testing.T) *s3fake.Server {
	t.Helper()
	fake := s3fake.New("dkwws")
	fake.AddCredential(uploaderKey, uploaderSecret, s3fake.UploaderPermissions)
	fake.AddCredential(viewerKey, viewerSecret, s3fake.ViewerPermissions)
	t.Cleanup(fake.Close)
	return fake
}

func newStore(t *testing.T, fake *s3fake.Server, key, secret string) *store.Store {
	t.Helper()
	s, err := store.New(fake.Config(key, secret))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return s
}

// The signature is verified by an independent implementation in s3fake, so a
// successful round trip is evidence that dkwws signs requests correctly.
func TestPutGetHeadRoundTrip(t *testing.T) {
	fake := newFake(t)
	uploader := newStore(t, fake, uploaderKey, uploaderSecret)
	reader := newStore(t, fake, viewerKey, viewerSecret)
	ctx := context.Background()

	if err := uploader.PutNew(ctx, "objects/abc", []byte("<h1>hi</h1>"), "text/html; charset=utf-8"); err != nil {
		t.Fatalf("PutNew: %v", err)
	}

	body, info, err := reader.Get(ctx, "objects/abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != "<h1>hi</h1>" {
		t.Errorf("body = %q, want %q", got, "<h1>hi</h1>")
	}
	if info.ContentType != "text/html; charset=utf-8" {
		t.Errorf("content type = %q", info.ContentType)
	}
	if info.Size != 11 {
		t.Errorf("size = %d, want 11", info.Size)
	}

	head, err := reader.Head(ctx, "objects/abc")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head.Size != 11 {
		t.Errorf("head size = %d, want 11", head.Size)
	}
}

func TestPutNewRefusesToOverwrite(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, uploaderKey, uploaderSecret)
	ctx := context.Background()

	if err := s.PutNew(ctx, "objects/abc", []byte("first"), "text/plain"); err != nil {
		t.Fatalf("first PutNew: %v", err)
	}
	err := s.PutNew(ctx, "objects/abc", []byte("second"), "text/plain")
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("second PutNew error = %v, want ErrAlreadyExists", err)
	}
	if body, _ := fake.Object("objects/abc"); string(body) != "first" {
		t.Errorf("stored body = %q, want the original", body)
	}
}

func TestGetMissingKeyIsNotFound(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, viewerKey, viewerSecret)

	_, _, err := s.Get(context.Background(), "objects/nope")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

// Read-only viewer credentials must not be able to write, which is what keeps
// a compromised viewer from planting a link record.
func TestViewerCredentialsCannotWrite(t *testing.T) {
	fake := newFake(t)
	reader := newStore(t, fake, viewerKey, viewerSecret)
	writer := newStore(t, fake, uploaderKey, uploaderSecret)
	ctx := context.Background()

	if err := writer.PutNew(ctx, "objects/abc", []byte("data"), "text/plain"); err != nil {
		t.Fatalf("PutNew: %v", err)
	}
	for _, key := range []string{"objects/def", "links/abc.json"} {
		if err := reader.PutNew(ctx, key, []byte("data"), "text/plain"); err == nil {
			t.Errorf("viewer credentials were allowed to write %s", key)
		}
	}
	if _, _, err := reader.Get(ctx, "objects/abc"); err != nil {
		t.Fatalf("viewer Get: %v", err)
	}
}

// The documented uploader policy grants no read access to objects/. Anything
// the CLI does beyond writing objects and reading link records would break for
// a correctly provisioned machine, so the store must not depend on it.
func TestUploaderCredentialsCannotReadObjects(t *testing.T) {
	fake := newFake(t)
	uploader := newStore(t, fake, uploaderKey, uploaderSecret)
	ctx := context.Background()

	if err := uploader.PutNew(ctx, "objects/abc", []byte("data"), "text/plain"); err != nil {
		t.Fatalf("PutNew: %v", err)
	}
	if _, _, err := uploader.Get(ctx, "objects/abc"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("uploader Get error = %v, want the denial to surface as ErrNotFound", err)
	}
}

func TestUnknownCredentialsAreRejected(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, "AKIAUNKNOWN", "not-a-real-secret")

	err := s.PutNew(context.Background(), "objects/abc", []byte("data"), "text/plain")
	if err == nil {
		t.Fatal("unknown credentials were allowed to upload")
	}
	if len(fake.Keys("objects/")) != 0 {
		t.Errorf("unknown credentials stored %v", fake.Keys("objects/"))
	}
}

// Reads mask a denial as ErrNotFound so the viewer cannot be probed, but a
// write has no bucket to probe and must say what actually happened.
func TestWriteReportsDeniedCredentialsAsSuch(t *testing.T) {
	fake := newFake(t)
	reader := newStore(t, fake, viewerKey, viewerSecret)

	err := reader.PutNew(context.Background(), "objects/abc", []byte("data"), "text/plain")
	if !errors.Is(err, store.ErrAccessDenied) {
		t.Fatalf("PutNew error = %v, want ErrAccessDenied", err)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Error("a refused write should not look like a missing object")
	}
}

// A read masks a denial as ErrNotFound, so the viewer answers identically
// whether a key is missing or off-limits.
func TestDeniedReadIsMaskedAsNotFound(t *testing.T) {
	fake := newFake(t)
	uploader := newStore(t, fake, uploaderKey, uploaderSecret)
	ctx := context.Background()

	if err := uploader.PutNew(ctx, "objects/abc", []byte("data"), "text/plain"); err != nil {
		t.Fatalf("PutNew: %v", err)
	}
	if _, _, err := uploader.Get(ctx, "objects/abc"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get error = %v, want it masked as ErrNotFound", err)
	}
}

// Only an unambiguous credential failure counts as one. S3 answers a missing
// key with 403 AccessDenied when the caller cannot list the bucket, which the
// documented viewer policy does not allow, so a bare denial says nothing about
// whether our key pair is still good.
func TestCredentialsRejectedIgnoresAmbiguousDenials(t *testing.T) {
	fake := newFake(t)
	ctx := context.Background()

	uploader := newStore(t, fake, uploaderKey, uploaderSecret)
	if err := uploader.PutNew(ctx, "objects/abc", []byte("data"), "text/plain"); err != nil {
		t.Fatalf("PutNew: %v", err)
	}
	_, _, err := uploader.Get(ctx, "objects/abc")
	if store.CredentialsRejected(err) {
		t.Errorf("a policy denial was reported as a credential failure: %v", err)
	}

	reader := newStore(t, fake, viewerKey, viewerSecret)
	if _, _, err := reader.Get(ctx, "objects/definitely-not-there"); store.CredentialsRejected(err) {
		t.Errorf("a missing object was reported as a credential failure: %v", err)
	}

	// A revoked or rotated key pair, which is the case worth an alert.
	revoked := newStore(t, fake, "AKIAREVOKED", "gone")
	if _, err := revoked.GetLink(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !store.CredentialsRejected(err) {
		t.Errorf("a revoked access key was not recognised: %v", err)
	}
}

// The fake pins the signing region, so a client that signs for the wrong one
// is rejected here just as a real backend would reject it.
func TestSigningRegionIsChecked(t *testing.T) {
	fake := newFake(t)
	cfg := fake.Config(uploaderKey, uploaderSecret)
	cfg.Region = "totally-bogus-region"
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutNew(context.Background(), "objects/abc", []byte("data"), "text/plain"); err == nil {
		t.Fatal("a request signed for the wrong region was accepted")
	}
}

func TestWrongSecretIsRejected(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, uploaderKey, "wrong-secret")

	err := s.PutNew(context.Background(), "objects/abc", []byte("data"), "text/plain")
	if err == nil {
		t.Fatal("a bad signature was accepted")
	}
}

func TestGetLinkRejectsMalformedRecords(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, uploaderKey, uploaderSecret)
	ctx := context.Background()

	token, err := link.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	// A record pointing outside objects/ must be refused, so a poisoned
	// record cannot make the viewer read an arbitrary key.
	bad := link.Record{
		Version:   link.RecordVersion,
		ObjectKey: "links/../../etc/passwd",
		Filename:  "x.html",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := s.PutLink(ctx, token, bad); err != nil {
		t.Fatalf("PutLink: %v", err)
	}
	if _, err := s.GetLink(ctx, token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetLink error = %v, want ErrNotFound", err)
	}
}

func TestGetLinkRejectsMalformedToken(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, uploaderKey, uploaderSecret)

	if _, err := s.GetLink(context.Background(), "../../objects/abc"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetLink error = %v, want ErrNotFound", err)
	}
}
