package web

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

// assetVersions hashes every static file so URLs change when content does,
// which lets browsers cache them for a year.
func assetVersions(static fs.FS) map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static, p)
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		out["/static/"+p] = hex.EncodeToString(h[:4])
		return nil
	})
	return out
}

// asset returns a static URL with its content hash.
func (s *Server) asset(p string) string {
	if v, ok := s.versions[p]; ok {
		return p + "?v=" + v
	}
	return p
}

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, 5); return w }}

type gzWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
	on      bool
}

func (g *gzWriter) WriteHeader(code int) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		ct := h.Get("Content-Type")
		if h.Get("Content-Encoding") == "" && code != http.StatusNoContent && code != http.StatusNotModified &&
			(strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "text/css") || strings.HasPrefix(ct, "text/javascript") ||
				strings.HasPrefix(ct, "application/javascript") || strings.HasPrefix(ct, "application/json") ||
				strings.HasPrefix(ct, "image/svg") || strings.HasPrefix(ct, "text/csv") || strings.HasPrefix(ct, "text/plain")) {
			g.on = true
			h.Set("Content-Encoding", "gzip")
			h.Del("Content-Length")
			g.gz = gzPool.Get().(*gzip.Writer)
			g.gz.Reset(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzWriter) Write(b []byte) (int, error) {
	if !g.decided {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.on {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// Flush keeps streaming responses (CSV export) moving.
func (g *gzWriter) Flush() {
	if g.on {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// compress gzips text responses for clients that accept it. The live stream
// is left alone so events go out immediately.
func compress(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.URL.Path == "/api/live" {
			h.ServeHTTP(w, r)
			return
		}
		g := &gzWriter{ResponseWriter: w}
		h.ServeHTTP(g, r)
		if g.on {
			_ = g.gz.Close()
			gzPool.Put(g.gz)
		}
	})
}
