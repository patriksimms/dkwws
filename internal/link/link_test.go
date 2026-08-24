package link

import (
	"strings"
	"testing"
	"time"
)

func TestNewTokenHasEnoughEntropy(t *testing.T) {
	if TokenBytes*8 < 128 {
		t.Fatalf("token entropy is %d bits, want at least 128", TokenBytes*8)
	}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		token, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidToken(token) {
			t.Fatalf("NewToken produced %q, which ValidToken rejects", token)
		}
		if seen[token] {
			t.Fatalf("NewToken repeated %q", token)
		}
		seen[token] = true
	}
}

func TestValidToken(t *testing.T) {
	good, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{
		{"generated", good, true},
		{"empty", "", false},
		{"too short", good[:len(good)-1], false},
		{"too long", good + "a", false},
		{"uppercase", strings.ToUpper(good), false},
		{"path traversal", strings.Repeat("../", 20)[:len(good)], false},
		{"outside base32 alphabet", strings.Repeat("1", len(good)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidToken(tc.token); got != tc.want {
				t.Errorf("ValidToken(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plan.html", "plan.html"},
		{"/tmp/report.html", "report.html"},
		{`C:\Users\x\plan.html`, "plan.html"},
		{"../../etc/passwd", "passwd"},
		{"..", "file"},
		{"", "file"},
		{"my plan (v2).html", "my-plan--v2-.html"},
		{"weird?name=1", "weird-name-1"},
		{strings.Repeat("a", 200) + ".html", strings.Repeat("a", 95) + ".html"},
	} {
		if got := SanitizeFilename(tc.in); got != tc.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeFilenameIsIdempotent(t *testing.T) {
	for _, in := range []string{"plan.html", "../../etc/passwd", "my plan.html", "", ".."} {
		once := SanitizeFilename(in)
		if twice := SanitizeFilename(once); twice != once {
			t.Errorf("SanitizeFilename(%q) = %q, then %q", in, once, twice)
		}
	}
}

func TestContentTypeFor(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plan.html", HTMLContentType},
		{"plan.HTM", HTMLContentType},
		{"notes.txt", "text/plain; charset=utf-8"},
		{"data", DefaultContentType},
	} {
		if got := ContentTypeFor(tc.in); got != tc.want {
			t.Errorf("ContentTypeFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRecordExpired(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	rec := Record{ExpiresAt: now}
	if !rec.Expired(now) {
		t.Error("a record is expired at exactly its expiry")
	}
	if rec.Expired(now.Add(-time.Nanosecond)) {
		t.Error("a record is not expired just before its expiry")
	}
	if !rec.Expired(now.Add(time.Nanosecond)) {
		t.Error("a record is expired just after its expiry")
	}
}

func TestDefaultTTLIsThirtyDays(t *testing.T) {
	if DefaultTTL != 30*24*time.Hour {
		t.Errorf("DefaultTTL = %v, want 720h", DefaultTTL)
	}
}

func TestRecordValidate(t *testing.T) {
	valid := Record{
		Version:   RecordVersion,
		ObjectKey: "objects/" + strings.Repeat("a", 26),
		Filename:  "plan.html",
		ExpiresAt: time.Now(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	for name, mutate := range map[string]func(Record) Record{
		"wrong version":     func(r Record) Record { r.Version = 99; return r },
		"key outside scope": func(r Record) Record { r.ObjectKey = "links/abc.json"; return r },
		"traversal key":     func(r Record) Record { r.ObjectKey = "objects/../links/x"; return r },
		"unsafe filename":   func(r Record) Record { r.Filename = "../x.html"; return r },
		"empty filename":    func(r Record) Record { r.Filename = ""; return r },
		"no expiry":         func(r Record) Record { r.ExpiresAt = time.Time{}; return r },
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(valid).Validate(); err == nil {
				t.Error("Validate accepted a malformed record")
			}
		})
	}
}
