//! Wire protocol shared with the control plane.
//!
//! One WebSocket **binary** message == one `Frame`. Logical streams are
//! multiplexed over the socket with a 32-bit stream id. This must stay in
//! byte-for-byte sync with the Go implementation in
//! `control-plane/internal/protocol`.

use serde::{Deserialize, Serialize};

/// Message types.
pub const MSG_OPEN: u8 = 1;
pub const MSG_DATA: u8 = 2;
pub const MSG_CLOSE: u8 = 3;
pub const MSG_ERROR: u8 = 4;
pub const MSG_PING: u8 = 5;
pub const MSG_PONG: u8 = 6;

/// Stream id reserved for connection-level messages (ping/pong).
pub const STREAM_CONTROL: u32 = 0;

/// Fixed header size of a frame.
pub const HEADER_SIZE: usize = 9;

/// Default maximum size of a single frame payload.
pub const DEFAULT_MAX_FRAME_SIZE: usize = 1 << 20;

/// A single protocol message.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Frame {
    pub msg_type: u8,
    pub stream: u32,
    pub flags: u32,
    pub payload: Vec<u8>,
}

/// What the proxy tells an agent to dial when it opens a stream.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OpenMeta {
    pub stream: u32,
    pub protocol: String,
    pub target: String,
    pub host: String,
    #[serde(default)]
    pub tls: bool,
}

#[derive(Debug, thiserror::Error)]
pub enum ProtocolError {
    #[error("frame exceeds max size")]
    FrameTooBig,
    #[error("short frame: {0} bytes")]
    ShortFrame(usize),
    #[error("invalid header length")]
    BadHeader,
}

impl Frame {
    /// Serialise this frame to its wire representation.
    pub fn encode(&self, max_payload: usize) -> Result<Vec<u8>, ProtocolError> {
        if self.payload.len() > max_payload {
            return Err(ProtocolError::FrameTooBig);
        }
        let mut buf = Vec::with_capacity(HEADER_SIZE + self.payload.len());
        buf.push(self.msg_type);
        buf.extend_from_slice(&self.stream.to_be_bytes());
        buf.extend_from_slice(&self.flags.to_be_bytes());
        buf.extend_from_slice(&self.payload);
        Ok(buf)
    }

    /// Parse a frame from wire bytes.
    pub fn decode(b: &[u8], max_payload: usize) -> Result<Frame, ProtocolError> {
        if b.len() < HEADER_SIZE {
            return Err(ProtocolError::ShortFrame(b.len()));
        }
        if b.len() > max_payload + HEADER_SIZE {
            return Err(ProtocolError::FrameTooBig);
        }
        Ok(Frame {
            msg_type: b[0],
            stream: u32::from_be_bytes([b[1], b[2], b[3], b[4]]),
            flags: u32::from_be_bytes([b[5], b[6], b[7], b[8]]),
            payload: b[HEADER_SIZE..].to_vec(),
        })
    }
}

impl OpenMeta {
    /// Convenience constructor for tests / callers.
    pub fn new(stream: u32, protocol: &str, target: &str, host: &str) -> Self {
        Self {
            stream,
            protocol: protocol.to_string(),
            target: target.to_string(),
            host: host.to_string(),
            tls: false,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip() {
        for f in [
            Frame {
                msg_type: MSG_OPEN,
                stream: 1,
                flags: 0,
                payload: br#"{"stream":1,"protocol":"http"}"#.to_vec(),
            },
            Frame {
                msg_type: MSG_DATA,
                stream: 7,
                flags: 0,
                payload: b"GET / HTTP/1.1\r\n\r\n".to_vec(),
            },
            Frame {
                msg_type: MSG_CLOSE,
                stream: 7,
                flags: 0,
                payload: vec![],
            },
        ] {
            let enc = f.encode(DEFAULT_MAX_FRAME_SIZE).unwrap();
            let dec = Frame::decode(&enc, DEFAULT_MAX_FRAME_SIZE).unwrap();
            assert_eq!(f, dec);
        }
    }

    #[test]
    fn oversized_rejected() {
        let f = Frame {
            msg_type: MSG_DATA,
            stream: 1,
            flags: 0,
            payload: vec![0u8; DEFAULT_MAX_FRAME_SIZE + 1],
        };
        assert!(f.encode(DEFAULT_MAX_FRAME_SIZE).is_err());
    }

    #[test]
    fn short_rejected() {
        assert!(Frame::decode(&[1, 2, 3], DEFAULT_MAX_FRAME_SIZE).is_err());
    }
}
