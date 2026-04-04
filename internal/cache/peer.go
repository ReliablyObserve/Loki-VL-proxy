package cache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PeerCache implements a distributed cache layer (L3) between local cache and VL backend.
//
// Strategy: local-first with gossip directory
//   - L1 has data and it's fresh → serve immediately (0 hops)
//   - L1 miss → check key directory: which peers have this key?
//   - If a peer has it → fetch from that peer (1 hop)
//   - If no peer has it → fall through to VL
//   - After VL fetch → gossip "I have key K" to all peers (tiny metadata, no data)
//   - Other peers pull full data only when they actually need it
//
// This minimizes hops: if the LB sends the same query to the same peer twice,
// it's served from L1 with zero network. Different peer? Check directory first.
type PeerCache struct {
	mu           sync.RWMutex
	ring         *hashRing
	selfAddr     string // this instance's address (e.g., "10.0.0.1:3100")
	peers        []string
	client       *http.Client
	log          *slog.Logger
	done         chan struct{}
	discoveryFn  func() ([]string, error) // returns peer addresses
	discoveryInt time.Duration

	// Key directory: tracks which peers have which keys (gossip-populated).
	// key → set of peer addresses that have this key.
	// Lightweight: only stores key+addr strings, not actual data.
	keyDir     sync.Map // string → *peerSet

	// Per-peer circuit breakers (address → breaker)
	breakers   sync.Map // string → *peerBreaker

	// Singleflight to prevent cache stampede across peers
	inflight   sync.Map // key → *inflightEntry

	// Stats
	PeerHits   atomic.Int64
	PeerMisses atomic.Int64
	PeerErrors atomic.Int64
	DirHits    atomic.Int64 // key found in directory (skip hash ring)
}

// peerSet is a thread-safe set of peer addresses that have a given key.
type peerSet struct {
	mu    sync.RWMutex
	addrs map[string]time.Time // addr → when added (for expiry)
}

type inflightEntry struct {
	done   chan struct{}
	result []byte
	ok     bool
}

// PeerConfig configures the distributed peer cache.
type PeerConfig struct {
	// SelfAddr is this instance's address (ip:port).
	SelfAddr string

	// DiscoveryType: "dns" (headless service), "static" (comma-separated list), or "" (disabled).
	DiscoveryType string

	// DNSName is the headless service DNS name (e.g., "loki-vl-proxy-headless.default.svc.cluster.local").
	DNSName string

	// StaticPeers is a comma-separated list of peer addresses.
	StaticPeers string

	// Port is the peer cache HTTP port (default: 3100).
	Port int

	// DiscoveryInterval is how often to refresh peer list (default: 15s).
	DiscoveryInterval time.Duration

	// Timeout for peer HTTP requests (default: 2s).
	Timeout time.Duration

	Logger *slog.Logger
}

// NewPeerCache creates a distributed peer cache with the given configuration.
func NewPeerCache(cfg PeerConfig) *PeerCache {
	if cfg.Port == 0 {
		cfg.Port = 3100
	}
	if cfg.DiscoveryInterval == 0 {
		cfg.DiscoveryInterval = 15 * time.Second
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	pc := &PeerCache{
		selfAddr: cfg.SelfAddr,
		ring:     newHashRing(150), // 150 virtual nodes per peer
		client: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 10,
				MaxConnsPerHost:     20,
				IdleConnTimeout:     30 * time.Second,
			},
		},
		log:          cfg.Logger,
		done:         make(chan struct{}),
		discoveryInt: cfg.DiscoveryInterval,
	}

	// Configure discovery function
	switch cfg.DiscoveryType {
	case "dns":
		pc.discoveryFn = func() ([]string, error) {
			return discoverDNS(cfg.DNSName, cfg.Port)
		}
	case "static":
		staticPeers := parsePeerList(cfg.StaticPeers)
		pc.discoveryFn = func() ([]string, error) {
			return staticPeers, nil
		}
	default:
		return pc // no discovery, peer cache disabled
	}

	// Initial discovery
	if pc.discoveryFn != nil {
		if peers, err := pc.discoveryFn(); err == nil {
			pc.updatePeers(peers)
		}
		go pc.discoveryLoop()
	}

	return pc
}

// IsOwner returns true if this instance is the canonical owner for the given key.
// The proxy uses this to decide: if we're the owner, skip L3 (we ARE the authority).
// If we're not the owner, ask the owner via L3 before hitting VL.
func (pc *PeerCache) IsOwner(key string) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	if len(pc.peers) == 0 {
		return true // no peers = we're the only instance
	}
	owner := pc.ring.get(key)
	return owner == pc.selfAddr || owner == ""
}

// Set writes a value to the owning peer so it becomes the canonical holder.
// Called after fetching from VL — ensures the owner has the data for other peers.
// Fire-and-forget: doesn't block on the write.
func (pc *PeerCache) Set(key string, value []byte) {
	pc.mu.RLock()
	if len(pc.peers) == 0 {
		pc.mu.RUnlock()
		return
	}
	owner := pc.ring.get(key)
	pc.mu.RUnlock()

	// Don't write to self — L1/L2 Set already handles it
	if owner == pc.selfAddr || owner == "" {
		return
	}

	if !pc.peerAllowed(owner) {
		return
	}

	// Fire-and-forget write to owner
	go func() {
		url := fmt.Sprintf("http://%s/_cache/set?key=%s", owner, key)
		ctx, cancel := context.WithTimeout(context.Background(), pc.client.Timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(value)))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := pc.client.Do(req)
		if err != nil {
			pc.recordPeerFailure(owner)
			return
		}
		resp.Body.Close()
		pc.recordPeerSuccess(owner)
	}()
}

// Get fetches a value from a peer that has this key.
// Strategy: check key directory first (who has it?) → fetch from nearest available.
// Falls back to consistent hash owner if directory is empty.
// Returns (value, true) on hit, (nil, false) on miss.
func (pc *PeerCache) Get(key string) ([]byte, bool) {
	pc.mu.RLock()
	if len(pc.peers) == 0 {
		pc.mu.RUnlock()
		return nil, false
	}
	pc.mu.RUnlock()

	// Phase 1: Check key directory — any peer that already has this key
	target := pc.findPeerWithKey(key)

	// Phase 2: Fall back to consistent hash owner
	if target == "" {
		pc.mu.RLock()
		target = pc.ring.get(key)
		pc.mu.RUnlock()
	}

	// Don't fetch from self — L1/L2 already checked
	if target == pc.selfAddr || target == "" {
		return nil, false
	}

	// Check per-peer circuit breaker
	if !pc.peerAllowed(target) {
		pc.PeerErrors.Add(1)
		return nil, false
	}

	// Singleflight: if another goroutine is already fetching this key, wait for it
	if entry, loaded := pc.inflight.LoadOrStore(key, &inflightEntry{done: make(chan struct{})}); loaded {
		inf := entry.(*inflightEntry)
		<-inf.done
		if inf.ok {
			pc.PeerHits.Add(1)
		} else {
			pc.PeerMisses.Add(1)
		}
		return inf.result, inf.ok
	}

	inf := pc.getInflight(key)
	defer func() {
		close(inf.done)
		pc.inflight.Delete(key)
	}()

	// HTTP GET from target peer
	url := fmt.Sprintf("http://%s/_cache/get?key=%s", target, key)
	ctx, cancel := context.WithTimeout(context.Background(), pc.client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		pc.recordPeerFailure(target)
		inf.ok = false
		pc.PeerErrors.Add(1)
		return nil, false
	}

	resp, err := pc.client.Do(req)
	if err != nil {
		pc.recordPeerFailure(target)
		inf.ok = false
		pc.PeerErrors.Add(1)
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		pc.recordPeerSuccess(target)
		inf.ok = false
		pc.PeerMisses.Add(1)
		return nil, false
	}

	if resp.StatusCode != http.StatusOK {
		pc.recordPeerFailure(target)
		inf.ok = false
		pc.PeerErrors.Add(1)
		return nil, false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		pc.recordPeerFailure(target)
		inf.ok = false
		pc.PeerErrors.Add(1)
		return nil, false
	}

	pc.recordPeerSuccess(target)
	inf.result = body
	inf.ok = true
	pc.PeerHits.Add(1)
	return body, true
}

func (pc *PeerCache) getInflight(key string) *inflightEntry {
	v, _ := pc.inflight.Load(key)
	return v.(*inflightEntry)
}

// ServeHTTP handles incoming peer cache requests.
// GET  /_cache/get?key=... — return cached value (or 404)
// POST /_cache/set?key=... — store value from peer (write-through)
// POST /_cache/dir?key=...&from=... — gossip: peer announces it has a key
func (pc *PeerCache) ServeHTTP(w http.ResponseWriter, r *http.Request, localCache *Cache) {
	path := r.URL.Path
	key := r.URL.Query().Get("key")

	// Handle directory gossip (no key validation needed for from param)
	if path == "/_cache/dir" && r.Method == "POST" {
		from := r.URL.Query().Get("from")
		if key != "" && from != "" {
			pc.recordKeyFrom(key, from)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}

	if r.Method == "POST" {
		// Write-through from another peer
		body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB max
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		localCache.Set(key, body)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// GET — read from local cache
	value, ok := localCache.Get(key)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(value)
}

// findPeerWithKey checks the key directory for a peer that has this key (not self).
// Returns the peer address, or "" if no peer is known to have it.
func (pc *PeerCache) findPeerWithKey(key string) string {
	v, ok := pc.keyDir.Load(key)
	if !ok {
		return ""
	}
	ps := v.(*peerSet)
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	now := time.Now()
	for addr, added := range ps.addrs {
		if addr == pc.selfAddr {
			continue // skip self
		}
		// Expire directory entries after 5 minutes
		if now.Sub(added) > 5*time.Minute {
			continue
		}
		if pc.peerAllowed(addr) {
			pc.DirHits.Add(1)
			return addr
		}
	}
	return ""
}

// GossipHaveKey announces to all peers that this instance has the given key.
// Lightweight: only sends key name + self address (no data).
// Fire-and-forget, non-blocking.
func (pc *PeerCache) GossipHaveKey(key string) {
	// Record locally first
	pc.recordKeyLocal(key)

	pc.mu.RLock()
	peers := make([]string, len(pc.peers))
	copy(peers, pc.peers)
	pc.mu.RUnlock()

	for _, peer := range peers {
		if peer == pc.selfAddr {
			continue
		}
		go func(addr string) {
			url := fmt.Sprintf("http://%s/_cache/dir?key=%s&from=%s", addr, key, pc.selfAddr)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
			if err != nil {
				return
			}
			resp, err := pc.client.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
		}(peer)
	}
}

// recordKeyLocal records that a peer has a given key in the local directory.
func (pc *PeerCache) recordKeyLocal(key string) {
	pc.recordKeyFrom(key, pc.selfAddr)
}

// recordKeyFrom records that a specific peer has a given key.
func (pc *PeerCache) recordKeyFrom(key string, fromAddr string) {
	v, _ := pc.keyDir.LoadOrStore(key, &peerSet{addrs: make(map[string]time.Time)})
	ps := v.(*peerSet)
	ps.mu.Lock()
	ps.addrs[fromAddr] = time.Now()
	ps.mu.Unlock()
}

// PeerCount returns the number of active peers (excluding self).
func (pc *PeerCache) PeerCount() int {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	count := 0
	for _, p := range pc.peers {
		if p != pc.selfAddr {
			count++
		}
	}
	return count
}

// Peers returns the current peer list.
func (pc *PeerCache) Peers() []string {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make([]string, len(pc.peers))
	copy(result, pc.peers)
	return result
}

// Close stops the discovery loop.
func (pc *PeerCache) Close() {
	select {
	case <-pc.done:
	default:
		close(pc.done)
	}
}

// Stats returns peer cache statistics as JSON.
func (pc *PeerCache) Stats() map[string]interface{} {
	return map[string]interface{}{
		"peers":       pc.PeerCount(),
		"peer_hits":   pc.PeerHits.Load(),
		"peer_misses": pc.PeerMisses.Load(),
		"peer_errors": pc.PeerErrors.Load(),
		"dir_hits":    pc.DirHits.Load(),
	}
}

// --- Internal ---

func (pc *PeerCache) updatePeers(peers []string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.peers = peers
	pc.ring = newHashRing(150)
	for _, p := range peers {
		pc.ring.add(p)
	}
	pc.log.Info("peer cache updated", "peers", len(peers), "self", pc.selfAddr)
}

func (pc *PeerCache) discoveryLoop() {
	ticker := time.NewTicker(pc.discoveryInt)
	defer ticker.Stop()
	for {
		select {
		case <-pc.done:
			return
		case <-ticker.C:
		}
		if pc.discoveryFn == nil {
			continue
		}
		peers, err := pc.discoveryFn()
		if err != nil {
			pc.log.Warn("peer discovery failed", "error", err)
			continue
		}
		pc.updatePeers(peers)
	}
}

// --- DNS Discovery ---

func discoverDNS(name string, port int) ([]string, error) {
	ips, err := net.LookupHost(name)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup %q: %w", name, err)
	}
	peers := make([]string, 0, len(ips))
	for _, ip := range ips {
		peers = append(peers, fmt.Sprintf("%s:%d", ip, port))
	}
	sort.Strings(peers) // deterministic ordering
	return peers, nil
}

func parsePeerList(s string) []string {
	var peers []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			peers = append(peers, p)
		}
	}
	return peers
}

// --- Consistent Hash Ring ---

type hashRing struct {
	vnodes   int
	ring     []uint32
	nodeMap  map[uint32]string // hash → address
}

func newHashRing(vnodes int) *hashRing {
	return &hashRing{
		vnodes:  vnodes,
		nodeMap: make(map[uint32]string),
	}
}

func (hr *hashRing) add(addr string) {
	for i := 0; i < hr.vnodes; i++ {
		h := hashKey(fmt.Sprintf("%s#%d", addr, i))
		hr.ring = append(hr.ring, h)
		hr.nodeMap[h] = addr
	}
	sort.Slice(hr.ring, func(i, j int) bool { return hr.ring[i] < hr.ring[j] })
}

func (hr *hashRing) get(key string) string {
	if len(hr.ring) == 0 {
		return ""
	}
	h := hashKey(key)
	idx := sort.Search(len(hr.ring), func(i int) bool { return hr.ring[i] >= h })
	if idx == len(hr.ring) {
		idx = 0 // wrap around
	}
	return hr.nodeMap[hr.ring[idx]]
}

func hashKey(key string) uint32 {
	h := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint32(h[:4])
}

// --- Per-Peer Circuit Breaker ---

type peerBreaker struct {
	failures  atomic.Int64
	lastFail  atomic.Int64 // unix millis
	threshold int64
	cooldown  time.Duration
}

func (pc *PeerCache) peerAllowed(addr string) bool {
	v, _ := pc.breakers.LoadOrStore(addr, &peerBreaker{
		threshold: 5,
		cooldown:  10 * time.Second,
	})
	pb := v.(*peerBreaker)

	if pb.failures.Load() >= pb.threshold {
		// Check cooldown
		lastFail := time.UnixMilli(pb.lastFail.Load())
		if time.Since(lastFail) < pb.cooldown {
			return false // still in cooldown
		}
		// Reset after cooldown (half-open)
		pb.failures.Store(0)
	}
	return true
}

func (pc *PeerCache) recordPeerFailure(addr string) {
	v, _ := pc.breakers.LoadOrStore(addr, &peerBreaker{
		threshold: 5,
		cooldown:  10 * time.Second,
	})
	pb := v.(*peerBreaker)
	pb.failures.Add(1)
	pb.lastFail.Store(time.Now().UnixMilli())
}

func (pc *PeerCache) recordPeerSuccess(addr string) {
	v, ok := pc.breakers.Load(addr)
	if ok {
		pb := v.(*peerBreaker)
		pb.failures.Store(0)
	}
}

// MetricsJSON returns peer cache metrics as JSON bytes.
func (pc *PeerCache) MetricsJSON() []byte {
	stats := pc.Stats()
	b, _ := json.Marshal(stats)
	return b
}
