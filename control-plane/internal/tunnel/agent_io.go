package tunnel

import (
	"context"
	"errors"
	"log/slog"

	"github.com/coder/websocket"

	"octoport/control-plane/internal/protocol"
)

// WriteLoop drains the write queue, serialising all outbound frames on the
// single WebSocket connection. Run it as a goroutine.
func (a *Agent) WriteLoop(ctx context.Context) {
	for {
		select {
		case <-a.closed:
			return
		case <-ctx.Done():
			return
		case b := <-a.writeCh:
			if err := a.Conn.Write(ctx, websocket.MessageBinary, b); err != nil {
				return
			}
		}
	}
}

// ReadLoop consumes inbound frames and dispatches them to streams. It exits
// on socket error or context cancellation, then tears the agent down.
func (a *Agent) ReadLoop(ctx context.Context, maxFrame int, onClose func(*Agent)) {
	defer func() {
		a.Close()
		if onClose != nil {
			onClose(a)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		typ, data, err := a.Conn.Read(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Debug("agent read error", "agent", a.ID, "err", err)
			}
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		f, err := protocol.Decode(data, maxFrame)
		if err != nil {
			slog.Warn("dropping malformed agent frame", "agent", a.ID, "err", err)
			continue
		}
		a.dispatch(f)
	}
}

func (a *Agent) dispatch(f protocol.Frame) {
	switch f.Type {
	case protocol.MsgData, protocol.MsgClose, protocol.MsgError:
		s, ok := a.Stream(f.Stream)
		if !ok {
			return // stream already finished; stale frame
		}
		// Deliver in order. The consumer (proxy pump) calls Finish() after it
		// has drained every preceding DATA frame, so a fast CLOSE frame can
		// never cut off buffered response bytes.
		s.Dispatch(f)
	case protocol.MsgPing:
		pong, _ := protocol.Frame{Type: protocol.MsgPong, Stream: protocol.StreamControl}.Encode(protocol.DefaultMaxFrameSize)
		a.Enqueue(pong)
	case protocol.MsgOpen:
		// agents never open streams toward the proxy
		slog.Debug("ignoring agent-sent OPEN", "agent", a.ID)
	}
}
