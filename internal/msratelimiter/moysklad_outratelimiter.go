package msratelimiter

import (
	"log"
	"time"
	"warehouseHelper/internal/config"
)

type MoySkladOutRateLimiter struct {
	ticker *time.Ticker
	ch     chan struct{}
}

func NewMoySkladOutRateLimiter(c *config.MoySkladConfig) *MoySkladOutRateLimiter {
	r := &MoySkladOutRateLimiter{
		ticker: time.NewTicker(c.TimeSpan / time.Duration(c.RequestCap)),
		ch:     make(chan struct{}, c.RequestCap),
	}
	go r.run()

	return r
}

func (r *MoySkladOutRateLimiter) Chan() <-chan struct{} {
	return r.ch
}

func (r *MoySkladOutRateLimiter) Wait() {
	log.Println("Waiting fo Ratelimiter")
	<-r.ch
}

func (r *MoySkladOutRateLimiter) Stop() {
	r.ticker.Stop()
	close(r.ch)
}

func (r *MoySkladOutRateLimiter) run() {
	for range r.ticker.C {
		select {
		case r.ch <- struct{}{}:
		default:
		}
	}
}
