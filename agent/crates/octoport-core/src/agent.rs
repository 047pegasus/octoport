//! The streaming agent. Connects to the control plane over WebSocket,
//! claims the authenticated user's tunnels, and forwards traffic between the
//! public proxy (via multiplexed streams) and local TCP sockets.

use std::collections::HashMap;

use anyhow::{anyhow, Result};
use futures_util::{SinkExt, StreamExt};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio::sync::mpsc;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::Message;
use tokio_tungstenite::{connect_async, MaybeTlsStream, WebSocketStream};
use tracing::{debug, info, warn};

use crate::protocol::{Frame, OpenMeta, MSG_CLOSE, MSG_DATA, MSG_ERROR, MSG_OPEN, MSG_PING, MSG_PONG, STREAM_CONTROL};

/// A handle to one open stream, held by the read loop.
struct StreamHandle {
    /// Sends inbound bytes (from the proxy) to the local writer task.
    inbound: mpsc::Sender<Vec<u8>>,
}

/// Mutable session state carried by the read loop after the socket is split.
struct Session {
    max_frame_size: usize,
    streams: HashMap<u32, StreamHandle>,
    max_streams: usize,
    outbound: mpsc::UnboundedSender<Frame>,
}

/// An open WebSocket connection to the control plane.
pub struct Agent {
    ws: WebSocketStream<MaybeTlsStream<tokio::net::TcpStream>>,
    session: Session,
    /// Receiver consumed by the writer task once `run` splits the socket.
    outbound_rx: Option<mpsc::UnboundedReceiver<Frame>>,
}

impl Agent {
    /// Connect and authenticate. The token must carry the `agent` scope.
    pub async fn connect(ws_url: &str, token: &str, max_frame_size: usize, max_streams: usize) -> Result<Self> {
        let mut req = ws_url.into_client_request()?;
        req.headers_mut()
            .insert("Authorization", format!("Bearer {token}").parse()?);
        req.headers_mut()
            .insert("User-Agent", format!("octoport-agent/{}", env!("CARGO_PKG_VERSION")).parse()?);

        let (ws, _resp) = connect_async(req).await.map_err(|e| anyhow!("websocket connect failed: {e}"))?;
        let (outbound, outbound_rx) = mpsc::unbounded_channel::<Frame>();

        Ok(Agent {
            ws,
            session: Session {
                max_frame_size,
                streams: HashMap::new(),
                max_streams,
                outbound,
            },
            outbound_rx: Some(outbound_rx),
        })
    }

    /// Run the read loop until the connection drops or a fatal error occurs.
    pub async fn run(self) -> Result<()> {
        let Agent { ws, mut session, mut outbound_rx } = self;
        info!("agent connected to control plane");

        let (mut sink, mut reader) = ws.split();
        let max_frame_size = session.max_frame_size;
        let mut out_rx = outbound_rx.take().expect("run called once");
        tokio::spawn(async move {
            // Keepalive: nudge the socket so middleboxes (Cloudflare Tunnel,
            // NAT, load balancers) don't reap an idle WebSocket.
            let mut ping = tokio::time::interval(std::time::Duration::from_secs(25));
            ping.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
            loop {
                tokio::select! {
                    frame = out_rx.recv() => {
                        let Some(frame) = frame else { break };
                        let bytes = match frame.encode(max_frame_size) {
                            Ok(b) => b,
                            Err(e) => {
                                warn!("dropping oversize frame: {e}");
                                continue;
                            }
                        };
                        if sink.send(Message::Binary(bytes)).await.is_err() {
                            break;
                        }
                    }
                    _ = ping.tick() => {
                        let f = Frame { msg_type: MSG_PING, stream: STREAM_CONTROL, flags: 0, payload: vec![] };
                        if let Ok(bytes) = f.encode(max_frame_size) {
                            if sink.send(Message::Binary(bytes)).await.is_err() {
                                break;
                            }
                        }
                    }
                }
            }
        });

        while let Some(msg) = reader.next().await {
            let msg = msg.map_err(|e| anyhow!("websocket read: {e}"))?;
            if let Message::Binary(bytes) = msg {
                let frame = match Frame::decode(&bytes, session.max_frame_size) {
                    Ok(f) => f,
                    Err(e) => {
                        warn!("bad frame from control plane: {e}");
                        continue;
                    }
                };
                session.handle(frame).await;
            } else if let Message::Close(_) = msg {
                break;
            }
        }
        info!("agent disconnected from control plane");
        Ok(())
    }
}

impl Session {
    async fn handle(&mut self, frame: Frame) {
        match frame.msg_type {
            MSG_OPEN => {
                if self.streams.len() >= self.max_streams {
                    warn!("rejecting stream {}: limit reached", frame.stream);
                    self.send(Frame {
                        msg_type: MSG_ERROR,
                        stream: frame.stream,
                        flags: 0,
                        payload: b"stream limit reached".to_vec(),
                    });
                    return;
                }
                self.handle_open(&frame.payload).await;
            }
            MSG_DATA => {
                let stream = frame.stream;
                // Clone the sender so the borrow on `self.streams` ends before
                // we may need `&mut self` to close the stream below.
                let Some(tx) = self.streams.get(&stream).map(|h| h.inbound.clone()) else {
                    // Unknown stream: the client half already closed. Dropping
                    // the payload is correct; killing the session is not.
                    return;
                };
                match tx.try_send(frame.payload) {
                    Ok(()) => {}
                    Err(mpsc::error::TrySendError::Full(payload)) => {
                        // The local service is slower than the tunnel. Apply
                        // real backpressure by awaiting a slot instead of
                        // tearing the connection down.
                        //
                        // The previous code treated a full queue as a fatal
                        // error and killed the stream, so any upload larger
                        // than the 64-frame buffer -- or any local app that
                        // paused briefly -- dropped the request outright.
                        // Awaiting here does briefly head-of-line block other
                        // streams on this agent, which is the right trade:
                        // a short stall beats a dropped connection, and the
                        // stall is bounded by the local socket draining.
                        if tx.send(payload).await.is_err() {
                            self.close_stream(stream);
                        }
                    }
                    Err(mpsc::error::TrySendError::Closed(_)) => {
                        self.send(Frame {
                            msg_type: MSG_ERROR,
                            stream,
                            flags: 0,
                            payload: b"stream closed".to_vec(),
                        });
                        self.close_stream(stream);
                    }
                }
            }
            MSG_CLOSE | MSG_ERROR => {
                self.close_stream(frame.stream);
            }
            MSG_PING => {
                self.send(Frame {
                    msg_type: MSG_PONG,
                    stream: STREAM_CONTROL,
                    flags: 0,
                    payload: frame.payload,
                });
            }
            // PONG is the server's answer to our keepalive PING; nothing to do.
            MSG_PONG => {}
            other => debug!("ignoring unexpected frame type {other}"),
        }
    }

    fn send(&self, frame: Frame) {
        let _ = self.outbound.send(frame);
    }

    /// Open a new stream: dial the local service and pump both directions.
    async fn handle_open(&mut self, payload: &[u8]) {
        let meta: OpenMeta = match serde_json::from_slice(payload) {
            Ok(m) => m,
            Err(e) => {
                warn!("bad OPEN metadata: {e}");
                return;
            }
        };

        let local = match TcpStream::connect(&meta.target).await {
            Ok(c) => {
                debug!("stream {}: dial {} ok", meta.stream, meta.target);
                c
            }
            Err(e) => {
                warn!("dial {} failed: {e}", meta.target);
                self.send(Frame {
                    msg_type: MSG_ERROR,
                    stream: meta.stream,
                    flags: 0,
                    payload: format!("dial failed: {e}").into_bytes(),
                });
                return;
            }
        };
        let _ = local.set_nodelay(true);

        let (mut local_reader, mut local_writer) = local.into_split();

        // inbound channel: proxy -> local service
        let (inbound_tx, mut inbound_rx) = mpsc::channel::<Vec<u8>>(64);
        let stream = meta.stream;
        self.streams.insert(stream, StreamHandle { inbound: inbound_tx });

        // response_started fires when the local service begins sending data
        // (or closes). The writer task waits on it before half-closing its
        // write side, so the FIN never races ahead of the response: some
        // servers (Vite/Astro dev) abort the connection if the client closes
        // its write side before they've started responding.
        let (response_started_tx, response_started_rx) = tokio::sync::oneshot::channel::<()>();

        // local writer task: bytes from proxy -> local service
        let outbound = self.outbound.clone();
        tokio::spawn(async move {
            let mut written = 0usize;
            while let Some(data) = inbound_rx.recv().await {
                if local_writer.write_all(&data).await.is_err() {
                    debug!("stream {stream}: local write failed");
                    let _ = outbound.send(Frame {
                        msg_type: MSG_ERROR,
                        stream,
                        flags: 0,
                        payload: b"local write failed".to_vec(),
                    });
                    break;
                }
                written += data.len();
            }
            let _ = response_started_rx.await;
            debug!("stream {stream}: writer done, {written} bytes written, shutting down");
            let _ = local_writer.shutdown().await;
        });

        // local reader task: local service -> proxy (DATA + CLOSE)
        let outbound = self.outbound.clone();
        let mut response_started_tx = Some(response_started_tx);
        tokio::spawn(async move {
            let mut buf = vec![0u8; 32 * 1024];
            loop {
                match local_reader.read(&mut buf).await {
                    Ok(0) => {
                        debug!("stream {stream}: local EOF");
                        if let Some(tx) = response_started_tx.take() {
                            let _ = tx.send(());
                        }
                        break;
                    }
                    Ok(n) => {
                        if let Some(tx) = response_started_tx.take() {
                            let _ = tx.send(());
                        }
                        debug!("stream {stream}: read {n} bytes");
                        if outbound
                            .send(Frame {
                                msg_type: MSG_DATA,
                                stream,
                                flags: 0,
                                payload: buf[..n].to_vec(),
                            })
                            .is_err()
                        {
                            break;
                        }
                    }
                    Err(e) => {
                        debug!("local read error on stream {stream}: {e}");
                        if let Some(tx) = response_started_tx.take() {
                            let _ = tx.send(());
                        }
                        break;
                    }
                }
            }
            debug!("stream {stream}: reader task ending");
            let _ = outbound.send(Frame {
                msg_type: MSG_CLOSE,
                stream,
                flags: 0,
                payload: vec![],
            });
        });
    }

    fn close_stream(&mut self, stream: u32) {
        if self.streams.remove(&stream).is_some() {
            debug!("closed stream {stream}");
        }
    }
}
