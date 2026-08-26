package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/patriksimms/dkwws/internal/s3fake"
	"github.com/patriksimms/dkwws/internal/store"
)

const (
	uploaderKey    = "AKIAUPLOADER"
	uploaderSecret = "uploader-secret"
)

func newFake(t *testing.T) *s3fake.Server {
	t.Helper()
	fake := s3fake.New("dkwws")
	fake.AddCredential(uploaderKey, uploaderSecret, s3fake.UploaderPermissions)
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
// successful upload is evidence that dkwws signs requests correctly.
func TestPutNewStoresTheObject(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, uploaderKey, uploaderSecret)

	if err := s.PutNew(context.Background(), "public/abc/plan.html", []byte("<h1>hi</h1>"), "text/html; charset=utf-8"); err != nil {
		t.Fatalf("PutNew: %v", err)
	}
	body, ok := fake.Object("public/abc/plan.html")
	if !ok {
		t.Fatal("the object was not stored")
	}
	if string(body) != "<h1>hi</h1>" {
		t.Errorf("stored body = %q", body)
	}
}

func TestPutNewRefusesToOverwrite(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, uploaderKey, uploaderSecret)
	ctx := context.Background()

	if err := s.PutNew(ctx, "public/abc/plan.html", []byte("first"), "text/plain"); err != nil {
		t.Fatalf("first PutNew: %v", err)
	}
	err := s.PutNew(ctx, "public/abc/plan.html", []byte("second"), "text/plain")
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("second PutNew error = %v, want ErrAlreadyExists", err)
	}
	if body, _ := fake.Object("public/abc/plan.html"); string(body) != "first" {
		t.Errorf("stored body = %q, want the original", body)
	}
}

// The documented uploader policy grants writes under the public prefix and
// nothing else, so the store must not depend on more than that.
func TestUploaderCannotWriteOutsideThePublicPrefix(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, uploaderKey, uploaderSecret)

	err := s.PutNew(context.Background(), "private/secret.txt", []byte("data"), "text/plain")
	if !errors.Is(err, store.ErrAccessDenied) {
		t.Fatalf("PutNew error = %v, want ErrAccessDenied", err)
	}
}

func TestUnknownCredentialsAreRejected(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, "AKIAUNKNOWN", "not-a-real-secret")

	err := s.PutNew(context.Background(), "public/abc/plan.html", []byte("data"), "text/plain")
	if !errors.Is(err, store.ErrAccessDenied) {
		t.Fatalf("PutNew error = %v, want ErrAccessDenied", err)
	}
	if len(fake.Keys("public/")) != 0 {
		t.Errorf("unknown credentials stored %v", fake.Keys("public/"))
	}
}

func TestWrongSecretIsRejected(t *testing.T) {
	fake := newFake(t)
	s := newStore(t, fake, uploaderKey, "wrong-secret")

	if err := s.PutNew(context.Background(), "public/abc/plan.html", []byte("data"), "text/plain"); err == nil {
		t.Fatal("a bad signature was accepted")
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
	if err := s.PutNew(context.Background(), "public/abc/plan.html", []byte("data"), "text/plain"); err == nil {
		t.Fatal("a request signed for the wrong region was accepted")
	}
}
