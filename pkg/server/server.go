/*
Copyright 2023 KStreamer Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"bytes"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strconv"
)

type TCPServer struct {
	address string
	port    int
	l       net.Listener
}

// NewTCPServer creates a new TCP server. It does not start the server.
func NewTCPServer(address string, port int) *TCPServer {
	return &TCPServer{
		address: address,
		port:    port,
	}
}

// Start starts the TCP server in a new goroutine.
func (s *TCPServer) Start() error {
	slog.Debug("Starting TCP server", "address", s.address, "port", s.port)
	l, err := net.Listen("tcp", s.address+":"+strconv.Itoa(s.port))
	if err != nil {
		slog.Error("Failed to start TCP server: %s", err)
		return err
	}
	slog.Debug("TCP server listening")
	s.l = l

	go func() {
		for {
			// When the server is stopped, the listener is closed and Accept() returns
			conn, err := l.Accept()
			if err != nil {
				slog.Error("Failed to accept TCP connection: %s", err)
				return
			}
			go s.handleConnection(conn)
		}
	}()

	return nil
}

// Stop stops the TCP server.
func (s *TCPServer) Stop() error {
	slog.Debug("Stopping TCP server", "address", s.address, "port", s.port)
	if s.l == nil {
		slog.Debug("TCP server not running")
		return nil
	}
	err := s.l.Close()
	if err != nil {
		slog.Error("Failed to stop TCP server: %s", err)
		return err
	}
	s.l = nil
	return nil
}

func (s *TCPServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	slog.Debug("Handling TCP connection", "address", conn.RemoteAddr())
	sizeBuf := make([]byte, 4)
	n, err := conn.Read(sizeBuf)
	size := binary.BigEndian.Uint32(sizeBuf)
	slog.Debug("Received data from TCP connection", "read bytes", n, "size", size)

	message := make([]byte, size)
	n, err = conn.Read(message)
	//req := &sarama.Request{}
	//req.decode(&sarama.RealDecoder{Raw: message})
	//slog.Debug("Received data from TCP connection", "read bytes", n, "correlation id", req.CorrelationID, "client id", req.ClientID, "api key", req.Body.key(), "api version", req.Body.version(), "body", req.Body)

	//res := &sarama.ApiVersionsResponse{
	//	Version: 2,
	//}

	if err != nil {
		slog.Error("Failed to read from TCP connection: %s", err)
		return
	}
}

func getCompactString(m []byte) string {
	// Create a reader from the data slice.
	reader := bytes.NewReader(m)

	// Read the VARINT value.
	value, _ := ReadVarint(reader)
	slog.Debug("Read compact string", "value", value)
	//
	//length, n := binary.Uvarint(message)
	//s := string(message[n : n+int(length)])
	//slog.Debug("Read compact string", "length", length, "string", s)
	return ""
}

func ReadVarint(r io.Reader) (int32, error) {
	var result int32
	var shift uint

	for {
		// Read one byte at a time.
		b := make([]byte, 1)
		_, err := r.Read(b)
		if err != nil {
			return 0, err
		}

		// Extract the lower 7 bits from the byte and add to the result.
		value := int32(b[0] & 0x7F)
		result |= value << shift

		// Check if the high bit (8th bit) is set, indicating more bytes to read.
		if b[0]&0x80 == 0 {
			break
		}

		shift += 7
	}

	// Zig-zag decoding: Shift right by one to undo the zig-zag encoding.
	return (result >> 1) ^ -(result & 1), nil
}
