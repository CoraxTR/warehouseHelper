package msapiclient

import (
	"log"
	"time"
)

type moySkladOutRateLimiter struct {
	ticker *time.Ticker
	ch     chan struct{}
}

func NewMoySkladOutRateLimiter(limit int, interval time.Duration) *moySkladOutRateLimiter {
	r := &moySkladOutRateLimiter{
		ticker: time.NewTicker(interval / time.Duration(limit)),
		ch:     make(chan struct{}, limit),
	}
	go r.run()

	return r
}

func (r *moySkladOutRateLimiter) Chan() <-chan struct{} {
	return r.ch
}

func (r *moySkladOutRateLimiter) Wait() {
	log.Println("Waiting fo Ratelimiter")
	<-r.ch
}

func (r *moySkladOutRateLimiter) Stop() {
	r.ticker.Stop()
	close(r.ch)
}

func (r *moySkladOutRateLimiter) run() {
	for range r.ticker.C {
		select {
		case r.ch <- struct{}{}:
		default:
		}
	}
}
