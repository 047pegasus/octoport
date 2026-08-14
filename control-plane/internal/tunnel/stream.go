package tunnel

import (
	"context"
	"encoding/json"
	"sync"

	"octoport/control-plane/internal/protocol"
)

// Stream multiplexes one proxied connection over an agent's WebSocket.
// The proxy goroutine owns the stream: it reads frames from `In` and forwards
// them to the client; it closes via Finish().
type Stream struct {
	id    uint32
	agent *Agent

	In chan protocol.Frame

	done     chan struct{}
	doneOnce sync.Once

	// WriteHook is called once when the stream closes so the proxy can also
	// release its half (e.g. close the client conn). Set by the proxy side.
	WriteHook func()
}

// NewStream creates an unregistered stream with a buffered inbound queue.
func NewStream(buf int) *Stream {
	return &Stream{
		In:   make(chan protocol.Frame, buf),
		done: make(chan struct{}),
	}
}

// ID returns the assigned stream id (0 until registered).
func (s *Stream) ID() uint32 { return s.id }

// Agent returns the owning agent.
func (s *Stream) Agent() *Agent { return s.agent }

// Done is closed when the stream is finished.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Dispatch delivers a frame from the agent to the stream's consumer. It is
// called by the agent read loop.
func (s *Stream) Dispatch(f protocol.Frame) {
	select {
	case s.In <- f:
	case <-s.done:
	}
}

// Finish marks the stream complete and removes it from its agent.
func (s *Stream) Finish() {
	s.doneOnce.Do(func() {
		close(s.done)
		if s.agent != nil {
			s.agent.RemoveStream(s.id)
		}
		if s.WriteHook != nil {
			s.WriteHook()
		}
	})
}

// notifyError finishes a stream because the agent went away.
func (s *Stream) notifyError(reason string) {
	s.Finish()
	select {
	case s.In <- protocol.Frame{Type: protocol.MsgError, Stream: s.id, Payload: []byte(reason)}:
	default:
	}
}

// SendOpen queues an OPEN frame for the stream.
func (s *Stream) SendOpen(ctx context.Context, meta protocol.OpenMeta, maxFrame int) error {
	payload, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	f := protocol.Frame{Type: protocol.MsgOpen, Stream: s.id, Payload: payload}
	b, err := f.Encode(maxFrame)
	if err != nil {
		return err
	}
	s.agent.Enqueue(b)
	return nil
}

// SendData queues a DATA frame.
func (s *Stream) SendData(data []byte, maxFrame int) error {
	f := protocol.Frame{Type: protocol.MsgData, Stream: s.id, Payload: data}
	b, err := f.Encode(maxFrame)
	if err != nil {
		return err
	}
	s.agent.Enqueue(b)
	return nil
}

// SendClose queues a CLOSE frame.
func (s *Stream) SendClose() error {
	f := protocol.Frame{Type: protocol.MsgClose, Stream: s.id}
	b, err := f.Encode(protocol.DefaultMaxFrameSize)
	if err != nil {
		return err
	}
	s.agent.Enqueue(b)
	return nil
}
