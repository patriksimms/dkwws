package link

import "testing"

func TestShareURL(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{
		"https://dkwws.example.com",
		"https://dkwws.example.com/",
	} {
		got, err := ShareURL(base, token, "plan.html")
		if err != nil {
			t.Fatalf("ShareURL(%q): %v", base, err)
		}
		want := "https://dkwws.example.com/s/" + token + "/plan.html"
		if got != want {
			t.Errorf("ShareURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestShareURLRejectsRelativeBase(t *testing.T) {
	token, _ := NewToken()
	if _, err := ShareURL("dkwws.example.com", token, "plan.html"); err == nil {
		t.Error("ShareURL accepted a base URL with no scheme")
	}
}

func TestTokenFromShareURLRoundTrip(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	url, err := ShareURL("https://dkwws.example.com", token, "plan.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		url,
		"  " + url + "  ",
		url + "?utm_source=chat",
		"https://dkwws.example.com/s/" + token,
		token,
	} {
		got, err := TokenFromShareURL(in)
		if err != nil {
			t.Fatalf("TokenFromShareURL(%q): %v", in, err)
		}
		if got != token {
			t.Errorf("TokenFromShareURL(%q) = %q, want %q", in, got, token)
		}
	}
}

func TestTokenFromShareURLRejectsJunk(t *testing.T) {
	for _, in := range []string{
		"",
		"https://dkwws.example.com/",
		"https://dkwws.example.com/s/short/plan.html",
		"not a url at all",
	} {
		if got, err := TokenFromShareURL(in); err == nil {
			t.Errorf("TokenFromShareURL(%q) = %q, want an error", in, got)
		}
	}
}
