package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/ah-naf/pastebin/lb/internal/pool"
)

type Proxy struct {
	pool *pool.Pool
}

func New(p *pool.Pool) *Proxy {
	return &Proxy{pool: p}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		r.Body.Close()
		bodyBytes = b
	}

	p.forward(w, r, bodyBytes, 2)
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, bodyBytes []byte, attemptsLeft int) {
	backend, ok := p.pool.Next()
	if !ok {
		http.Error(w, "no healthy backend available", http.StatusServiceUnavailable)
		return
	}

	target, err := url.Parse(backend.Addr)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	rp := httputil.NewSingleHostReverseProxy(target)
	failed := false
	rp.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		failed = true
	}
	rp.ServeHTTP(w, r)

	if failed {
		if attemptsLeft > 1 {
			p.forward(w, r, bodyBytes, attemptsLeft-1)
			return
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

}
