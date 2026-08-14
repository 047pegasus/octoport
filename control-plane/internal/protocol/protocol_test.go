package protocol

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []Frame{
		{Type: MsgOpen, Stream: 1, Payload: []byte(`{"stream":1,"protocol":"http"}`)},
		{Type: MsgData, Stream: 7, Payload: []byte("GET / HTTP/1.1\r\n\r\n")},
		{Type: MsgClose, Stream: 7},
		{Type: MsgError, Stream: 3, Payload: []byte("connection refused")},
		{Type: MsgPing, Stream: StreamControl},
	}
	for _, f := range cases {
		enc, err := f.Encode(DefaultMaxFrameSize)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		dec, err := Decode(enc, DefaultMaxFrameSize)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dec.Type != f.Type || dec.Stream != f.Stream || dec.Flags != f.Flags {
			t.Fatalf("header mismatch: got %+v want %+v", dec, f)
		}
		if !bytes.Equal(dec.Payload, f.Payload) {
			t.Fatalf("payload mismatch: got %q want %q", dec.Payload, f.Payload)
		}
	}
}

func TestEncodeRejectsOversized(t *testing.T) {
	f := Frame{Type: MsgData, Stream: 1, Payload: make([]byte, DefaultMaxFrameSize+1)}
	if _, err := f.Encode(DefaultMaxFrameSize); err == nil {
		t.Fatal("expected ErrFrameTooBig")
	}
}

func TestDecodeRejectsShort(t *testing.T) {
	if _, err := Decode([]byte{1, 2, 3}, DefaultMaxFrameSize); err == nil {
		t.Fatal("expected error for short frame")
	}
}

func TestDecodeRejectsOversized(t *testing.T) {
	buf := make([]byte, HeaderSize+DefaultMaxFrameSize+1)
	if _, err := Decode(buf, DefaultMaxFrameSize); err == nil {
		t.Fatal("expected ErrFrameTooBig")
	}
}
