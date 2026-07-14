// Command testupstream is the synthetic "app" the e2e suite runs behind
// marquee in wrapper mode. It binds 127.0.0.1:$PORT (the env marquee
// injects) and serves the minimum surface the smoke assertions need: an
// HTML page, a Host echo, static JSON, a one-frame WebSocket echo, and an
// SSE stream. -delay postpones binding to exercise the starting page;
// -tag is inert and only makes the process findable by pgrep.
package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>testupstream</title>
</head>
<body>
<h1>hello from testupstream</h1>
</body>
</html>
`

const data = `{"items":[1,2,3],"decoy":"</body>"}`

func main() {
	delay := flag.Duration("delay", 0, "sleep before binding the port")
	flag.String("tag", "", "inert marker so tests can find this process")
	flag.Parse()

	port := os.Getenv("PORT")
	if port == "" {
		log.Fatal("testupstream: PORT not set")
	}
	if *delay > 0 {
		time.Sleep(*delay)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", servePage)
	mux.HandleFunc("/echo-host", serveHost)
	mux.HandleFunc("/data.json", serveJSON)
	mux.HandleFunc("/ws", serveWS)
	mux.HandleFunc("/sse", serveSSE)

	log.Fatal(http.ListenAndServe(net.JoinHostPort("127.0.0.1", port), mux))
}

func servePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, page)
}

func serveHost(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, r.Host)
}

func serveJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, data)
}

func serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, "data: first\n\n")
	flusher.Flush()
	select {
	case <-r.Context().Done():
		return
	case <-time.After(2 * time.Second):
	}
	_, _ = io.WriteString(w, "data: second\n\n")
	flusher.Flush()
}

// wsGUID is the fixed key-derivation constant from RFC 6455 §1.3.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// serveWS is a deliberately minimal RFC 6455 server: it completes the
// handshake, reads one masked unfragmented text frame, and echoes the
// payload back unmasked. Just enough to satisfy the e2e client.
func serveWS(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		http.Error(w, "not a websocket handshake", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	sum := sha1.Sum([]byte(key + wsGUID))
	handshake := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := rw.WriteString(handshake); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}

	payload, err := readMaskedTextFrame(rw.Reader)
	if err != nil {
		return
	}
	frame := append([]byte{0x81, byte(len(payload))}, payload...)
	if _, err := rw.Write(frame); err != nil {
		return
	}
	_ = rw.Flush()
}

func readMaskedTextFrame(r *bufio.Reader) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	if header[0] != 0x81 {
		return nil, fmt.Errorf("unexpected frame header byte 0x%02x", header[0])
	}
	if header[1]&0x80 == 0 {
		return nil, errors.New("client frame not masked")
	}
	length := int(header[1] & 0x7f)
	if length > 125 {
		return nil, errors.New("extended payload lengths unsupported")
	}
	var mask [4]byte
	if _, err := io.ReadFull(r, mask[:]); err != nil {
		return nil, err
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return payload, nil
}
