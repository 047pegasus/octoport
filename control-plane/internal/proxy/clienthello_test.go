package proxy

import "encoding/binary"

// buildClientHello constructs a valid TLS 1.2 ClientHello carrying an SNI
// extension, exercising the SNI parser.
func buildClientHello(serverName string) []byte {
	name := []byte(serverName)
	sni := make([]byte, 0, 5+len(name))
	sni = appendU16(sni, len(name)+3) // server name list length
	sni = append(sni, 0)              // name type: host_name
	sni = appendU16(sni, len(name))   // name length
	sni = append(sni, name...)        // name

	ext := make([]byte, 0, 4+len(sni))
	ext = appendU16(ext, 0) // server_name extension type
	ext = appendU16(ext, len(sni))
	ext = append(ext, sni...)

	// Handshake body: client_version + random(32) + session_id + ciphers +
	// compression + extensions (all lengths big-endian).
	body := []byte{0x03, 0x03}
	random := make([]byte, 32)
	body = append(body, random...)
	body = append(body, 0x00)              // session id length
	body = appendU16(body, 2)              // cipher suites length
	body = appendU16(body, 0x1301)         // TLS_AES_128_GCM_SHA256
	body = append(body, 0x01)              // compression methods length
	body = append(body, 0x00)              // null compression
	body = appendU16(body, len(ext))       // extensions length
	body = append(body, ext...)

	// Handshake message: type(1) + length(3) + body.
	hs := []byte{0x01} // ClientHello
	hs = appendU24(hs, len(body))
	hs = append(hs, body...)

	// TLS record: content_type + version + length.
	record := []byte{0x16, 0x03, 0x03}
	record = appendU16(record, len(hs))
	record = append(record, hs...)
	return record
}

func appendU16(b []byte, vs ...int) []byte {
	for _, v := range vs {
		var buf [2]byte
		binary.BigEndian.PutUint16(buf[:], uint16(v))
		b = append(b, buf[:]...)
	}
	return b
}

func appendU24(b []byte, v int) []byte {
	b = append(b, byte(v>>16), byte(v>>8), byte(v))
	return b
}
