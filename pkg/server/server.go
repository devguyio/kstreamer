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
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	slog.Debug("Received data from TCP connection", "read bytes", n, "data", string(buf[:n]))
	if err != nil {
		slog.Error("Failed to read from TCP connection: %s", err)
		return
	}
}
