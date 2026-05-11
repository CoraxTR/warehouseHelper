package ms_workerpool

import (
	"log"
	"time"
	"warehouseHelper/internal/config"
)

type MSOutRateLimiter struct {
	ticker *time.Ticker
	ch     chan struct{}
}

func NewMSOutRateLimiter(c *config.MSConfig) *MSOutRateLimiter {
	r := &MSOutRateLimiter{
		ticker: time.NewTicker(c.TimeSpan / time.Duration(c.RequestCap)),
		ch:     make(chan struct{}, c.RequestCap),
	}
	go r.run()

	return r
}

func (r *MSOutRateLimiter) Chan() <-chan struct{} {
	return r.ch
}

func (r *MSOutRateLimiter) Wait() {
	log.Println("Waiting for Ratelimiter")
	<-r.ch
}

func (r *MSOutRateLimiter) Stop() {
	r.ticker.Stop()
	close(r.ch)
}

func (r *MSOutRateLimiter) run() {
	for range r.ticker.C {
		select {
		case r.ch <- struct{}{}:
		default:
		}
	}
}
