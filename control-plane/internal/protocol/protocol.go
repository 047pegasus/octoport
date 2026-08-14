// Package protocol defines the binary framing used between the control plane
// and agents over a single WebSocket connection.
//
// One WebSocket binary message == one Frame. Multiple logical streams are
// multiplexed over the same socket using a 32-bit stream id.
//
// Frame layout (all integers big-endian):
//
//	+------+----------+---------+-------------------+
//	| type | streamId |  flags  |     payload       |
//	+------+----------+---------+-------------------+
//	| 1B   | 4B       | 4B      | variable (<=max)  |
//	+------+----------+---------+-------------------+
//
// Message types:
//
//	OPEN  (1): proxy -> agent, payload is JSON OpenMeta
//	DATA  (2): raw bytes in either direction
//	CLOSE (3): end of a stream (EOF / graceful)
//	ERROR (4): stream failed, payload is a reason string
//	PING  (5) / PONG (6): keep-alive at the socket level
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Message types.
const (
	MsgOpen  byte = 1
	MsgData  byte = 2
	MsgClose byte = 3
	MsgError byte = 4
	MsgPing  byte = 5
	MsgPong  byte = 6
)

// Stream id reserved for connection-level messages (ping/pong).
const StreamControl uint32 = 0

const (
	// HeaderSize is the fixed part of a frame.
	HeaderSize = 9
	// DefaultMaxFrameSize caps a single DATA payload.
	DefaultMaxFrameSize = 1 << 20
)

// Frame is a single protocol message.
type Frame struct {
	Type    byte
	Stream  uint32
	Flags   uint32
	Payload []byte
}

// ErrFrameTooBig is returned when a frame exceeds the configured maximum.
var ErrFrameTooBig = errors.New("protocol: frame exceeds max size")

// Encode serialises a frame to its wire representation.
func (f Frame) Encode(maxSize int) ([]byte, error) {
	if len(f.Payload) > maxSize {
		return nil, ErrFrameTooBig
	}
	buf := make([]byte, HeaderSize+len(f.Payload))
	buf[0] = f.Type
	binary.BigEndian.PutUint32(buf[1:5], f.Stream)
	binary.BigEndian.PutUint32(buf[5:9], f.Flags)
	copy(buf[HeaderSize:], f.Payload)
	return buf, nil
}

// Decode parses a frame from wire bytes.
func Decode(b []byte, maxSize int) (Frame, error) {
	if len(b) < HeaderSize {
		return Frame{}, fmt.Errorf("protocol: short frame (%d bytes)", len(b))
	}
	if len(b) > maxSize+HeaderSize {
		return Frame{}, ErrFrameTooBig
	}
	return Frame{
		Type:    b[0],
		Stream:  binary.BigEndian.Uint32(b[1:5]),
		Flags:   binary.BigEndian.Uint32(b[5:9]),
		Payload: b[HeaderSize:],
	}, nil
}

// OpenMeta tells an agent what to dial and how when the proxy opens a stream.
type OpenMeta struct {
	Stream   uint32 `json:"stream"`
	Protocol string `json:"protocol"` // "http" | "tcp"
	Target   string `json:"target"`   // local address the agent dials
	Host     string `json:"host"`     // public Host the client used
	TLS      bool   `json:"tls"`      // whether the client connection was TLS
}
