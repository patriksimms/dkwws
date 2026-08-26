package link

import (
	"strings"
	"testing"
)

func TestNewObjectIDHasEnoughEntropy(t *testing.T) {
	if ObjectIDBytes*8 < 128 {
		t.Fatalf("object id entropy is %d bits, want at least 128", ObjectIDBytes*8)
	}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id, err := NewObjectID()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidObjectID(id) {
			t.Fatalf("NewObjectID produced %q, which ValidObjectID rejects", id)
		}
		if seen[id] {
			t.Fatalf("NewObjectID repeated %q", id)
		}
		seen[id] = true
	}
}

func TestValidObjectID(t *testing.T) {
	good, err := NewObjectID()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		id   string
		want bool
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
			if got := ValidObjectID(tc.id); got != tc.want {
				t.Errorf("ValidObjectID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

// The key is the whole secret, so it must land under the public prefix and
// carry the random id in its own path segment.
func TestObjectKey(t *testing.T) {
	id, err := NewObjectID()
	if err != nil {
		t.Fatal(err)
	}
	key := ObjectKey(id, "plan.html")
	if want := "public/" + id + "/plan.html"; key != want {
		t.Errorf("ObjectKey = %q, want %q", key, want)
	}
	if !strings.HasPrefix(key, Prefix) {
		t.Errorf("ObjectKey = %q, want it under %q", key, Prefix)
	}
}

func TestPublicURL(t *testing.T) {
	for _, base := range []string{
		"https://s3.example.com/dkwws",
		"https://s3.example.com/dkwws/",
	} {
		got, err := PublicURL(base, "public/abc/plan.html")
		if err != nil {
			t.Fatalf("PublicURL(%q): %v", base, err)
		}
		want := "https://s3.example.com/dkwws/public/abc/plan.html"
		if got != want {
			t.Errorf("PublicURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestPublicURLRejectsRelativeBase(t *testing.T) {
	if _, err := PublicURL("s3.example.com", "public/abc/plan.html"); err == nil {
		t.Error("PublicURL accepted a base URL with no scheme")
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

// The result becomes one segment of a URL, so it must never reintroduce a
// separator or an empty segment however it was truncated.
func TestSanitizeFilenameIsAlwaysOneSafeSegment(t *testing.T) {
	inputs := []string{"plan.html", "../../etc/passwd", "my plan.html", "", "..",
		"计划.html", strings.Repeat("a", 200), strings.Repeat("a", 200) + ".html"}
	for _, unit := range []string{"ab ", "abc ", "abcd ", "a ", "..a", "a-"} {
		inputs = append(inputs, strings.Repeat(unit, 120))
	}
	for _, in := range inputs {
		got := SanitizeFilename(in)
		if got == "" {
			t.Errorf("SanitizeFilename(%q...) is empty", in[:min(len(in), 12)])
		}
		if strings.ContainsAny(got, "/\\") {
			t.Errorf("SanitizeFilename(%q...) = %q contains a separator", in[:min(len(in), 12)], got)
		}
		if strings.HasPrefix(got, ".") || strings.HasSuffix(got, ".") || strings.HasSuffix(got, "-") {
			t.Errorf("SanitizeFilename(%q...) = %q has a stray separator at an end", in[:min(len(in), 12)], got)
		}
		if len(got) > 100 {
			t.Errorf("SanitizeFilename(%q...) is %d bytes", in[:min(len(in), 12)], len(got))
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
