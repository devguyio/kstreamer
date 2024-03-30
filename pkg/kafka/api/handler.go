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

package api

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kcore-io/sarama"
)

type EncodedRequest []byte
type EncodedResponse []byte

// Handler is the API catalog for the Kafka server. It is responsible for handling Kafka requests.
//
// A Handler is a dispatcher that dispatches Kafka requests and return responses. It contains handlers for
// all the Kafka API keys that are supported by the server.
type Handler struct {
	ClusterID    string
	ControllerID int
	LeaderEpoch  int32
	// {
	// 		"orders":
	//			{
	//				0 : [ Record, Record,...],
	//				1 : [ Record, Record,...]
	//			},
	// 		"customers":
	//			{
	//				0 : [ Record, Record,...],
	//				1 : [ Record, Record,...]
	//			}
	// }
	commitLog map[string]map[int32][]sarama.Record
}

func NewAPIHandler(clusterID string, controllerID int) *Handler {
	return &Handler{
		ClusterID:    clusterID,
		ControllerID: controllerID,
		commitLog:    make(map[string]map[int32][]sarama.Record),
	}
}

func (h *Handler) Handle(encodedRequest EncodedRequest) (EncodedResponse, error) {
	// Parse the request
	req := sarama.Request{}
	err := req.Decode(&sarama.RealDecoder{Raw: encodedRequest})
	if err != nil {
		slog.Error("Failed to decode request", "error", err)
		return nil, fmt.Errorf("failed to decode request: %w", err)
	}
	slog.Debug(
		"Decoded request. Dispatching...", "client id", req.ClientID, "correlation id", req.CorrelationID,
		"api key",
		req.Body.APIKey(), "api version", req.Body.APIVersion(), "body", req.Body,
	)

	resp, err := h.dispatch(&req)
	if err != nil {
		slog.Error("Failed to dispatch request", "error", err)
		return nil, fmt.Errorf("failed to dispatch request: %w", err)
	}

	encodedResp, err := sarama.Encode(resp, nil)
	if err != nil {
		slog.Error("Failed to encode response", err)
		return nil, fmt.Errorf("failed to encode response: %w", err)
	}
	slog.Debug("Commit log content", "commit log", h.commitLog)
	return encodedResp, nil
}

func (h *Handler) dispatch(req *sarama.Request) (*sarama.Response, error) {
	var responseBody sarama.ProtocolBody
	var err error
	var responseHeaderVersion int16 = ResponseHeaderVersion

	switch req.Body.APIKey() {
	case ApiVersionsApiKey:
		apiVersionsReq, ok := req.Body.(*sarama.ApiVersionsRequest)
		if !ok {
			return nil, errors.New("invalid request")
		}
		slog.Debug("Dispatching request", "api key", req.Body.APIKey(), "ApiVersions request", apiVersionsReq)
		responseBody, err = h.handleAPIVersions(req.CorrelationID, req.ClientID, *apiVersionsReq)
		if err != nil {
			return nil, fmt.Errorf("error while handling ApiVersions request: %w", err)
		}
	case MetadataApiKey:
		metaReq, ok := req.Body.(*sarama.MetadataRequest)
		if !ok {
			// TODO: return a more specific error that shows the invalid request
			return nil, errors.New("invalid request")
		}
		slog.Debug("Dispatching request", "api key", req.Body.APIKey(), "Metadata request", metaReq)
		responseBody, err = h.handleMetadata(req.CorrelationID, req.ClientID, *metaReq)
		if err != nil {
			return nil, fmt.Errorf("error while handling Metadata request: %w", err)
		}

	case ProduceApiKey:
		produceReq, ok := req.Body.(*sarama.ProduceRequest)
		if !ok {
			return nil, errors.New("invalid request")
		}
		slog.Debug("Dispatching request", "api key", req.Body.APIKey(), "Produce request", produceReq)
		responseBody, err = h.handleProduce(req.CorrelationID, req.ClientID, *produceReq)
		if err != nil {
			return nil, fmt.Errorf("error while handling Produce request: %w", err)
		}
		responseHeaderVersion = 0

	default:
		return nil, errors.New(fmt.Sprintf("no handler found for request %v", req.Body.APIKey()))
	}

	slog.Debug(
		"Dispatched request", "api key", req.Body.APIKey(), "Correlation ID", req.CorrelationID, "response",
		responseBody,
	)

	return &sarama.Response{
		CorrelationID: req.CorrelationID,
		Version:       responseHeaderVersion,
		Body:          responseBody,
	}, nil
}

func (h *Handler) handleAPIVersions(_ int32, _ string, _ sarama.ApiVersionsRequest) (
	*sarama.ApiVersionsResponse,
	error,
) {

	// TODO: Make the ApiKeys dynamic
	return &sarama.ApiVersionsResponse{
		ApiKeys: []sarama.ApiVersionsResponseKey{
			{
				ApiKey:     ApiVersionsApiKey,
				Version:    ApiVersionsRequestVersion,
				MinVersion: 0,
				MaxVersion: ApiVersionsRequestVersion,
			},
			{
				ApiKey:     MetadataApiKey,
				Version:    MetadataRequestVersion,
				MinVersion: 0,
				MaxVersion: MetadataRequestVersion,
			},
			{
				ApiKey:     ProduceApiKey,
				Version:    ProduceRequestVersion,
				MinVersion: 0,
				MaxVersion: ProduceRequestVersion,
			},
		},
		Version:        ApiVersionsRequestVersion,
		ErrorCode:      0,
		ThrottleTimeMs: 0,
	}, nil

}

func (h *Handler) handleMetadata(_ int32, _ string, request sarama.MetadataRequest) (*sarama.MetadataResponse, error) {

	topics := make([]*sarama.TopicMetadata, len(request.Topics))
	for i, topic := range request.Topics {
		topics[i] = &sarama.TopicMetadata{
			Err:        sarama.ErrNoError,
			Name:       topic,
			IsInternal: false,
			Partitions: h.getPartitions(topic),
		}
	}

	return &sarama.MetadataResponse{
		ThrottleTimeMs: 0,
		Version:        MetadataRequestVersion,
		Brokers: []*sarama.Broker{
			{
				ID:   0,
				Addr: "localhost:9092",
			},
		},
		ClusterID:    &h.ClusterID,
		ControllerID: int32(h.ControllerID),
		Topics:       topics,
	}, nil
}

func (h *Handler) handleProduce(_ int32, _ string, request sarama.ProduceRequest) (*sarama.ProduceResponse, error) {
	// TODO: Handle KIP-951 for version 10
	//  https://cwiki.apache.org/confluence/display/KAFKA/KIP-951%3A+Leader+discovery+optimisations+for+the+client

	produceBlocks := make(map[string]map[int32]*sarama.ProduceResponseBlock)

	for t, p := range request.Records {
		slog.Debug("Produce request", "topic", t, "partitions", p)
		if _, ok := h.commitLog[t]; !ok {
			h.commitLog[t] = make(map[int32][]sarama.Record)
		}
		if _, ok := produceBlocks[t]; !ok {
			produceBlocks[t] = make(map[int32]*sarama.ProduceResponseBlock)
		}
		for pID, rs := range p {
			if _, ok := h.commitLog[t][pID]; !ok {
				h.commitLog[t][pID] = []sarama.Record{}
			}
			if _, ok := produceBlocks[t][pID]; !ok {
				produceBlocks[t][pID] = &sarama.ProduceResponseBlock{
					Err:         sarama.ErrNoError,
					Offset:      0,
					Timestamp:   time.Time{},
					StartOffset: 0,
				}
			}
			slog.Debug("Produce request", "partition", pID, "records", rs)
			for _, record := range rs.RecordBatch.Records {
				slog.Debug("Produce request", "record", record)
				h.commitLog[t][pID] = append(h.commitLog[t][pID], *record)
			}
		}
	}

	return &sarama.ProduceResponse{
		Blocks:       produceBlocks,
		Version:      ProduceRequestVersion,
		ThrottleTime: 0,
	}, nil
}

func (h *Handler) getPartitions(topic string) []*sarama.PartitionMetadata {
	return []*sarama.PartitionMetadata{
		{
			Err:             sarama.ErrNoError,
			ID:              h.getTopicLeader(topic),
			Leader:          0,
			Replicas:        h.getReplicas(topic),
			Isr:             []int32{0},
			LeaderEpoch:     h.LeaderEpoch,
			OfflineReplicas: h.getOfflineReplicas(topic),
		},
	}
}

func (h *Handler) getTopicLeader(_ string) int32 {
	return 0
}

func (h *Handler) getReplicas(_ string) []int32 {
	return []int32{0}
}

func (h *Handler) getOfflineReplicas(_ string) []int32 {
	return []int32{}
}
