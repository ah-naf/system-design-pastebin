package pool

import (
	"context"
	"net/http"
	"time"
)

func (p *Pool) StartHealthChecks(ctx context.Context, interval time.Duration, client *http.Client) {
	go func() {
		p.checkAll(client)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.checkAll(client)
			}
		}
	}()
}

func (p *Pool) checkAll(client *http.Client) {
	for _, b := range p.backends {
		go func(b *Backend) {
			resp, err := client.Get(b.Addr + "/healthz")

			healthy := err == nil && resp.StatusCode == http.StatusOK

			if resp != nil {
				resp.Body.Close()
			}

			b.healthy.Store(healthy)
		}(b)
	}
}
