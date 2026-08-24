// Package viewer serves share links. It resolves a link token to a private
// link record, checks the expiry and streams the referenced object.
//
// The viewer only ever needs read access to the bucket.
package viewer

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/patriksimms/dkwws/internal/link"
	"github.com/patriksimms/dkwws/internal/store"
)

// MaxCacheAge bounds how long a client may cache a response. Responses are
// never cacheable past the link's own expiry, so a link really does stop
// working when it expires.
const MaxCacheAge = 5 * time.Minute

// Handler serves the viewer routes.
type Handler struct {
	store  *store.Store
	logger *slog.Logger
	now    func() time.Time
}

// Options configure a Handler.
type Options struct {
	Logger *slog.Logger
	// Now overrides the clock. Tests use this to cross an expiry boundary.
	Now func() time.Time
}

// New returns an http.Handler serving /healthz, /robots.txt and share links.
func New(s *store.Store, opts Options) http.Handler {
	h := &Handler{store: s, logger: opts.Logger, now: opts.Now}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	if h.now == nil {
		h.now = time.Now
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writePlain(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		writePlain(w, http.StatusOK, "User-agent: *\nDisallow: /\n")
	})
	mux.HandleFunc("GET /s/{token}", h.serveCanonical)
	mux.HandleFunc("GET /s/{token}/{filename}", h.serveLink)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writePlain(w, http.StatusNotFound, "not found")
	})
	return mux
}

// serveCanonical redirects a link URL that lost its filename back to the full
// form, so that the browser sees a sensible name for the document.
func (h *Handler) serveCanonical(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.lookup(w, r, r.PathValue("token"))
	if !ok {
		return
	}
	http.Redirect(w, r, "/s/"+r.PathValue("token")+"/"+rec.Filename, http.StatusFound)
}

func (h *Handler) serveLink(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	rec, ok := h.lookup(w, r, token)
	if !ok {
		return
	}
	if r.PathValue("filename") != rec.Filename {
		http.Redirect(w, r, "/s/"+token+"/"+rec.Filename, http.StatusFound)
		return
	}

	h.setResponseHeaders(w, rec)

	if r.Method == http.MethodHead {
		info, err := h.store.Head(r.Context(), rec.ObjectKey)
		if err != nil {
			h.objectError(w, token, err)
			return
		}
		h.setContentHeaders(w, rec, info)
		w.WriteHeader(http.StatusOK)
		return
	}

	body, info, err := h.store.Get(r.Context(), rec.ObjectKey)
	if err != nil {
		h.objectError(w, token, err)
		return
	}
	defer body.Close()

	h.setContentHeaders(w, rec, info)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		// The status line is already sent; all that is left is to record it.
		h.logger.Warn("share link body truncated",
			"token", tokenPrefix(token), "error", err)
	}
}

// lookup resolves a token to a valid, unexpired record, writing the error
// response itself when it cannot.
func (h *Handler) lookup(w http.ResponseWriter, r *http.Request, token string) (link.Record, bool) {
	if !link.ValidToken(token) {
		h.deny(w, token, "malformed token")
		return link.Record{}, false
	}
	rec, err := h.store.GetLink(r.Context(), token)
	if errors.Is(err, store.ErrNotFound) {
		h.deny(w, token, "unknown token")
		return link.Record{}, false
	}
	if err != nil {
		h.logger.Error("resolve share link", "token", tokenPrefix(token), "error", err)
		w.Header().Set("Cache-Control", "no-store")
		writePlain(w, http.StatusBadGateway, "storage backend unavailable")
		return link.Record{}, false
	}
	if rec.Expired(h.now()) {
		h.logger.Info("share link expired", "token", tokenPrefix(token))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		writePlain(w, http.StatusGone, "this link has expired")
		return link.Record{}, false
	}
	return rec, true
}

// deny answers every rejected token identically, so a caller cannot tell an
// unknown token from one that exists but is not theirs.
func (h *Handler) deny(w http.ResponseWriter, token, reason string) {
	h.logger.Info("share link rejected", "token", tokenPrefix(token), "reason", reason)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	writePlain(w, http.StatusNotFound, "not found")
}

// objectError handles a link record that resolves to an object the viewer
// cannot read. That is a storage-side inconsistency, not a client error.
func (h *Handler) objectError(w http.ResponseWriter, token string, err error) {
	status := http.StatusBadGateway
	message := "storage backend unavailable"
	if errors.Is(err, store.ErrNotFound) {
		status, message = http.StatusNotFound, "not found"
	}
	h.logger.Error("read linked object", "token", tokenPrefix(token), "error", err)
	w.Header().Set("Cache-Control", "no-store")
	writePlain(w, status, message)
}

// setResponseHeaders applies the caching and indexing rules that make a link
// stop working at its expiry.
func (h *Handler) setResponseHeaders(w http.ResponseWriter, rec link.Record) {
	remaining := rec.ExpiresAt.Sub(h.now())
	maxAge := remaining
	if maxAge > MaxCacheAge {
		maxAge = MaxCacheAge
	}
	if maxAge < 0 {
		maxAge = 0
	}
	w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d, must-revalidate", int(maxAge.Seconds())))
	w.Header().Set("Expires", rec.ExpiresAt.UTC().Format(http.TimeFormat))
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (h *Handler) setContentHeaders(w http.ResponseWriter, rec link.Record, info store.ObjectInfo) {
	contentType := rec.ContentType
	if contentType == "" {
		contentType = info.ContentType
	}
	if contentType == "" {
		contentType = link.DefaultContentType
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("inline", map[string]string{"filename": rec.Filename}))
	if size := info.Size; size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintln(w, body)
}

// tokenPrefix truncates a token for logging. A full token in a log line is a
// working share link for anyone who can read the logs.
func tokenPrefix(token string) string {
	const n = 6
	if len(token) <= n {
		return token
	}
	return token[:n] + "..."
}
