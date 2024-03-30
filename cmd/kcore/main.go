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

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"kcore/pkg/kafka/api"
	"kcore/pkg/kafka/connection"
	"kcore/pkg/server"
)

func main() {
	flags := GetFlags()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle termination signals gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("Received termination signal")
		cancel()
	}()

	l := slog.LevelInfo
	if flags.Verbose {
		l = slog.LevelDebug
		slog.Info("Verbose logging enabled")
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	slog.SetDefault(slog.New(h))

	// TODO: pass a context to the server and weave it through the connection handler
	s := server.NewTCPServer(
		flags.Address, flags.Port, func() server.ConnectionHandler {
			return connection.NewConnectionHandler(
				api.NewAPIHandler(flags.ClusterID, flags.ControllerID),
			)
		},
	)

	slog.Info("Starting kcore...")
	go func() {
		if err := s.Start(); err != nil {
			slog.Error("Failed to start kcore", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("Shutting down kcore...")
	if err := s.Stop(); err != nil {
		slog.Error("Failed to stop kcore", "error", err)
	}
}

// Flags contains the command line flags for the kcore server.
type Flags struct {
	Verbose      bool
	Address      string
	Port         int
	ClusterID    string
	ControllerID int
}

// GetFlags returns the command line flags for the kcore server.
func GetFlags() Flags {
	return Flags{
		Verbose:      *flag.Bool("verbose", true, "Enable verbose logging"),
		Address:      *flag.String("address", "127.0.0.1", "Address to listen on"),
		Port:         *flag.Int("port", 9092, "Port to listen on"),
		ClusterID:    *flag.String("kcore-cluster", "kcore", "Kafka cluster ID"),
		ControllerID: *flag.Int("kcore-controller", 0, "Kafka controller ID"),
	}
}
