/*
Copyright 2024 KCore Authors

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

package connection

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"

	"kcore/pkg/kafka/api"
	"kcore/pkg/server"
)

const ProcessingQueueSize = 2

type handler struct {
	conn   net.Conn
	ctx    context.Context
	cancel context.CancelFunc
	api    *api.Handler
}

func NewConnectionHandler(requestHandler *api.Handler) server.ConnectionHandler {
	ctx, cancel := context.WithCancel(context.Background())
	mgr := &handler{
		api:    requestHandler,
		ctx:    ctx,
		cancel: cancel,
	}
	// TODO: return error
	return mgr
}

func (h *handler) HandleConnection(conn net.Conn) {
	h.conn = conn
	h.run()
}

/**
 * Starts reading from the connection
 * and handling requests.
 */
func (h *handler) run() {
	defer h.conn.Close()
	for {
		// Read the request size (4 bytes)
		buffer := make([]byte, 4)
		slog.Debug("Reading request message size")
		n, err := h.conn.Read(buffer)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			slog.Error("Failed to read request message size from connection", err)
			return
		}
		if n != 4 {
			slog.Error("Failed to read request message size from connection", "read bytes", n, "Expected", 4)
			return
		}
		reqSize := binary.BigEndian.Uint32(buffer)
		slog.Debug("Read request message size from connection", "bytes", n, "request message size", reqSize)

		// Read the request (reqSize bytes)
		buffer = make([]byte, reqSize)
		n, err = h.conn.Read(buffer)
		if err != nil {
			slog.Error("Failed to read request from connection", err)
			return
		}
		if n != int(reqSize) {
			slog.Error("Failed to read request from connection", "read bytes", n, "Expected", reqSize)
			return
		}
		slog.Debug("Read request from connection", "size", n)

		// Handle the request
		resp, err := h.api.Handle(buffer)
		if err != nil {
			slog.Error("Failed to handle request", err)
			return
		}

		if _, err = h.conn.Write(resp); err != nil {
			slog.Error("Failed to write response to connection", err)
			return
		}
	}
}
