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
	"os"
	"strconv"
	"testing"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

const (
	TEST_ADDRESS = "127.0.0.1"
	TEST_PORT    = 8080
)

func TestMain(m *testing.M) {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))

	s := NewTCPServer(TEST_ADDRESS, TEST_PORT)
	err := s.Start()
	if err != nil {
		slog.Error("Failed to start TCP server: %s", err)
		os.Exit(1)
	}
	defer s.Stop()
	m.Run()
}

//func TestNewTCPServer(t *testing.T) {
//	conn, err := net.Dial("tcp", TEST_ADDRESS+":"+strconv.Itoa(TEST_PORT))
//	if err != nil {
//		t.Fatalf("Failed to connect to TCP server: %s", err)
//	}
//
//	buf := []byte("Hello world")
//	n, err := conn.Write(buf)
//	if err != nil {
//		t.Fatalf("Failed to write to TCP server: %s", err)
//	}
//	if n != len(buf) {
//		t.Fatalf("Failed to write all bytes to TCP server: wrote %d bytes, expected %d", n, len(buf))
//	}
//
//	defer conn.Close()
//}

func TestReadKafkaMessage(t *testing.T) {
	config := &kafka.ConfigMap{
		"bootstrap.servers": TEST_ADDRESS + ":" + strconv.Itoa(TEST_PORT),
		"client.id":         "go-producer",
	}

	producer, err := kafka.NewProducer(config)
	if err != nil {
		t.Fatalf("Failed to create Kafka producer: %s", err)
	}
	defer producer.Close()

	topic := "test"
	message := "Hello world"

	deliveryChan := make(chan kafka.Event)

	err = producer.Produce(&kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
		Value:          []byte(message),
	}, deliveryChan)

	if err != nil {
		t.Fatalf("Failed to produce message: %s", err)
	}

	e := <-deliveryChan
	m := e.(*kafka.Message)

	if m.TopicPartition.Error != nil {
		t.Fatalf("Delivery failed: %s", m.TopicPartition.Error)
	}
}
