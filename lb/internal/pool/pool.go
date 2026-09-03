package pool

import "sync/atomic"

type Backend struct {
	Addr    string
	healthy atomic.Bool
}

type Pool struct {
	backends []*Backend
	counter  atomic.Uint64
}

func New(addrs []string) *Pool {
	backends := make([]*Backend, len(addrs))
	for i, addr := range addrs {
		b := &Backend{
			Addr: addr,
		}
		b.healthy.Store(true)
		backends[i] = b
	}
	return &Pool{backends: backends}
}

func (p *Pool) Next() (*Backend, bool) {
	n := len(p.backends)
	if n == 0 {
		return nil, false
	}

	start := p.counter.Add(1)
	for i := 0; i < n; i++ {
		idx := (start + uint64(i)) % uint64(n)
		b := p.backends[idx]
		if b.healthy.Load() {
			return b, true
		}
	}
	return nil, false
}

func (p *Pool) HasHealthy() bool {
	for _, b := range p.backends {
		if b.healthy.Load() {
			return true
		}
	}
	return false
}
