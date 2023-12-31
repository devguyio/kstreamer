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
	"encoding/hex"
	"fmt"
	"net"
	"testing"

	"github.com/k-streamer/sarama"
)

const (
	TEST_ADDRESS = "127.0.0.1"
	TEST_PORT    = 8080
)

func TestMain(m *testing.M) {
	//h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	//slog.SetDefault(slog.New(h))
	//
	//s := NewTCPServer(TEST_ADDRESS, TEST_PORT)
	//err := s.Start()
	//if err != nil {
	//	slog.Error("Failed to start TCP server: %s", err)
	//	os.Exit(1)
	//}
	//defer s.Stop()
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
	//config := &kafka.ConfigMap{
	//	"bootstrap.servers": TEST_ADDRESS + ":" + strconv.Itoa(TEST_PORT),
	//	"client.id":         "go-producer",
	//}
	//
	//producer, err := kafka.NewProducer(config)
	//if err != nil {
	//	t.Fatalf("Failed to create Kafka producer: %s", err)
	//}
	//defer producer.Close()
	//
	//topic := "test"
	//message := "Hello world"
	//
	//deliveryChan := make(chan kafka.Event)
	//
	//err = producer.Produce(&kafka.Message{
	//	TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
	//	Value:          []byte(message),
	//}, deliveryChan)
	//
	//if err != nil {
	//	t.Fatalf("Failed to produce message: %s", err)
	//}
	//
	//e := <-deliveryChan
	//m := e.(*kafka.Message)
	//
	//if m.TopicPartition.Error != nil {
	//	t.Fatalf("Delivery failed: %s", m.TopicPartition.Error)
	//}
}

func TestDebugKafkaAPIVersion(t *testing.T) {
	serverAddr := "logos:9092" // Replace with the Kafka server address and port.

	// Establish a TCP connection to the Kafka server.
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		fmt.Printf("Failed to connect to Kafka server: %v\n", err)
		return
	}
	defer conn.Close()

	// Send the version request.
	apiVersionRequest := sarama.ApiVersionsRequest{
		Version:               3,
		ClientSoftwareName:    "sarama",
		ClientSoftwareVersion: "1.27.0",
	}
	request := sarama.Request{
		CorrelationID: 1,
		ClientID:      "sarama",
		Body:          &apiVersionRequest,
	}
	//err = request.encode(&sarama.RealEncoder{})
	buf, err := sarama.Encode(&request, nil)
	//buf := make([]byte, 4)
	//binary.BigEndian.PutUint32(buf, uint32(len(body)))
	//buf = append(buf, body...)
	fmt.Println("Size of version request:", hex.EncodeToString(buf))
	//conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	//_, err = conn.Write([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	_, err = conn.Write(buf)
	if err != nil {
		fmt.Printf("Failed to send version request: %v\n", err)
		return
	}

	//conn.Close()

	// Read and print the response.
	//response := make([]byte, 8)
	//n, err := io.ReadFull(conn, response)
	//if err != nil {
	//	fmt.Printf("Failed to read response: %v\n", err)
	//	return
	//}
	//
	//fmt.Printf("Received %d bytes of response:\n", n)
	//fmt.Println(string(response[:n]))

	//// Sleep for a while to keep the connection open for debugging purposes.
	//fmt.Println("Sleeping for 10 seconds to keep the connection open for debugging...")
	//time.Sleep(5 * time.Second)
}
