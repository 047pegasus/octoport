package tunnel

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"octoport/control-plane/internal/cache"
)

// Agent represents a live, authenticated agent connection.
type Agent struct {
	ID     string
	UserID string
	Conn   *websocket.Conn

	writeCh chan []byte
	closed  chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	streams  map[uint32]*Stream
	nextID   atomic.Uint32
}

// NewAgent wires up an agent's write pump.
func NewAgent(id, userID string, conn *websocket.Conn, queueSize int) *Agent {
	a := &Agent{
		ID:      id,
		UserID:  userID,
		Conn:    conn,
		writeCh: make(chan []byte, queueSize),
		closed:  make(chan struct{}),
		streams: make(map[uint32]*Stream),
	}
	return a
}

// AllocStream reserves a stream id and registers the stream.
func (a *Agent) AllocStream(s *Stream) uint32 {
	id := a.nextID.Add(1)
	s.id = id
	s.agent = a
	a.mu.Lock()
	a.streams[id] = s
	a.mu.Unlock()
	return id
}

// Stream returns a registered stream, if any.
func (a *Agent) Stream(id uint32) (*Stream, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.streams[id]
	return s, ok
}

// RemoveStream unregisters a stream.
func (a *Agent) RemoveStream(id uint32) {
	a.mu.Lock()
	delete(a.streams, id)
	a.mu.Unlock()
}

// Enqueue queues a raw frame for the write pump. Returns false if the agent
// is gone.
func (a *Agent) Enqueue(b []byte) bool {
	select {
	case a.writeCh <- b:
		return true
	case <-a.closed:
		return false
	}
}

// Close tears the agent down, notifying every live stream and closing the
// socket.
func (a *Agent) Close() {
	a.closeOnce.Do(func() {
		close(a.closed)
		a.mu.Lock()
		streams := make([]*Stream, 0, len(a.streams))
		for _, s := range a.streams {
			streams = append(streams, s)
		}
		a.streams = map[uint32]*Stream{}
		a.mu.Unlock()
		for _, s := range streams {
			s.notifyError("agent disconnected")
		}
		_ = a.Conn.Close(websocket.StatusGoingAway, "agent closed")
	})
}

// Tunnel binds a cache entry to the streams currently carrying its traffic.
type Tunnel struct {
	Entry    *cache.TunnelEntry
	Protocol string
	Streams  map[uint32]*Stream
	Stats    *TunnelStats
}

// TunnelStats tracks lifetime counters for a tunnel (used by the GUI's
// activity / usage views). All fields are atomics.
type TunnelStats struct {
	Requests     atomic.Uint64
	BytesIn      atomic.Uint64
	BytesOut     atomic.Uint64
	LastActiveAt atomic.Int64 // unix seconds
}

// NewTunnel constructs a tunnel with a stats block.
func NewTunnel(entry *cache.TunnelEntry) *Tunnel {
	return &Tunnel{
		Entry:    entry,
		Protocol: entry.Protocol,
		Streams:  make(map[uint32]*Stream),
		Stats:    &TunnelStats{},
	}
}

// BumpActivity records one request on the tunnel and refreshes last-active.
func (t *Tunnel) BumpActivity() {
	if t.Stats == nil {
		return
	}
	t.Stats.Requests.Add(1)
	t.Stats.LastActiveAt.Store(time.Now().Unix())
}

// AddBytes records payload sizes flowing through the tunnel.
func (t *Tunnel) AddBytes(in, out int) {
	if t.Stats == nil {
		return
	}
	if in > 0 {
		t.Stats.BytesIn.Add(uint64(in))
	}
	if out > 0 {
		t.Stats.BytesOut.Add(uint64(out))
	}
}

// SnapshotStats returns a plain copy of the tunnel's stats counters.
func (t *Tunnel) SnapshotStats() (requests, bytesIn, bytesOut, lastActive uint64) {
	if t.Stats == nil {
		return 0, 0, 0, 0
	}
	return t.Stats.Requests.Load(), t.Stats.BytesIn.Load(), t.Stats.BytesOut.Load(), uint64(t.Stats.LastActiveAt.Load())
}

// Hub owns the agent and tunnel registries for the whole control plane.
type Hub struct {
	mu       sync.RWMutex
	agents   map[string]*Agent
	tunnels  map[string]*Tunnel // subdomain -> tunnel
	byAgent  map[string]map[string]struct{}
	byUser   map[string]map[string]struct{}

	// OnTunnelChange, when set, is invoked after the routing state for a
	// user's tunnels changes (bind / unbind / idle expiry / destroy). It is
	// never called while h.mu is held. The api layer wires it to push a fresh
	// tunnel list over SSE so connected GUIs stay current without polling.
	OnTunnelChange func(userID string)
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{
		agents:  make(map[string]*Agent),
		tunnels: make(map[string]*Tunnel),
		byAgent: make(map[string]map[string]struct{}),
		byUser:  make(map[string]map[string]struct{}),
	}
}

// notify fires the optional change hook (assumes h.mu is NOT held).
func (h *Hub) notify(userID string) {
	if h.OnTunnelChange != nil {
		h.OnTunnelChange(userID)
	}
}

// RegisterAgent adds an agent and all of its tunnels to the registries.
func (h *Hub) RegisterAgent(a *Agent, tunnels []*cache.TunnelEntry) {
	h.mu.Lock()
	h.agents[a.ID] = a
	subs := make(map[string]struct{}, len(tunnels))
	for _, t := range tunnels {
		subs[t.Subdomain] = struct{}{}
		h.tunnels[t.Subdomain] = NewTunnel(t)
		if h.byUser[a.UserID] == nil {
			h.byUser[a.UserID] = make(map[string]struct{})
		}
		h.byUser[a.UserID][t.Subdomain] = struct{}{}
	}
	h.byAgent[a.ID] = subs
	h.mu.Unlock()
	h.notify(a.UserID)
}

// UnregisterAgent removes an agent and its tunnels.
func (h *Hub) UnregisterAgent(a *Agent) {
	h.mu.Lock()
	delete(h.agents, a.ID)
	for sub := range h.byAgent[a.ID] {
		delete(h.tunnels, sub)
		if set := h.byUser[a.UserID]; set != nil {
			delete(set, sub)
		}
	}
	delete(h.byAgent, a.ID)
	h.mu.Unlock()
	h.notify(a.UserID)
}

// Lookup finds the tunnel serving a subdomain.
func (h *Hub) Lookup(subdomain string) (*Tunnel, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	t, ok := h.tunnels[subdomain]
	return t, ok
}

// AgentByID returns a live agent.
func (h *Hub) AgentByID(id string) (*Agent, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	a, ok := h.agents[id]
	return a, ok
}

// AgentByUser returns any live agent owned by the user. Used to bind freshly
// created tunnels to an already-connected agent so they are routable
// immediately instead of waiting for a reconnect.
func (h *Hub) AgentByUser(userID string) (*Agent, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, a := range h.agents {
		if a.UserID == userID {
			return a, true
		}
	}
	return nil, false
}

// RegisterTunnel binds a single tunnel to a live agent, making it routable
// without a reconnect. It must be called after the cache entry is updated
// with the agent id so the registries stay consistent.
func (h *Hub) RegisterTunnel(a *Agent, t *cache.TunnelEntry) {
	h.mu.Lock()
	h.tunnels[t.Subdomain] = NewTunnel(t)
	if h.byAgent[a.ID] == nil {
		h.byAgent[a.ID] = make(map[string]struct{})
	}
	h.byAgent[a.ID][t.Subdomain] = struct{}{}
	if h.byUser[a.UserID] == nil {
		h.byUser[a.UserID] = make(map[string]struct{})
	}
	h.byUser[a.UserID][t.Subdomain] = struct{}{}
	h.mu.Unlock()
	h.notify(a.UserID)
}

// AddStream records a stream on a tunnel so sweeps can reach it.
func (h *Hub) AddStream(subdomain string, s *Stream) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.tunnels[subdomain]; ok {
		if t.Streams == nil {
			t.Streams = make(map[uint32]*Stream)
		}
		t.Streams[s.id] = s
	}
}

// RemoveStream forgets a finished stream on a tunnel.
func (h *Hub) RemoveStream(subdomain string, s *Stream) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.tunnels[subdomain]; ok {
		delete(t.Streams, s.id)
	}
}

// Snapshot returns a point-in-time view of every live tunnel.
func (h *Hub) Snapshot() map[string]*Tunnel {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]*Tunnel, len(h.tunnels))
	for k, t := range h.tunnels {
		cp := *t
		cp.Streams = make(map[uint32]*Stream, len(t.Streams))
		for id, s := range t.Streams {
			cp.Streams[id] = s
		}
		out[k] = &cp
	}
	return out
}

// SnapshotByUser returns a point-in-time view of one user's live tunnels,
// indexed so it stays O(user's tunnels) regardless of global scale.
func (h *Hub) SnapshotByUser(userID string) map[string]*Tunnel {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]*Tunnel, len(h.byUser[userID]))
	for sub := range h.byUser[userID] {
		if t, ok := h.tunnels[sub]; ok {
			cp := *t
			out[sub] = &cp
		}
	}
	return out
}

// TunnelCount returns the number of live tunnels (used by status endpoints).
func (h *Hub) TunnelCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.tunnels)
}

// AgentCount returns the number of connected agents.
func (h *Hub) AgentCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.agents)
}
