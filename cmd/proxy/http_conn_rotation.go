package main

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ReliablyObserve/Loki-VL-proxy/internal/metrics"
)

type httpConnRotationConfig struct {
	maxAge         time.Duration
	maxAgeJitter   time.Duration
	maxRequests    int64
	overloadMaxAge time.Duration
}

type httpConnRotator struct {
	cfg      httpConnRotationConfig
	metrics  *metrics.Metrics
	pressure func() bool
	seq      atomic.Uint64
}

type httpConnState struct {
	acceptedAt     time.Time
	maxAge         time.Duration
	overloadMaxAge time.Duration
	requestCount   atomic.Int64
}

type httpConnStateContextKey struct{}

func newHTTPConnRotator(cfg httpConnRotationConfig, m *metrics.Metrics, pressure func() bool) *httpConnRotator {
	if cfg.maxAge < 0 {
		cfg.maxAge = 0
	}
	if cfg.maxAgeJitter < 0 {
		cfg.maxAgeJitter = 0
	}
	if cfg.maxRequests < 0 {
		cfg.maxRequests = 0
	}
	if cfg.overloadMaxAge < 0 {
		cfg.overloadMaxAge = 0
	}
	if cfg.maxAge == 0 && cfg.maxRequests == 0 && cfg.overloadMaxAge == 0 {
		return nil
	}
	return &httpConnRotator{
		cfg:      cfg,
		metrics:  m,
		pressure: pressure,
	}
}

func (r *httpConnRotator) Wrap(next http.Handler) http.Handler {
	if r == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		state, _ := req.Context().Value(httpConnStateContextKey{}).(*httpConnState)
		if reason := r.recordAndDecide(req, state, time.Now()); reason != "" {
			w.Header().Set("Connection", "close")
			req.Close = true
			if r.metrics != nil {
				r.metrics.RecordHTTPConnectionRotation(reason)
			}
		}
		next.ServeHTTP(w, req)
	})
}

func (r *httpConnRotator) ConnContextHook() func(context.Context, net.Conn) context.Context {
	if r == nil {
		return nil
	}
	return func(ctx context.Context, conn net.Conn) context.Context {
		seq := r.seq.Add(1)
		state := &httpConnState{
			acceptedAt:     time.Now(),
			maxAge:         jitterDuration(r.cfg.maxAge, r.cfg.maxAgeJitter, conn, seq),
			overloadMaxAge: jitterDuration(r.cfg.overloadMaxAge, r.cfg.maxAgeJitter, conn, seq+1),
		}
		return context.WithValue(ctx, httpConnStateContextKey{}, state)
	}
}

func (r *httpConnRotator) recordAndDecide(req *http.Request, state *httpConnState, now time.Time) string {
	if r == nil || req == nil || state == nil {
		return ""
	}
	if req.ProtoMajor != 1 {
		return ""
	}
	if strings.TrimSpace(req.Header.Get("Upgrade")) != "" {
		return ""
	}
	requestCount := state.requestCount.Add(1)
	age := now.Sub(state.acceptedAt)

	if r.isOverloaded() && state.overloadMaxAge > 0 && age >= state.overloadMaxAge {
		return "overload"
	}
	if r.cfg.maxRequests > 0 && requestCount >= r.cfg.maxRequests {
		return "request_limit"
	}
	if state.maxAge > 0 && age >= state.maxAge {
		return "age"
	}
	return ""
}

func (r *httpConnRotator) isOverloaded() bool {
	return r != nil && r.pressure != nil && r.pressure()
}

func jitterDuration(base, jitter time.Duration, conn net.Conn, seq uint64) time.Duration {
	if base <= 0 {
		return 0
	}
	if jitter <= 0 {
		return base
	}
	maxJitter := int64(jitter)
	if maxJitter <= 0 {
		return base
	}
	sum := fnv.New64a()
	if conn != nil {
		_, _ = sum.Write([]byte(conn.RemoteAddr().String()))
		_, _ = sum.Write([]byte(conn.LocalAddr().String()))
	}
	var seqBuf [8]byte
	binary.LittleEndian.PutUint64(seqBuf[:], seq)
	_, _ = sum.Write(seqBuf[:])
	offset := int64(sum.Sum64()%uint64((maxJitter*2)+1)) - maxJitter
	jittered := base + time.Duration(offset)
	if jittered <= 0 {
		return time.Second
	}
	return jittered
}
