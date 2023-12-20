Kafka protocol guide
====================

This document covers the wire protocol implemented in Kafka. It is meant to give a readable guide to the protocol that covers the available requests, their binary format, and the proper way to make use of them to implement a client. This document assumes you understand the basic design and terminology described [here](https://kafka.apache.org/documentation.html#design)

* [Preliminaries](#protocol_preliminaries)
    * [Network](#protocol_network)
    * [Partitioning and bootstrapping](#protocol_partitioning)
    * [Partitioning Strategies](#protocol_partitioning_strategies)
    * [Batching](#protocol_batching)
    * [Versioning and Compatibility](#protocol_compatibility)
    * [Retrieving Supported API versions](#api_versions)
    * [SASL Authentication Sequence](#sasl_handshake)
* [The Protocol](#protocol_details)
    * [Protocol Primitive Types](#protocol_types)
    * [Notes on reading the request format grammars](#protocol_grammar)
    * [Common Request and Response Structure](#protocol_common)
    * [Record Batch](#protocol_recordbatch)
* [Evolving the Protocol](#protocol_evolution)
    * [The Request Header](#protocol_versioning)
    * [Versioning](#protocol_versioning)
* [Constants](#protocol_constants)
    * [Error Codes](#protocol_error_codes)
    * [Api Keys](#protocol_api_keys)
* [The Messages](#protocol_messages)
* [Some Common Philosophical Questions](#protocol_philosophy)

#### [Preliminaries](#protocol_preliminaries)

##### [Network](#protocol_network)

Kafka uses a binary protocol over TCP. The protocol defines all APIs as request response message pairs. All messages are size delimited and are made up of the following primitive types.

The client initiates a socket connection and then writes a sequence of request messages and reads back the corresponding response message. No handshake is required on connection or disconnection. TCP is happier if you maintain persistent connections used for many requests to amortize the cost of the TCP handshake, but beyond this penalty connecting is pretty cheap.

The client will likely need to maintain a connection to multiple brokers, as data is partitioned and the clients will need to talk to the server that has their data. However it should not generally be necessary to maintain multiple connections to a single broker from a single client instance (i.e. connection pooling).

The server guarantees that on a single TCP connection, requests will be processed in the order they are sent and responses will return in that order as well. The broker's request processing allows only a single in-flight request per connection in order to guarantee this ordering. Note that clients can (and ideally should) use non-blocking IO to implement request pipelining and achieve higher throughput. i.e., clients can send requests even while awaiting responses for preceding requests since the outstanding requests will be buffered in the underlying OS socket buffer. All requests are initiated by the client, and result in a corresponding response message from the server except where noted.

The server has a configurable maximum limit on request size and any request that exceeds this limit will result in the socket being disconnected.

##### [Partitioning and bootstrapping](#protocol_partitioning)

Kafka is a partitioned system so not all servers have the complete data set. Instead recall that topics are split into a pre-defined number of partitions, P, and each partition is replicated with some replication factor, N. Topic partitions themselves are just ordered "commit logs" numbered 0, 1, ..., P-1.

All systems of this nature have the question of how a particular piece of data is assigned to a particular partition. Kafka clients directly control this assignment, the brokers themselves enforce no particular semantics of which messages should be published to a particular partition. Rather, to publish messages the client directly addresses messages to a particular partition, and when fetching messages, fetches from a particular partition. If two clients want to use the same partitioning scheme they must use the same method to compute the mapping of key to partition.

These requests to publish or fetch data must be sent to the broker that is currently acting as the leader for a given partition. This condition is enforced by the broker, so a request for a particular partition to the wrong broker will result in an the NotLeaderForPartition error code (described below).

How can the client find out which topics exist, what partitions they have, and which brokers currently host those partitions so that it can direct its requests to the right hosts? This information is dynamic, so you can't just configure each client with some static mapping file. Instead all Kafka brokers can answer a metadata request that describes the current state of the cluster: what topics there are, which partitions those topics have, which broker is the leader for those partitions, and the host and port information for these brokers.

In other words, the client needs to somehow find one broker and that broker will tell the client about all the other brokers that exist and what partitions they host. This first broker may itself go down so the best practice for a client implementation is to take a list of two or three URLs to bootstrap from. The user can then choose to use a load balancer or just statically configure two or three of their Kafka hosts in the clients.

The client does not need to keep polling to see if the cluster has changed; it can fetch metadata once when it is instantiated cache that metadata until it receives an error indicating that the metadata is out of date. This error can come in two forms: (1) a socket error indicating the client cannot communicate with a particular broker, (2) an error code in the response to a request indicating that this broker no longer hosts the partition for which data was requested.

1.  Cycle through a list of "bootstrap" Kafka URLs until we find one we can connect to. Fetch cluster metadata.
2.  Process fetch or produce requests, directing them to the appropriate broker based on the topic/partitions they send to or fetch from.
3.  If we get an appropriate error, refresh the metadata and try again.

##### [Partitioning Strategies](#protocol_partitioning_strategies)

As mentioned above the assignment of messages to partitions is something the producing client controls. That said, how should this functionality be exposed to the end-user?

Partitioning really serves two purposes in Kafka:

1.  It balances data and request load over brokers
2.  It serves as a way to divvy up processing among consumer processes while allowing local state and preserving order within the partition. We call this semantic partitioning.

For a given use case you may care about only one of these or both.

To accomplish simple load balancing a simple approach would be for the client to just round robin requests over all brokers. Another alternative, in an environment where there are many more producers than brokers, would be to have each client chose a single partition at random and publish to that. This later strategy will result in far fewer TCP connections.

Semantic partitioning means using some key in the message to assign messages to partitions. For example if you were processing a click message stream you might want to partition the stream by the user id so that all data for a particular user would go to a single consumer. To accomplish this the client can take a key associated with the message and use some hash of this key to choose the partition to which to deliver the message.

##### [Batching](#protocol_batching)

Our APIs encourage batching small things together for efficiency. We have found this is a very significant performance win. Both our API to send messages and our API to fetch messages always work with a sequence of messages not a single message to encourage this. A clever client can make use of this and support an "asynchronous" mode in which it batches together messages sent individually and sends them in larger clumps. We go even further with this and allow the batching across multiple topics and partitions, so a produce request may contain data to append to many partitions and a fetch request may pull data from many partitions all at once.

The client implementer can choose to ignore this and send everything one at a time if they like.

##### [Compatibility](#protocol_compatibility)

Kafka has a "bidirectional" client compatibility policy. In other words, new clients can talk to old servers, and old clients can talk to new servers. This allows users to upgrade either clients or servers without experiencing any downtime.

Since the Kafka protocol has changed over time, clients and servers need to agree on the schema of the message that they are sending over the wire. This is done through API versioning.

Before each request is sent, the client sends the API key and the API version. These two 16-bit numbers, when taken together, uniquely identify the schema of the message to follow.

The intention is that clients will support a range of API versions. When communicating with a particular broker, a given client should use the highest API version supported by both and indicate this version in their requests.

The server will reject requests with a version it does not support, and will always respond to the client with exactly the protocol format it expects based on the version it included in its request. The intended upgrade path is that new features would first be rolled out on the server (with the older clients not making use of them) and then as newer clients are deployed these new features would gradually be taken advantage of.

Note that [KIP-482 tagged fields](https://cwiki.apache.org/confluence/display/KAFKA/KIP-482%3A+The+Kafka+Protocol+should+Support+Optional+Tagged+Fields) can be added to a request without incrementing the version number. This offers an additional way of evolving the message schema without breaking compatibility. Tagged fields do not take up any space when the field is not set. Therefore, if a field is rarely used, it is more efficient to make it a tagged field than to put it in the mandatory schema. However, tagged fields are ignored by recipients that don't know about them, which could pose a challenge if this is not the behavior that the sender wants. In such cases, a version bump may be more appropriate.

##### [Retrieving Supported API versions](#api_versions)

In order to work against multiple broker versions, clients need to know what versions of various APIs a broker supports. The broker exposes this information since 0.10.0.0 as described in [KIP-35](https://cwiki.apache.org/confluence/display/KAFKA/KIP-35+-+Retrieving+protocol+version). Clients should use the supported API versions information to choose the highest API version supported by both client and broker. If no such version exists, an error should be reported to the user.

The following sequence may be used by a client to obtain supported API versions from a broker.

1.  Client sends `ApiVersionsRequest` to a broker after connection has been established with the broker. If SSL is enabled, this happens after SSL connection has been established.
2.  On receiving `ApiVersionsRequest`, a broker returns its full list of supported ApiKeys and versions regardless of current authentication state (e.g., before SASL authentication on an SASL listener, do note that no Kafka protocol requests may take place on an SSL listener before the SSL handshake is finished). If this is considered to leak information about the broker version a workaround is to use SSL with client authentication which is performed at an earlier stage of the connection where the `ApiVersionRequest` is not available. Also, note that broker versions older than 0.10.0.0 do not support this API and will either ignore the request or close connection in response to the request.
3.  If multiple versions of an API are supported by broker and client, clients are recommended to use the latest version supported by the broker and itself.
4.  Deprecation of a protocol version is done by marking an API version as deprecated in the protocol documentation.
5.  Supported API versions obtained from a broker are only valid for the connection on which that information is obtained. In the event of disconnection, the client should obtain the information from the broker again, as the broker might have been upgraded/downgraded in the mean time.

##### [SASL Authentication Sequence](#sasl_handshake)

The following sequence is used for SASL authentication:

1.  Kafka `ApiVersionsRequest` may be sent by the client to obtain the version ranges of requests supported by the broker. This is optional.
2.  Kafka `SaslHandshakeRequest` containing the SASL mechanism for authentication is sent by the client. If the requested mechanism is not enabled in the server, the server responds with the list of supported mechanisms and closes the client connection. If the mechanism is enabled in the server, the server sends a successful response and continues with SASL authentication.
3.  The actual SASL authentication is now performed. If `SaslHandshakeRequest` version is v0, a series of SASL client and server tokens corresponding to the mechanism are sent as opaque packets without wrapping the messages with Kafka protocol headers. If `SaslHandshakeRequest` version is v1, the `SaslAuthenticate` request/response are used, where the actual SASL tokens are wrapped in the Kafka protocol. The error code in the final message from the broker will indicate if authentication succeeded or failed.
4.  If authentication succeeds, subsequent packets are handled as Kafka API requests. Otherwise, the client connection is closed.

For interoperability with 0.9.0.x clients, the first packet received by the server is handled as a SASL/GSSAPI client token if it is not a valid Kafka request. SASL/GSSAPI authentication is performed starting with this packet, skipping the first two steps above.

#### [The Protocol](#protocol_details)

##### [Protocol Primitive Types](#protocol_types)

The protocol is built out of the following primitive types.

| Type | Description |
| --- | --- |
| BOOLEAN | Represents a boolean value in a byte. Values 0 and 1 are used to represent false and true respectively. When reading a boolean value, any non-zero value is considered true. |
| INT8 | Represents an integer between -27 and 27-1 inclusive. |
| INT16 | Represents an integer between -215 and 215-1 inclusive. The values are encoded using two bytes in network byte order (big-endian). |
| INT32 | Represents an integer between -231 and 231-1 inclusive. The values are encoded using four bytes in network byte order (big-endian). |
| INT64 | Represents an integer between -263 and 263-1 inclusive. The values are encoded using eight bytes in network byte order (big-endian). |
| UINT32 | Represents an integer between 0 and 232-1 inclusive. The values are encoded using four bytes in network byte order (big-endian). |
| VARINT | Represents an integer between -231 and 231-1 inclusive. Encoding follows the variable-length zig-zag encoding from [Google Protocol Buffers](http://code.google.com/apis/protocolbuffers/docs/encoding.html). |
| VARLONG | Represents an integer between -263 and 263-1 inclusive. Encoding follows the variable-length zig-zag encoding from [Google Protocol Buffers](http://code.google.com/apis/protocolbuffers/docs/encoding.html). |
| UUID | Represents a type 4 immutable universally unique identifier (Uuid). The values are encoded using sixteen bytes in network byte order (big-endian). |
| FLOAT64 | Represents a double-precision 64-bit format IEEE 754 value. The values are encoded using eight bytes in network byte order (big-endian). |
| STRING | Represents a sequence of characters. First the length N is given as an INT16. Then N bytes follow which are the UTF-8 encoding of the character sequence. Length must not be negative. |
| COMPACT_STRING | Represents a sequence of characters. First the length N + 1 is given as an UNSIGNED_VARINT . Then N bytes follow which are the UTF-8 encoding of the character sequence. |
| NULLABLE_STRING | Represents a sequence of characters or null. For non-null strings, first the length N is given as an INT16. Then N bytes follow which are the UTF-8 encoding of the character sequence. A null value is encoded with length of -1 and there are no following bytes. |
| COMPACT\_NULLABLE\_STRING | Represents a sequence of characters. First the length N + 1 is given as an UNSIGNED_VARINT . Then N bytes follow which are the UTF-8 encoding of the character sequence. A null string is represented with a length of 0. |
| BYTES | Represents a raw sequence of bytes. First the length N is given as an INT32. Then N bytes follow. |
| COMPACT_BYTES | Represents a raw sequence of bytes. First the length N+1 is given as an UNSIGNED_VARINT.Then N bytes follow. |
| NULLABLE_BYTES | Represents a raw sequence of bytes or null. For non-null values, first the length N is given as an INT32. Then N bytes follow. A null value is encoded with length of -1 and there are no following bytes. |
| COMPACT\_NULLABLE\_BYTES | Represents a raw sequence of bytes. First the length N+1 is given as an UNSIGNED_VARINT.Then N bytes follow. A null object is represented with a length of 0. |
| RECORDS | Represents a sequence of Kafka records as NULLABLE_BYTES. For a detailed description of records see [Message Sets](https://kafka.apache.org/documentation/#messageformat). |
| ARRAY | Represents a sequence of objects of a given type T. Type T can be either a primitive type (e.g. STRING) or a structure. First, the length N is given as an INT32. Then N instances of type T follow. A null array is represented with a length of -1. In protocol documentation an array of T instances is referred to as \[T\]. |
| COMPACT_ARRAY | Represents a sequence of objects of a given type T. Type T can be either a primitive type (e.g. STRING) or a structure. First, the length N + 1 is given as an UNSIGNED_VARINT. Then N instances of type T follow. A null array is represented with a length of 0. In protocol documentation an array of T instances is referred to as \[T\]. |

##### [Notes on reading the request format grammars](#protocol_grammar)

The [BNF](https://en.wikipedia.org/wiki/Backus%E2%80%93Naur_Form)s below give an exact context free grammar for the request and response binary format. The BNF is intentionally not compact in order to give human-readable name. As always in a BNF a sequence of productions indicates concatenation. When there are multiple possible productions these are separated with '|' and may be enclosed in parenthesis for grouping. The top-level definition is always given first and subsequent sub-parts are indented.

##### [Common Request and Response Structure](#protocol_common)

All requests and responses originate from the following grammar which will be incrementally describe through the rest of this document:

    RequestOrResponse => Size (RequestMessage | ResponseMessage)
      Size => int32

| Field | Description |
| --- | --- |
| message_size | The message_size field gives the size of the subsequent request or response message in bytes. The client can read requests by first reading this 4 byte size as an integer N, and then reading and parsing the subsequent N bytes of the request. |

##### [Record Batch](#protocol_recordbatch)

A description of the record batch format can be found [here](https://kafka.apache.org/documentation/#recordbatch).

#### [Constants](#protocol_constants)

##### [Error Codes](#protocol_error_codes)

We use numeric codes to indicate what problem occurred on the server. These can be translated by the client into exceptions or whatever the appropriate error handling mechanism in the client language. Here is a table of the error codes currently in use:

| Error | Code | Retriable | Description |
| --- | --- | --- | --- |
| UNKNOWN\_SERVER\_ERROR | -1  | False | The server experienced an unexpected error when processing the request. |
| NONE | 0   | False |     |
| OFFSET\_OUT\_OF_RANGE | 1   | False | The requested offset is not within the range of offsets maintained by the server. |
| CORRUPT_MESSAGE | 2   | True | This message has failed its CRC checksum, exceeds the valid size, has a null key for a compacted topic, or is otherwise corrupt. |
| UNKNOWN\_TOPIC\_OR_PARTITION | 3   | True | This server does not host this topic-partition. |
| INVALID\_FETCH\_SIZE | 4   | False | The requested fetch size is invalid. |
| LEADER\_NOT\_AVAILABLE | 5   | True | There is no leader for this topic-partition as we are in the middle of a leadership election. |
| NOT\_LEADER\_OR_FOLLOWER | 6   | True | For requests intended only for the leader, this error indicates that the broker is not the current leader. For requests intended for any replica, this error indicates that the broker is not a replica of the topic partition. |
| REQUEST\_TIMED\_OUT | 7   | True | The request timed out. |
| BROKER\_NOT\_AVAILABLE | 8   | False | The broker is not available. |
| REPLICA\_NOT\_AVAILABLE | 9   | True | The replica is not available for the requested topic-partition. Produce/Fetch requests and other requests intended only for the leader or follower return NOT\_LEADER\_OR_FOLLOWER if the broker is not a replica of the topic-partition. |
| MESSAGE\_TOO\_LARGE | 10  | False | The request included a message larger than the max message size the server will accept. |
| STALE\_CONTROLLER\_EPOCH | 11  | False | The controller moved to another broker. |
| OFFSET\_METADATA\_TOO_LARGE | 12  | False | The metadata field of the offset request was too large. |
| NETWORK_EXCEPTION | 13  | True | The server disconnected before a response was received. |
| COORDINATOR\_LOAD\_IN_PROGRESS | 14  | True | The coordinator is loading and hence can't process requests. |
| COORDINATOR\_NOT\_AVAILABLE | 15  | True | The coordinator is not available. |
| NOT_COORDINATOR | 16  | True | This is not the correct coordinator. |
| INVALID\_TOPIC\_EXCEPTION | 17  | False | The request attempted to perform an operation on an invalid topic. |
| RECORD\_LIST\_TOO_LARGE | 18  | False | The request included message batch larger than the configured segment size on the server. |
| NOT\_ENOUGH\_REPLICAS | 19  | True | Messages are rejected since there are fewer in-sync replicas than required. |
| NOT\_ENOUGH\_REPLICAS\_AFTER\_APPEND | 20  | True | Messages are written to the log, but to fewer in-sync replicas than required. |
| INVALID\_REQUIRED\_ACKS | 21  | False | Produce request specified an invalid value for required acks. |
| ILLEGAL_GENERATION | 22  | False | Specified group generation id is not valid. |
| INCONSISTENT\_GROUP\_PROTOCOL | 23  | False | The group member's supported protocols are incompatible with those of existing members or first group member tried to join with empty protocol type or empty protocol list. |
| INVALID\_GROUP\_ID | 24  | False | The configured groupId is invalid. |
| UNKNOWN\_MEMBER\_ID | 25  | False | The coordinator is not aware of this member. |
| INVALID\_SESSION\_TIMEOUT | 26  | False | The session timeout is not within the range allowed by the broker (as configured by group.min.session.timeout.ms and group.max.session.timeout.ms). |
| REBALANCE\_IN\_PROGRESS | 27  | False | The group is rebalancing, so a rejoin is needed. |
| INVALID\_COMMIT\_OFFSET_SIZE | 28  | False | The committing offset data size is not valid. |
| TOPIC\_AUTHORIZATION\_FAILED | 29  | False | Topic authorization failed. |
| GROUP\_AUTHORIZATION\_FAILED | 30  | False | Group authorization failed. |
| CLUSTER\_AUTHORIZATION\_FAILED | 31  | False | Cluster authorization failed. |
| INVALID_TIMESTAMP | 32  | False | The timestamp of the message is out of acceptable range. |
| UNSUPPORTED\_SASL\_MECHANISM | 33  | False | The broker does not support the requested SASL mechanism. |
| ILLEGAL\_SASL\_STATE | 34  | False | Request is not valid given the current SASL state. |
| UNSUPPORTED_VERSION | 35  | False | The version of API is not supported. |
| TOPIC\_ALREADY\_EXISTS | 36  | False | Topic with this name already exists. |
| INVALID_PARTITIONS | 37  | False | Number of partitions is below 1. |
| INVALID\_REPLICATION\_FACTOR | 38  | False | Replication factor is below 1 or larger than the number of available brokers. |
| INVALID\_REPLICA\_ASSIGNMENT | 39  | False | Replica assignment is invalid. |
| INVALID_CONFIG | 40  | False | Configuration is invalid. |
| NOT_CONTROLLER | 41  | True | This is not the correct controller for this cluster. |
| INVALID_REQUEST | 42  | False | This most likely occurs because of a request being malformed by the client library or the message was sent to an incompatible broker. See the broker logs for more details. |
| UNSUPPORTED\_FOR\_MESSAGE_FORMAT | 43  | False | The message format version on the broker does not support the request. |
| POLICY_VIOLATION | 44  | False | Request parameters do not satisfy the configured policy. |
| OUT\_OF\_ORDER\_SEQUENCE\_NUMBER | 45  | False | The broker received an out of order sequence number. |
| DUPLICATE\_SEQUENCE\_NUMBER | 46  | False | The broker received a duplicate sequence number. |
| INVALID\_PRODUCER\_EPOCH | 47  | False | Producer attempted to produce with an old epoch. |
| INVALID\_TXN\_STATE | 48  | False | The producer attempted a transactional operation in an invalid state. |
| INVALID\_PRODUCER\_ID_MAPPING | 49  | False | The producer attempted to use a producer id which is not currently assigned to its transactional id. |
| INVALID\_TRANSACTION\_TIMEOUT | 50  | False | The transaction timeout is larger than the maximum value allowed by the broker (as configured by transaction.max.timeout.ms). |
| CONCURRENT_TRANSACTIONS | 51  | True | The producer attempted to update a transaction while another concurrent operation on the same transaction was ongoing. |
| TRANSACTION\_COORDINATOR\_FENCED | 52  | False | Indicates that the transaction coordinator sending a WriteTxnMarker is no longer the current coordinator for a given producer. |
| TRANSACTIONAL\_ID\_AUTHORIZATION_FAILED | 53  | False | Transactional Id authorization failed. |
| SECURITY_DISABLED | 54  | False | Security features are disabled. |
| OPERATION\_NOT\_ATTEMPTED | 55  | False | The broker did not attempt to execute this operation. This may happen for batched RPCs where some operations in the batch failed, causing the broker to respond without trying the rest. |
| KAFKA\_STORAGE\_ERROR | 56  | True | Disk error when trying to access log file on the disk. |
| LOG\_DIR\_NOT_FOUND | 57  | False | The user-specified log directory is not found in the broker config. |
| SASL\_AUTHENTICATION\_FAILED | 58  | False | SASL Authentication failed. |
| UNKNOWN\_PRODUCER\_ID | 59  | False | This exception is raised by the broker if it could not locate the producer metadata associated with the producerId in question. This could happen if, for instance, the producer's records were deleted because their retention time had elapsed. Once the last records of the producerId are removed, the producer's metadata is removed from the broker, and future appends by the producer will return this exception. |
| REASSIGNMENT\_IN\_PROGRESS | 60  | False | A partition reassignment is in progress. |
| DELEGATION\_TOKEN\_AUTH_DISABLED | 61  | False | Delegation Token feature is not enabled. |
| DELEGATION\_TOKEN\_NOT_FOUND | 62  | False | Delegation Token is not found on server. |
| DELEGATION\_TOKEN\_OWNER_MISMATCH | 63  | False | Specified Principal is not valid Owner/Renewer. |
| DELEGATION\_TOKEN\_REQUEST\_NOT\_ALLOWED | 64  | False | Delegation Token requests are not allowed on PLAINTEXT/1-way SSL channels and on delegation token authenticated channels. |
| DELEGATION\_TOKEN\_AUTHORIZATION_FAILED | 65  | False | Delegation Token authorization failed. |
| DELEGATION\_TOKEN\_EXPIRED | 66  | False | Delegation Token is expired. |
| INVALID\_PRINCIPAL\_TYPE | 67  | False | Supplied principalType is not supported. |
| NON\_EMPTY\_GROUP | 68  | False | The group is not empty. |
| GROUP\_ID\_NOT_FOUND | 69  | False | The group id does not exist. |
| FETCH\_SESSION\_ID\_NOT\_FOUND | 70  | True | The fetch session ID was not found. |
| INVALID\_FETCH\_SESSION_EPOCH | 71  | True | The fetch session epoch is invalid. |
| LISTENER\_NOT\_FOUND | 72  | True | There is no listener on the leader broker that matches the listener on which metadata request was processed. |
| TOPIC\_DELETION\_DISABLED | 73  | False | Topic deletion is disabled. |
| FENCED\_LEADER\_EPOCH | 74  | True | The leader epoch in the request is older than the epoch on the broker. |
| UNKNOWN\_LEADER\_EPOCH | 75  | True | The leader epoch in the request is newer than the epoch on the broker. |
| UNSUPPORTED\_COMPRESSION\_TYPE | 76  | False | The requesting client does not support the compression type of given partition. |
| STALE\_BROKER\_EPOCH | 77  | False | Broker epoch has changed. |
| OFFSET\_NOT\_AVAILABLE | 78  | True | The leader high watermark has not caught up from a recent leader election so the offsets cannot be guaranteed to be monotonically increasing. |
| MEMBER\_ID\_REQUIRED | 79  | False | The group member needs to have a valid member id before actually entering a consumer group. |
| PREFERRED\_LEADER\_NOT_AVAILABLE | 80  | True | The preferred leader was not available. |
| GROUP\_MAX\_SIZE_REACHED | 81  | False | The consumer group has reached its max size. |
| FENCED\_INSTANCE\_ID | 82  | False | The broker rejected this static consumer since another consumer with the same group.instance.id has registered with a different member.id. |
| ELIGIBLE\_LEADERS\_NOT_AVAILABLE | 83  | True | Eligible topic partition leaders are not available. |
| ELECTION\_NOT\_NEEDED | 84  | True | Leader election not needed for topic partition. |
| NO\_REASSIGNMENT\_IN_PROGRESS | 85  | False | No partition reassignment is in progress. |
| GROUP\_SUBSCRIBED\_TO_TOPIC | 86  | False | Deleting offsets of a topic is forbidden while the consumer group is actively subscribed to it. |
| INVALID_RECORD | 87  | False | This record has failed the validation on broker and hence will be rejected. |
| UNSTABLE\_OFFSET\_COMMIT | 88  | True | There are unstable offsets that need to be cleared. |
| THROTTLING\_QUOTA\_EXCEEDED | 89  | True | The throttling quota has been exceeded. |
| PRODUCER_FENCED | 90  | False | There is a newer producer with the same transactionalId which fences the current one. |
| RESOURCE\_NOT\_FOUND | 91  | False | A request illegally referred to a resource that does not exist. |
| DUPLICATE_RESOURCE | 92  | False | A request illegally referred to the same resource twice. |
| UNACCEPTABLE_CREDENTIAL | 93  | False | Requested credential would not meet criteria for acceptability. |
| INCONSISTENT\_VOTER\_SET | 94  | False | Indicates that the either the sender or recipient of a voter-only request is not one of the expected voters |
| INVALID\_UPDATE\_VERSION | 95  | False | The given update version was invalid. |
| FEATURE\_UPDATE\_FAILED | 96  | False | Unable to update finalized features due to an unexpected server error. |
| PRINCIPAL\_DESERIALIZATION\_FAILURE | 97  | False | Request principal deserialization failed during forwarding. This indicates an internal error on the broker cluster security setup. |
| SNAPSHOT\_NOT\_FOUND | 98  | False | Requested snapshot was not found |
| POSITION\_OUT\_OF_RANGE | 99  | False | Requested position is not greater than or equal to zero, and less than the size of the snapshot. |
| UNKNOWN\_TOPIC\_ID | 100 | True | This server does not host this topic ID. |
| DUPLICATE\_BROKER\_REGISTRATION | 101 | False | This broker ID is already in use. |
| BROKER\_ID\_NOT_REGISTERED | 102 | False | The given broker ID was not registered. |
| INCONSISTENT\_TOPIC\_ID | 103 | True | The log's topic ID did not match the topic ID in the request |
| INCONSISTENT\_CLUSTER\_ID | 104 | False | The clusterId in the request does not match that found on the server |
| TRANSACTIONAL\_ID\_NOT_FOUND | 105 | False | The transactionalId could not be found |
| FETCH\_SESSION\_TOPIC\_ID\_ERROR | 106 | True | The fetch session encountered inconsistent topic ID usage |
| INELIGIBLE_REPLICA | 107 | False | The new ISR contains at least one ineligible replica. |
| NEW\_LEADER\_ELECTED | 108 | False | The AlterPartition request successfully updated the partition state but the leader has changed. |
| OFFSET\_MOVED\_TO\_TIERED\_STORAGE | 109 | False | The requested offset is moved to tiered storage. |
| FENCED\_MEMBER\_EPOCH | 110 | False | The member epoch is fenced by the group coordinator. The member must abandon all its partitions and rejoin. |
| UNRELEASED\_INSTANCE\_ID | 111 | False | The instance ID is still used by another member in the consumer group. That member must leave first. |
| UNSUPPORTED_ASSIGNOR | 112 | False | The assignor or its version range is not supported by the consumer group. |
| STALE\_MEMBER\_EPOCH | 113 | False | The member epoch is stale. The member must retry after receiving its updated member epoch via the ConsumerGroupHeartbeat API. |

##### [Api Keys](#protocol_api_keys)

The following are the numeric codes that the ApiKey in the request can take for each of the below request types.

| Name | Key |
| --- | --- |
| [Produce](#The_Messages_Produce) | 0   |
| [Fetch](#The_Messages_Fetch) | 1   |
| [ListOffsets](#The_Messages_ListOffsets) | 2   |
| [Metadata](#The_Messages_Metadata) | 3   |
| [LeaderAndIsr](#The_Messages_LeaderAndIsr) | 4   |
| [StopReplica](#The_Messages_StopReplica) | 5   |
| [UpdateMetadata](#The_Messages_UpdateMetadata) | 6   |
| [ControlledShutdown](#The_Messages_ControlledShutdown) | 7   |
| [OffsetCommit](#The_Messages_OffsetCommit) | 8   |
| [OffsetFetch](#The_Messages_OffsetFetch) | 9   |
| [FindCoordinator](#The_Messages_FindCoordinator) | 10  |
| [JoinGroup](#The_Messages_JoinGroup) | 11  |
| [Heartbeat](#The_Messages_Heartbeat) | 12  |
| [LeaveGroup](#The_Messages_LeaveGroup) | 13  |
| [SyncGroup](#The_Messages_SyncGroup) | 14  |
| [DescribeGroups](#The_Messages_DescribeGroups) | 15  |
| [ListGroups](#The_Messages_ListGroups) | 16  |
| [SaslHandshake](#The_Messages_SaslHandshake) | 17  |
| [ApiVersions](#The_Messages_ApiVersions) | 18  |
| [CreateTopics](#The_Messages_CreateTopics) | 19  |
| [DeleteTopics](#The_Messages_DeleteTopics) | 20  |
| [DeleteRecords](#The_Messages_DeleteRecords) | 21  |
| [InitProducerId](#The_Messages_InitProducerId) | 22  |
| [OffsetForLeaderEpoch](#The_Messages_OffsetForLeaderEpoch) | 23  |
| [AddPartitionsToTxn](#The_Messages_AddPartitionsToTxn) | 24  |
| [AddOffsetsToTxn](#The_Messages_AddOffsetsToTxn) | 25  |
| [EndTxn](#The_Messages_EndTxn) | 26  |
| [WriteTxnMarkers](#The_Messages_WriteTxnMarkers) | 27  |
| [TxnOffsetCommit](#The_Messages_TxnOffsetCommit) | 28  |
| [DescribeAcls](#The_Messages_DescribeAcls) | 29  |
| [CreateAcls](#The_Messages_CreateAcls) | 30  |
| [DeleteAcls](#The_Messages_DeleteAcls) | 31  |
| [DescribeConfigs](#The_Messages_DescribeConfigs) | 32  |
| [AlterConfigs](#The_Messages_AlterConfigs) | 33  |
| [AlterReplicaLogDirs](#The_Messages_AlterReplicaLogDirs) | 34  |
| [DescribeLogDirs](#The_Messages_DescribeLogDirs) | 35  |
| [SaslAuthenticate](#The_Messages_SaslAuthenticate) | 36  |
| [CreatePartitions](#The_Messages_CreatePartitions) | 37  |
| [CreateDelegationToken](#The_Messages_CreateDelegationToken) | 38  |
| [RenewDelegationToken](#The_Messages_RenewDelegationToken) | 39  |
| [ExpireDelegationToken](#The_Messages_ExpireDelegationToken) | 40  |
| [DescribeDelegationToken](#The_Messages_DescribeDelegationToken) | 41  |
| [DeleteGroups](#The_Messages_DeleteGroups) | 42  |
| [ElectLeaders](#The_Messages_ElectLeaders) | 43  |
| [IncrementalAlterConfigs](#The_Messages_IncrementalAlterConfigs) | 44  |
| [AlterPartitionReassignments](#The_Messages_AlterPartitionReassignments) | 45  |
| [ListPartitionReassignments](#The_Messages_ListPartitionReassignments) | 46  |
| [OffsetDelete](#The_Messages_OffsetDelete) | 47  |
| [DescribeClientQuotas](#The_Messages_DescribeClientQuotas) | 48  |
| [AlterClientQuotas](#The_Messages_AlterClientQuotas) | 49  |
| [DescribeUserScramCredentials](#The_Messages_DescribeUserScramCredentials) | 50  |
| [AlterUserScramCredentials](#The_Messages_AlterUserScramCredentials) | 51  |
| [DescribeQuorum](#The_Messages_DescribeQuorum) | 55  |
| [AlterPartition](#The_Messages_AlterPartition) | 56  |
| [UpdateFeatures](#The_Messages_UpdateFeatures) | 57  |
| [Envelope](#The_Messages_Envelope) | 58  |
| [DescribeCluster](#The_Messages_DescribeCluster) | 60  |
| [DescribeProducers](#The_Messages_DescribeProducers) | 61  |
| [UnregisterBroker](#The_Messages_UnregisterBroker) | 64  |
| [DescribeTransactions](#The_Messages_DescribeTransactions) | 65  |
| [ListTransactions](#The_Messages_ListTransactions) | 66  |
| [AllocateProducerIds](#The_Messages_AllocateProducerIds) | 67  |
| [ConsumerGroupHeartbeat](#The_Messages_ConsumerGroupHeartbeat) | 68  |

#### [The Messages](#protocol_messages)

This section gives details on each of the individual API Messages, their usage, their binary format, and the meaning of their fields.

##### Headers:

Request Header v0 => request\_api\_key request\_api\_version correlation_id 
  request\_api\_key => INT16
  request\_api\_version => INT16
  correlation_id => INT32

| Field | Description |
| --- | --- |
| request\_api\_key | The API key of this request. |
| request\_api\_version | The API version of this request. |
| correlation_id | The correlation ID of this request. |

Request Header v1 => request\_api\_key request\_api\_version correlation\_id client\_id 
  request\_api\_key => INT16
  request\_api\_version => INT16
  correlation_id => INT32
  client\_id => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| request\_api\_key | The API key of this request. |
| request\_api\_version | The API version of this request. |
| correlation_id | The correlation ID of this request. |
| client_id | The client ID string. |

Request Header v2 => request\_api\_key request\_api\_version correlation\_id client\_id TAG_BUFFER 
  request\_api\_key => INT16
  request\_api\_version => INT16
  correlation_id => INT32
  client\_id => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| request\_api\_key | The API key of this request. |
| request\_api\_version | The API version of this request. |
| correlation_id | The correlation ID of this request. |
| client_id | The client ID string. |
| \_tagged\_fields | The tagged fields |

Response Header v0 => correlation_id 
  correlation_id => INT32

| Field | Description |
| --- | --- |
| correlation_id | The correlation ID of this response. |

Response Header v1 => correlation\_id TAG\_BUFFER 
  correlation_id => INT32

| Field | Description |
| --- | --- |
| correlation_id | The correlation ID of this response. |
| \_tagged\_fields | The tagged fields |

##### Produce API (Key: 0):

**Requests:**  

Produce Request (Version: 0) => acks timeout\_ms \[topic\_data\] 
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 1) => acks timeout\_ms \[topic\_data\] 
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 2) => acks timeout\_ms \[topic\_data\] 
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 3) => transactional\_id acks timeout\_ms \[topic_data\] 
  transactional\_id => NULLABLE\_STRING
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| transactional_id | The transactional ID, or null if the producer is not transactional. |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 4) => transactional\_id acks timeout\_ms \[topic_data\] 
  transactional\_id => NULLABLE\_STRING
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| transactional_id | The transactional ID, or null if the producer is not transactional. |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 5) => transactional\_id acks timeout\_ms \[topic_data\] 
  transactional\_id => NULLABLE\_STRING
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| transactional_id | The transactional ID, or null if the producer is not transactional. |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 6) => transactional\_id acks timeout\_ms \[topic_data\] 
  transactional\_id => NULLABLE\_STRING
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| transactional_id | The transactional ID, or null if the producer is not transactional. |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 7) => transactional\_id acks timeout\_ms \[topic_data\] 
  transactional\_id => NULLABLE\_STRING
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| transactional_id | The transactional ID, or null if the producer is not transactional. |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 8) => transactional\_id acks timeout\_ms \[topic_data\] 
  transactional\_id => NULLABLE\_STRING
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] 
    name => STRING
    partition_data => index records 
      index => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| transactional_id | The transactional ID, or null if the producer is not transactional. |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |

Produce Request (Version: 9) => transactional\_id acks timeout\_ms \[topic\_data\] TAG\_BUFFER 
  transactional\_id => COMPACT\_NULLABLE_STRING
  acks => INT16
  timeout_ms => INT32
  topic\_data => name \[partition\_data\] TAG_BUFFER 
    name => COMPACT_STRING
    partition\_data => index records TAG\_BUFFER 
      index => INT32
      records => COMPACT_RECORDS

| Field | Description |
| --- | --- |
| transactional_id | The transactional ID, or null if the producer is not transactional. |
| acks | The number of acknowledgments the producer requires the leader to have received before considering a request complete. Allowed values: 0 for no acknowledgments, 1 for only the leader and -1 for the full ISR. |
| timeout_ms | The timeout to await a response in milliseconds. |
| topic_data | Each topic to produce to. |
| name | The topic name. |
| partition_data | Each partition to produce to. |
| index | The partition index. |
| records | The record data to be produced. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

Produce Response (Version: 0) => \[responses\] 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base_offset 
      index => INT32
      error_code => INT16
      base_offset => INT64

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |

Produce Response (Version: 1) => \[responses\] throttle\_time\_ms 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base_offset 
      index => INT32
      error_code => INT16
      base_offset => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

Produce Response (Version: 2) => \[responses\] throttle\_time\_ms 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base\_offset log\_append\_time\_ms 
      index => INT32
      error_code => INT16
      base_offset => INT64
      log\_append\_time_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| log\_append\_time_ms | The timestamp returned by broker after appending the messages. If CreateTime is used for the topic, the timestamp will be -1. If LogAppendTime is used for the topic, the timestamp will be the broker local time when the messages are appended. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

Produce Response (Version: 3) => \[responses\] throttle\_time\_ms 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base\_offset log\_append\_time\_ms 
      index => INT32
      error_code => INT16
      base_offset => INT64
      log\_append\_time_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| log\_append\_time_ms | The timestamp returned by broker after appending the messages. If CreateTime is used for the topic, the timestamp will be -1. If LogAppendTime is used for the topic, the timestamp will be the broker local time when the messages are appended. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

Produce Response (Version: 4) => \[responses\] throttle\_time\_ms 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base\_offset log\_append\_time\_ms 
      index => INT32
      error_code => INT16
      base_offset => INT64
      log\_append\_time_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| log\_append\_time_ms | The timestamp returned by broker after appending the messages. If CreateTime is used for the topic, the timestamp will be -1. If LogAppendTime is used for the topic, the timestamp will be the broker local time when the messages are appended. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

Produce Response (Version: 5) => \[responses\] throttle\_time\_ms 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base\_offset log\_append\_time\_ms log\_start\_offset 
      index => INT32
      error_code => INT16
      base_offset => INT64
      log\_append\_time_ms => INT64
      log\_start\_offset => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| log\_append\_time_ms | The timestamp returned by broker after appending the messages. If CreateTime is used for the topic, the timestamp will be -1. If LogAppendTime is used for the topic, the timestamp will be the broker local time when the messages are appended. |
| log\_start\_offset | The log start offset. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

Produce Response (Version: 6) => \[responses\] throttle\_time\_ms 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base\_offset log\_append\_time\_ms log\_start\_offset 
      index => INT32
      error_code => INT16
      base_offset => INT64
      log\_append\_time_ms => INT64
      log\_start\_offset => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| log\_append\_time_ms | The timestamp returned by broker after appending the messages. If CreateTime is used for the topic, the timestamp will be -1. If LogAppendTime is used for the topic, the timestamp will be the broker local time when the messages are appended. |
| log\_start\_offset | The log start offset. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

Produce Response (Version: 7) => \[responses\] throttle\_time\_ms 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base\_offset log\_append\_time\_ms log\_start\_offset 
      index => INT32
      error_code => INT16
      base_offset => INT64
      log\_append\_time_ms => INT64
      log\_start\_offset => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| log\_append\_time_ms | The timestamp returned by broker after appending the messages. If CreateTime is used for the topic, the timestamp will be -1. If LogAppendTime is used for the topic, the timestamp will be the broker local time when the messages are appended. |
| log\_start\_offset | The log start offset. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

Produce Response (Version: 8) => \[responses\] throttle\_time\_ms 
  responses => name \[partition_responses\] 
    name => STRING
    partition\_responses => index error\_code base\_offset log\_append\_time\_ms log\_start\_offset \[record\_errors\] error\_message 
      index => INT32
      error_code => INT16
      base_offset => INT64
      log\_append\_time_ms => INT64
      log\_start\_offset => INT64
      record\_errors => batch\_index batch\_index\_error_message 
        batch_index => INT32
        batch\_index\_error\_message => NULLABLE\_STRING
      error\_message => NULLABLE\_STRING
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| log\_append\_time_ms | The timestamp returned by broker after appending the messages. If CreateTime is used for the topic, the timestamp will be -1. If LogAppendTime is used for the topic, the timestamp will be the broker local time when the messages are appended. |
| log\_start\_offset | The log start offset. |
| record_errors | The batch indices of records that caused the batch to be dropped |
| batch_index | The batch index of the record that cause the batch to be dropped |
| batch\_index\_error_message | The error message of the record that caused the batch to be dropped |
| error_message | The global error message summarizing the common root cause of the records that caused the batch to be dropped |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

Produce Response (Version: 9) => \[responses\] throttle\_time\_ms TAG_BUFFER 
  responses => name \[partition\_responses\] TAG\_BUFFER 
    name => COMPACT_STRING
    partition\_responses => index error\_code base\_offset log\_append\_time\_ms log\_start\_offset \[record\_errors\] error\_message TAG_BUFFER 
      index => INT32
      error_code => INT16
      base_offset => INT64
      log\_append\_time_ms => INT64
      log\_start\_offset => INT64
      record\_errors => batch\_index batch\_index\_error\_message TAG\_BUFFER 
        batch_index => INT32
        batch\_index\_error\_message => COMPACT\_NULLABLE_STRING
      error\_message => COMPACT\_NULLABLE_STRING
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| responses | Each produce response |
| name | The topic name |
| partition_responses | Each partition that we produced to within the topic. |
| index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| base_offset | The base offset. |
| log\_append\_time_ms | The timestamp returned by broker after appending the messages. If CreateTime is used for the topic, the timestamp will be -1. If LogAppendTime is used for the topic, the timestamp will be the broker local time when the messages are appended. |
| log\_start\_offset | The log start offset. |
| record_errors | The batch indices of records that caused the batch to be dropped |
| batch_index | The batch index of the record that cause the batch to be dropped |
| batch\_index\_error_message | The error message of the record that caused the batch to be dropped |
| \_tagged\_fields | The tagged fields |
| error_message | The global error message summarizing the common root cause of the records that caused the batch to be dropped |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| \_tagged\_fields | The tagged fields |

##### Fetch API (Key: 1):

**Requests:**  

Fetch Request (Version: 0) => replica\_id max\_wait\_ms min\_bytes \[topics\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      partition\_max\_bytes => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |

Fetch Request (Version: 1) => replica\_id max\_wait\_ms min\_bytes \[topics\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      partition\_max\_bytes => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |

Fetch Request (Version: 2) => replica\_id max\_wait\_ms min\_bytes \[topics\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      partition\_max\_bytes => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |

Fetch Request (Version: 3) => replica\_id max\_wait\_ms min\_bytes max_bytes \[topics\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      partition\_max\_bytes => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |

Fetch Request (Version: 4) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level \[topics\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      partition\_max\_bytes => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |

Fetch Request (Version: 5) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level \[topics\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset log\_start\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |

Fetch Request (Version: 6) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level \[topics\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset log\_start\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |

Fetch Request (Version: 7) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset log\_start\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic \[partitions\] 
    topic => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic | The topic name. |
| partitions | The partitions indexes to forget. |

Fetch Request (Version: 8) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition fetch\_offset log\_start\_offset partition\_max_bytes 
      partition => INT32
      fetch_offset => INT64
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic \[partitions\] 
    topic => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| fetch_offset | The message offset. |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic | The topic name. |
| partitions | The partitions indexes to forget. |

Fetch Request (Version: 9) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition current\_leader\_epoch fetch\_offset log\_start\_offset partition\_max_bytes 
      partition => INT32
      current\_leader\_epoch => INT32
      fetch_offset => INT64
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic \[partitions\] 
    topic => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| current\_leader\_epoch | The current leader epoch of the partition. |
| fetch_offset | The message offset. |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic | The topic name. |
| partitions | The partitions indexes to forget. |

Fetch Request (Version: 10) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition current\_leader\_epoch fetch\_offset log\_start\_offset partition\_max_bytes 
      partition => INT32
      current\_leader\_epoch => INT32
      fetch_offset => INT64
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic \[partitions\] 
    topic => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| current\_leader\_epoch | The current leader epoch of the partition. |
| fetch_offset | The message offset. |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic | The topic name. |
| partitions | The partitions indexes to forget. |

Fetch Request (Version: 11) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] rack_id 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition current\_leader\_epoch fetch\_offset log\_start\_offset partition\_max_bytes 
      partition => INT32
      current\_leader\_epoch => INT32
      fetch_offset => INT64
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic \[partitions\] 
    topic => STRING
    partitions => INT32
  rack_id => STRING

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| current\_leader\_epoch | The current leader epoch of the partition. |
| fetch_offset | The message offset. |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic | The topic name. |
| partitions | The partitions indexes to forget. |
| rack_id | Rack ID of the consumer making this request |

Fetch Request (Version: 12) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] rack\_id TAG\_BUFFER 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic \[partitions\] TAG_BUFFER 
    topic => COMPACT_STRING
    partitions => partition current\_leader\_epoch fetch\_offset last\_fetched\_epoch log\_start\_offset partition\_max\_bytes TAG\_BUFFER 
      partition => INT32
      current\_leader\_epoch => INT32
      fetch_offset => INT64
      last\_fetched\_epoch => INT32
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic \[partitions\] TAG_BUFFER 
    topic => COMPACT_STRING
    partitions => INT32
  rack\_id => COMPACT\_STRING

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic | The name of the topic to fetch. |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| current\_leader\_epoch | The current leader epoch of the partition. |
| fetch_offset | The message offset. |
| last\_fetched\_epoch | The epoch of the last fetched record or -1 if there is none |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic | The topic name. |
| partitions | The partitions indexes to forget. |
| \_tagged\_fields | The tagged fields |
| rack_id | Rack ID of the consumer making this request |
| \_tagged\_fields | The tagged fields |

Fetch Request (Version: 13) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] rack\_id TAG\_BUFFER 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition current\_leader\_epoch fetch\_offset last\_fetched\_epoch log\_start\_offset partition\_max\_bytes TAG\_BUFFER 
      partition => INT32
      current\_leader\_epoch => INT32
      fetch_offset => INT64
      last\_fetched\_epoch => INT32
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => INT32
  rack\_id => COMPACT\_STRING

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic_id | The unique topic ID |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| current\_leader\_epoch | The current leader epoch of the partition. |
| fetch_offset | The message offset. |
| last\_fetched\_epoch | The epoch of the last fetched record or -1 if there is none |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic_id | The unique topic ID |
| partitions | The partitions indexes to forget. |
| \_tagged\_fields | The tagged fields |
| rack_id | Rack ID of the consumer making this request |
| \_tagged\_fields | The tagged fields |

Fetch Request (Version: 14) => replica\_id max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] rack\_id TAG\_BUFFER 
  replica_id => INT32
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition current\_leader\_epoch fetch\_offset last\_fetched\_epoch log\_start\_offset partition\_max\_bytes TAG\_BUFFER 
      partition => INT32
      current\_leader\_epoch => INT32
      fetch_offset => INT64
      last\_fetched\_epoch => INT32
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => INT32
  rack\_id => COMPACT\_STRING

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic_id | The unique topic ID |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| current\_leader\_epoch | The current leader epoch of the partition. |
| fetch_offset | The message offset. |
| last\_fetched\_epoch | The epoch of the last fetched record or -1 if there is none |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic_id | The unique topic ID |
| partitions | The partitions indexes to forget. |
| \_tagged\_fields | The tagged fields |
| rack_id | Rack ID of the consumer making this request |
| \_tagged\_fields | The tagged fields |

Fetch Request (Version: 15) => max\_wait\_ms min\_bytes max\_bytes isolation\_level session\_id session\_epoch \[topics\] \[forgotten\_topics\_data\] rack\_id TAG_BUFFER 
  max\_wait\_ms => INT32
  min_bytes => INT32
  max_bytes => INT32
  isolation_level => INT8
  session_id => INT32
  session_epoch => INT32
  topics => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition current\_leader\_epoch fetch\_offset last\_fetched\_epoch log\_start\_offset partition\_max\_bytes TAG\_BUFFER 
      partition => INT32
      current\_leader\_epoch => INT32
      fetch_offset => INT64
      last\_fetched\_epoch => INT32
      log\_start\_offset => INT64
      partition\_max\_bytes => INT32
  forgotten\_topics\_data => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => INT32
  rack\_id => COMPACT\_STRING

| Field | Description |
| --- | --- |
| max\_wait\_ms | The maximum time in milliseconds to wait for the response. |
| min_bytes | The minimum bytes to accumulate in the response. |
| max_bytes | The maximum bytes to fetch. See KIP-74 for cases where this limit may not be honored. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| session_id | The fetch session ID. |
| session_epoch | The fetch session epoch, which is used for ordering requests in a session. |
| topics | The topics to fetch. |
| topic_id | The unique topic ID |
| partitions | The partitions to fetch. |
| partition | The partition index. |
| current\_leader\_epoch | The current leader epoch of the partition. |
| fetch_offset | The message offset. |
| last\_fetched\_epoch | The epoch of the last fetched record or -1 if there is none |
| log\_start\_offset | The earliest available offset of the follower replica. The field is only used when the request is sent by the follower. |
| partition\_max\_bytes | The maximum bytes to fetch from this partition. See KIP-74 for cases where this limit may not be honored. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| forgotten\_topics\_data | In an incremental fetch request, the partitions to remove. |
| topic_id | The unique topic ID |
| partitions | The partitions indexes to forget. |
| \_tagged\_fields | The tagged fields |
| rack_id | Rack ID of the consumer making this request |
| \_tagged\_fields | The tagged fields |

**Responses:**  

Fetch Response (Version: 0) => \[responses\] 
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high_watermark records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| records | The record data. |

Fetch Response (Version: 1) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high_watermark records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| records | The record data. |

Fetch Response (Version: 2) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high_watermark records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| records | The record data. |

Fetch Response (Version: 3) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high_watermark records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| records | The record data. |

Fetch Response (Version: 4) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset \[aborted\_transactions\] records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      aborted\_transactions => producer\_id first_offset 
        producer_id => INT64
        first_offset => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| records | The record data. |

Fetch Response (Version: 5) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first_offset 
        producer_id => INT64
        first_offset => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| records | The record data. |

Fetch Response (Version: 6) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first_offset 
        producer_id => INT64
        first_offset => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| records | The record data. |

Fetch Response (Version: 7) => throttle\_time\_ms error\_code session\_id \[responses\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first_offset 
        producer_id => INT64
        first_offset => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| records | The record data. |

Fetch Response (Version: 8) => throttle\_time\_ms error\_code session\_id \[responses\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first_offset 
        producer_id => INT64
        first_offset => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| records | The record data. |

Fetch Response (Version: 9) => throttle\_time\_ms error\_code session\_id \[responses\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first_offset 
        producer_id => INT64
        first_offset => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| records | The record data. |

Fetch Response (Version: 10) => throttle\_time\_ms error\_code session\_id \[responses\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first_offset 
        producer_id => INT64
        first_offset => INT64
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| records | The record data. |

Fetch Response (Version: 11) => throttle\_time\_ms error\_code session\_id \[responses\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic \[partitions\] 
    topic => STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] preferred\_read\_replica records 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first_offset 
        producer_id => INT64
        first_offset => INT64
      preferred\_read\_replica => INT32
      records => RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| preferred\_read\_replica | The preferred read replica for the consumer to use on its next fetch request |
| records | The record data. |

Fetch Response (Version: 12) => throttle\_time\_ms error\_code session\_id \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic \[partitions\] TAG_BUFFER 
    topic => COMPACT_STRING
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] preferred\_read\_replica records TAG_BUFFER 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first\_offset TAG\_BUFFER 
        producer_id => INT64
        first_offset => INT64
      preferred\_read\_replica => INT32
      records => COMPACT_RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic | The topic name. |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| \_tagged\_fields | The tagged fields |
| preferred\_read\_replica | The preferred read replica for the consumer to use on its next fetch request |
| records | The record data. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

Fetch Response (Version: 13) => throttle\_time\_ms error\_code session\_id \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] preferred\_read\_replica records TAG_BUFFER 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first\_offset TAG\_BUFFER 
        producer_id => INT64
        first_offset => INT64
      preferred\_read\_replica => INT32
      records => COMPACT_RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic_id | The unique topic ID |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| \_tagged\_fields | The tagged fields |
| preferred\_read\_replica | The preferred read replica for the consumer to use on its next fetch request |
| records | The record data. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

Fetch Response (Version: 14) => throttle\_time\_ms error\_code session\_id \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] preferred\_read\_replica records TAG_BUFFER 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first\_offset TAG\_BUFFER 
        producer_id => INT64
        first_offset => INT64
      preferred\_read\_replica => INT32
      records => COMPACT_RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic_id | The unique topic ID |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| \_tagged\_fields | The tagged fields |
| preferred\_read\_replica | The preferred read replica for the consumer to use on its next fetch request |
| records | The record data. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

Fetch Response (Version: 15) => throttle\_time\_ms error\_code session\_id \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  session_id => INT32
  responses => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition\_index error\_code high\_watermark last\_stable\_offset log\_start\_offset \[aborted\_transactions\] preferred\_read\_replica records TAG_BUFFER 
      partition_index => INT32
      error_code => INT16
      high_watermark => INT64
      last\_stable\_offset => INT64
      log\_start\_offset => INT64
      aborted\_transactions => producer\_id first\_offset TAG\_BUFFER 
        producer_id => INT64
        first_offset => INT64
      preferred\_read\_replica => INT32
      records => COMPACT_RECORDS

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| session_id | The fetch session ID, or 0 if this is not part of a fetch session. |
| responses | The response topics. |
| topic_id | The unique topic ID |
| partitions | The topic partitions. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no fetch error. |
| high_watermark | The current high water mark. |
| last\_stable\_offset | The last stable offset (or LSO) of the partition. This is the last offset such that the state of all transactional records prior to this offset have been decided (ABORTED or COMMITTED) |
| log\_start\_offset | The current log start offset. |
| aborted_transactions | The aborted transactions. |
| producer_id | The producer id associated with the aborted transaction. |
| first_offset | The first offset in the aborted transaction. |
| \_tagged\_fields | The tagged fields |
| preferred\_read\_replica | The preferred read replica for the consumer to use on its next fetch request |
| records | The record data. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### ListOffsets API (Key: 2):

**Requests:**  

ListOffsets Request (Version: 0) => replica_id \[topics\] 
  replica_id => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index timestamp max\_num_offsets 
      partition_index => INT32
      timestamp => INT64
      max\_num\_offsets => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| timestamp | The current timestamp. |
| max\_num\_offsets | The maximum number of offsets to report. |

ListOffsets Request (Version: 1) => replica_id \[topics\] 
  replica_id => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition_index timestamp 
      partition_index => INT32
      timestamp => INT64

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| timestamp | The current timestamp. |

ListOffsets Request (Version: 2) => replica\_id isolation\_level \[topics\] 
  replica_id => INT32
  isolation_level => INT8
  topics => name \[partitions\] 
    name => STRING
    partitions => partition_index timestamp 
      partition_index => INT32
      timestamp => INT64

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| timestamp | The current timestamp. |

ListOffsets Request (Version: 3) => replica\_id isolation\_level \[topics\] 
  replica_id => INT32
  isolation_level => INT8
  topics => name \[partitions\] 
    name => STRING
    partitions => partition_index timestamp 
      partition_index => INT32
      timestamp => INT64

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| timestamp | The current timestamp. |

ListOffsets Request (Version: 4) => replica\_id isolation\_level \[topics\] 
  replica_id => INT32
  isolation_level => INT8
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index current\_leader_epoch timestamp 
      partition_index => INT32
      current\_leader\_epoch => INT32
      timestamp => INT64

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| current\_leader\_epoch | The current leader epoch. |
| timestamp | The current timestamp. |

ListOffsets Request (Version: 5) => replica\_id isolation\_level \[topics\] 
  replica_id => INT32
  isolation_level => INT8
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index current\_leader_epoch timestamp 
      partition_index => INT32
      current\_leader\_epoch => INT32
      timestamp => INT64

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| current\_leader\_epoch | The current leader epoch. |
| timestamp | The current timestamp. |

ListOffsets Request (Version: 6) => replica\_id isolation\_level \[topics\] TAG_BUFFER 
  replica_id => INT32
  isolation_level => INT8
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index current\_leader\_epoch timestamp TAG\_BUFFER 
      partition_index => INT32
      current\_leader\_epoch => INT32
      timestamp => INT64

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| current\_leader\_epoch | The current leader epoch. |
| timestamp | The current timestamp. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

ListOffsets Request (Version: 7) => replica\_id isolation\_level \[topics\] TAG_BUFFER 
  replica_id => INT32
  isolation_level => INT8
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index current\_leader\_epoch timestamp TAG\_BUFFER 
      partition_index => INT32
      current\_leader\_epoch => INT32
      timestamp => INT64

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| current\_leader\_epoch | The current leader epoch. |
| timestamp | The current timestamp. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

ListOffsets Request (Version: 8) => replica\_id isolation\_level \[topics\] TAG_BUFFER 
  replica_id => INT32
  isolation_level => INT8
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index current\_leader\_epoch timestamp TAG\_BUFFER 
      partition_index => INT32
      current\_leader\_epoch => INT32
      timestamp => INT64

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the requester, or -1 if this request is being made by a normal consumer. |
| isolation_level | This setting controls the visibility of transactional records. Using READ\_UNCOMMITTED (isolation\_level = 0) makes all records visible. With READ\_COMMITTED (isolation\_level = 1), non-transactional and COMMITTED transactional records are visible. To be more concrete, READ_COMMITTED returns all data from offsets smaller than the current LSO (last stable offset), and enables the inclusion of the list of aborted transactions in the result, which allows consumers to discard ABORTED transactional records |
| topics | Each topic in the request. |
| name | The topic name. |
| partitions | Each partition in the request. |
| partition_index | The partition index. |
| current\_leader\_epoch | The current leader epoch. |
| timestamp | The current timestamp. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ListOffsets Response (Version: 0) => \[topics\] 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code \[old\_style\_offsets\] 
      partition_index => INT32
      error_code => INT16
      old\_style\_offsets => INT64

| Field | Description |
| --- | --- |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| old\_style\_offsets | The result offsets. |

ListOffsets Response (Version: 1) => \[topics\] 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code timestamp offset 
      partition_index => INT32
      error_code => INT16
      timestamp => INT64
      offset => INT64

| Field | Description |
| --- | --- |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| timestamp | The timestamp associated with the returned offset. |
| offset | The returned offset. |

ListOffsets Response (Version: 2) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code timestamp offset 
      partition_index => INT32
      error_code => INT16
      timestamp => INT64
      offset => INT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| timestamp | The timestamp associated with the returned offset. |
| offset | The returned offset. |

ListOffsets Response (Version: 3) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code timestamp offset 
      partition_index => INT32
      error_code => INT16
      timestamp => INT64
      offset => INT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| timestamp | The timestamp associated with the returned offset. |
| offset | The returned offset. |

ListOffsets Response (Version: 4) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code timestamp offset leader_epoch 
      partition_index => INT32
      error_code => INT16
      timestamp => INT64
      offset => INT64
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| timestamp | The timestamp associated with the returned offset. |
| offset | The returned offset. |
| leader_epoch |     |

ListOffsets Response (Version: 5) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code timestamp offset leader_epoch 
      partition_index => INT32
      error_code => INT16
      timestamp => INT64
      offset => INT64
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| timestamp | The timestamp associated with the returned offset. |
| offset | The returned offset. |
| leader_epoch |     |

ListOffsets Response (Version: 6) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index error\_code timestamp offset leader\_epoch TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16
      timestamp => INT64
      offset => INT64
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| timestamp | The timestamp associated with the returned offset. |
| offset | The returned offset. |
| leader_epoch |     |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

ListOffsets Response (Version: 7) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index error\_code timestamp offset leader\_epoch TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16
      timestamp => INT64
      offset => INT64
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| timestamp | The timestamp associated with the returned offset. |
| offset | The returned offset. |
| leader_epoch |     |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

ListOffsets Response (Version: 8) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index error\_code timestamp offset leader\_epoch TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16
      timestamp => INT64
      offset => INT64
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| timestamp | The timestamp associated with the returned offset. |
| offset | The returned offset. |
| leader_epoch |     |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### Metadata API (Key: 3):

**Requests:**  

Metadata Request (Version: 0) => \[topics\] 
  topics => name 
    name => STRING

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |

Metadata Request (Version: 1) => \[topics\] 
  topics => name 
    name => STRING

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |

Metadata Request (Version: 2) => \[topics\] 
  topics => name 
    name => STRING

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |

Metadata Request (Version: 3) => \[topics\] 
  topics => name 
    name => STRING

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |

Metadata Request (Version: 4) => \[topics\] allow\_auto\_topic_creation 
  topics => name 
    name => STRING
  allow\_auto\_topic_creation => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |

Metadata Request (Version: 5) => \[topics\] allow\_auto\_topic_creation 
  topics => name 
    name => STRING
  allow\_auto\_topic_creation => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |

Metadata Request (Version: 6) => \[topics\] allow\_auto\_topic_creation 
  topics => name 
    name => STRING
  allow\_auto\_topic_creation => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |

Metadata Request (Version: 7) => \[topics\] allow\_auto\_topic_creation 
  topics => name 
    name => STRING
  allow\_auto\_topic_creation => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |

Metadata Request (Version: 8) => \[topics\] allow\_auto\_topic\_creation include\_cluster\_authorized\_operations include\_topic\_authorized_operations 
  topics => name 
    name => STRING
  allow\_auto\_topic_creation => BOOLEAN
  include\_cluster\_authorized_operations => BOOLEAN
  include\_topic\_authorized_operations => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |
| include\_cluster\_authorized_operations | Whether to include cluster authorized operations. |
| include\_topic\_authorized_operations | Whether to include topic authorized operations. |

Metadata Request (Version: 9) => \[topics\] allow\_auto\_topic\_creation include\_cluster\_authorized\_operations include\_topic\_authorized\_operations TAG\_BUFFER 
  topics => name TAG_BUFFER 
    name => COMPACT_STRING
  allow\_auto\_topic_creation => BOOLEAN
  include\_cluster\_authorized_operations => BOOLEAN
  include\_topic\_authorized_operations => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| name | The topic name. |
| \_tagged\_fields | The tagged fields |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |
| include\_cluster\_authorized_operations | Whether to include cluster authorized operations. |
| include\_topic\_authorized_operations | Whether to include topic authorized operations. |
| \_tagged\_fields | The tagged fields |

Metadata Request (Version: 10) => \[topics\] allow\_auto\_topic\_creation include\_cluster\_authorized\_operations include\_topic\_authorized\_operations TAG\_BUFFER 
  topics => topic\_id name TAG\_BUFFER 
    topic_id => UUID
    name => COMPACT\_NULLABLE\_STRING
  allow\_auto\_topic_creation => BOOLEAN
  include\_cluster\_authorized_operations => BOOLEAN
  include\_topic\_authorized_operations => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| topic_id | The topic id. |
| name | The topic name. |
| \_tagged\_fields | The tagged fields |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |
| include\_cluster\_authorized_operations | Whether to include cluster authorized operations. |
| include\_topic\_authorized_operations | Whether to include topic authorized operations. |
| \_tagged\_fields | The tagged fields |

Metadata Request (Version: 11) => \[topics\] allow\_auto\_topic\_creation include\_topic\_authorized\_operations TAG_BUFFER 
  topics => topic\_id name TAG\_BUFFER 
    topic_id => UUID
    name => COMPACT\_NULLABLE\_STRING
  allow\_auto\_topic_creation => BOOLEAN
  include\_topic\_authorized_operations => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| topic_id | The topic id. |
| name | The topic name. |
| \_tagged\_fields | The tagged fields |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |
| include\_topic\_authorized_operations | Whether to include topic authorized operations. |
| \_tagged\_fields | The tagged fields |

Metadata Request (Version: 12) => \[topics\] allow\_auto\_topic\_creation include\_topic\_authorized\_operations TAG_BUFFER 
  topics => topic\_id name TAG\_BUFFER 
    topic_id => UUID
    name => COMPACT\_NULLABLE\_STRING
  allow\_auto\_topic_creation => BOOLEAN
  include\_topic\_authorized_operations => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to fetch metadata for. |
| topic_id | The topic id. |
| name | The topic name. |
| \_tagged\_fields | The tagged fields |
| allow\_auto\_topic_creation | If this is true, the broker may auto-create topics that we requested which do not already exist, if it is configured to do so. |
| include\_topic\_authorized_operations | Whether to include topic authorized operations. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

Metadata Response (Version: 0) => \[brokers\] \[topics\] 
  brokers => node_id host port 
    node_id => INT32
    host => STRING
    port => INT32
  topics => error_code name \[partitions\] 
    error_code => INT16
    name => STRING
    partitions => error\_code partition\_index leader\_id \[replica\_nodes\] \[isr_nodes\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      replica_nodes => INT32
      isr_nodes => INT32

| Field | Description |
| --- | --- |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |

Metadata Response (Version: 1) => \[brokers\] controller_id \[topics\] 
  brokers => node_id host port rack 
    node_id => INT32
    host => STRING
    port => INT32
    rack => NULLABLE_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] 
    error_code => INT16
    name => STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id \[replica\_nodes\] \[isr_nodes\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      replica_nodes => INT32
      isr_nodes => INT32

| Field | Description |
| --- | --- |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |

Metadata Response (Version: 2) => \[brokers\] cluster\_id controller\_id \[topics\] 
  brokers => node_id host port rack 
    node_id => INT32
    host => STRING
    port => INT32
    rack => NULLABLE_STRING
  cluster\_id => NULLABLE\_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] 
    error_code => INT16
    name => STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id \[replica\_nodes\] \[isr_nodes\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      replica_nodes => INT32
      isr_nodes => INT32

| Field | Description |
| --- | --- |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |

Metadata Response (Version: 3) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] 
  throttle\_time\_ms => INT32
  brokers => node_id host port rack 
    node_id => INT32
    host => STRING
    port => INT32
    rack => NULLABLE_STRING
  cluster\_id => NULLABLE\_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] 
    error_code => INT16
    name => STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id \[replica\_nodes\] \[isr_nodes\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      replica_nodes => INT32
      isr_nodes => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |

Metadata Response (Version: 4) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] 
  throttle\_time\_ms => INT32
  brokers => node_id host port rack 
    node_id => INT32
    host => STRING
    port => INT32
    rack => NULLABLE_STRING
  cluster\_id => NULLABLE\_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] 
    error_code => INT16
    name => STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id \[replica\_nodes\] \[isr_nodes\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      replica_nodes => INT32
      isr_nodes => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |

Metadata Response (Version: 5) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] 
  throttle\_time\_ms => INT32
  brokers => node_id host port rack 
    node_id => INT32
    host => STRING
    port => INT32
    rack => NULLABLE_STRING
  cluster\_id => NULLABLE\_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] 
    error_code => INT16
    name => STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id \[replica\_nodes\] \[isr\_nodes\] \[offline\_replicas\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      replica_nodes => INT32
      isr_nodes => INT32
      offline_replicas => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |
| offline_replicas | The set of offline replicas of this partition. |

Metadata Response (Version: 6) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] 
  throttle\_time\_ms => INT32
  brokers => node_id host port rack 
    node_id => INT32
    host => STRING
    port => INT32
    rack => NULLABLE_STRING
  cluster\_id => NULLABLE\_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] 
    error_code => INT16
    name => STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id \[replica\_nodes\] \[isr\_nodes\] \[offline\_replicas\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      replica_nodes => INT32
      isr_nodes => INT32
      offline_replicas => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |
| offline_replicas | The set of offline replicas of this partition. |

Metadata Response (Version: 7) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] 
  throttle\_time\_ms => INT32
  brokers => node_id host port rack 
    node_id => INT32
    host => STRING
    port => INT32
    rack => NULLABLE_STRING
  cluster\_id => NULLABLE\_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] 
    error_code => INT16
    name => STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id leader\_epoch \[replica\_nodes\] \[isr\_nodes\] \[offline_replicas\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      leader_epoch => INT32
      replica_nodes => INT32
      isr_nodes => INT32
      offline_replicas => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| leader_epoch | The leader epoch of this partition. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |
| offline_replicas | The set of offline replicas of this partition. |

Metadata Response (Version: 8) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] cluster\_authorized\_operations 
  throttle\_time\_ms => INT32
  brokers => node_id host port rack 
    node_id => INT32
    host => STRING
    port => INT32
    rack => NULLABLE_STRING
  cluster\_id => NULLABLE\_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] topic\_authorized\_operations 
    error_code => INT16
    name => STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id leader\_epoch \[replica\_nodes\] \[isr\_nodes\] \[offline_replicas\] 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      leader_epoch => INT32
      replica_nodes => INT32
      isr_nodes => INT32
      offline_replicas => INT32
    topic\_authorized\_operations => INT32
  cluster\_authorized\_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| leader_epoch | The leader epoch of this partition. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |
| offline_replicas | The set of offline replicas of this partition. |
| topic\_authorized\_operations | 32-bit bitfield to represent authorized operations for this topic. |
| cluster\_authorized\_operations | 32-bit bitfield to represent authorized operations for this cluster. |

Metadata Response (Version: 9) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] cluster\_authorized\_operations TAG_BUFFER 
  throttle\_time\_ms => INT32
  brokers => node\_id host port rack TAG\_BUFFER 
    node_id => INT32
    host => COMPACT_STRING
    port => INT32
    rack => COMPACT\_NULLABLE\_STRING
  cluster\_id => COMPACT\_NULLABLE_STRING
  controller_id => INT32
  topics => error\_code name is\_internal \[partitions\] topic\_authorized\_operations TAG_BUFFER 
    error_code => INT16
    name => COMPACT_STRING
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id leader\_epoch \[replica\_nodes\] \[isr\_nodes\] \[offline\_replicas\] TAG\_BUFFER 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      leader_epoch => INT32
      replica_nodes => INT32
      isr_nodes => INT32
      offline_replicas => INT32
    topic\_authorized\_operations => INT32
  cluster\_authorized\_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| \_tagged\_fields | The tagged fields |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| leader_epoch | The leader epoch of this partition. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |
| offline_replicas | The set of offline replicas of this partition. |
| \_tagged\_fields | The tagged fields |
| topic\_authorized\_operations | 32-bit bitfield to represent authorized operations for this topic. |
| \_tagged\_fields | The tagged fields |
| cluster\_authorized\_operations | 32-bit bitfield to represent authorized operations for this cluster. |
| \_tagged\_fields | The tagged fields |

Metadata Response (Version: 10) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] cluster\_authorized\_operations TAG_BUFFER 
  throttle\_time\_ms => INT32
  brokers => node\_id host port rack TAG\_BUFFER 
    node_id => INT32
    host => COMPACT_STRING
    port => INT32
    rack => COMPACT\_NULLABLE\_STRING
  cluster\_id => COMPACT\_NULLABLE_STRING
  controller_id => INT32
  topics => error\_code name topic\_id is\_internal \[partitions\] topic\_authorized\_operations TAG\_BUFFER 
    error_code => INT16
    name => COMPACT_STRING
    topic_id => UUID
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id leader\_epoch \[replica\_nodes\] \[isr\_nodes\] \[offline\_replicas\] TAG\_BUFFER 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      leader_epoch => INT32
      replica_nodes => INT32
      isr_nodes => INT32
      offline_replicas => INT32
    topic\_authorized\_operations => INT32
  cluster\_authorized\_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| \_tagged\_fields | The tagged fields |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| topic_id | The topic id. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| leader_epoch | The leader epoch of this partition. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |
| offline_replicas | The set of offline replicas of this partition. |
| \_tagged\_fields | The tagged fields |
| topic\_authorized\_operations | 32-bit bitfield to represent authorized operations for this topic. |
| \_tagged\_fields | The tagged fields |
| cluster\_authorized\_operations | 32-bit bitfield to represent authorized operations for this cluster. |
| \_tagged\_fields | The tagged fields |

Metadata Response (Version: 11) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  brokers => node\_id host port rack TAG\_BUFFER 
    node_id => INT32
    host => COMPACT_STRING
    port => INT32
    rack => COMPACT\_NULLABLE\_STRING
  cluster\_id => COMPACT\_NULLABLE_STRING
  controller_id => INT32
  topics => error\_code name topic\_id is\_internal \[partitions\] topic\_authorized\_operations TAG\_BUFFER 
    error_code => INT16
    name => COMPACT_STRING
    topic_id => UUID
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id leader\_epoch \[replica\_nodes\] \[isr\_nodes\] \[offline\_replicas\] TAG\_BUFFER 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      leader_epoch => INT32
      replica_nodes => INT32
      isr_nodes => INT32
      offline_replicas => INT32
    topic\_authorized\_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| \_tagged\_fields | The tagged fields |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| topic_id | The topic id. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| leader_epoch | The leader epoch of this partition. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |
| offline_replicas | The set of offline replicas of this partition. |
| \_tagged\_fields | The tagged fields |
| topic\_authorized\_operations | 32-bit bitfield to represent authorized operations for this topic. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

Metadata Response (Version: 12) => throttle\_time\_ms \[brokers\] cluster\_id controller\_id \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  brokers => node\_id host port rack TAG\_BUFFER 
    node_id => INT32
    host => COMPACT_STRING
    port => INT32
    rack => COMPACT\_NULLABLE\_STRING
  cluster\_id => COMPACT\_NULLABLE_STRING
  controller_id => INT32
  topics => error\_code name topic\_id is\_internal \[partitions\] topic\_authorized\_operations TAG\_BUFFER 
    error_code => INT16
    name => COMPACT\_NULLABLE\_STRING
    topic_id => UUID
    is_internal => BOOLEAN
    partitions => error\_code partition\_index leader\_id leader\_epoch \[replica\_nodes\] \[isr\_nodes\] \[offline\_replicas\] TAG\_BUFFER 
      error_code => INT16
      partition_index => INT32
      leader_id => INT32
      leader_epoch => INT32
      replica_nodes => INT32
      isr_nodes => INT32
      offline_replicas => INT32
    topic\_authorized\_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| brokers | Each broker in the response. |
| node_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| \_tagged\_fields | The tagged fields |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| topics | Each topic in the response. |
| error_code | The topic error, or 0 if there was no error. |
| name | The topic name. |
| topic_id | The topic id. |
| is_internal | True if the topic is internal. |
| partitions | Each partition in the topic. |
| error_code | The partition error, or 0 if there was no error. |
| partition_index | The partition index. |
| leader_id | The ID of the leader broker. |
| leader_epoch | The leader epoch of this partition. |
| replica_nodes | The set of all nodes that host this partition. |
| isr_nodes | The set of nodes that are in sync with the leader for this partition. |
| offline_replicas | The set of offline replicas of this partition. |
| \_tagged\_fields | The tagged fields |
| topic\_authorized\_operations | 32-bit bitfield to represent authorized operations for this topic. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### LeaderAndIsr API (Key: 4):

**Requests:**  

LeaderAndIsr Request (Version: 0) => controller\_id controller\_epoch \[ungrouped\_partition\_states\] \[live_leaders\] 
  controller_id => INT32
  controller_epoch => INT32
  ungrouped\_partition\_states => topic\_name partition\_index controller\_epoch leader leader\_epoch \[isr\] partition_epoch \[replicas\] 
    topic_name => STRING
    partition_index => INT32
    controller_epoch => INT32
    leader => INT32
    leader_epoch => INT32
    isr => INT32
    partition_epoch => INT32
    replicas => INT32
  live\_leaders => broker\_id host_name port 
    broker_id => INT32
    host_name => STRING
    port => INT32

| Field | Description |
| --- | --- |
| controller_id | The current controller ID. |
| controller_epoch | The current controller epoch. |
| ungrouped\_partition\_states | The state of each partition, in a v0 or v1 message. |
| topic_name | The topic name. This is only present in v0 or v1. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition. The epoch is a monotonically increasing value which is incremented after every partition change. (Since the LeaderAndIsr request is only used by the legacy controller, this corresponds to the zkVersion) |
| replicas | The replica IDs. |
| live_leaders | The current live leaders. |
| broker_id | The leader's broker ID. |
| host_name | The leader's hostname. |
| port | The leader's port. |

LeaderAndIsr Request (Version: 1) => controller\_id controller\_epoch \[ungrouped\_partition\_states\] \[live_leaders\] 
  controller_id => INT32
  controller_epoch => INT32
  ungrouped\_partition\_states => topic\_name partition\_index controller\_epoch leader leader\_epoch \[isr\] partition\_epoch \[replicas\] is\_new 
    topic_name => STRING
    partition_index => INT32
    controller_epoch => INT32
    leader => INT32
    leader_epoch => INT32
    isr => INT32
    partition_epoch => INT32
    replicas => INT32
    is_new => BOOLEAN
  live\_leaders => broker\_id host_name port 
    broker_id => INT32
    host_name => STRING
    port => INT32

| Field | Description |
| --- | --- |
| controller_id | The current controller ID. |
| controller_epoch | The current controller epoch. |
| ungrouped\_partition\_states | The state of each partition, in a v0 or v1 message. |
| topic_name | The topic name. This is only present in v0 or v1. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition. The epoch is a monotonically increasing value which is incremented after every partition change. (Since the LeaderAndIsr request is only used by the legacy controller, this corresponds to the zkVersion) |
| replicas | The replica IDs. |
| is_new | Whether the replica should have existed on the broker or not. |
| live_leaders | The current live leaders. |
| broker_id | The leader's broker ID. |
| host_name | The leader's hostname. |
| port | The leader's port. |

LeaderAndIsr Request (Version: 2) => controller\_id controller\_epoch broker\_epoch \[topic\_states\] \[live_leaders\] 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name \[partition_states\] 
    topic_name => STRING
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] partition\_epoch \[replicas\] is\_new 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      partition_epoch => INT32
      replicas => INT32
      is_new => BOOLEAN
  live\_leaders => broker\_id host_name port 
    broker_id => INT32
    host_name => STRING
    port => INT32

| Field | Description |
| --- | --- |
| controller_id | The current controller ID. |
| controller_epoch | The current controller epoch. |
| broker_epoch | The current broker epoch. |
| topic_states | Each topic. |
| topic_name | The topic name. |
| partition_states | The state of each partition |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition. The epoch is a monotonically increasing value which is incremented after every partition change. (Since the LeaderAndIsr request is only used by the legacy controller, this corresponds to the zkVersion) |
| replicas | The replica IDs. |
| is_new | Whether the replica should have existed on the broker or not. |
| live_leaders | The current live leaders. |
| broker_id | The leader's broker ID. |
| host_name | The leader's hostname. |
| port | The leader's port. |

LeaderAndIsr Request (Version: 3) => controller\_id controller\_epoch broker\_epoch \[topic\_states\] \[live_leaders\] 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name \[partition_states\] 
    topic_name => STRING
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] partition\_epoch \[replicas\] \[adding\_replicas\] \[removing\_replicas\] is\_new 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      partition_epoch => INT32
      replicas => INT32
      adding_replicas => INT32
      removing_replicas => INT32
      is_new => BOOLEAN
  live\_leaders => broker\_id host_name port 
    broker_id => INT32
    host_name => STRING
    port => INT32

| Field | Description |
| --- | --- |
| controller_id | The current controller ID. |
| controller_epoch | The current controller epoch. |
| broker_epoch | The current broker epoch. |
| topic_states | Each topic. |
| topic_name | The topic name. |
| partition_states | The state of each partition |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition. The epoch is a monotonically increasing value which is incremented after every partition change. (Since the LeaderAndIsr request is only used by the legacy controller, this corresponds to the zkVersion) |
| replicas | The replica IDs. |
| adding_replicas | The replica IDs that we are adding this partition to, or null if no replicas are being added. |
| removing_replicas | The replica IDs that we are removing this partition from, or null if no replicas are being removed. |
| is_new | Whether the replica should have existed on the broker or not. |
| live_leaders | The current live leaders. |
| broker_id | The leader's broker ID. |
| host_name | The leader's hostname. |
| port | The leader's port. |

LeaderAndIsr Request (Version: 4) => controller\_id controller\_epoch broker\_epoch \[topic\_states\] \[live\_leaders\] TAG\_BUFFER 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name \[partition\_states\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] partition\_epoch \[replicas\] \[adding\_replicas\] \[removing\_replicas\] is\_new TAG_BUFFER 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      partition_epoch => INT32
      replicas => INT32
      adding_replicas => INT32
      removing_replicas => INT32
      is_new => BOOLEAN
  live\_leaders => broker\_id host\_name port TAG\_BUFFER 
    broker_id => INT32
    host\_name => COMPACT\_STRING
    port => INT32

| Field | Description |
| --- | --- |
| controller_id | The current controller ID. |
| controller_epoch | The current controller epoch. |
| broker_epoch | The current broker epoch. |
| topic_states | Each topic. |
| topic_name | The topic name. |
| partition_states | The state of each partition |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition. The epoch is a monotonically increasing value which is incremented after every partition change. (Since the LeaderAndIsr request is only used by the legacy controller, this corresponds to the zkVersion) |
| replicas | The replica IDs. |
| adding_replicas | The replica IDs that we are adding this partition to, or null if no replicas are being added. |
| removing_replicas | The replica IDs that we are removing this partition from, or null if no replicas are being removed. |
| is_new | Whether the replica should have existed on the broker or not. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| live_leaders | The current live leaders. |
| broker_id | The leader's broker ID. |
| host_name | The leader's hostname. |
| port | The leader's port. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

LeaderAndIsr Request (Version: 5) => controller\_id controller\_epoch broker\_epoch type \[topic\_states\] \[live\_leaders\] TAG\_BUFFER 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  type => INT8
  topic\_states => topic\_name topic\_id \[partition\_states\] TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    topic_id => UUID
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] partition\_epoch \[replicas\] \[adding\_replicas\] \[removing\_replicas\] is\_new TAG_BUFFER 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      partition_epoch => INT32
      replicas => INT32
      adding_replicas => INT32
      removing_replicas => INT32
      is_new => BOOLEAN
  live\_leaders => broker\_id host\_name port TAG\_BUFFER 
    broker_id => INT32
    host\_name => COMPACT\_STRING
    port => INT32

| Field | Description |
| --- | --- |
| controller_id | The current controller ID. |
| controller_epoch | The current controller epoch. |
| broker_epoch | The current broker epoch. |
| type | The type that indicates whether all topics are included in the request |
| topic_states | Each topic. |
| topic_name | The topic name. |
| topic_id | The unique topic ID. |
| partition_states | The state of each partition |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition. The epoch is a monotonically increasing value which is incremented after every partition change. (Since the LeaderAndIsr request is only used by the legacy controller, this corresponds to the zkVersion) |
| replicas | The replica IDs. |
| adding_replicas | The replica IDs that we are adding this partition to, or null if no replicas are being added. |
| removing_replicas | The replica IDs that we are removing this partition from, or null if no replicas are being removed. |
| is_new | Whether the replica should have existed on the broker or not. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| live_leaders | The current live leaders. |
| broker_id | The leader's broker ID. |
| host_name | The leader's hostname. |
| port | The leader's port. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

LeaderAndIsr Request (Version: 6) => controller\_id controller\_epoch broker\_epoch type \[topic\_states\] \[live\_leaders\] TAG\_BUFFER 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  type => INT8
  topic\_states => topic\_name topic\_id \[partition\_states\] TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    topic_id => UUID
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] partition\_epoch \[replicas\] \[adding\_replicas\] \[removing\_replicas\] is\_new leader\_recovery\_state TAG_BUFFER 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      partition_epoch => INT32
      replicas => INT32
      adding_replicas => INT32
      removing_replicas => INT32
      is_new => BOOLEAN
      leader\_recovery\_state => INT8
  live\_leaders => broker\_id host\_name port TAG\_BUFFER 
    broker_id => INT32
    host\_name => COMPACT\_STRING
    port => INT32

| Field | Description |
| --- | --- |
| controller_id | The current controller ID. |
| controller_epoch | The current controller epoch. |
| broker_epoch | The current broker epoch. |
| type | The type that indicates whether all topics are included in the request |
| topic_states | Each topic. |
| topic_name | The topic name. |
| topic_id | The unique topic ID. |
| partition_states | The state of each partition |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition. The epoch is a monotonically increasing value which is incremented after every partition change. (Since the LeaderAndIsr request is only used by the legacy controller, this corresponds to the zkVersion) |
| replicas | The replica IDs. |
| adding_replicas | The replica IDs that we are adding this partition to, or null if no replicas are being added. |
| removing_replicas | The replica IDs that we are removing this partition from, or null if no replicas are being removed. |
| is_new | Whether the replica should have existed on the broker or not. |
| leader\_recovery\_state | 1 if the partition is recovering from an unclean leader election; 0 otherwise. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| live_leaders | The current live leaders. |
| broker_id | The leader's broker ID. |
| host_name | The leader's hostname. |
| port | The leader's port. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

LeaderAndIsr Request (Version: 7) => controller\_id is\_kraft\_controller controller\_epoch broker\_epoch type \[topic\_states\] \[live\_leaders\] TAG\_BUFFER 
  controller_id => INT32
  is\_kraft\_controller => BOOLEAN
  controller_epoch => INT32
  broker_epoch => INT64
  type => INT8
  topic\_states => topic\_name topic\_id \[partition\_states\] TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    topic_id => UUID
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] partition\_epoch \[replicas\] \[adding\_replicas\] \[removing\_replicas\] is\_new leader\_recovery\_state TAG_BUFFER 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      partition_epoch => INT32
      replicas => INT32
      adding_replicas => INT32
      removing_replicas => INT32
      is_new => BOOLEAN
      leader\_recovery\_state => INT8
  live\_leaders => broker\_id host\_name port TAG\_BUFFER 
    broker_id => INT32
    host\_name => COMPACT\_STRING
    port => INT32

| Field | Description |
| --- | --- |
| controller_id | The current controller ID. |
| is\_kraft\_controller | If KRaft controller id is used during migration. See KIP-866 |
| controller_epoch | The current controller epoch. |
| broker_epoch | The current broker epoch. |
| type | The type that indicates whether all topics are included in the request |
| topic_states | Each topic. |
| topic_name | The topic name. |
| topic_id | The unique topic ID. |
| partition_states | The state of each partition |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition. The epoch is a monotonically increasing value which is incremented after every partition change. (Since the LeaderAndIsr request is only used by the legacy controller, this corresponds to the zkVersion) |
| replicas | The replica IDs. |
| adding_replicas | The replica IDs that we are adding this partition to, or null if no replicas are being added. |
| removing_replicas | The replica IDs that we are removing this partition from, or null if no replicas are being removed. |
| is_new | Whether the replica should have existed on the broker or not. |
| leader\_recovery\_state | 1 if the partition is recovering from an unclean leader election; 0 otherwise. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| live_leaders | The current live leaders. |
| broker_id | The leader's broker ID. |
| host_name | The leader's hostname. |
| port | The leader's port. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

LeaderAndIsr Response (Version: 0) => error\_code \[partition\_errors\] 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code 
    topic_name => STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| partition_errors | Each partition in v0 to v4 message. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |

LeaderAndIsr Response (Version: 1) => error\_code \[partition\_errors\] 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code 
    topic_name => STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| partition_errors | Each partition in v0 to v4 message. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |

LeaderAndIsr Response (Version: 2) => error\_code \[partition\_errors\] 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code 
    topic_name => STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| partition_errors | Each partition in v0 to v4 message. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |

LeaderAndIsr Response (Version: 3) => error\_code \[partition\_errors\] 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code 
    topic_name => STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| partition_errors | Each partition in v0 to v4 message. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |

LeaderAndIsr Response (Version: 4) => error\_code \[partition\_errors\] TAG_BUFFER 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| partition_errors | Each partition in v0 to v4 message. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

LeaderAndIsr Response (Version: 5) => error\_code \[topics\] TAG\_BUFFER 
  error_code => INT16
  topics => topic\_id \[partition\_errors\] TAG_BUFFER 
    topic_id => UUID
    partition\_errors => partition\_index error\_code TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| topics | Each topic |
| topic_id | The unique topic ID |
| partition_errors | Each partition. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

LeaderAndIsr Response (Version: 6) => error\_code \[topics\] TAG\_BUFFER 
  error_code => INT16
  topics => topic\_id \[partition\_errors\] TAG_BUFFER 
    topic_id => UUID
    partition\_errors => partition\_index error\_code TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| topics | Each topic |
| topic_id | The unique topic ID |
| partition_errors | Each partition. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

LeaderAndIsr Response (Version: 7) => error\_code \[topics\] TAG\_BUFFER 
  error_code => INT16
  topics => topic\_id \[partition\_errors\] TAG_BUFFER 
    topic_id => UUID
    partition\_errors => partition\_index error\_code TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| topics | Each topic |
| topic_id | The unique topic ID |
| partition_errors | Each partition. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### StopReplica API (Key: 5):

**Requests:**  

StopReplica Request (Version: 0) => controller\_id controller\_epoch delete\_partitions \[ungrouped\_partitions\] 
  controller_id => INT32
  controller_epoch => INT32
  delete_partitions => BOOLEAN
  ungrouped\_partitions => topic\_name partition_index 
    topic_name => STRING
    partition_index => INT32

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| delete_partitions | Whether these partitions should be deleted. |
| ungrouped_partitions | The partitions to stop. |
| topic_name | The topic name. |
| partition_index | The partition index. |

StopReplica Request (Version: 1) => controller\_id controller\_epoch broker\_epoch delete\_partitions \[topics\] 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  delete_partitions => BOOLEAN
  topics => name \[partition_indexes\] 
    name => STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| broker_epoch | The broker epoch. |
| delete_partitions | Whether these partitions should be deleted. |
| topics | The topics to stop. |
| name | The topic name. |
| partition_indexes | The partition indexes. |

StopReplica Request (Version: 2) => controller\_id controller\_epoch broker\_epoch delete\_partitions \[topics\] TAG_BUFFER 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  delete_partitions => BOOLEAN
  topics => name \[partition\_indexes\] TAG\_BUFFER 
    name => COMPACT_STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| broker_epoch | The broker epoch. |
| delete_partitions | Whether these partitions should be deleted. |
| topics | The topics to stop. |
| name | The topic name. |
| partition_indexes | The partition indexes. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

StopReplica Request (Version: 3) => controller\_id controller\_epoch broker\_epoch \[topic\_states\] TAG_BUFFER 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name \[partition\_states\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partition\_states => partition\_index leader\_epoch delete\_partition TAG_BUFFER 
      partition_index => INT32
      leader_epoch => INT32
      delete_partition => BOOLEAN

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| broker_epoch | The broker epoch. |
| topic_states | Each topic. |
| topic_name | The topic name. |
| partition_states | The state of each partition |
| partition_index | The partition index. |
| leader_epoch | The leader epoch. |
| delete_partition | Whether this partition should be deleted. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

StopReplica Request (Version: 4) => controller\_id is\_kraft\_controller controller\_epoch broker\_epoch \[topic\_states\] TAG_BUFFER 
  controller_id => INT32
  is\_kraft\_controller => BOOLEAN
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name \[partition\_states\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partition\_states => partition\_index leader\_epoch delete\_partition TAG_BUFFER 
      partition_index => INT32
      leader_epoch => INT32
      delete_partition => BOOLEAN

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| is\_kraft\_controller | If KRaft controller id is used during migration. See KIP-866 |
| controller_epoch | The controller epoch. |
| broker_epoch | The broker epoch. |
| topic_states | Each topic. |
| topic_name | The topic name. |
| partition_states | The state of each partition |
| partition_index | The partition index. |
| leader_epoch | The leader epoch. |
| delete_partition | Whether this partition should be deleted. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

StopReplica Response (Version: 0) => error\_code \[partition\_errors\] 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code 
    topic_name => STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The top-level error code, or 0 if there was no top-level error. |
| partition_errors | The responses for each partition. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no partition error. |

StopReplica Response (Version: 1) => error\_code \[partition\_errors\] 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code 
    topic_name => STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The top-level error code, or 0 if there was no top-level error. |
| partition_errors | The responses for each partition. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no partition error. |

StopReplica Response (Version: 2) => error\_code \[partition\_errors\] TAG_BUFFER 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The top-level error code, or 0 if there was no top-level error. |
| partition_errors | The responses for each partition. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no partition error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

StopReplica Response (Version: 3) => error\_code \[partition\_errors\] TAG_BUFFER 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The top-level error code, or 0 if there was no top-level error. |
| partition_errors | The responses for each partition. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no partition error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

StopReplica Response (Version: 4) => error\_code \[partition\_errors\] TAG_BUFFER 
  error_code => INT16
  partition\_errors => topic\_name partition\_index error\_code TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    partition_index => INT32
    error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The top-level error code, or 0 if there was no top-level error. |
| partition_errors | The responses for each partition. |
| topic_name | The topic name. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no partition error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### UpdateMetadata API (Key: 6):

**Requests:**  

UpdateMetadata Request (Version: 0) => controller\_id controller\_epoch \[ungrouped\_partition\_states\] \[live_brokers\] 
  controller_id => INT32
  controller_epoch => INT32
  ungrouped\_partition\_states => topic\_name partition\_index controller\_epoch leader leader\_epoch \[isr\] zk_version \[replicas\] 
    topic_name => STRING
    partition_index => INT32
    controller_epoch => INT32
    leader => INT32
    leader_epoch => INT32
    isr => INT32
    zk_version => INT32
    replicas => INT32
  live\_brokers => id v0\_host v0_port 
    id => INT32
    v0_host => STRING
    v0_port => INT32

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| ungrouped\_partition\_states | In older versions of this RPC, each partition that we would like to update. |
| topic_name | In older versions of this RPC, the topic name. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| live_brokers |     |
| id  | The broker id. |
| v0_host | The broker hostname. |
| v0_port | The broker port. |

UpdateMetadata Request (Version: 1) => controller\_id controller\_epoch \[ungrouped\_partition\_states\] \[live_brokers\] 
  controller_id => INT32
  controller_epoch => INT32
  ungrouped\_partition\_states => topic\_name partition\_index controller\_epoch leader leader\_epoch \[isr\] zk_version \[replicas\] 
    topic_name => STRING
    partition_index => INT32
    controller_epoch => INT32
    leader => INT32
    leader_epoch => INT32
    isr => INT32
    zk_version => INT32
    replicas => INT32
  live_brokers => id \[endpoints\] 
    id => INT32
    endpoints => port host security_protocol 
      port => INT32
      host => STRING
      security_protocol => INT16

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| ungrouped\_partition\_states | In older versions of this RPC, each partition that we would like to update. |
| topic_name | In older versions of this RPC, the topic name. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| live_brokers |     |
| id  | The broker id. |
| endpoints | The broker endpoints. |
| port | The port of this endpoint |
| host | The hostname of this endpoint |
| security_protocol | The security protocol type. |

UpdateMetadata Request (Version: 2) => controller\_id controller\_epoch \[ungrouped\_partition\_states\] \[live_brokers\] 
  controller_id => INT32
  controller_epoch => INT32
  ungrouped\_partition\_states => topic\_name partition\_index controller\_epoch leader leader\_epoch \[isr\] zk_version \[replicas\] 
    topic_name => STRING
    partition_index => INT32
    controller_epoch => INT32
    leader => INT32
    leader_epoch => INT32
    isr => INT32
    zk_version => INT32
    replicas => INT32
  live_brokers => id \[endpoints\] rack 
    id => INT32
    endpoints => port host security_protocol 
      port => INT32
      host => STRING
      security_protocol => INT16
    rack => NULLABLE_STRING

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| ungrouped\_partition\_states | In older versions of this RPC, each partition that we would like to update. |
| topic_name | In older versions of this RPC, the topic name. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| live_brokers |     |
| id  | The broker id. |
| endpoints | The broker endpoints. |
| port | The port of this endpoint |
| host | The hostname of this endpoint |
| security_protocol | The security protocol type. |
| rack | The rack which this broker belongs to. |

UpdateMetadata Request (Version: 3) => controller\_id controller\_epoch \[ungrouped\_partition\_states\] \[live_brokers\] 
  controller_id => INT32
  controller_epoch => INT32
  ungrouped\_partition\_states => topic\_name partition\_index controller\_epoch leader leader\_epoch \[isr\] zk_version \[replicas\] 
    topic_name => STRING
    partition_index => INT32
    controller_epoch => INT32
    leader => INT32
    leader_epoch => INT32
    isr => INT32
    zk_version => INT32
    replicas => INT32
  live_brokers => id \[endpoints\] rack 
    id => INT32
    endpoints => port host listener security_protocol 
      port => INT32
      host => STRING
      listener => STRING
      security_protocol => INT16
    rack => NULLABLE_STRING

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| ungrouped\_partition\_states | In older versions of this RPC, each partition that we would like to update. |
| topic_name | In older versions of this RPC, the topic name. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| live_brokers |     |
| id  | The broker id. |
| endpoints | The broker endpoints. |
| port | The port of this endpoint |
| host | The hostname of this endpoint |
| listener | The listener name. |
| security_protocol | The security protocol type. |
| rack | The rack which this broker belongs to. |

UpdateMetadata Request (Version: 4) => controller\_id controller\_epoch \[ungrouped\_partition\_states\] \[live_brokers\] 
  controller_id => INT32
  controller_epoch => INT32
  ungrouped\_partition\_states => topic\_name partition\_index controller\_epoch leader leader\_epoch \[isr\] zk\_version \[replicas\] \[offline\_replicas\] 
    topic_name => STRING
    partition_index => INT32
    controller_epoch => INT32
    leader => INT32
    leader_epoch => INT32
    isr => INT32
    zk_version => INT32
    replicas => INT32
    offline_replicas => INT32
  live_brokers => id \[endpoints\] rack 
    id => INT32
    endpoints => port host listener security_protocol 
      port => INT32
      host => STRING
      listener => STRING
      security_protocol => INT16
    rack => NULLABLE_STRING

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| ungrouped\_partition\_states | In older versions of this RPC, each partition that we would like to update. |
| topic_name | In older versions of this RPC, the topic name. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| offline_replicas | The replicas of this partition which are offline. |
| live_brokers |     |
| id  | The broker id. |
| endpoints | The broker endpoints. |
| port | The port of this endpoint |
| host | The hostname of this endpoint |
| listener | The listener name. |
| security_protocol | The security protocol type. |
| rack | The rack which this broker belongs to. |

UpdateMetadata Request (Version: 5) => controller\_id controller\_epoch broker\_epoch \[topic\_states\] \[live_brokers\] 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name \[partition_states\] 
    topic_name => STRING
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] zk\_version \[replicas\] \[offline\_replicas\] 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      zk_version => INT32
      replicas => INT32
      offline_replicas => INT32
  live_brokers => id \[endpoints\] rack 
    id => INT32
    endpoints => port host listener security_protocol 
      port => INT32
      host => STRING
      listener => STRING
      security_protocol => INT16
    rack => NULLABLE_STRING

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| broker_epoch | The broker epoch. |
| topic_states | In newer versions of this RPC, each topic that we would like to update. |
| topic_name | The topic name. |
| partition_states | The partition that we would like to update. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| offline_replicas | The replicas of this partition which are offline. |
| live_brokers |     |
| id  | The broker id. |
| endpoints | The broker endpoints. |
| port | The port of this endpoint |
| host | The hostname of this endpoint |
| listener | The listener name. |
| security_protocol | The security protocol type. |
| rack | The rack which this broker belongs to. |

UpdateMetadata Request (Version: 6) => controller\_id controller\_epoch broker\_epoch \[topic\_states\] \[live\_brokers\] TAG\_BUFFER 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name \[partition\_states\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] zk\_version \[replicas\] \[offline\_replicas\] TAG_BUFFER 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      zk_version => INT32
      replicas => INT32
      offline_replicas => INT32
  live\_brokers => id \[endpoints\] rack TAG\_BUFFER 
    id => INT32
    endpoints => port host listener security\_protocol TAG\_BUFFER 
      port => INT32
      host => COMPACT_STRING
      listener => COMPACT_STRING
      security_protocol => INT16
    rack => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| broker_epoch | The broker epoch. |
| topic_states | In newer versions of this RPC, each topic that we would like to update. |
| topic_name | The topic name. |
| partition_states | The partition that we would like to update. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| offline_replicas | The replicas of this partition which are offline. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| live_brokers |     |
| id  | The broker id. |
| endpoints | The broker endpoints. |
| port | The port of this endpoint |
| host | The hostname of this endpoint |
| listener | The listener name. |
| security_protocol | The security protocol type. |
| \_tagged\_fields | The tagged fields |
| rack | The rack which this broker belongs to. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

UpdateMetadata Request (Version: 7) => controller\_id controller\_epoch broker\_epoch \[topic\_states\] \[live\_brokers\] TAG\_BUFFER 
  controller_id => INT32
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name topic\_id \[partition\_states\] TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    topic_id => UUID
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] zk\_version \[replicas\] \[offline\_replicas\] TAG_BUFFER 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      zk_version => INT32
      replicas => INT32
      offline_replicas => INT32
  live\_brokers => id \[endpoints\] rack TAG\_BUFFER 
    id => INT32
    endpoints => port host listener security\_protocol TAG\_BUFFER 
      port => INT32
      host => COMPACT_STRING
      listener => COMPACT_STRING
      security_protocol => INT16
    rack => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| controller_epoch | The controller epoch. |
| broker_epoch | The broker epoch. |
| topic_states | In newer versions of this RPC, each topic that we would like to update. |
| topic_name | The topic name. |
| topic_id | The topic id. |
| partition_states | The partition that we would like to update. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| offline_replicas | The replicas of this partition which are offline. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| live_brokers |     |
| id  | The broker id. |
| endpoints | The broker endpoints. |
| port | The port of this endpoint |
| host | The hostname of this endpoint |
| listener | The listener name. |
| security_protocol | The security protocol type. |
| \_tagged\_fields | The tagged fields |
| rack | The rack which this broker belongs to. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

UpdateMetadata Request (Version: 8) => controller\_id is\_kraft\_controller controller\_epoch broker\_epoch \[topic\_states\] \[live\_brokers\] TAG\_BUFFER 
  controller_id => INT32
  is\_kraft\_controller => BOOLEAN
  controller_epoch => INT32
  broker_epoch => INT64
  topic\_states => topic\_name topic\_id \[partition\_states\] TAG_BUFFER 
    topic\_name => COMPACT\_STRING
    topic_id => UUID
    partition\_states => partition\_index controller\_epoch leader leader\_epoch \[isr\] zk\_version \[replicas\] \[offline\_replicas\] TAG_BUFFER 
      partition_index => INT32
      controller_epoch => INT32
      leader => INT32
      leader_epoch => INT32
      isr => INT32
      zk_version => INT32
      replicas => INT32
      offline_replicas => INT32
  live\_brokers => id \[endpoints\] rack TAG\_BUFFER 
    id => INT32
    endpoints => port host listener security\_protocol TAG\_BUFFER 
      port => INT32
      host => COMPACT_STRING
      listener => COMPACT_STRING
      security_protocol => INT16
    rack => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| controller_id | The controller id. |
| is\_kraft\_controller | If KRaft controller id is used during migration. See KIP-866 |
| controller_epoch | The controller epoch. |
| broker_epoch | The broker epoch. |
| topic_states | In newer versions of this RPC, each topic that we would like to update. |
| topic_name | The topic name. |
| topic_id | The topic id. |
| partition_states | The partition that we would like to update. |
| partition_index | The partition index. |
| controller_epoch | The controller epoch. |
| leader | The ID of the broker which is the current partition leader. |
| leader_epoch | The leader epoch of this partition. |
| isr | The brokers which are in the ISR for this partition. |
| zk_version | The Zookeeper version. |
| replicas | All the replicas of this partition. |
| offline_replicas | The replicas of this partition which are offline. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| live_brokers |     |
| id  | The broker id. |
| endpoints | The broker endpoints. |
| port | The port of this endpoint |
| host | The hostname of this endpoint |
| listener | The listener name. |
| security_protocol | The security protocol type. |
| \_tagged\_fields | The tagged fields |
| rack | The rack which this broker belongs to. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

UpdateMetadata Response (Version: 0) => error_code 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |

UpdateMetadata Response (Version: 1) => error_code 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |

UpdateMetadata Response (Version: 2) => error_code 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |

UpdateMetadata Response (Version: 3) => error_code 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |

UpdateMetadata Response (Version: 4) => error_code 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |

UpdateMetadata Response (Version: 5) => error_code 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |

UpdateMetadata Response (Version: 6) => error\_code TAG\_BUFFER 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

UpdateMetadata Response (Version: 7) => error\_code TAG\_BUFFER 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

UpdateMetadata Response (Version: 8) => error\_code TAG\_BUFFER 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

##### ControlledShutdown API (Key: 7):

**Requests:**  

ControlledShutdown Request (Version: 0) => broker_id 
  broker_id => INT32

| Field | Description |
| --- | --- |
| broker_id | The id of the broker for which controlled shutdown has been requested. |

ControlledShutdown Request (Version: 1) => broker_id 
  broker_id => INT32

| Field | Description |
| --- | --- |
| broker_id | The id of the broker for which controlled shutdown has been requested. |

ControlledShutdown Request (Version: 2) => broker\_id broker\_epoch 
  broker_id => INT32
  broker_epoch => INT64

| Field | Description |
| --- | --- |
| broker_id | The id of the broker for which controlled shutdown has been requested. |
| broker_epoch | The broker epoch. |

ControlledShutdown Request (Version: 3) => broker\_id broker\_epoch TAG_BUFFER 
  broker_id => INT32
  broker_epoch => INT64

| Field | Description |
| --- | --- |
| broker_id | The id of the broker for which controlled shutdown has been requested. |
| broker_epoch | The broker epoch. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ControlledShutdown Response (Version: 0) => error\_code \[remaining\_partitions\] 
  error_code => INT16
  remaining\_partitions => topic\_name partition_index 
    topic_name => STRING
    partition_index => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error code. |
| remaining_partitions | The partitions that the broker still leads. |
| topic_name | The name of the topic. |
| partition_index | The index of the partition. |

ControlledShutdown Response (Version: 1) => error\_code \[remaining\_partitions\] 
  error_code => INT16
  remaining\_partitions => topic\_name partition_index 
    topic_name => STRING
    partition_index => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error code. |
| remaining_partitions | The partitions that the broker still leads. |
| topic_name | The name of the topic. |
| partition_index | The index of the partition. |

ControlledShutdown Response (Version: 2) => error\_code \[remaining\_partitions\] 
  error_code => INT16
  remaining\_partitions => topic\_name partition_index 
    topic_name => STRING
    partition_index => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error code. |
| remaining_partitions | The partitions that the broker still leads. |
| topic_name | The name of the topic. |
| partition_index | The index of the partition. |

ControlledShutdown Response (Version: 3) => error\_code \[remaining\_partitions\] TAG_BUFFER 
  error_code => INT16
  remaining\_partitions => topic\_name partition\_index TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partition_index => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error code. |
| remaining_partitions | The partitions that the broker still leads. |
| topic_name | The name of the topic. |
| partition_index | The index of the partition. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### OffsetCommit API (Key: 8):

**Requests:**  

OffsetCommit Request (Version: 0) => group_id \[topics\] 
  group_id => STRING
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed_metadata | Any associated metadata the client wants to keep. |

OffsetCommit Request (Version: 1) => group\_id generation\_id\_or\_member\_epoch member\_id \[topics\] 
  group_id => STRING
  generation\_id\_or\_member\_epoch => INT32
  member_id => STRING
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset commit\_timestamp committed\_metadata 
      partition_index => INT32
      committed_offset => INT64
      commit_timestamp => INT64
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| commit_timestamp | The timestamp of the commit. |
| committed_metadata | Any associated metadata the client wants to keep. |

OffsetCommit Request (Version: 2) => group\_id generation\_id\_or\_member\_epoch member\_id retention\_time\_ms \[topics\] 
  group_id => STRING
  generation\_id\_or\_member\_epoch => INT32
  member_id => STRING
  retention\_time\_ms => INT64
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| retention\_time\_ms | The time period in ms to retain the offset. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed_metadata | Any associated metadata the client wants to keep. |

OffsetCommit Request (Version: 3) => group\_id generation\_id\_or\_member\_epoch member\_id retention\_time\_ms \[topics\] 
  group_id => STRING
  generation\_id\_or\_member\_epoch => INT32
  member_id => STRING
  retention\_time\_ms => INT64
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| retention\_time\_ms | The time period in ms to retain the offset. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed_metadata | Any associated metadata the client wants to keep. |

OffsetCommit Request (Version: 4) => group\_id generation\_id\_or\_member\_epoch member\_id retention\_time\_ms \[topics\] 
  group_id => STRING
  generation\_id\_or\_member\_epoch => INT32
  member_id => STRING
  retention\_time\_ms => INT64
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| retention\_time\_ms | The time period in ms to retain the offset. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed_metadata | Any associated metadata the client wants to keep. |

OffsetCommit Request (Version: 5) => group\_id generation\_id\_or\_member\_epoch member\_id \[topics\] 
  group_id => STRING
  generation\_id\_or\_member\_epoch => INT32
  member_id => STRING
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed_metadata | Any associated metadata the client wants to keep. |

OffsetCommit Request (Version: 6) => group\_id generation\_id\_or\_member\_epoch member\_id \[topics\] 
  group_id => STRING
  generation\_id\_or\_member\_epoch => INT32
  member_id => STRING
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed\_leader\_epoch | The leader epoch of this partition. |
| committed_metadata | Any associated metadata the client wants to keep. |

OffsetCommit Request (Version: 7) => group\_id generation\_id\_or\_member\_epoch member\_id group\_instance\_id \[topics\] 
  group_id => STRING
  generation\_id\_or\_member\_epoch => INT32
  member_id => STRING
  group\_instance\_id => NULLABLE_STRING
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed\_leader\_epoch | The leader epoch of this partition. |
| committed_metadata | Any associated metadata the client wants to keep. |

OffsetCommit Request (Version: 8) => group\_id generation\_id\_or\_member\_epoch member\_id group\_instance\_id \[topics\] TAG_BUFFER 
  group\_id => COMPACT\_STRING
  generation\_id\_or\_member\_epoch => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch committed\_metadata TAG\_BUFFER 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      committed\_metadata => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed\_leader\_epoch | The leader epoch of this partition. |
| committed_metadata | Any associated metadata the client wants to keep. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

OffsetCommit Request (Version: 9) => group\_id generation\_id\_or\_member\_epoch member\_id group\_instance\_id \[topics\] TAG_BUFFER 
  group\_id => COMPACT\_STRING
  generation\_id\_or\_member\_epoch => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch committed\_metadata TAG\_BUFFER 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      committed\_metadata => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation\_id\_or\_member\_epoch | The generation of the group if using the generic group protocol or the member epoch if using the consumer protocol. |
| member_id | The member ID assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| topics | The topics to commit offsets for. |
| name | The topic name. |
| partitions | Each partition to commit offsets for. |
| partition_index | The partition index. |
| committed_offset | The message offset to be committed. |
| committed\_leader\_epoch | The leader epoch of this partition. |
| committed_metadata | Any associated metadata the client wants to keep. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

OffsetCommit Response (Version: 0) => \[topics\] 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

OffsetCommit Response (Version: 1) => \[topics\] 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

OffsetCommit Response (Version: 2) => \[topics\] 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

OffsetCommit Response (Version: 3) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

OffsetCommit Response (Version: 4) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

OffsetCommit Response (Version: 5) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

OffsetCommit Response (Version: 6) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

OffsetCommit Response (Version: 7) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

OffsetCommit Response (Version: 8) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index error\_code TAG_BUFFER 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

OffsetCommit Response (Version: 9) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index error\_code TAG_BUFFER 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### OffsetFetch API (Key: 9):

**Requests:**  

OffsetFetch Request (Version: 0) => group_id \[topics\] 
  group_id => STRING
  topics => name \[partition_indexes\] 
    name => STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| group_id | The group to fetch offsets for. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |

OffsetFetch Request (Version: 1) => group_id \[topics\] 
  group_id => STRING
  topics => name \[partition_indexes\] 
    name => STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| group_id | The group to fetch offsets for. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |

OffsetFetch Request (Version: 2) => group_id \[topics\] 
  group_id => STRING
  topics => name \[partition_indexes\] 
    name => STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| group_id | The group to fetch offsets for. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |

OffsetFetch Request (Version: 3) => group_id \[topics\] 
  group_id => STRING
  topics => name \[partition_indexes\] 
    name => STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| group_id | The group to fetch offsets for. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |

OffsetFetch Request (Version: 4) => group_id \[topics\] 
  group_id => STRING
  topics => name \[partition_indexes\] 
    name => STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| group_id | The group to fetch offsets for. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |

OffsetFetch Request (Version: 5) => group_id \[topics\] 
  group_id => STRING
  topics => name \[partition_indexes\] 
    name => STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| group_id | The group to fetch offsets for. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |

OffsetFetch Request (Version: 6) => group\_id \[topics\] TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  topics => name \[partition\_indexes\] TAG\_BUFFER 
    name => COMPACT_STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| group_id | The group to fetch offsets for. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

OffsetFetch Request (Version: 7) => group\_id \[topics\] require\_stable TAG_BUFFER 
  group\_id => COMPACT\_STRING
  topics => name \[partition\_indexes\] TAG\_BUFFER 
    name => COMPACT_STRING
    partition_indexes => INT32
  require_stable => BOOLEAN

| Field | Description |
| --- | --- |
| group_id | The group to fetch offsets for. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |
| \_tagged\_fields | The tagged fields |
| require_stable | Whether broker should hold on returning unstable offsets but set a retriable error code for the partitions. |
| \_tagged\_fields | The tagged fields |

OffsetFetch Request (Version: 8) => \[groups\] require\_stable TAG\_BUFFER 
  groups => group\_id \[topics\] TAG\_BUFFER 
    group\_id => COMPACT\_STRING
    topics => name \[partition\_indexes\] TAG\_BUFFER 
      name => COMPACT_STRING
      partition_indexes => INT32
  require_stable => BOOLEAN

| Field | Description |
| --- | --- |
| groups | Each group we would like to fetch offsets for |
| group_id | The group ID. |
| topics | Each topic we would like to fetch offsets for, or null to fetch offsets for all topics. |
| name | The topic name. |
| partition_indexes | The partition indexes we would like to fetch offsets for. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| require_stable | Whether broker should hold on returning unstable offsets but set a retriable error code for the partitions. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

OffsetFetch Response (Version: 0) => \[topics\] 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset metadata error_code 
      partition_index => INT32
      committed_offset => INT64
      metadata => NULLABLE_STRING
      error_code => INT16

| Field | Description |
| --- | --- |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| metadata | The partition metadata. |
| error_code | The error code, or 0 if there was no error. |

OffsetFetch Response (Version: 1) => \[topics\] 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset metadata error_code 
      partition_index => INT32
      committed_offset => INT64
      metadata => NULLABLE_STRING
      error_code => INT16

| Field | Description |
| --- | --- |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| metadata | The partition metadata. |
| error_code | The error code, or 0 if there was no error. |

OffsetFetch Response (Version: 2) => \[topics\] error_code 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset metadata error_code 
      partition_index => INT32
      committed_offset => INT64
      metadata => NULLABLE_STRING
      error_code => INT16
  error_code => INT16

| Field | Description |
| --- | --- |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| metadata | The partition metadata. |
| error_code | The error code, or 0 if there was no error. |
| error_code | The top-level error code, or 0 if there was no error. |

OffsetFetch Response (Version: 3) => throttle\_time\_ms \[topics\] error_code 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset metadata error_code 
      partition_index => INT32
      committed_offset => INT64
      metadata => NULLABLE_STRING
      error_code => INT16
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| metadata | The partition metadata. |
| error_code | The error code, or 0 if there was no error. |
| error_code | The top-level error code, or 0 if there was no error. |

OffsetFetch Response (Version: 4) => throttle\_time\_ms \[topics\] error_code 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset metadata error_code 
      partition_index => INT32
      committed_offset => INT64
      metadata => NULLABLE_STRING
      error_code => INT16
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| metadata | The partition metadata. |
| error_code | The error code, or 0 if there was no error. |
| error_code | The top-level error code, or 0 if there was no error. |

OffsetFetch Response (Version: 5) => throttle\_time\_ms \[topics\] error_code 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch metadata error_code 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      metadata => NULLABLE_STRING
      error_code => INT16
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| committed\_leader\_epoch | The leader epoch. |
| metadata | The partition metadata. |
| error_code | The error code, or 0 if there was no error. |
| error_code | The top-level error code, or 0 if there was no error. |

OffsetFetch Response (Version: 6) => throttle\_time\_ms \[topics\] error\_code TAG\_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch metadata error\_code TAG\_BUFFER 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      metadata => COMPACT\_NULLABLE\_STRING
      error_code => INT16
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| committed\_leader\_epoch | The leader epoch. |
| metadata | The partition metadata. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| error_code | The top-level error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

OffsetFetch Response (Version: 7) => throttle\_time\_ms \[topics\] error\_code TAG\_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch metadata error\_code TAG\_BUFFER 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      metadata => COMPACT\_NULLABLE\_STRING
      error_code => INT16
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| committed\_leader\_epoch | The leader epoch. |
| metadata | The partition metadata. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| error_code | The top-level error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

OffsetFetch Response (Version: 8) => throttle\_time\_ms \[groups\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  groups => group\_id \[topics\] error\_code TAG_BUFFER 
    group\_id => COMPACT\_STRING
    topics => name \[partitions\] TAG_BUFFER 
      name => COMPACT_STRING
      partitions => partition\_index committed\_offset committed\_leader\_epoch metadata error\_code TAG\_BUFFER 
        partition_index => INT32
        committed_offset => INT64
        committed\_leader\_epoch => INT32
        metadata => COMPACT\_NULLABLE\_STRING
        error_code => INT16
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| groups | The responses per group id. |
| group_id | The group ID. |
| topics | The responses per topic. |
| name | The topic name. |
| partitions | The responses per partition |
| partition_index | The partition index. |
| committed_offset | The committed message offset. |
| committed\_leader\_epoch | The leader epoch. |
| metadata | The partition metadata. |
| error_code | The partition-level error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| error_code | The group-level error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### FindCoordinator API (Key: 10):

**Requests:**  

FindCoordinator Request (Version: 0) => key 
  key => STRING

| Field | Description |
| --- | --- |
| key | The coordinator key. |

FindCoordinator Request (Version: 1) => key key_type 
  key => STRING
  key_type => INT8

| Field | Description |
| --- | --- |
| key | The coordinator key. |
| key_type | The coordinator key type. (Group, transaction, etc.) |

FindCoordinator Request (Version: 2) => key key_type 
  key => STRING
  key_type => INT8

| Field | Description |
| --- | --- |
| key | The coordinator key. |
| key_type | The coordinator key type. (Group, transaction, etc.) |

FindCoordinator Request (Version: 3) => key key\_type TAG\_BUFFER 
  key => COMPACT_STRING
  key_type => INT8

| Field | Description |
| --- | --- |
| key | The coordinator key. |
| key_type | The coordinator key type. (Group, transaction, etc.) |
| \_tagged\_fields | The tagged fields |

FindCoordinator Request (Version: 4) => key\_type \[coordinator\_keys\] TAG_BUFFER 
  key_type => INT8
  coordinator\_keys => COMPACT\_STRING

| Field | Description |
| --- | --- |
| key_type | The coordinator key type. (Group, transaction, etc.) |
| coordinator_keys | The coordinator keys. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

FindCoordinator Response (Version: 0) => error\_code node\_id host port 
  error_code => INT16
  node_id => INT32
  host => STRING
  port => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| node_id | The node id. |
| host | The host name. |
| port | The port. |

FindCoordinator Response (Version: 1) => throttle\_time\_ms error\_code error\_message node_id host port 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => NULLABLE\_STRING
  node_id => INT32
  host => STRING
  port => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| node_id | The node id. |
| host | The host name. |
| port | The port. |

FindCoordinator Response (Version: 2) => throttle\_time\_ms error\_code error\_message node_id host port 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => NULLABLE\_STRING
  node_id => INT32
  host => STRING
  port => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| node_id | The node id. |
| host | The host name. |
| port | The port. |

FindCoordinator Response (Version: 3) => throttle\_time\_ms error\_code error\_message node\_id host port TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  node_id => INT32
  host => COMPACT_STRING
  port => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| node_id | The node id. |
| host | The host name. |
| port | The port. |
| \_tagged\_fields | The tagged fields |

FindCoordinator Response (Version: 4) => throttle\_time\_ms \[coordinators\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  coordinators => key node\_id host port error\_code error\_message TAG\_BUFFER 
    key => COMPACT_STRING
    node_id => INT32
    host => COMPACT_STRING
    port => INT32
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| coordinators | Each coordinator result in the response |
| key | The coordinator key. |
| node_id | The node id. |
| host | The host name. |
| port | The port. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### JoinGroup API (Key: 11):

**Requests:**  

JoinGroup Request (Version: 0) => group\_id session\_timeout\_ms member\_id protocol_type \[protocols\] 
  group_id => STRING
  session\_timeout\_ms => INT32
  member_id => STRING
  protocol_type => STRING
  protocols => name metadata 
    name => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| member_id | The member id assigned by the group coordinator. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |

JoinGroup Request (Version: 1) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id protocol_type \[protocols\] 
  group_id => STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member_id => STRING
  protocol_type => STRING
  protocols => name metadata 
    name => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |

JoinGroup Request (Version: 2) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id protocol_type \[protocols\] 
  group_id => STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member_id => STRING
  protocol_type => STRING
  protocols => name metadata 
    name => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |

JoinGroup Request (Version: 3) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id protocol_type \[protocols\] 
  group_id => STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member_id => STRING
  protocol_type => STRING
  protocols => name metadata 
    name => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |

JoinGroup Request (Version: 4) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id protocol_type \[protocols\] 
  group_id => STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member_id => STRING
  protocol_type => STRING
  protocols => name metadata 
    name => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |

JoinGroup Request (Version: 5) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id group\_instance\_id protocol_type \[protocols\] 
  group_id => STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member_id => STRING
  group\_instance\_id => NULLABLE_STRING
  protocol_type => STRING
  protocols => name metadata 
    name => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |

JoinGroup Request (Version: 6) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id group\_instance\_id protocol\_type \[protocols\] TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  protocol\_type => COMPACT\_STRING
  protocols => name metadata TAG_BUFFER 
    name => COMPACT_STRING
    metadata => COMPACT_BYTES

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

JoinGroup Request (Version: 7) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id group\_instance\_id protocol\_type \[protocols\] TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  protocol\_type => COMPACT\_STRING
  protocols => name metadata TAG_BUFFER 
    name => COMPACT_STRING
    metadata => COMPACT_BYTES

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

JoinGroup Request (Version: 8) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id group\_instance\_id protocol\_type \[protocols\] reason TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  protocol\_type => COMPACT\_STRING
  protocols => name metadata TAG_BUFFER 
    name => COMPACT_STRING
    metadata => COMPACT_BYTES
  reason => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |
| \_tagged\_fields | The tagged fields |
| reason | The reason why the member (re-)joins the group. |
| \_tagged\_fields | The tagged fields |

JoinGroup Request (Version: 9) => group\_id session\_timeout\_ms rebalance\_timeout\_ms member\_id group\_instance\_id protocol\_type \[protocols\] reason TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  session\_timeout\_ms => INT32
  rebalance\_timeout\_ms => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  protocol\_type => COMPACT\_STRING
  protocols => name metadata TAG_BUFFER 
    name => COMPACT_STRING
    metadata => COMPACT_BYTES
  reason => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| session\_timeout\_ms | The coordinator considers the consumer dead if it receives no heartbeat after this timeout in milliseconds. |
| rebalance\_timeout\_ms | The maximum time in milliseconds that the coordinator will wait for each member to rejoin when rebalancing the group. |
| member_id | The member id assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| protocol_type | The unique name the for class of protocols implemented by the group we want to join. |
| protocols | The list of protocols that the member supports. |
| name | The protocol name. |
| metadata | The protocol metadata. |
| \_tagged\_fields | The tagged fields |
| reason | The reason why the member (re-)joins the group. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

JoinGroup Response (Version: 0) => error\_code generation\_id protocol\_name leader member\_id \[members\] 
  error_code => INT16
  generation_id => INT32
  protocol_name => STRING
  leader => STRING
  member_id => STRING
  members => member_id metadata 
    member_id => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| metadata | The group member metadata. |

JoinGroup Response (Version: 1) => error\_code generation\_id protocol\_name leader member\_id \[members\] 
  error_code => INT16
  generation_id => INT32
  protocol_name => STRING
  leader => STRING
  member_id => STRING
  members => member_id metadata 
    member_id => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| metadata | The group member metadata. |

JoinGroup Response (Version: 2) => throttle\_time\_ms error\_code generation\_id protocol\_name leader member\_id \[members\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  generation_id => INT32
  protocol_name => STRING
  leader => STRING
  member_id => STRING
  members => member_id metadata 
    member_id => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| metadata | The group member metadata. |

JoinGroup Response (Version: 3) => throttle\_time\_ms error\_code generation\_id protocol\_name leader member\_id \[members\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  generation_id => INT32
  protocol_name => STRING
  leader => STRING
  member_id => STRING
  members => member_id metadata 
    member_id => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| metadata | The group member metadata. |

JoinGroup Response (Version: 4) => throttle\_time\_ms error\_code generation\_id protocol\_name leader member\_id \[members\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  generation_id => INT32
  protocol_name => STRING
  leader => STRING
  member_id => STRING
  members => member_id metadata 
    member_id => STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| metadata | The group member metadata. |

JoinGroup Response (Version: 5) => throttle\_time\_ms error\_code generation\_id protocol\_name leader member\_id \[members\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  generation_id => INT32
  protocol_name => STRING
  leader => STRING
  member_id => STRING
  members => member\_id group\_instance_id metadata 
    member_id => STRING
    group\_instance\_id => NULLABLE_STRING
    metadata => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| metadata | The group member metadata. |

JoinGroup Response (Version: 6) => throttle\_time\_ms error\_code generation\_id protocol\_name leader member\_id \[members\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  generation_id => INT32
  protocol\_name => COMPACT\_STRING
  leader => COMPACT_STRING
  member\_id => COMPACT\_STRING
  members => member\_id group\_instance\_id metadata TAG\_BUFFER 
    member\_id => COMPACT\_STRING
    group\_instance\_id => COMPACT\_NULLABLE\_STRING
    metadata => COMPACT_BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| metadata | The group member metadata. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

JoinGroup Response (Version: 7) => throttle\_time\_ms error\_code generation\_id protocol\_type protocol\_name leader member\_id \[members\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  generation_id => INT32
  protocol\_type => COMPACT\_NULLABLE_STRING
  protocol\_name => COMPACT\_NULLABLE_STRING
  leader => COMPACT_STRING
  member\_id => COMPACT\_STRING
  members => member\_id group\_instance\_id metadata TAG\_BUFFER 
    member\_id => COMPACT\_STRING
    group\_instance\_id => COMPACT\_NULLABLE\_STRING
    metadata => COMPACT_BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_type | The group protocol name. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| metadata | The group member metadata. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

JoinGroup Response (Version: 8) => throttle\_time\_ms error\_code generation\_id protocol\_type protocol\_name leader member\_id \[members\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  generation_id => INT32
  protocol\_type => COMPACT\_NULLABLE_STRING
  protocol\_name => COMPACT\_NULLABLE_STRING
  leader => COMPACT_STRING
  member\_id => COMPACT\_STRING
  members => member\_id group\_instance\_id metadata TAG\_BUFFER 
    member\_id => COMPACT\_STRING
    group\_instance\_id => COMPACT\_NULLABLE\_STRING
    metadata => COMPACT_BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_type | The group protocol name. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| metadata | The group member metadata. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

JoinGroup Response (Version: 9) => throttle\_time\_ms error\_code generation\_id protocol\_type protocol\_name leader skip\_assignment member\_id \[members\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  generation_id => INT32
  protocol\_type => COMPACT\_NULLABLE_STRING
  protocol\_name => COMPACT\_NULLABLE_STRING
  leader => COMPACT_STRING
  skip_assignment => BOOLEAN
  member\_id => COMPACT\_STRING
  members => member\_id group\_instance\_id metadata TAG\_BUFFER 
    member\_id => COMPACT\_STRING
    group\_instance\_id => COMPACT\_NULLABLE\_STRING
    metadata => COMPACT_BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| generation_id | The generation ID of the group. |
| protocol_type | The group protocol name. |
| protocol_name | The group protocol selected by the coordinator. |
| leader | The leader of the group. |
| skip_assignment | True if the leader must skip running the assignment. |
| member_id | The member ID assigned by the group coordinator. |
| members |     |
| member_id | The group member ID. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| metadata | The group member metadata. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### Heartbeat API (Key: 12):

**Requests:**  

Heartbeat Request (Version: 0) => group\_id generation\_id member_id 
  group_id => STRING
  generation_id => INT32
  member_id => STRING

| Field | Description |
| --- | --- |
| group_id | The group id. |
| generation_id | The generation of the group. |
| member_id | The member ID. |

Heartbeat Request (Version: 1) => group\_id generation\_id member_id 
  group_id => STRING
  generation_id => INT32
  member_id => STRING

| Field | Description |
| --- | --- |
| group_id | The group id. |
| generation_id | The generation of the group. |
| member_id | The member ID. |

Heartbeat Request (Version: 2) => group\_id generation\_id member_id 
  group_id => STRING
  generation_id => INT32
  member_id => STRING

| Field | Description |
| --- | --- |
| group_id | The group id. |
| generation_id | The generation of the group. |
| member_id | The member ID. |

Heartbeat Request (Version: 3) => group\_id generation\_id member\_id group\_instance_id 
  group_id => STRING
  generation_id => INT32
  member_id => STRING
  group\_instance\_id => NULLABLE_STRING

| Field | Description |
| --- | --- |
| group_id | The group id. |
| generation_id | The generation of the group. |
| member_id | The member ID. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |

Heartbeat Request (Version: 4) => group\_id generation\_id member\_id group\_instance\_id TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  generation_id => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The group id. |
| generation_id | The generation of the group. |
| member_id | The member ID. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

Heartbeat Response (Version: 0) => error_code 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |

Heartbeat Response (Version: 1) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |

Heartbeat Response (Version: 2) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |

Heartbeat Response (Version: 3) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |

Heartbeat Response (Version: 4) => throttle\_time\_ms error\_code TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

##### LeaveGroup API (Key: 13):

**Requests:**  

LeaveGroup Request (Version: 0) => group\_id member\_id 
  group_id => STRING
  member_id => STRING

| Field | Description |
| --- | --- |
| group_id | The ID of the group to leave. |
| member_id | The member ID to remove from the group. |

LeaveGroup Request (Version: 1) => group\_id member\_id 
  group_id => STRING
  member_id => STRING

| Field | Description |
| --- | --- |
| group_id | The ID of the group to leave. |
| member_id | The member ID to remove from the group. |

LeaveGroup Request (Version: 2) => group\_id member\_id 
  group_id => STRING
  member_id => STRING

| Field | Description |
| --- | --- |
| group_id | The ID of the group to leave. |
| member_id | The member ID to remove from the group. |

LeaveGroup Request (Version: 3) => group_id \[members\] 
  group_id => STRING
  members => member\_id group\_instance_id 
    member_id => STRING
    group\_instance\_id => NULLABLE_STRING

| Field | Description |
| --- | --- |
| group_id | The ID of the group to leave. |
| members | List of leaving member identities. |
| member_id | The member ID to remove from the group. |
| group\_instance\_id | The group instance ID to remove from the group. |

LeaveGroup Request (Version: 4) => group\_id \[members\] TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  members => member\_id group\_instance\_id TAG\_BUFFER 
    member\_id => COMPACT\_STRING
    group\_instance\_id => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The ID of the group to leave. |
| members | List of leaving member identities. |
| member_id | The member ID to remove from the group. |
| group\_instance\_id | The group instance ID to remove from the group. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

LeaveGroup Request (Version: 5) => group\_id \[members\] TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  members => member\_id group\_instance\_id reason TAG\_BUFFER 
    member\_id => COMPACT\_STRING
    group\_instance\_id => COMPACT\_NULLABLE\_STRING
    reason => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| group_id | The ID of the group to leave. |
| members | List of leaving member identities. |
| member_id | The member ID to remove from the group. |
| group\_instance\_id | The group instance ID to remove from the group. |
| reason | The reason why the member left the group. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

LeaveGroup Response (Version: 0) => error_code 
  error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |

LeaveGroup Response (Version: 1) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |

LeaveGroup Response (Version: 2) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |

LeaveGroup Response (Version: 3) => throttle\_time\_ms error_code \[members\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  members => member\_id group\_instance\_id error\_code 
    member_id => STRING
    group\_instance\_id => NULLABLE_STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| members | List of leaving member responses. |
| member_id | The member ID to remove from the group. |
| group\_instance\_id | The group instance ID to remove from the group. |
| error_code | The error code, or 0 if there was no error. |

LeaveGroup Response (Version: 4) => throttle\_time\_ms error\_code \[members\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  members => member\_id group\_instance\_id error\_code TAG_BUFFER 
    member\_id => COMPACT\_STRING
    group\_instance\_id => COMPACT\_NULLABLE\_STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| members | List of leaving member responses. |
| member_id | The member ID to remove from the group. |
| group\_instance\_id | The group instance ID to remove from the group. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

LeaveGroup Response (Version: 5) => throttle\_time\_ms error\_code \[members\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  members => member\_id group\_instance\_id error\_code TAG_BUFFER 
    member\_id => COMPACT\_STRING
    group\_instance\_id => COMPACT\_NULLABLE\_STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| members | List of leaving member responses. |
| member_id | The member ID to remove from the group. |
| group\_instance\_id | The group instance ID to remove from the group. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### SyncGroup API (Key: 14):

**Requests:**  

SyncGroup Request (Version: 0) => group\_id generation\_id member_id \[assignments\] 
  group_id => STRING
  generation_id => INT32
  member_id => STRING
  assignments => member_id assignment 
    member_id => STRING
    assignment => BYTES

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation_id | The generation of the group. |
| member_id | The member ID assigned by the group. |
| assignments | Each assignment. |
| member_id | The ID of the member to assign. |
| assignment | The member assignment. |

SyncGroup Request (Version: 1) => group\_id generation\_id member_id \[assignments\] 
  group_id => STRING
  generation_id => INT32
  member_id => STRING
  assignments => member_id assignment 
    member_id => STRING
    assignment => BYTES

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation_id | The generation of the group. |
| member_id | The member ID assigned by the group. |
| assignments | Each assignment. |
| member_id | The ID of the member to assign. |
| assignment | The member assignment. |

SyncGroup Request (Version: 2) => group\_id generation\_id member_id \[assignments\] 
  group_id => STRING
  generation_id => INT32
  member_id => STRING
  assignments => member_id assignment 
    member_id => STRING
    assignment => BYTES

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation_id | The generation of the group. |
| member_id | The member ID assigned by the group. |
| assignments | Each assignment. |
| member_id | The ID of the member to assign. |
| assignment | The member assignment. |

SyncGroup Request (Version: 3) => group\_id generation\_id member\_id group\_instance_id \[assignments\] 
  group_id => STRING
  generation_id => INT32
  member_id => STRING
  group\_instance\_id => NULLABLE_STRING
  assignments => member_id assignment 
    member_id => STRING
    assignment => BYTES

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation_id | The generation of the group. |
| member_id | The member ID assigned by the group. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| assignments | Each assignment. |
| member_id | The ID of the member to assign. |
| assignment | The member assignment. |

SyncGroup Request (Version: 4) => group\_id generation\_id member\_id group\_instance\_id \[assignments\] TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  generation_id => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  assignments => member\_id assignment TAG\_BUFFER 
    member\_id => COMPACT\_STRING
    assignment => COMPACT_BYTES

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation_id | The generation of the group. |
| member_id | The member ID assigned by the group. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| assignments | Each assignment. |
| member_id | The ID of the member to assign. |
| assignment | The member assignment. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

SyncGroup Request (Version: 5) => group\_id generation\_id member\_id group\_instance\_id protocol\_type protocol\_name \[assignments\] TAG\_BUFFER 
  group\_id => COMPACT\_STRING
  generation_id => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  protocol\_type => COMPACT\_NULLABLE_STRING
  protocol\_name => COMPACT\_NULLABLE_STRING
  assignments => member\_id assignment TAG\_BUFFER 
    member\_id => COMPACT\_STRING
    assignment => COMPACT_BYTES

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| generation_id | The generation of the group. |
| member_id | The member ID assigned by the group. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| protocol_type | The group protocol type. |
| protocol_name | The group protocol name. |
| assignments | Each assignment. |
| member_id | The ID of the member to assign. |
| assignment | The member assignment. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

SyncGroup Response (Version: 0) => error_code assignment 
  error_code => INT16
  assignment => BYTES

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| assignment | The member assignment. |

SyncGroup Response (Version: 1) => throttle\_time\_ms error_code assignment 
  throttle\_time\_ms => INT32
  error_code => INT16
  assignment => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| assignment | The member assignment. |

SyncGroup Response (Version: 2) => throttle\_time\_ms error_code assignment 
  throttle\_time\_ms => INT32
  error_code => INT16
  assignment => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| assignment | The member assignment. |

SyncGroup Response (Version: 3) => throttle\_time\_ms error_code assignment 
  throttle\_time\_ms => INT32
  error_code => INT16
  assignment => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| assignment | The member assignment. |

SyncGroup Response (Version: 4) => throttle\_time\_ms error\_code assignment TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  assignment => COMPACT_BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| assignment | The member assignment. |
| \_tagged\_fields | The tagged fields |

SyncGroup Response (Version: 5) => throttle\_time\_ms error\_code protocol\_type protocol\_name assignment TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  protocol\_type => COMPACT\_NULLABLE_STRING
  protocol\_name => COMPACT\_NULLABLE_STRING
  assignment => COMPACT_BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| protocol_type | The group protocol type. |
| protocol_name | The group protocol name. |
| assignment | The member assignment. |
| \_tagged\_fields | The tagged fields |

##### DescribeGroups API (Key: 15):

**Requests:**  

DescribeGroups Request (Version: 0) => \[groups\] 
  groups => STRING

| Field | Description |
| --- | --- |
| groups | The names of the groups to describe |

DescribeGroups Request (Version: 1) => \[groups\] 
  groups => STRING

| Field | Description |
| --- | --- |
| groups | The names of the groups to describe |

DescribeGroups Request (Version: 2) => \[groups\] 
  groups => STRING

| Field | Description |
| --- | --- |
| groups | The names of the groups to describe |

DescribeGroups Request (Version: 3) => \[groups\] include\_authorized\_operations 
  groups => STRING
  include\_authorized\_operations => BOOLEAN

| Field | Description |
| --- | --- |
| groups | The names of the groups to describe |
| include\_authorized\_operations | Whether to include authorized operations. |

DescribeGroups Request (Version: 4) => \[groups\] include\_authorized\_operations 
  groups => STRING
  include\_authorized\_operations => BOOLEAN

| Field | Description |
| --- | --- |
| groups | The names of the groups to describe |
| include\_authorized\_operations | Whether to include authorized operations. |

DescribeGroups Request (Version: 5) => \[groups\] include\_authorized\_operations TAG_BUFFER 
  groups => COMPACT_STRING
  include\_authorized\_operations => BOOLEAN

| Field | Description |
| --- | --- |
| groups | The names of the groups to describe |
| include\_authorized\_operations | Whether to include authorized operations. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeGroups Response (Version: 0) => \[groups\] 
  groups => error\_code group\_id group\_state protocol\_type protocol_data \[members\] 
    error_code => INT16
    group_id => STRING
    group_state => STRING
    protocol_type => STRING
    protocol_data => STRING
    members => member\_id client\_id client\_host member\_metadata member_assignment 
      member_id => STRING
      client_id => STRING
      client_host => STRING
      member_metadata => BYTES
      member_assignment => BYTES

| Field | Description |
| --- | --- |
| groups | Each described group. |
| error_code | The describe error, or 0 if there was no error. |
| group_id | The group ID string. |
| group_state | The group state string, or the empty string. |
| protocol_type | The group protocol type, or the empty string. |
| protocol_data | The group protocol data, or the empty string. |
| members | The group members. |
| member_id | The member ID assigned by the group coordinator. |
| client_id | The client ID used in the member's latest join group request. |
| client_host | The client host. |
| member_metadata | The metadata corresponding to the current group protocol in use. |
| member_assignment | The current assignment provided by the group leader. |

DescribeGroups Response (Version: 1) => throttle\_time\_ms \[groups\] 
  throttle\_time\_ms => INT32
  groups => error\_code group\_id group\_state protocol\_type protocol_data \[members\] 
    error_code => INT16
    group_id => STRING
    group_state => STRING
    protocol_type => STRING
    protocol_data => STRING
    members => member\_id client\_id client\_host member\_metadata member_assignment 
      member_id => STRING
      client_id => STRING
      client_host => STRING
      member_metadata => BYTES
      member_assignment => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| groups | Each described group. |
| error_code | The describe error, or 0 if there was no error. |
| group_id | The group ID string. |
| group_state | The group state string, or the empty string. |
| protocol_type | The group protocol type, or the empty string. |
| protocol_data | The group protocol data, or the empty string. |
| members | The group members. |
| member_id | The member ID assigned by the group coordinator. |
| client_id | The client ID used in the member's latest join group request. |
| client_host | The client host. |
| member_metadata | The metadata corresponding to the current group protocol in use. |
| member_assignment | The current assignment provided by the group leader. |

DescribeGroups Response (Version: 2) => throttle\_time\_ms \[groups\] 
  throttle\_time\_ms => INT32
  groups => error\_code group\_id group\_state protocol\_type protocol_data \[members\] 
    error_code => INT16
    group_id => STRING
    group_state => STRING
    protocol_type => STRING
    protocol_data => STRING
    members => member\_id client\_id client\_host member\_metadata member_assignment 
      member_id => STRING
      client_id => STRING
      client_host => STRING
      member_metadata => BYTES
      member_assignment => BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| groups | Each described group. |
| error_code | The describe error, or 0 if there was no error. |
| group_id | The group ID string. |
| group_state | The group state string, or the empty string. |
| protocol_type | The group protocol type, or the empty string. |
| protocol_data | The group protocol data, or the empty string. |
| members | The group members. |
| member_id | The member ID assigned by the group coordinator. |
| client_id | The client ID used in the member's latest join group request. |
| client_host | The client host. |
| member_metadata | The metadata corresponding to the current group protocol in use. |
| member_assignment | The current assignment provided by the group leader. |

DescribeGroups Response (Version: 3) => throttle\_time\_ms \[groups\] 
  throttle\_time\_ms => INT32
  groups => error\_code group\_id group\_state protocol\_type protocol\_data \[members\] authorized\_operations 
    error_code => INT16
    group_id => STRING
    group_state => STRING
    protocol_type => STRING
    protocol_data => STRING
    members => member\_id client\_id client\_host member\_metadata member_assignment 
      member_id => STRING
      client_id => STRING
      client_host => STRING
      member_metadata => BYTES
      member_assignment => BYTES
    authorized_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| groups | Each described group. |
| error_code | The describe error, or 0 if there was no error. |
| group_id | The group ID string. |
| group_state | The group state string, or the empty string. |
| protocol_type | The group protocol type, or the empty string. |
| protocol_data | The group protocol data, or the empty string. |
| members | The group members. |
| member_id | The member ID assigned by the group coordinator. |
| client_id | The client ID used in the member's latest join group request. |
| client_host | The client host. |
| member_metadata | The metadata corresponding to the current group protocol in use. |
| member_assignment | The current assignment provided by the group leader. |
| authorized_operations | 32-bit bitfield to represent authorized operations for this group. |

DescribeGroups Response (Version: 4) => throttle\_time\_ms \[groups\] 
  throttle\_time\_ms => INT32
  groups => error\_code group\_id group\_state protocol\_type protocol\_data \[members\] authorized\_operations 
    error_code => INT16
    group_id => STRING
    group_state => STRING
    protocol_type => STRING
    protocol_data => STRING
    members => member\_id group\_instance\_id client\_id client\_host member\_metadata member_assignment 
      member_id => STRING
      group\_instance\_id => NULLABLE_STRING
      client_id => STRING
      client_host => STRING
      member_metadata => BYTES
      member_assignment => BYTES
    authorized_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| groups | Each described group. |
| error_code | The describe error, or 0 if there was no error. |
| group_id | The group ID string. |
| group_state | The group state string, or the empty string. |
| protocol_type | The group protocol type, or the empty string. |
| protocol_data | The group protocol data, or the empty string. |
| members | The group members. |
| member_id | The member ID assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| client_id | The client ID used in the member's latest join group request. |
| client_host | The client host. |
| member_metadata | The metadata corresponding to the current group protocol in use. |
| member_assignment | The current assignment provided by the group leader. |
| authorized_operations | 32-bit bitfield to represent authorized operations for this group. |

DescribeGroups Response (Version: 5) => throttle\_time\_ms \[groups\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  groups => error\_code group\_id group\_state protocol\_type protocol\_data \[members\] authorized\_operations TAG_BUFFER 
    error_code => INT16
    group\_id => COMPACT\_STRING
    group\_state => COMPACT\_STRING
    protocol\_type => COMPACT\_STRING
    protocol\_data => COMPACT\_STRING
    members => member\_id group\_instance\_id client\_id client\_host member\_metadata member\_assignment TAG\_BUFFER 
      member\_id => COMPACT\_STRING
      group\_instance\_id => COMPACT\_NULLABLE\_STRING
      client\_id => COMPACT\_STRING
      client\_host => COMPACT\_STRING
      member\_metadata => COMPACT\_BYTES
      member\_assignment => COMPACT\_BYTES
    authorized_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| groups | Each described group. |
| error_code | The describe error, or 0 if there was no error. |
| group_id | The group ID string. |
| group_state | The group state string, or the empty string. |
| protocol_type | The group protocol type, or the empty string. |
| protocol_data | The group protocol data, or the empty string. |
| members | The group members. |
| member_id | The member ID assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| client_id | The client ID used in the member's latest join group request. |
| client_host | The client host. |
| member_metadata | The metadata corresponding to the current group protocol in use. |
| member_assignment | The current assignment provided by the group leader. |
| \_tagged\_fields | The tagged fields |
| authorized_operations | 32-bit bitfield to represent authorized operations for this group. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### ListGroups API (Key: 16):

**Requests:**  

ListGroups Request (Version: 0) => 

| Field | Description |
| --- | --- |

ListGroups Request (Version: 1) => 

| Field | Description |
| --- | --- |

ListGroups Request (Version: 2) => 

| Field | Description |
| --- | --- |

ListGroups Request (Version: 3) => TAG_BUFFER 

| Field | Description |
| --- | --- |
| \_tagged\_fields | The tagged fields |

ListGroups Request (Version: 4) => \[states\_filter\] TAG\_BUFFER 
  states\_filter => COMPACT\_STRING

| Field | Description |
| --- | --- |
| states_filter | The states of the groups we want to list. If empty all groups are returned with their state. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ListGroups Response (Version: 0) => error_code \[groups\] 
  error_code => INT16
  groups => group\_id protocol\_type 
    group_id => STRING
    protocol_type => STRING

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| groups | Each group in the response. |
| group_id | The group ID. |
| protocol_type | The group protocol type. |

ListGroups Response (Version: 1) => throttle\_time\_ms error_code \[groups\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  groups => group\_id protocol\_type 
    group_id => STRING
    protocol_type => STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| groups | Each group in the response. |
| group_id | The group ID. |
| protocol_type | The group protocol type. |

ListGroups Response (Version: 2) => throttle\_time\_ms error_code \[groups\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  groups => group\_id protocol\_type 
    group_id => STRING
    protocol_type => STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| groups | Each group in the response. |
| group_id | The group ID. |
| protocol_type | The group protocol type. |

ListGroups Response (Version: 3) => throttle\_time\_ms error\_code \[groups\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  groups => group\_id protocol\_type TAG_BUFFER 
    group\_id => COMPACT\_STRING
    protocol\_type => COMPACT\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| groups | Each group in the response. |
| group_id | The group ID. |
| protocol_type | The group protocol type. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

ListGroups Response (Version: 4) => throttle\_time\_ms error\_code \[groups\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  groups => group\_id protocol\_type group\_state TAG\_BUFFER 
    group\_id => COMPACT\_STRING
    protocol\_type => COMPACT\_STRING
    group\_state => COMPACT\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| groups | Each group in the response. |
| group_id | The group ID. |
| protocol_type | The group protocol type. |
| group_state | The group state name. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### SaslHandshake API (Key: 17):

**Requests:**  

SaslHandshake Request (Version: 0) => mechanism 
  mechanism => STRING

| Field | Description |
| --- | --- |
| mechanism | The SASL mechanism chosen by the client. |

SaslHandshake Request (Version: 1) => mechanism 
  mechanism => STRING

| Field | Description |
| --- | --- |
| mechanism | The SASL mechanism chosen by the client. |

**Responses:**  

SaslHandshake Response (Version: 0) => error_code \[mechanisms\] 
  error_code => INT16
  mechanisms => STRING

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| mechanisms | The mechanisms enabled in the server. |

SaslHandshake Response (Version: 1) => error_code \[mechanisms\] 
  error_code => INT16
  mechanisms => STRING

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| mechanisms | The mechanisms enabled in the server. |

##### ApiVersions API (Key: 18):

**Requests:**  

ApiVersions Request (Version: 0) => 

| Field | Description |
| --- | --- |

ApiVersions Request (Version: 1) => 

| Field | Description |
| --- | --- |

ApiVersions Request (Version: 2) => 

| Field | Description |
| --- | --- |

ApiVersions Request (Version: 3) => client\_software\_name client\_software\_version TAG_BUFFER 
  client\_software\_name => COMPACT_STRING
  client\_software\_version => COMPACT_STRING

| Field | Description |
| --- | --- |
| client\_software\_name | The name of the client. |
| client\_software\_version | The version of the client. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ApiVersions Response (Version: 0) => error\_code \[api\_keys\] 
  error_code => INT16
  api\_keys => api\_key min\_version max\_version 
    api_key => INT16
    min_version => INT16
    max_version => INT16

| Field | Description |
| --- | --- |
| error_code | The top-level error code. |
| api_keys | The APIs supported by the broker. |
| api_key | The API index. |
| min_version | The minimum supported version, inclusive. |
| max_version | The maximum supported version, inclusive. |

ApiVersions Response (Version: 1) => error\_code \[api\_keys\] throttle\_time\_ms 
  error_code => INT16
  api\_keys => api\_key min\_version max\_version 
    api_key => INT16
    min_version => INT16
    max_version => INT16
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error code. |
| api_keys | The APIs supported by the broker. |
| api_key | The API index. |
| min_version | The minimum supported version, inclusive. |
| max_version | The maximum supported version, inclusive. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

ApiVersions Response (Version: 2) => error\_code \[api\_keys\] throttle\_time\_ms 
  error_code => INT16
  api\_keys => api\_key min\_version max\_version 
    api_key => INT16
    min_version => INT16
    max_version => INT16
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error code. |
| api_keys | The APIs supported by the broker. |
| api_key | The API index. |
| min_version | The minimum supported version, inclusive. |
| max_version | The maximum supported version, inclusive. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

ApiVersions Response (Version: 3) => error\_code \[api\_keys\] throttle\_time\_ms TAG_BUFFER 
  error_code => INT16
  api\_keys => api\_key min\_version max\_version TAG_BUFFER 
    api_key => INT16
    min_version => INT16
    max_version => INT16
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error code. |
| api_keys | The APIs supported by the broker. |
| api_key | The API index. |
| min_version | The minimum supported version, inclusive. |
| max_version | The maximum supported version, inclusive. |
| \_tagged\_fields | The tagged fields |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| \_tagged\_fields | The tagged fields |

##### CreateTopics API (Key: 19):

**Requests:**  

CreateTopics Request (Version: 0) => \[topics\] timeout_ms 
  topics => name num\_partitions replication\_factor \[assignments\] \[configs\] 
    name => STRING
    num_partitions => INT32
    replication_factor => INT16
    assignments => partition\_index \[broker\_ids\] 
      partition_index => INT32
      broker_ids => INT32
    configs => name value 
      name => STRING
      value => NULLABLE_STRING
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topics | The topics to create. |
| name | The topic name. |
| num_partitions | The number of partitions to create in the topic, or -1 if we are either specifying a manual partition assignment or using the default partitions. |
| replication_factor | The number of replicas to create for each partition in the topic, or -1 if we are either specifying a manual partition assignment or using the default replication factor. |
| assignments | The manual partition assignment, or the empty array if we are using automatic assignment. |
| partition_index | The partition index. |
| broker_ids | The brokers to place the partition on. |
| configs | The custom topic configurations to set. |
| name | The configuration name. |
| value | The configuration value. |
| timeout_ms | How long to wait in milliseconds before timing out the request. |

CreateTopics Request (Version: 1) => \[topics\] timeout\_ms validate\_only 
  topics => name num\_partitions replication\_factor \[assignments\] \[configs\] 
    name => STRING
    num_partitions => INT32
    replication_factor => INT16
    assignments => partition\_index \[broker\_ids\] 
      partition_index => INT32
      broker_ids => INT32
    configs => name value 
      name => STRING
      value => NULLABLE_STRING
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to create. |
| name | The topic name. |
| num_partitions | The number of partitions to create in the topic, or -1 if we are either specifying a manual partition assignment or using the default partitions. |
| replication_factor | The number of replicas to create for each partition in the topic, or -1 if we are either specifying a manual partition assignment or using the default replication factor. |
| assignments | The manual partition assignment, or the empty array if we are using automatic assignment. |
| partition_index | The partition index. |
| broker_ids | The brokers to place the partition on. |
| configs | The custom topic configurations to set. |
| name | The configuration name. |
| value | The configuration value. |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| validate_only | If true, check that the topics can be created as specified, but don't create anything. |

CreateTopics Request (Version: 2) => \[topics\] timeout\_ms validate\_only 
  topics => name num\_partitions replication\_factor \[assignments\] \[configs\] 
    name => STRING
    num_partitions => INT32
    replication_factor => INT16
    assignments => partition\_index \[broker\_ids\] 
      partition_index => INT32
      broker_ids => INT32
    configs => name value 
      name => STRING
      value => NULLABLE_STRING
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to create. |
| name | The topic name. |
| num_partitions | The number of partitions to create in the topic, or -1 if we are either specifying a manual partition assignment or using the default partitions. |
| replication_factor | The number of replicas to create for each partition in the topic, or -1 if we are either specifying a manual partition assignment or using the default replication factor. |
| assignments | The manual partition assignment, or the empty array if we are using automatic assignment. |
| partition_index | The partition index. |
| broker_ids | The brokers to place the partition on. |
| configs | The custom topic configurations to set. |
| name | The configuration name. |
| value | The configuration value. |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| validate_only | If true, check that the topics can be created as specified, but don't create anything. |

CreateTopics Request (Version: 3) => \[topics\] timeout\_ms validate\_only 
  topics => name num\_partitions replication\_factor \[assignments\] \[configs\] 
    name => STRING
    num_partitions => INT32
    replication_factor => INT16
    assignments => partition\_index \[broker\_ids\] 
      partition_index => INT32
      broker_ids => INT32
    configs => name value 
      name => STRING
      value => NULLABLE_STRING
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to create. |
| name | The topic name. |
| num_partitions | The number of partitions to create in the topic, or -1 if we are either specifying a manual partition assignment or using the default partitions. |
| replication_factor | The number of replicas to create for each partition in the topic, or -1 if we are either specifying a manual partition assignment or using the default replication factor. |
| assignments | The manual partition assignment, or the empty array if we are using automatic assignment. |
| partition_index | The partition index. |
| broker_ids | The brokers to place the partition on. |
| configs | The custom topic configurations to set. |
| name | The configuration name. |
| value | The configuration value. |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| validate_only | If true, check that the topics can be created as specified, but don't create anything. |

CreateTopics Request (Version: 4) => \[topics\] timeout\_ms validate\_only 
  topics => name num\_partitions replication\_factor \[assignments\] \[configs\] 
    name => STRING
    num_partitions => INT32
    replication_factor => INT16
    assignments => partition\_index \[broker\_ids\] 
      partition_index => INT32
      broker_ids => INT32
    configs => name value 
      name => STRING
      value => NULLABLE_STRING
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to create. |
| name | The topic name. |
| num_partitions | The number of partitions to create in the topic, or -1 if we are either specifying a manual partition assignment or using the default partitions. |
| replication_factor | The number of replicas to create for each partition in the topic, or -1 if we are either specifying a manual partition assignment or using the default replication factor. |
| assignments | The manual partition assignment, or the empty array if we are using automatic assignment. |
| partition_index | The partition index. |
| broker_ids | The brokers to place the partition on. |
| configs | The custom topic configurations to set. |
| name | The configuration name. |
| value | The configuration value. |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| validate_only | If true, check that the topics can be created as specified, but don't create anything. |

CreateTopics Request (Version: 5) => \[topics\] timeout\_ms validate\_only TAG_BUFFER 
  topics => name num\_partitions replication\_factor \[assignments\] \[configs\] TAG_BUFFER 
    name => COMPACT_STRING
    num_partitions => INT32
    replication_factor => INT16
    assignments => partition\_index \[broker\_ids\] TAG_BUFFER 
      partition_index => INT32
      broker_ids => INT32
    configs => name value TAG_BUFFER 
      name => COMPACT_STRING
      value => COMPACT\_NULLABLE\_STRING
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to create. |
| name | The topic name. |
| num_partitions | The number of partitions to create in the topic, or -1 if we are either specifying a manual partition assignment or using the default partitions. |
| replication_factor | The number of replicas to create for each partition in the topic, or -1 if we are either specifying a manual partition assignment or using the default replication factor. |
| assignments | The manual partition assignment, or the empty array if we are using automatic assignment. |
| partition_index | The partition index. |
| broker_ids | The brokers to place the partition on. |
| \_tagged\_fields | The tagged fields |
| configs | The custom topic configurations to set. |
| name | The configuration name. |
| value | The configuration value. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| validate_only | If true, check that the topics can be created as specified, but don't create anything. |
| \_tagged\_fields | The tagged fields |

CreateTopics Request (Version: 6) => \[topics\] timeout\_ms validate\_only TAG_BUFFER 
  topics => name num\_partitions replication\_factor \[assignments\] \[configs\] TAG_BUFFER 
    name => COMPACT_STRING
    num_partitions => INT32
    replication_factor => INT16
    assignments => partition\_index \[broker\_ids\] TAG_BUFFER 
      partition_index => INT32
      broker_ids => INT32
    configs => name value TAG_BUFFER 
      name => COMPACT_STRING
      value => COMPACT\_NULLABLE\_STRING
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to create. |
| name | The topic name. |
| num_partitions | The number of partitions to create in the topic, or -1 if we are either specifying a manual partition assignment or using the default partitions. |
| replication_factor | The number of replicas to create for each partition in the topic, or -1 if we are either specifying a manual partition assignment or using the default replication factor. |
| assignments | The manual partition assignment, or the empty array if we are using automatic assignment. |
| partition_index | The partition index. |
| broker_ids | The brokers to place the partition on. |
| \_tagged\_fields | The tagged fields |
| configs | The custom topic configurations to set. |
| name | The configuration name. |
| value | The configuration value. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| validate_only | If true, check that the topics can be created as specified, but don't create anything. |
| \_tagged\_fields | The tagged fields |

CreateTopics Request (Version: 7) => \[topics\] timeout\_ms validate\_only TAG_BUFFER 
  topics => name num\_partitions replication\_factor \[assignments\] \[configs\] TAG_BUFFER 
    name => COMPACT_STRING
    num_partitions => INT32
    replication_factor => INT16
    assignments => partition\_index \[broker\_ids\] TAG_BUFFER 
      partition_index => INT32
      broker_ids => INT32
    configs => name value TAG_BUFFER 
      name => COMPACT_STRING
      value => COMPACT\_NULLABLE\_STRING
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | The topics to create. |
| name | The topic name. |
| num_partitions | The number of partitions to create in the topic, or -1 if we are either specifying a manual partition assignment or using the default partitions. |
| replication_factor | The number of replicas to create for each partition in the topic, or -1 if we are either specifying a manual partition assignment or using the default replication factor. |
| assignments | The manual partition assignment, or the empty array if we are using automatic assignment. |
| partition_index | The partition index. |
| broker_ids | The brokers to place the partition on. |
| \_tagged\_fields | The tagged fields |
| configs | The custom topic configurations to set. |
| name | The configuration name. |
| value | The configuration value. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| validate_only | If true, check that the topics can be created as specified, but don't create anything. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

CreateTopics Response (Version: 0) => \[topics\] 
  topics => name error_code 
    name => STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| topics | Results for each topic we tried to create. |
| name | The topic name. |
| error_code | The error code, or 0 if there was no error. |

CreateTopics Response (Version: 1) => \[topics\] 
  topics => name error\_code error\_message 
    name => STRING
    error_code => INT16
    error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| topics | Results for each topic we tried to create. |
| name | The topic name. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |

CreateTopics Response (Version: 2) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name error\_code error\_message 
    name => STRING
    error_code => INT16
    error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Results for each topic we tried to create. |
| name | The topic name. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |

CreateTopics Response (Version: 3) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name error\_code error\_message 
    name => STRING
    error_code => INT16
    error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Results for each topic we tried to create. |
| name | The topic name. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |

CreateTopics Response (Version: 4) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name error\_code error\_message 
    name => STRING
    error_code => INT16
    error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Results for each topic we tried to create. |
| name | The topic name. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |

CreateTopics Response (Version: 5) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name error\_code error\_message num\_partitions replication\_factor \[configs\] TAG_BUFFER 
    name => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    num_partitions => INT32
    replication_factor => INT16
    configs => name value read\_only config\_source is\_sensitive TAG\_BUFFER 
      name => COMPACT_STRING
      value => COMPACT\_NULLABLE\_STRING
      read_only => BOOLEAN
      config_source => INT8
      is_sensitive => BOOLEAN

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Results for each topic we tried to create. |
| name | The topic name. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| num_partitions | Number of partitions of the topic. |
| replication_factor | Replication factor of the topic. |
| configs | Configuration of the topic. |
| name | The configuration name. |
| value | The configuration value. |
| read_only | True if the configuration is read-only. |
| config_source | The configuration source. |
| is_sensitive | True if this configuration is sensitive. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

CreateTopics Response (Version: 6) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name error\_code error\_message num\_partitions replication\_factor \[configs\] TAG_BUFFER 
    name => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    num_partitions => INT32
    replication_factor => INT16
    configs => name value read\_only config\_source is\_sensitive TAG\_BUFFER 
      name => COMPACT_STRING
      value => COMPACT\_NULLABLE\_STRING
      read_only => BOOLEAN
      config_source => INT8
      is_sensitive => BOOLEAN

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Results for each topic we tried to create. |
| name | The topic name. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| num_partitions | Number of partitions of the topic. |
| replication_factor | Replication factor of the topic. |
| configs | Configuration of the topic. |
| name | The configuration name. |
| value | The configuration value. |
| read_only | True if the configuration is read-only. |
| config_source | The configuration source. |
| is_sensitive | True if this configuration is sensitive. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

CreateTopics Response (Version: 7) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name topic\_id error\_code error\_message num\_partitions replication\_factor \[configs\] TAG\_BUFFER 
    name => COMPACT_STRING
    topic_id => UUID
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    num_partitions => INT32
    replication_factor => INT16
    configs => name value read\_only config\_source is\_sensitive TAG\_BUFFER 
      name => COMPACT_STRING
      value => COMPACT\_NULLABLE\_STRING
      read_only => BOOLEAN
      config_source => INT8
      is_sensitive => BOOLEAN

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Results for each topic we tried to create. |
| name | The topic name. |
| topic_id | The unique topic ID |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| num_partitions | Number of partitions of the topic. |
| replication_factor | Replication factor of the topic. |
| configs | Configuration of the topic. |
| name | The configuration name. |
| value | The configuration value. |
| read_only | True if the configuration is read-only. |
| config_source | The configuration source. |
| is_sensitive | True if this configuration is sensitive. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### DeleteTopics API (Key: 20):

**Requests:**  

DeleteTopics Request (Version: 0) => \[topic\_names\] timeout\_ms 
  topic_names => STRING
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topic_names | The names of the topics to delete |
| timeout_ms | The length of time in milliseconds to wait for the deletions to complete. |

DeleteTopics Request (Version: 1) => \[topic\_names\] timeout\_ms 
  topic_names => STRING
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topic_names | The names of the topics to delete |
| timeout_ms | The length of time in milliseconds to wait for the deletions to complete. |

DeleteTopics Request (Version: 2) => \[topic\_names\] timeout\_ms 
  topic_names => STRING
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topic_names | The names of the topics to delete |
| timeout_ms | The length of time in milliseconds to wait for the deletions to complete. |

DeleteTopics Request (Version: 3) => \[topic\_names\] timeout\_ms 
  topic_names => STRING
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topic_names | The names of the topics to delete |
| timeout_ms | The length of time in milliseconds to wait for the deletions to complete. |

DeleteTopics Request (Version: 4) => \[topic\_names\] timeout\_ms TAG_BUFFER 
  topic\_names => COMPACT\_STRING
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topic_names | The names of the topics to delete |
| timeout_ms | The length of time in milliseconds to wait for the deletions to complete. |
| \_tagged\_fields | The tagged fields |

DeleteTopics Request (Version: 5) => \[topic\_names\] timeout\_ms TAG_BUFFER 
  topic\_names => COMPACT\_STRING
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topic_names | The names of the topics to delete |
| timeout_ms | The length of time in milliseconds to wait for the deletions to complete. |
| \_tagged\_fields | The tagged fields |

DeleteTopics Request (Version: 6) => \[topics\] timeout\_ms TAG\_BUFFER 
  topics => name topic\_id TAG\_BUFFER 
    name => COMPACT\_NULLABLE\_STRING
    topic_id => UUID
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topics | The name or topic ID of the topic |
| name | The topic name |
| topic_id | The unique topic ID |
| \_tagged\_fields | The tagged fields |
| timeout_ms | The length of time in milliseconds to wait for the deletions to complete. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DeleteTopics Response (Version: 0) => \[responses\] 
  responses => name error_code 
    name => STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| responses | The results for each topic we tried to delete. |
| name | The topic name |
| error_code | The deletion error, or 0 if the deletion succeeded. |

DeleteTopics Response (Version: 1) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => name error_code 
    name => STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The results for each topic we tried to delete. |
| name | The topic name |
| error_code | The deletion error, or 0 if the deletion succeeded. |

DeleteTopics Response (Version: 2) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => name error_code 
    name => STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The results for each topic we tried to delete. |
| name | The topic name |
| error_code | The deletion error, or 0 if the deletion succeeded. |

DeleteTopics Response (Version: 3) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => name error_code 
    name => STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The results for each topic we tried to delete. |
| name | The topic name |
| error_code | The deletion error, or 0 if the deletion succeeded. |

DeleteTopics Response (Version: 4) => throttle\_time\_ms \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  responses => name error\_code TAG\_BUFFER 
    name => COMPACT_STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The results for each topic we tried to delete. |
| name | The topic name |
| error_code | The deletion error, or 0 if the deletion succeeded. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DeleteTopics Response (Version: 5) => throttle\_time\_ms \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  responses => name error\_code error\_message TAG_BUFFER 
    name => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The results for each topic we tried to delete. |
| name | The topic name |
| error_code | The deletion error, or 0 if the deletion succeeded. |
| error_message | The error message, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DeleteTopics Response (Version: 6) => throttle\_time\_ms \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  responses => name topic\_id error\_code error\_message TAG\_BUFFER 
    name => COMPACT\_NULLABLE\_STRING
    topic_id => UUID
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The results for each topic we tried to delete. |
| name | The topic name |
| topic_id | the unique topic ID |
| error_code | The deletion error, or 0 if the deletion succeeded. |
| error_message | The error message, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### DeleteRecords API (Key: 21):

**Requests:**  

DeleteRecords Request (Version: 0) => \[topics\] timeout_ms 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition_index offset 
      partition_index => INT32
      offset => INT64
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topics | Each topic that we want to delete records from. |
| name | The topic name. |
| partitions | Each partition that we want to delete records from. |
| partition_index | The partition index. |
| offset | The deletion offset. |
| timeout_ms | How long to wait for the deletion to complete, in milliseconds. |

DeleteRecords Request (Version: 1) => \[topics\] timeout_ms 
  topics => name \[partitions\] 
    name => STRING
    partitions => partition_index offset 
      partition_index => INT32
      offset => INT64
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topics | Each topic that we want to delete records from. |
| name | The topic name. |
| partitions | Each partition that we want to delete records from. |
| partition_index | The partition index. |
| offset | The deletion offset. |
| timeout_ms | How long to wait for the deletion to complete, in milliseconds. |

DeleteRecords Request (Version: 2) => \[topics\] timeout\_ms TAG\_BUFFER 
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index offset TAG\_BUFFER 
      partition_index => INT32
      offset => INT64
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topics | Each topic that we want to delete records from. |
| name | The topic name. |
| partitions | Each partition that we want to delete records from. |
| partition_index | The partition index. |
| offset | The deletion offset. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| timeout_ms | How long to wait for the deletion to complete, in milliseconds. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DeleteRecords Response (Version: 0) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index low\_watermark error_code 
      partition_index => INT32
      low_watermark => INT64
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic that we wanted to delete records from. |
| name | The topic name. |
| partitions | Each partition that we wanted to delete records from. |
| partition_index | The partition index. |
| low_watermark | The partition low water mark. |
| error_code | The deletion error code, or 0 if the deletion succeeded. |

DeleteRecords Response (Version: 1) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index low\_watermark error_code 
      partition_index => INT32
      low_watermark => INT64
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic that we wanted to delete records from. |
| name | The topic name. |
| partitions | Each partition that we wanted to delete records from. |
| partition_index | The partition index. |
| low_watermark | The partition low water mark. |
| error_code | The deletion error code, or 0 if the deletion succeeded. |

DeleteRecords Response (Version: 2) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index low\_watermark error\_code TAG\_BUFFER 
      partition_index => INT32
      low_watermark => INT64
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic that we wanted to delete records from. |
| name | The topic name. |
| partitions | Each partition that we wanted to delete records from. |
| partition_index | The partition index. |
| low_watermark | The partition low water mark. |
| error_code | The deletion error code, or 0 if the deletion succeeded. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### InitProducerId API (Key: 22):

**Requests:**  

InitProducerId Request (Version: 0) => transactional\_id transaction\_timeout_ms 
  transactional\_id => NULLABLE\_STRING
  transaction\_timeout\_ms => INT32

| Field | Description |
| --- | --- |
| transactional_id | The transactional id, or null if the producer is not transactional. |
| transaction\_timeout\_ms | The time in ms to wait before aborting idle transactions sent by this producer. This is only relevant if a TransactionalId has been defined. |

InitProducerId Request (Version: 1) => transactional\_id transaction\_timeout_ms 
  transactional\_id => NULLABLE\_STRING
  transaction\_timeout\_ms => INT32

| Field | Description |
| --- | --- |
| transactional_id | The transactional id, or null if the producer is not transactional. |
| transaction\_timeout\_ms | The time in ms to wait before aborting idle transactions sent by this producer. This is only relevant if a TransactionalId has been defined. |

InitProducerId Request (Version: 2) => transactional\_id transaction\_timeout\_ms TAG\_BUFFER 
  transactional\_id => COMPACT\_NULLABLE_STRING
  transaction\_timeout\_ms => INT32

| Field | Description |
| --- | --- |
| transactional_id | The transactional id, or null if the producer is not transactional. |
| transaction\_timeout\_ms | The time in ms to wait before aborting idle transactions sent by this producer. This is only relevant if a TransactionalId has been defined. |
| \_tagged\_fields | The tagged fields |

InitProducerId Request (Version: 3) => transactional\_id transaction\_timeout\_ms producer\_id producer\_epoch TAG\_BUFFER 
  transactional\_id => COMPACT\_NULLABLE_STRING
  transaction\_timeout\_ms => INT32
  producer_id => INT64
  producer_epoch => INT16

| Field | Description |
| --- | --- |
| transactional_id | The transactional id, or null if the producer is not transactional. |
| transaction\_timeout\_ms | The time in ms to wait before aborting idle transactions sent by this producer. This is only relevant if a TransactionalId has been defined. |
| producer_id | The producer id. This is used to disambiguate requests if a transactional id is reused following its expiration. |
| producer_epoch | The producer's current epoch. This will be checked against the producer epoch on the broker, and the request will return an error if they do not match. |
| \_tagged\_fields | The tagged fields |

InitProducerId Request (Version: 4) => transactional\_id transaction\_timeout\_ms producer\_id producer\_epoch TAG\_BUFFER 
  transactional\_id => COMPACT\_NULLABLE_STRING
  transaction\_timeout\_ms => INT32
  producer_id => INT64
  producer_epoch => INT16

| Field | Description |
| --- | --- |
| transactional_id | The transactional id, or null if the producer is not transactional. |
| transaction\_timeout\_ms | The time in ms to wait before aborting idle transactions sent by this producer. This is only relevant if a TransactionalId has been defined. |
| producer_id | The producer id. This is used to disambiguate requests if a transactional id is reused following its expiration. |
| producer_epoch | The producer's current epoch. This will be checked against the producer epoch on the broker, and the request will return an error if they do not match. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

InitProducerId Response (Version: 0) => throttle\_time\_ms error\_code producer\_id producer_epoch 
  throttle\_time\_ms => INT32
  error_code => INT16
  producer_id => INT64
  producer_epoch => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| producer_id | The current producer id. |
| producer_epoch | The current epoch associated with the producer id. |

InitProducerId Response (Version: 1) => throttle\_time\_ms error\_code producer\_id producer_epoch 
  throttle\_time\_ms => INT32
  error_code => INT16
  producer_id => INT64
  producer_epoch => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| producer_id | The current producer id. |
| producer_epoch | The current epoch associated with the producer id. |

InitProducerId Response (Version: 2) => throttle\_time\_ms error\_code producer\_id producer\_epoch TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  producer_id => INT64
  producer_epoch => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| producer_id | The current producer id. |
| producer_epoch | The current epoch associated with the producer id. |
| \_tagged\_fields | The tagged fields |

InitProducerId Response (Version: 3) => throttle\_time\_ms error\_code producer\_id producer\_epoch TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  producer_id => INT64
  producer_epoch => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| producer_id | The current producer id. |
| producer_epoch | The current epoch associated with the producer id. |
| \_tagged\_fields | The tagged fields |

InitProducerId Response (Version: 4) => throttle\_time\_ms error\_code producer\_id producer\_epoch TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  producer_id => INT64
  producer_epoch => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| producer_id | The current producer id. |
| producer_epoch | The current epoch associated with the producer id. |
| \_tagged\_fields | The tagged fields |

##### OffsetForLeaderEpoch API (Key: 23):

**Requests:**  

OffsetForLeaderEpoch Request (Version: 0) => \[topics\] 
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition leader_epoch 
      partition => INT32
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| topics | Each topic to get offsets for. |
| topic | The topic name. |
| partitions | Each partition to get offsets for. |
| partition | The partition index. |
| leader_epoch | The epoch to look up an offset for. |

OffsetForLeaderEpoch Request (Version: 1) => \[topics\] 
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition leader_epoch 
      partition => INT32
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| topics | Each topic to get offsets for. |
| topic | The topic name. |
| partitions | Each partition to get offsets for. |
| partition | The partition index. |
| leader_epoch | The epoch to look up an offset for. |

OffsetForLeaderEpoch Request (Version: 2) => \[topics\] 
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition current\_leader\_epoch leader_epoch 
      partition => INT32
      current\_leader\_epoch => INT32
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| topics | Each topic to get offsets for. |
| topic | The topic name. |
| partitions | Each partition to get offsets for. |
| partition | The partition index. |
| current\_leader\_epoch | An epoch used to fence consumers/replicas with old metadata. If the epoch provided by the client is larger than the current epoch known to the broker, then the UNKNOWN\_LEADER\_EPOCH error code will be returned. If the provided epoch is smaller, then the FENCED\_LEADER\_EPOCH error code will be returned. |
| leader_epoch | The epoch to look up an offset for. |

OffsetForLeaderEpoch Request (Version: 3) => replica_id \[topics\] 
  replica_id => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => partition current\_leader\_epoch leader_epoch 
      partition => INT32
      current\_leader\_epoch => INT32
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| topics | Each topic to get offsets for. |
| topic | The topic name. |
| partitions | Each partition to get offsets for. |
| partition | The partition index. |
| current\_leader\_epoch | An epoch used to fence consumers/replicas with old metadata. If the epoch provided by the client is larger than the current epoch known to the broker, then the UNKNOWN\_LEADER\_EPOCH error code will be returned. If the provided epoch is smaller, then the FENCED\_LEADER\_EPOCH error code will be returned. |
| leader_epoch | The epoch to look up an offset for. |

OffsetForLeaderEpoch Request (Version: 4) => replica\_id \[topics\] TAG\_BUFFER 
  replica_id => INT32
  topics => topic \[partitions\] TAG_BUFFER 
    topic => COMPACT_STRING
    partitions => partition current\_leader\_epoch leader\_epoch TAG\_BUFFER 
      partition => INT32
      current\_leader\_epoch => INT32
      leader_epoch => INT32

| Field | Description |
| --- | --- |
| replica_id | The broker ID of the follower, of -1 if this request is from a consumer. |
| topics | Each topic to get offsets for. |
| topic | The topic name. |
| partitions | Each partition to get offsets for. |
| partition | The partition index. |
| current\_leader\_epoch | An epoch used to fence consumers/replicas with old metadata. If the epoch provided by the client is larger than the current epoch known to the broker, then the UNKNOWN\_LEADER\_EPOCH error code will be returned. If the provided epoch is smaller, then the FENCED\_LEADER\_EPOCH error code will be returned. |
| leader_epoch | The epoch to look up an offset for. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

OffsetForLeaderEpoch Response (Version: 0) => \[topics\] 
  topics => topic \[partitions\] 
    topic => STRING
    partitions => error\_code partition end\_offset 
      error_code => INT16
      partition => INT32
      end_offset => INT64

| Field | Description |
| --- | --- |
| topics | Each topic we fetched offsets for. |
| topic | The topic name. |
| partitions | Each partition in the topic we fetched offsets for. |
| error_code | The error code 0, or if there was no error. |
| partition | The partition index. |
| end_offset | The end offset of the epoch. |

OffsetForLeaderEpoch Response (Version: 1) => \[topics\] 
  topics => topic \[partitions\] 
    topic => STRING
    partitions => error\_code partition leader\_epoch end_offset 
      error_code => INT16
      partition => INT32
      leader_epoch => INT32
      end_offset => INT64

| Field | Description |
| --- | --- |
| topics | Each topic we fetched offsets for. |
| topic | The topic name. |
| partitions | Each partition in the topic we fetched offsets for. |
| error_code | The error code 0, or if there was no error. |
| partition | The partition index. |
| leader_epoch | The leader epoch of the partition. |
| end_offset | The end offset of the epoch. |

OffsetForLeaderEpoch Response (Version: 2) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => error\_code partition leader\_epoch end_offset 
      error_code => INT16
      partition => INT32
      leader_epoch => INT32
      end_offset => INT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic we fetched offsets for. |
| topic | The topic name. |
| partitions | Each partition in the topic we fetched offsets for. |
| error_code | The error code 0, or if there was no error. |
| partition | The partition index. |
| leader_epoch | The leader epoch of the partition. |
| end_offset | The end offset of the epoch. |

OffsetForLeaderEpoch Response (Version: 3) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => topic \[partitions\] 
    topic => STRING
    partitions => error\_code partition leader\_epoch end_offset 
      error_code => INT16
      partition => INT32
      leader_epoch => INT32
      end_offset => INT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic we fetched offsets for. |
| topic | The topic name. |
| partitions | Each partition in the topic we fetched offsets for. |
| error_code | The error code 0, or if there was no error. |
| partition | The partition index. |
| leader_epoch | The leader epoch of the partition. |
| end_offset | The end offset of the epoch. |

OffsetForLeaderEpoch Response (Version: 4) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => topic \[partitions\] TAG_BUFFER 
    topic => COMPACT_STRING
    partitions => error\_code partition leader\_epoch end\_offset TAG\_BUFFER 
      error_code => INT16
      partition => INT32
      leader_epoch => INT32
      end_offset => INT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic we fetched offsets for. |
| topic | The topic name. |
| partitions | Each partition in the topic we fetched offsets for. |
| error_code | The error code 0, or if there was no error. |
| partition | The partition index. |
| leader_epoch | The leader epoch of the partition. |
| end_offset | The end offset of the epoch. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AddPartitionsToTxn API (Key: 24):

**Requests:**  

AddPartitionsToTxn Request (Version: 0) => v3\_and\_below\_transactional\_id v3\_and\_below\_producer\_id v3\_and\_below\_producer\_epoch \[v3\_and\_below_topics\] 
  v3\_and\_below\_transactional\_id => STRING
  v3\_and\_below\_producer\_id => INT64
  v3\_and\_below\_producer\_epoch => INT16
  v3\_and\_below_topics => name \[partitions\] 
    name => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| v3\_and\_below\_transactional\_id | The transactional id corresponding to the transaction. |
| v3\_and\_below\_producer\_id | Current producer id in use by the transactional id. |
| v3\_and\_below\_producer\_epoch | Current epoch associated with the producer id. |
| v3\_and\_below_topics | The partitions to add to the transaction. |
| name | The name of the topic. |
| partitions | The partition indexes to add to the transaction |

AddPartitionsToTxn Request (Version: 1) => v3\_and\_below\_transactional\_id v3\_and\_below\_producer\_id v3\_and\_below\_producer\_epoch \[v3\_and\_below_topics\] 
  v3\_and\_below\_transactional\_id => STRING
  v3\_and\_below\_producer\_id => INT64
  v3\_and\_below\_producer\_epoch => INT16
  v3\_and\_below_topics => name \[partitions\] 
    name => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| v3\_and\_below\_transactional\_id | The transactional id corresponding to the transaction. |
| v3\_and\_below\_producer\_id | Current producer id in use by the transactional id. |
| v3\_and\_below\_producer\_epoch | Current epoch associated with the producer id. |
| v3\_and\_below_topics | The partitions to add to the transaction. |
| name | The name of the topic. |
| partitions | The partition indexes to add to the transaction |

AddPartitionsToTxn Request (Version: 2) => v3\_and\_below\_transactional\_id v3\_and\_below\_producer\_id v3\_and\_below\_producer\_epoch \[v3\_and\_below_topics\] 
  v3\_and\_below\_transactional\_id => STRING
  v3\_and\_below\_producer\_id => INT64
  v3\_and\_below\_producer\_epoch => INT16
  v3\_and\_below_topics => name \[partitions\] 
    name => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| v3\_and\_below\_transactional\_id | The transactional id corresponding to the transaction. |
| v3\_and\_below\_producer\_id | Current producer id in use by the transactional id. |
| v3\_and\_below\_producer\_epoch | Current epoch associated with the producer id. |
| v3\_and\_below_topics | The partitions to add to the transaction. |
| name | The name of the topic. |
| partitions | The partition indexes to add to the transaction |

AddPartitionsToTxn Request (Version: 3) => v3\_and\_below\_transactional\_id v3\_and\_below\_producer\_id v3\_and\_below\_producer\_epoch \[v3\_and\_below\_topics\] TAG\_BUFFER 
  v3\_and\_below\_transactional\_id => COMPACT_STRING
  v3\_and\_below\_producer\_id => INT64
  v3\_and\_below\_producer\_epoch => INT16
  v3\_and\_below\_topics => name \[partitions\] TAG\_BUFFER 
    name => COMPACT_STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| v3\_and\_below\_transactional\_id | The transactional id corresponding to the transaction. |
| v3\_and\_below\_producer\_id | Current producer id in use by the transactional id. |
| v3\_and\_below\_producer\_epoch | Current epoch associated with the producer id. |
| v3\_and\_below_topics | The partitions to add to the transaction. |
| name | The name of the topic. |
| partitions | The partition indexes to add to the transaction |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

AddPartitionsToTxn Request (Version: 4) => \[transactions\] TAG_BUFFER 
  transactions => transactional\_id producer\_id producer\_epoch verify\_only \[topics\] TAG_BUFFER 
    transactional\_id => COMPACT\_STRING
    producer_id => INT64
    producer_epoch => INT16
    verify_only => BOOLEAN
    topics => name \[partitions\] TAG_BUFFER 
      name => COMPACT_STRING
      partitions => INT32

| Field | Description |
| --- | --- |
| transactions | List of transactions to add partitions to. |
| transactional_id | The transactional id corresponding to the transaction. |
| producer_id | Current producer id in use by the transactional id. |
| producer_epoch | Current epoch associated with the producer id. |
| verify_only | Boolean to signify if we want to check if the partition is in the transaction rather than add it. |
| topics | The partitions to add to the transaction. |
| name | The name of the topic. |
| partitions | The partition indexes to add to the transaction |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AddPartitionsToTxn Response (Version: 0) => throttle\_time\_ms \[results\_by\_topic\_v3\_and_below\] 
  throttle\_time\_ms => INT32
  results\_by\_topic\_v3\_and\_below => name \[results\_by_partition\] 
    name => STRING
    results\_by\_partition => partition\_index partition\_error_code 
      partition_index => INT32
      partition\_error\_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results\_by\_topic\_v3\_and_below | The results for each topic. |
| name | The topic name. |
| results\_by\_partition | The results for each partition |
| partition_index | The partition indexes. |
| partition\_error\_code | The response error code. |

AddPartitionsToTxn Response (Version: 1) => throttle\_time\_ms \[results\_by\_topic\_v3\_and_below\] 
  throttle\_time\_ms => INT32
  results\_by\_topic\_v3\_and\_below => name \[results\_by_partition\] 
    name => STRING
    results\_by\_partition => partition\_index partition\_error_code 
      partition_index => INT32
      partition\_error\_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results\_by\_topic\_v3\_and_below | The results for each topic. |
| name | The topic name. |
| results\_by\_partition | The results for each partition |
| partition_index | The partition indexes. |
| partition\_error\_code | The response error code. |

AddPartitionsToTxn Response (Version: 2) => throttle\_time\_ms \[results\_by\_topic\_v3\_and_below\] 
  throttle\_time\_ms => INT32
  results\_by\_topic\_v3\_and\_below => name \[results\_by_partition\] 
    name => STRING
    results\_by\_partition => partition\_index partition\_error_code 
      partition_index => INT32
      partition\_error\_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results\_by\_topic\_v3\_and_below | The results for each topic. |
| name | The topic name. |
| results\_by\_partition | The results for each partition |
| partition_index | The partition indexes. |
| partition\_error\_code | The response error code. |

AddPartitionsToTxn Response (Version: 3) => throttle\_time\_ms \[results\_by\_topic\_v3\_and\_below\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  results\_by\_topic\_v3\_and\_below => name \[results\_by\_partition\] TAG\_BUFFER 
    name => COMPACT_STRING
    results\_by\_partition => partition\_index partition\_error\_code TAG\_BUFFER 
      partition_index => INT32
      partition\_error\_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results\_by\_topic\_v3\_and_below | The results for each topic. |
| name | The topic name. |
| results\_by\_partition | The results for each partition |
| partition_index | The partition indexes. |
| partition\_error\_code | The response error code. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

AddPartitionsToTxn Response (Version: 4) => throttle\_time\_ms error\_code \[results\_by\_transaction\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  results\_by\_transaction => transactional\_id \[topic\_results\] TAG_BUFFER 
    transactional\_id => COMPACT\_STRING
    topic\_results => name \[results\_by\_partition\] TAG\_BUFFER 
      name => COMPACT_STRING
      results\_by\_partition => partition\_index partition\_error\_code TAG\_BUFFER 
        partition_index => INT32
        partition\_error\_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The response top level error code. |
| results\_by\_transaction | Results categorized by transactional ID. |
| transactional_id | The transactional id corresponding to the transaction. |
| topic_results | The results for each topic. |
| name | The topic name. |
| results\_by\_partition | The results for each partition |
| partition_index | The partition indexes. |
| partition\_error\_code | The response error code. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AddOffsetsToTxn API (Key: 25):

**Requests:**  

AddOffsetsToTxn Request (Version: 0) => transactional\_id producer\_id producer\_epoch group\_id 
  transactional_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  group_id => STRING

| Field | Description |
| --- | --- |
| transactional_id | The transactional id corresponding to the transaction. |
| producer_id | Current producer id in use by the transactional id. |
| producer_epoch | Current epoch associated with the producer id. |
| group_id | The unique group identifier. |

AddOffsetsToTxn Request (Version: 1) => transactional\_id producer\_id producer\_epoch group\_id 
  transactional_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  group_id => STRING

| Field | Description |
| --- | --- |
| transactional_id | The transactional id corresponding to the transaction. |
| producer_id | Current producer id in use by the transactional id. |
| producer_epoch | Current epoch associated with the producer id. |
| group_id | The unique group identifier. |

AddOffsetsToTxn Request (Version: 2) => transactional\_id producer\_id producer\_epoch group\_id 
  transactional_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  group_id => STRING

| Field | Description |
| --- | --- |
| transactional_id | The transactional id corresponding to the transaction. |
| producer_id | Current producer id in use by the transactional id. |
| producer_epoch | Current epoch associated with the producer id. |
| group_id | The unique group identifier. |

AddOffsetsToTxn Request (Version: 3) => transactional\_id producer\_id producer\_epoch group\_id TAG_BUFFER 
  transactional\_id => COMPACT\_STRING
  producer_id => INT64
  producer_epoch => INT16
  group\_id => COMPACT\_STRING

| Field | Description |
| --- | --- |
| transactional_id | The transactional id corresponding to the transaction. |
| producer_id | Current producer id in use by the transactional id. |
| producer_epoch | Current epoch associated with the producer id. |
| group_id | The unique group identifier. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AddOffsetsToTxn Response (Version: 0) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The response error code, or 0 if there was no error. |

AddOffsetsToTxn Response (Version: 1) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The response error code, or 0 if there was no error. |

AddOffsetsToTxn Response (Version: 2) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The response error code, or 0 if there was no error. |

AddOffsetsToTxn Response (Version: 3) => throttle\_time\_ms error\_code TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The response error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

##### EndTxn API (Key: 26):

**Requests:**  

EndTxn Request (Version: 0) => transactional\_id producer\_id producer_epoch committed 
  transactional_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  committed => BOOLEAN

| Field | Description |
| --- | --- |
| transactional_id | The ID of the transaction to end. |
| producer_id | The producer ID. |
| producer_epoch | The current epoch associated with the producer. |
| committed | True if the transaction was committed, false if it was aborted. |

EndTxn Request (Version: 1) => transactional\_id producer\_id producer_epoch committed 
  transactional_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  committed => BOOLEAN

| Field | Description |
| --- | --- |
| transactional_id | The ID of the transaction to end. |
| producer_id | The producer ID. |
| producer_epoch | The current epoch associated with the producer. |
| committed | True if the transaction was committed, false if it was aborted. |

EndTxn Request (Version: 2) => transactional\_id producer\_id producer_epoch committed 
  transactional_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  committed => BOOLEAN

| Field | Description |
| --- | --- |
| transactional_id | The ID of the transaction to end. |
| producer_id | The producer ID. |
| producer_epoch | The current epoch associated with the producer. |
| committed | True if the transaction was committed, false if it was aborted. |

EndTxn Request (Version: 3) => transactional\_id producer\_id producer\_epoch committed TAG\_BUFFER 
  transactional\_id => COMPACT\_STRING
  producer_id => INT64
  producer_epoch => INT16
  committed => BOOLEAN

| Field | Description |
| --- | --- |
| transactional_id | The ID of the transaction to end. |
| producer_id | The producer ID. |
| producer_epoch | The current epoch associated with the producer. |
| committed | True if the transaction was committed, false if it was aborted. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

EndTxn Response (Version: 0) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |

EndTxn Response (Version: 1) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |

EndTxn Response (Version: 2) => throttle\_time\_ms error_code 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |

EndTxn Response (Version: 3) => throttle\_time\_ms error\_code TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

##### WriteTxnMarkers API (Key: 27):

**Requests:**  

WriteTxnMarkers Request (Version: 0) => \[markers\] 
  markers => producer\_id producer\_epoch transaction\_result \[topics\] coordinator\_epoch 
    producer_id => INT64
    producer_epoch => INT16
    transaction_result => BOOLEAN
    topics => name \[partition_indexes\] 
      name => STRING
      partition_indexes => INT32
    coordinator_epoch => INT32

| Field | Description |
| --- | --- |
| markers | The transaction markers to be written. |
| producer_id | The current producer ID. |
| producer_epoch | The current epoch associated with the producer ID. |
| transaction_result | The result of the transaction to write to the partitions (false = ABORT, true = COMMIT). |
| topics | Each topic that we want to write transaction marker(s) for. |
| name | The topic name. |
| partition_indexes | The indexes of the partitions to write transaction markers for. |
| coordinator_epoch | Epoch associated with the transaction state partition hosted by this transaction coordinator |

WriteTxnMarkers Request (Version: 1) => \[markers\] TAG_BUFFER 
  markers => producer\_id producer\_epoch transaction\_result \[topics\] coordinator\_epoch TAG_BUFFER 
    producer_id => INT64
    producer_epoch => INT16
    transaction_result => BOOLEAN
    topics => name \[partition\_indexes\] TAG\_BUFFER 
      name => COMPACT_STRING
      partition_indexes => INT32
    coordinator_epoch => INT32

| Field | Description |
| --- | --- |
| markers | The transaction markers to be written. |
| producer_id | The current producer ID. |
| producer_epoch | The current epoch associated with the producer ID. |
| transaction_result | The result of the transaction to write to the partitions (false = ABORT, true = COMMIT). |
| topics | Each topic that we want to write transaction marker(s) for. |
| name | The topic name. |
| partition_indexes | The indexes of the partitions to write transaction markers for. |
| \_tagged\_fields | The tagged fields |
| coordinator_epoch | Epoch associated with the transaction state partition hosted by this transaction coordinator |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

WriteTxnMarkers Response (Version: 0) => \[markers\] 
  markers => producer_id \[topics\] 
    producer_id => INT64
    topics => name \[partitions\] 
      name => STRING
      partitions => partition\_index error\_code 
        partition_index => INT32
        error_code => INT16

| Field | Description |
| --- | --- |
| markers | The results for writing makers. |
| producer_id | The current producer ID in use by the transactional ID. |
| topics | The results by topic. |
| name | The topic name. |
| partitions | The results by partition. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

WriteTxnMarkers Response (Version: 1) => \[markers\] TAG_BUFFER 
  markers => producer\_id \[topics\] TAG\_BUFFER 
    producer_id => INT64
    topics => name \[partitions\] TAG_BUFFER 
      name => COMPACT_STRING
      partitions => partition\_index error\_code TAG_BUFFER 
        partition_index => INT32
        error_code => INT16

| Field | Description |
| --- | --- |
| markers | The results for writing makers. |
| producer_id | The current producer ID in use by the transactional ID. |
| topics | The results by topic. |
| name | The topic name. |
| partitions | The results by partition. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### TxnOffsetCommit API (Key: 28):

**Requests:**  

TxnOffsetCommit Request (Version: 0) => transactional\_id group\_id producer\_id producer\_epoch \[topics\] 
  transactional_id => STRING
  group_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| transactional_id | The ID of the transaction. |
| group_id | The ID of the group. |
| producer_id | The current producer ID in use by the transactional ID. |
| producer_epoch | The current epoch associated with the producer ID. |
| topics | Each topic that we want to commit offsets for. |
| name | The topic name. |
| partitions | The partitions inside the topic that we want to commit offsets for. |
| partition_index | The index of the partition within the topic. |
| committed_offset | The message offset to be committed. |
| committed_metadata | Any associated metadata the client wants to keep. |

TxnOffsetCommit Request (Version: 1) => transactional\_id group\_id producer\_id producer\_epoch \[topics\] 
  transactional_id => STRING
  group_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| transactional_id | The ID of the transaction. |
| group_id | The ID of the group. |
| producer_id | The current producer ID in use by the transactional ID. |
| producer_epoch | The current epoch associated with the producer ID. |
| topics | Each topic that we want to commit offsets for. |
| name | The topic name. |
| partitions | The partitions inside the topic that we want to commit offsets for. |
| partition_index | The index of the partition within the topic. |
| committed_offset | The message offset to be committed. |
| committed_metadata | Any associated metadata the client wants to keep. |

TxnOffsetCommit Request (Version: 2) => transactional\_id group\_id producer\_id producer\_epoch \[topics\] 
  transactional_id => STRING
  group_id => STRING
  producer_id => INT64
  producer_epoch => INT16
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch committed_metadata 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      committed\_metadata => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| transactional_id | The ID of the transaction. |
| group_id | The ID of the group. |
| producer_id | The current producer ID in use by the transactional ID. |
| producer_epoch | The current epoch associated with the producer ID. |
| topics | Each topic that we want to commit offsets for. |
| name | The topic name. |
| partitions | The partitions inside the topic that we want to commit offsets for. |
| partition_index | The index of the partition within the topic. |
| committed_offset | The message offset to be committed. |
| committed\_leader\_epoch | The leader epoch of the last consumed record. |
| committed_metadata | Any associated metadata the client wants to keep. |

TxnOffsetCommit Request (Version: 3) => transactional\_id group\_id producer\_id producer\_epoch generation\_id member\_id group\_instance\_id \[topics\] TAG_BUFFER 
  transactional\_id => COMPACT\_STRING
  group\_id => COMPACT\_STRING
  producer_id => INT64
  producer_epoch => INT16
  generation_id => INT32
  member\_id => COMPACT\_STRING
  group\_instance\_id => COMPACT\_NULLABLE\_STRING
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index committed\_offset committed\_leader\_epoch committed\_metadata TAG\_BUFFER 
      partition_index => INT32
      committed_offset => INT64
      committed\_leader\_epoch => INT32
      committed\_metadata => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| transactional_id | The ID of the transaction. |
| group_id | The ID of the group. |
| producer_id | The current producer ID in use by the transactional ID. |
| producer_epoch | The current epoch associated with the producer ID. |
| generation_id | The generation of the consumer. |
| member_id | The member ID assigned by the group coordinator. |
| group\_instance\_id | The unique identifier of the consumer instance provided by end user. |
| topics | Each topic that we want to commit offsets for. |
| name | The topic name. |
| partitions | The partitions inside the topic that we want to commit offsets for. |
| partition_index | The index of the partition within the topic. |
| committed_offset | The message offset to be committed. |
| committed\_leader\_epoch | The leader epoch of the last consumed record. |
| committed_metadata | Any associated metadata the client wants to keep. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

TxnOffsetCommit Response (Version: 0) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

TxnOffsetCommit Response (Version: 1) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

TxnOffsetCommit Response (Version: 2) => throttle\_time\_ms \[topics\] 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

TxnOffsetCommit Response (Version: 3) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index error\_code TAG_BUFFER 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### DescribeAcls API (Key: 29):

**Requests:**  

DescribeAcls Request (Version: 0) => resource\_type\_filter resource\_name\_filter principal\_filter host\_filter operation permission_type 
  resource\_type\_filter => INT8
  resource\_name\_filter => NULLABLE_STRING
  principal\_filter => NULLABLE\_STRING
  host\_filter => NULLABLE\_STRING
  operation => INT8
  permission_type => INT8

| Field | Description |
| --- | --- |
| resource\_type\_filter | The resource type. |
| resource\_name\_filter | The resource name, or null to match any resource name. |
| principal_filter | The principal to match, or null to match any principal. |
| host_filter | The host to match, or null to match any host. |
| operation | The operation to match. |
| permission_type | The permission type to match. |

DescribeAcls Request (Version: 1) => resource\_type\_filter resource\_name\_filter pattern\_type\_filter principal\_filter host\_filter operation permission_type 
  resource\_type\_filter => INT8
  resource\_name\_filter => NULLABLE_STRING
  pattern\_type\_filter => INT8
  principal\_filter => NULLABLE\_STRING
  host\_filter => NULLABLE\_STRING
  operation => INT8
  permission_type => INT8

| Field | Description |
| --- | --- |
| resource\_type\_filter | The resource type. |
| resource\_name\_filter | The resource name, or null to match any resource name. |
| pattern\_type\_filter | The resource pattern to match. |
| principal_filter | The principal to match, or null to match any principal. |
| host_filter | The host to match, or null to match any host. |
| operation | The operation to match. |
| permission_type | The permission type to match. |

DescribeAcls Request (Version: 2) => resource\_type\_filter resource\_name\_filter pattern\_type\_filter principal\_filter host\_filter operation permission\_type TAG\_BUFFER 
  resource\_type\_filter => INT8
  resource\_name\_filter => COMPACT\_NULLABLE\_STRING
  pattern\_type\_filter => INT8
  principal\_filter => COMPACT\_NULLABLE_STRING
  host\_filter => COMPACT\_NULLABLE_STRING
  operation => INT8
  permission_type => INT8

| Field | Description |
| --- | --- |
| resource\_type\_filter | The resource type. |
| resource\_name\_filter | The resource name, or null to match any resource name. |
| pattern\_type\_filter | The resource pattern to match. |
| principal_filter | The principal to match, or null to match any principal. |
| host_filter | The host to match, or null to match any host. |
| operation | The operation to match. |
| permission_type | The permission type to match. |
| \_tagged\_fields | The tagged fields |

DescribeAcls Request (Version: 3) => resource\_type\_filter resource\_name\_filter pattern\_type\_filter principal\_filter host\_filter operation permission\_type TAG\_BUFFER 
  resource\_type\_filter => INT8
  resource\_name\_filter => COMPACT\_NULLABLE\_STRING
  pattern\_type\_filter => INT8
  principal\_filter => COMPACT\_NULLABLE_STRING
  host\_filter => COMPACT\_NULLABLE_STRING
  operation => INT8
  permission_type => INT8

| Field | Description |
| --- | --- |
| resource\_type\_filter | The resource type. |
| resource\_name\_filter | The resource name, or null to match any resource name. |
| pattern\_type\_filter | The resource pattern to match. |
| principal_filter | The principal to match, or null to match any principal. |
| host_filter | The host to match, or null to match any host. |
| operation | The operation to match. |
| permission_type | The permission type to match. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeAcls Response (Version: 0) => throttle\_time\_ms error\_code error\_message \[resources\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => NULLABLE\_STRING
  resources => resource\_type resource\_name \[acls\] 
    resource_type => INT8
    resource_name => STRING
    acls => principal host operation permission_type 
      principal => STRING
      host => STRING
      operation => INT8
      permission_type => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| resources | Each Resource that is referenced in an ACL. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| acls | The ACLs. |
| principal | The ACL principal. |
| host | The ACL host. |
| operation | The ACL operation. |
| permission_type | The ACL permission type. |

DescribeAcls Response (Version: 1) => throttle\_time\_ms error\_code error\_message \[resources\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => NULLABLE\_STRING
  resources => resource\_type resource\_name pattern_type \[acls\] 
    resource_type => INT8
    resource_name => STRING
    pattern_type => INT8
    acls => principal host operation permission_type 
      principal => STRING
      host => STRING
      operation => INT8
      permission_type => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| resources | Each Resource that is referenced in an ACL. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| pattern_type | The resource pattern type. |
| acls | The ACLs. |
| principal | The ACL principal. |
| host | The ACL host. |
| operation | The ACL operation. |
| permission_type | The ACL permission type. |

DescribeAcls Response (Version: 2) => throttle\_time\_ms error\_code error\_message \[resources\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  resources => resource\_type resource\_name pattern\_type \[acls\] TAG\_BUFFER 
    resource_type => INT8
    resource\_name => COMPACT\_STRING
    pattern_type => INT8
    acls => principal host operation permission\_type TAG\_BUFFER 
      principal => COMPACT_STRING
      host => COMPACT_STRING
      operation => INT8
      permission_type => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| resources | Each Resource that is referenced in an ACL. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| pattern_type | The resource pattern type. |
| acls | The ACLs. |
| principal | The ACL principal. |
| host | The ACL host. |
| operation | The ACL operation. |
| permission_type | The ACL permission type. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DescribeAcls Response (Version: 3) => throttle\_time\_ms error\_code error\_message \[resources\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  resources => resource\_type resource\_name pattern\_type \[acls\] TAG\_BUFFER 
    resource_type => INT8
    resource\_name => COMPACT\_STRING
    pattern_type => INT8
    acls => principal host operation permission\_type TAG\_BUFFER 
      principal => COMPACT_STRING
      host => COMPACT_STRING
      operation => INT8
      permission_type => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| resources | Each Resource that is referenced in an ACL. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| pattern_type | The resource pattern type. |
| acls | The ACLs. |
| principal | The ACL principal. |
| host | The ACL host. |
| operation | The ACL operation. |
| permission_type | The ACL permission type. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### CreateAcls API (Key: 30):

**Requests:**  

CreateAcls Request (Version: 0) => \[creations\] 
  creations => resource\_type resource\_name principal host operation permission_type 
    resource_type => INT8
    resource_name => STRING
    principal => STRING
    host => STRING
    operation => INT8
    permission_type => INT8

| Field | Description |
| --- | --- |
| creations | The ACLs that we want to create. |
| resource_type | The type of the resource. |
| resource_name | The resource name for the ACL. |
| principal | The principal for the ACL. |
| host | The host for the ACL. |
| operation | The operation type for the ACL (read, write, etc.). |
| permission_type | The permission type for the ACL (allow, deny, etc.). |

CreateAcls Request (Version: 1) => \[creations\] 
  creations => resource\_type resource\_name resource\_pattern\_type principal host operation permission_type 
    resource_type => INT8
    resource_name => STRING
    resource\_pattern\_type => INT8
    principal => STRING
    host => STRING
    operation => INT8
    permission_type => INT8

| Field | Description |
| --- | --- |
| creations | The ACLs that we want to create. |
| resource_type | The type of the resource. |
| resource_name | The resource name for the ACL. |
| resource\_pattern\_type | The pattern type for the ACL. |
| principal | The principal for the ACL. |
| host | The host for the ACL. |
| operation | The operation type for the ACL (read, write, etc.). |
| permission_type | The permission type for the ACL (allow, deny, etc.). |

CreateAcls Request (Version: 2) => \[creations\] TAG_BUFFER 
  creations => resource\_type resource\_name resource\_pattern\_type principal host operation permission\_type TAG\_BUFFER 
    resource_type => INT8
    resource\_name => COMPACT\_STRING
    resource\_pattern\_type => INT8
    principal => COMPACT_STRING
    host => COMPACT_STRING
    operation => INT8
    permission_type => INT8

| Field | Description |
| --- | --- |
| creations | The ACLs that we want to create. |
| resource_type | The type of the resource. |
| resource_name | The resource name for the ACL. |
| resource\_pattern\_type | The pattern type for the ACL. |
| principal | The principal for the ACL. |
| host | The host for the ACL. |
| operation | The operation type for the ACL (read, write, etc.). |
| permission_type | The permission type for the ACL (allow, deny, etc.). |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

CreateAcls Request (Version: 3) => \[creations\] TAG_BUFFER 
  creations => resource\_type resource\_name resource\_pattern\_type principal host operation permission\_type TAG\_BUFFER 
    resource_type => INT8
    resource\_name => COMPACT\_STRING
    resource\_pattern\_type => INT8
    principal => COMPACT_STRING
    host => COMPACT_STRING
    operation => INT8
    permission_type => INT8

| Field | Description |
| --- | --- |
| creations | The ACLs that we want to create. |
| resource_type | The type of the resource. |
| resource_name | The resource name for the ACL. |
| resource\_pattern\_type | The pattern type for the ACL. |
| principal | The principal for the ACL. |
| host | The host for the ACL. |
| operation | The operation type for the ACL (read, write, etc.). |
| permission_type | The permission type for the ACL (allow, deny, etc.). |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

CreateAcls Response (Version: 0) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => error\_code error\_message 
    error_code => INT16
    error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each ACL creation. |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |

CreateAcls Response (Version: 1) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => error\_code error\_message 
    error_code => INT16
    error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each ACL creation. |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |

CreateAcls Response (Version: 2) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => error\_code error\_message TAG_BUFFER 
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each ACL creation. |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

CreateAcls Response (Version: 3) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => error\_code error\_message TAG_BUFFER 
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each ACL creation. |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### DeleteAcls API (Key: 31):

**Requests:**  

DeleteAcls Request (Version: 0) => \[filters\] 
  filters => resource\_type\_filter resource\_name\_filter principal\_filter host\_filter operation permission_type 
    resource\_type\_filter => INT8
    resource\_name\_filter => NULLABLE_STRING
    principal\_filter => NULLABLE\_STRING
    host\_filter => NULLABLE\_STRING
    operation => INT8
    permission_type => INT8

| Field | Description |
| --- | --- |
| filters | The filters to use when deleting ACLs. |
| resource\_type\_filter | The resource type. |
| resource\_name\_filter | The resource name. |
| principal_filter | The principal filter, or null to accept all principals. |
| host_filter | The host filter, or null to accept all hosts. |
| operation | The ACL operation. |
| permission_type | The permission type. |

DeleteAcls Request (Version: 1) => \[filters\] 
  filters => resource\_type\_filter resource\_name\_filter pattern\_type\_filter principal\_filter host\_filter operation permission_type 
    resource\_type\_filter => INT8
    resource\_name\_filter => NULLABLE_STRING
    pattern\_type\_filter => INT8
    principal\_filter => NULLABLE\_STRING
    host\_filter => NULLABLE\_STRING
    operation => INT8
    permission_type => INT8

| Field | Description |
| --- | --- |
| filters | The filters to use when deleting ACLs. |
| resource\_type\_filter | The resource type. |
| resource\_name\_filter | The resource name. |
| pattern\_type\_filter | The pattern type. |
| principal_filter | The principal filter, or null to accept all principals. |
| host_filter | The host filter, or null to accept all hosts. |
| operation | The ACL operation. |
| permission_type | The permission type. |

DeleteAcls Request (Version: 2) => \[filters\] TAG_BUFFER 
  filters => resource\_type\_filter resource\_name\_filter pattern\_type\_filter principal\_filter host\_filter operation permission\_type TAG\_BUFFER 
    resource\_type\_filter => INT8
    resource\_name\_filter => COMPACT\_NULLABLE\_STRING
    pattern\_type\_filter => INT8
    principal\_filter => COMPACT\_NULLABLE_STRING
    host\_filter => COMPACT\_NULLABLE_STRING
    operation => INT8
    permission_type => INT8

| Field | Description |
| --- | --- |
| filters | The filters to use when deleting ACLs. |
| resource\_type\_filter | The resource type. |
| resource\_name\_filter | The resource name. |
| pattern\_type\_filter | The pattern type. |
| principal_filter | The principal filter, or null to accept all principals. |
| host_filter | The host filter, or null to accept all hosts. |
| operation | The ACL operation. |
| permission_type | The permission type. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DeleteAcls Request (Version: 3) => \[filters\] TAG_BUFFER 
  filters => resource\_type\_filter resource\_name\_filter pattern\_type\_filter principal\_filter host\_filter operation permission\_type TAG\_BUFFER 
    resource\_type\_filter => INT8
    resource\_name\_filter => COMPACT\_NULLABLE\_STRING
    pattern\_type\_filter => INT8
    principal\_filter => COMPACT\_NULLABLE_STRING
    host\_filter => COMPACT\_NULLABLE_STRING
    operation => INT8
    permission_type => INT8

| Field | Description |
| --- | --- |
| filters | The filters to use when deleting ACLs. |
| resource\_type\_filter | The resource type. |
| resource\_name\_filter | The resource name. |
| pattern\_type\_filter | The pattern type. |
| principal_filter | The principal filter, or null to accept all principals. |
| host_filter | The host filter, or null to accept all hosts. |
| operation | The ACL operation. |
| permission_type | The permission type. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DeleteAcls Response (Version: 0) => throttle\_time\_ms \[filter_results\] 
  throttle\_time\_ms => INT32
  filter\_results => error\_code error\_message \[matching\_acls\] 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    matching\_acls => error\_code error\_message resource\_type resource\_name principal host operation permission\_type 
      error_code => INT16
      error\_message => NULLABLE\_STRING
      resource_type => INT8
      resource_name => STRING
      principal => STRING
      host => STRING
      operation => INT8
      permission_type => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| filter_results | The results for each filter. |
| error_code | The error code, or 0 if the filter succeeded. |
| error_message | The error message, or null if the filter succeeded. |
| matching_acls | The ACLs which matched this filter. |
| error_code | The deletion error code, or 0 if the deletion succeeded. |
| error_message | The deletion error message, or null if the deletion succeeded. |
| resource_type | The ACL resource type. |
| resource_name | The ACL resource name. |
| principal | The ACL principal. |
| host | The ACL host. |
| operation | The ACL operation. |
| permission_type | The ACL permission type. |

DeleteAcls Response (Version: 1) => throttle\_time\_ms \[filter_results\] 
  throttle\_time\_ms => INT32
  filter\_results => error\_code error\_message \[matching\_acls\] 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    matching\_acls => error\_code error\_message resource\_type resource\_name pattern\_type principal host operation permission_type 
      error_code => INT16
      error\_message => NULLABLE\_STRING
      resource_type => INT8
      resource_name => STRING
      pattern_type => INT8
      principal => STRING
      host => STRING
      operation => INT8
      permission_type => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| filter_results | The results for each filter. |
| error_code | The error code, or 0 if the filter succeeded. |
| error_message | The error message, or null if the filter succeeded. |
| matching_acls | The ACLs which matched this filter. |
| error_code | The deletion error code, or 0 if the deletion succeeded. |
| error_message | The deletion error message, or null if the deletion succeeded. |
| resource_type | The ACL resource type. |
| resource_name | The ACL resource name. |
| pattern_type | The ACL resource pattern type. |
| principal | The ACL principal. |
| host | The ACL host. |
| operation | The ACL operation. |
| permission_type | The ACL permission type. |

DeleteAcls Response (Version: 2) => throttle\_time\_ms \[filter\_results\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  filter\_results => error\_code error\_message \[matching\_acls\] TAG_BUFFER 
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    matching\_acls => error\_code error\_message resource\_type resource\_name pattern\_type principal host operation permission\_type TAG\_BUFFER 
      error_code => INT16
      error\_message => COMPACT\_NULLABLE_STRING
      resource_type => INT8
      resource\_name => COMPACT\_STRING
      pattern_type => INT8
      principal => COMPACT_STRING
      host => COMPACT_STRING
      operation => INT8
      permission_type => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| filter_results | The results for each filter. |
| error_code | The error code, or 0 if the filter succeeded. |
| error_message | The error message, or null if the filter succeeded. |
| matching_acls | The ACLs which matched this filter. |
| error_code | The deletion error code, or 0 if the deletion succeeded. |
| error_message | The deletion error message, or null if the deletion succeeded. |
| resource_type | The ACL resource type. |
| resource_name | The ACL resource name. |
| pattern_type | The ACL resource pattern type. |
| principal | The ACL principal. |
| host | The ACL host. |
| operation | The ACL operation. |
| permission_type | The ACL permission type. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DeleteAcls Response (Version: 3) => throttle\_time\_ms \[filter\_results\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  filter\_results => error\_code error\_message \[matching\_acls\] TAG_BUFFER 
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    matching\_acls => error\_code error\_message resource\_type resource\_name pattern\_type principal host operation permission\_type TAG\_BUFFER 
      error_code => INT16
      error\_message => COMPACT\_NULLABLE_STRING
      resource_type => INT8
      resource\_name => COMPACT\_STRING
      pattern_type => INT8
      principal => COMPACT_STRING
      host => COMPACT_STRING
      operation => INT8
      permission_type => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| filter_results | The results for each filter. |
| error_code | The error code, or 0 if the filter succeeded. |
| error_message | The error message, or null if the filter succeeded. |
| matching_acls | The ACLs which matched this filter. |
| error_code | The deletion error code, or 0 if the deletion succeeded. |
| error_message | The deletion error message, or null if the deletion succeeded. |
| resource_type | The ACL resource type. |
| resource_name | The ACL resource name. |
| pattern_type | The ACL resource pattern type. |
| principal | The ACL principal. |
| host | The ACL host. |
| operation | The ACL operation. |
| permission_type | The ACL permission type. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### DescribeConfigs API (Key: 32):

**Requests:**  

DescribeConfigs Request (Version: 0) => \[resources\] 
  resources => resource\_type resource\_name \[configuration_keys\] 
    resource_type => INT8
    resource_name => STRING
    configuration_keys => STRING

| Field | Description |
| --- | --- |
| resources | The resources whose configurations we want to describe. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configuration_keys | The configuration keys to list, or null to list all configuration keys. |

DescribeConfigs Request (Version: 1) => \[resources\] include_synonyms 
  resources => resource\_type resource\_name \[configuration_keys\] 
    resource_type => INT8
    resource_name => STRING
    configuration_keys => STRING
  include_synonyms => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The resources whose configurations we want to describe. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configuration_keys | The configuration keys to list, or null to list all configuration keys. |
| include_synonyms | True if we should include all synonyms. |

DescribeConfigs Request (Version: 2) => \[resources\] include_synonyms 
  resources => resource\_type resource\_name \[configuration_keys\] 
    resource_type => INT8
    resource_name => STRING
    configuration_keys => STRING
  include_synonyms => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The resources whose configurations we want to describe. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configuration_keys | The configuration keys to list, or null to list all configuration keys. |
| include_synonyms | True if we should include all synonyms. |

DescribeConfigs Request (Version: 3) => \[resources\] include\_synonyms include\_documentation 
  resources => resource\_type resource\_name \[configuration_keys\] 
    resource_type => INT8
    resource_name => STRING
    configuration_keys => STRING
  include_synonyms => BOOLEAN
  include_documentation => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The resources whose configurations we want to describe. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configuration_keys | The configuration keys to list, or null to list all configuration keys. |
| include_synonyms | True if we should include all synonyms. |
| include_documentation | True if we should include configuration documentation. |

DescribeConfigs Request (Version: 4) => \[resources\] include\_synonyms include\_documentation TAG_BUFFER 
  resources => resource\_type resource\_name \[configuration\_keys\] TAG\_BUFFER 
    resource_type => INT8
    resource\_name => COMPACT\_STRING
    configuration\_keys => COMPACT\_STRING
  include_synonyms => BOOLEAN
  include_documentation => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The resources whose configurations we want to describe. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configuration_keys | The configuration keys to list, or null to list all configuration keys. |
| \_tagged\_fields | The tagged fields |
| include_synonyms | True if we should include all synonyms. |
| include_documentation | True if we should include configuration documentation. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeConfigs Response (Version: 0) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => error\_code error\_message resource\_type resource\_name \[configs\] 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    resource_type => INT8
    resource_name => STRING
    configs => name value read\_only is\_default is_sensitive 
      name => STRING
      value => NULLABLE_STRING
      read_only => BOOLEAN
      is_default => BOOLEAN
      is_sensitive => BOOLEAN

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each resource. |
| error_code | The error code, or 0 if we were able to successfully describe the configurations. |
| error_message | The error message, or null if we were able to successfully describe the configurations. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | Each listed configuration. |
| name | The configuration name. |
| value | The configuration value. |
| read_only | True if the configuration is read-only. |
| is_default | True if the configuration is not set. |
| is_sensitive | True if this configuration is sensitive. |

DescribeConfigs Response (Version: 1) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => error\_code error\_message resource\_type resource\_name \[configs\] 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    resource_type => INT8
    resource_name => STRING
    configs => name value read\_only config\_source is_sensitive \[synonyms\] 
      name => STRING
      value => NULLABLE_STRING
      read_only => BOOLEAN
      config_source => INT8
      is_sensitive => BOOLEAN
      synonyms => name value source 
        name => STRING
        value => NULLABLE_STRING
        source => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each resource. |
| error_code | The error code, or 0 if we were able to successfully describe the configurations. |
| error_message | The error message, or null if we were able to successfully describe the configurations. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | Each listed configuration. |
| name | The configuration name. |
| value | The configuration value. |
| read_only | True if the configuration is read-only. |
| config_source | The configuration source. |
| is_sensitive | True if this configuration is sensitive. |
| synonyms | The synonyms for this configuration key. |
| name | The synonym name. |
| value | The synonym value. |
| source | The synonym source. |

DescribeConfigs Response (Version: 2) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => error\_code error\_message resource\_type resource\_name \[configs\] 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    resource_type => INT8
    resource_name => STRING
    configs => name value read\_only config\_source is_sensitive \[synonyms\] 
      name => STRING
      value => NULLABLE_STRING
      read_only => BOOLEAN
      config_source => INT8
      is_sensitive => BOOLEAN
      synonyms => name value source 
        name => STRING
        value => NULLABLE_STRING
        source => INT8

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each resource. |
| error_code | The error code, or 0 if we were able to successfully describe the configurations. |
| error_message | The error message, or null if we were able to successfully describe the configurations. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | Each listed configuration. |
| name | The configuration name. |
| value | The configuration value. |
| read_only | True if the configuration is read-only. |
| config_source | The configuration source. |
| is_sensitive | True if this configuration is sensitive. |
| synonyms | The synonyms for this configuration key. |
| name | The synonym name. |
| value | The synonym value. |
| source | The synonym source. |

DescribeConfigs Response (Version: 3) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => error\_code error\_message resource\_type resource\_name \[configs\] 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    resource_type => INT8
    resource_name => STRING
    configs => name value read\_only config\_source is\_sensitive \[synonyms\] config\_type documentation 
      name => STRING
      value => NULLABLE_STRING
      read_only => BOOLEAN
      config_source => INT8
      is_sensitive => BOOLEAN
      synonyms => name value source 
        name => STRING
        value => NULLABLE_STRING
        source => INT8
      config_type => INT8
      documentation => NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each resource. |
| error_code | The error code, or 0 if we were able to successfully describe the configurations. |
| error_message | The error message, or null if we were able to successfully describe the configurations. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | Each listed configuration. |
| name | The configuration name. |
| value | The configuration value. |
| read_only | True if the configuration is read-only. |
| config_source | The configuration source. |
| is_sensitive | True if this configuration is sensitive. |
| synonyms | The synonyms for this configuration key. |
| name | The synonym name. |
| value | The synonym value. |
| source | The synonym source. |
| config_type | The configuration data type. Type can be one of the following values - BOOLEAN, STRING, INT, SHORT, LONG, DOUBLE, LIST, CLASS, PASSWORD |
| documentation | The configuration documentation. |

DescribeConfigs Response (Version: 4) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => error\_code error\_message resource\_type resource\_name \[configs\] TAG_BUFFER 
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    resource_type => INT8
    resource\_name => COMPACT\_STRING
    configs => name value read\_only config\_source is\_sensitive \[synonyms\] config\_type documentation TAG_BUFFER 
      name => COMPACT_STRING
      value => COMPACT\_NULLABLE\_STRING
      read_only => BOOLEAN
      config_source => INT8
      is_sensitive => BOOLEAN
      synonyms => name value source TAG_BUFFER 
        name => COMPACT_STRING
        value => COMPACT\_NULLABLE\_STRING
        source => INT8
      config_type => INT8
      documentation => COMPACT\_NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each resource. |
| error_code | The error code, or 0 if we were able to successfully describe the configurations. |
| error_message | The error message, or null if we were able to successfully describe the configurations. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | Each listed configuration. |
| name | The configuration name. |
| value | The configuration value. |
| read_only | True if the configuration is read-only. |
| config_source | The configuration source. |
| is_sensitive | True if this configuration is sensitive. |
| synonyms | The synonyms for this configuration key. |
| name | The synonym name. |
| value | The synonym value. |
| source | The synonym source. |
| \_tagged\_fields | The tagged fields |
| config_type | The configuration data type. Type can be one of the following values - BOOLEAN, STRING, INT, SHORT, LONG, DOUBLE, LIST, CLASS, PASSWORD |
| documentation | The configuration documentation. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AlterConfigs API (Key: 33):

**Requests:**  

AlterConfigs Request (Version: 0) => \[resources\] validate_only 
  resources => resource\_type resource\_name \[configs\] 
    resource_type => INT8
    resource_name => STRING
    configs => name value 
      name => STRING
      value => NULLABLE_STRING
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The updates for each resource. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | The configurations. |
| name | The configuration key name. |
| value | The value to set for the configuration key. |
| validate_only | True if we should validate the request, but not change the configurations. |

AlterConfigs Request (Version: 1) => \[resources\] validate_only 
  resources => resource\_type resource\_name \[configs\] 
    resource_type => INT8
    resource_name => STRING
    configs => name value 
      name => STRING
      value => NULLABLE_STRING
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The updates for each resource. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | The configurations. |
| name | The configuration key name. |
| value | The value to set for the configuration key. |
| validate_only | True if we should validate the request, but not change the configurations. |

AlterConfigs Request (Version: 2) => \[resources\] validate\_only TAG\_BUFFER 
  resources => resource\_type resource\_name \[configs\] TAG_BUFFER 
    resource_type => INT8
    resource\_name => COMPACT\_STRING
    configs => name value TAG_BUFFER 
      name => COMPACT_STRING
      value => COMPACT\_NULLABLE\_STRING
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The updates for each resource. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | The configurations. |
| name | The configuration key name. |
| value | The value to set for the configuration key. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| validate_only | True if we should validate the request, but not change the configurations. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AlterConfigs Response (Version: 0) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => error\_code error\_message resource\_type resource\_name 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    resource_type => INT8
    resource_name => STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The responses for each resource. |
| error_code | The resource error code. |
| error_message | The resource error message, or null if there was no error. |
| resource_type | The resource type. |
| resource_name | The resource name. |

AlterConfigs Response (Version: 1) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => error\_code error\_message resource\_type resource\_name 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    resource_type => INT8
    resource_name => STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The responses for each resource. |
| error_code | The resource error code. |
| error_message | The resource error message, or null if there was no error. |
| resource_type | The resource type. |
| resource_name | The resource name. |

AlterConfigs Response (Version: 2) => throttle\_time\_ms \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  responses => error\_code error\_message resource\_type resource\_name TAG_BUFFER 
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    resource_type => INT8
    resource\_name => COMPACT\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The responses for each resource. |
| error_code | The resource error code. |
| error_message | The resource error message, or null if there was no error. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AlterReplicaLogDirs API (Key: 34):

**Requests:**  

AlterReplicaLogDirs Request (Version: 0) => \[dirs\] 
  dirs => path \[topics\] 
    path => STRING
    topics => name \[partitions\] 
      name => STRING
      partitions => INT32

| Field | Description |
| --- | --- |
| dirs | The alterations to make for each directory. |
| path | The absolute directory path. |
| topics | The topics to add to the directory. |
| name | The topic name. |
| partitions | The partition indexes. |

AlterReplicaLogDirs Request (Version: 1) => \[dirs\] 
  dirs => path \[topics\] 
    path => STRING
    topics => name \[partitions\] 
      name => STRING
      partitions => INT32

| Field | Description |
| --- | --- |
| dirs | The alterations to make for each directory. |
| path | The absolute directory path. |
| topics | The topics to add to the directory. |
| name | The topic name. |
| partitions | The partition indexes. |

AlterReplicaLogDirs Request (Version: 2) => \[dirs\] TAG_BUFFER 
  dirs => path \[topics\] TAG_BUFFER 
    path => COMPACT_STRING
    topics => name \[partitions\] TAG_BUFFER 
      name => COMPACT_STRING
      partitions => INT32

| Field | Description |
| --- | --- |
| dirs | The alterations to make for each directory. |
| path | The absolute directory path. |
| topics | The topics to add to the directory. |
| name | The topic name. |
| partitions | The partition indexes. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AlterReplicaLogDirs Response (Version: 0) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => topic_name \[partitions\] 
    topic_name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each topic. |
| topic_name | The name of the topic. |
| partitions | The results for each partition. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

AlterReplicaLogDirs Response (Version: 1) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => topic_name \[partitions\] 
    topic_name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each topic. |
| topic_name | The name of the topic. |
| partitions | The results for each partition. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

AlterReplicaLogDirs Response (Version: 2) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index error\_code TAG_BUFFER 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for each topic. |
| topic_name | The name of the topic. |
| partitions | The results for each partition. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### DescribeLogDirs API (Key: 35):

**Requests:**  

DescribeLogDirs Request (Version: 0) => \[topics\] 
  topics => topic \[partitions\] 
    topic => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| topics | Each topic that we want to describe log directories for, or null for all topics. |
| topic | The topic name |
| partitions | The partition indexes. |

DescribeLogDirs Request (Version: 1) => \[topics\] 
  topics => topic \[partitions\] 
    topic => STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| topics | Each topic that we want to describe log directories for, or null for all topics. |
| topic | The topic name |
| partitions | The partition indexes. |

DescribeLogDirs Request (Version: 2) => \[topics\] TAG_BUFFER 
  topics => topic \[partitions\] TAG_BUFFER 
    topic => COMPACT_STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| topics | Each topic that we want to describe log directories for, or null for all topics. |
| topic | The topic name |
| partitions | The partition indexes. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DescribeLogDirs Request (Version: 3) => \[topics\] TAG_BUFFER 
  topics => topic \[partitions\] TAG_BUFFER 
    topic => COMPACT_STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| topics | Each topic that we want to describe log directories for, or null for all topics. |
| topic | The topic name |
| partitions | The partition indexes. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DescribeLogDirs Request (Version: 4) => \[topics\] TAG_BUFFER 
  topics => topic \[partitions\] TAG_BUFFER 
    topic => COMPACT_STRING
    partitions => INT32

| Field | Description |
| --- | --- |
| topics | Each topic that we want to describe log directories for, or null for all topics. |
| topic | The topic name |
| partitions | The partition indexes. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeLogDirs Response (Version: 0) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => error\_code log\_dir \[topics\] 
    error_code => INT16
    log_dir => STRING
    topics => name \[partitions\] 
      name => STRING
      partitions => partition\_index partition\_size offset\_lag is\_future_key 
        partition_index => INT32
        partition_size => INT64
        offset_lag => INT64
        is\_future\_key => BOOLEAN

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The log directories. |
| error_code | The error code, or 0 if there was no error. |
| log_dir | The absolute log directory path. |
| topics | Each topic. |
| name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| partition_size | The size of the log segments in this partition in bytes. |
| offset_lag | The lag of the log's LEO w.r.t. partition's HW (if it is the current log for the partition) or current replica's LEO (if it is the future log for the partition) |
| is\_future\_key | True if this log is created by AlterReplicaLogDirsRequest and will replace the current log of the replica in the future. |

DescribeLogDirs Response (Version: 1) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => error\_code log\_dir \[topics\] 
    error_code => INT16
    log_dir => STRING
    topics => name \[partitions\] 
      name => STRING
      partitions => partition\_index partition\_size offset\_lag is\_future_key 
        partition_index => INT32
        partition_size => INT64
        offset_lag => INT64
        is\_future\_key => BOOLEAN

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The log directories. |
| error_code | The error code, or 0 if there was no error. |
| log_dir | The absolute log directory path. |
| topics | Each topic. |
| name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| partition_size | The size of the log segments in this partition in bytes. |
| offset_lag | The lag of the log's LEO w.r.t. partition's HW (if it is the current log for the partition) or current replica's LEO (if it is the future log for the partition) |
| is\_future\_key | True if this log is created by AlterReplicaLogDirsRequest and will replace the current log of the replica in the future. |

DescribeLogDirs Response (Version: 2) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => error\_code log\_dir \[topics\] TAG_BUFFER 
    error_code => INT16
    log\_dir => COMPACT\_STRING
    topics => name \[partitions\] TAG_BUFFER 
      name => COMPACT_STRING
      partitions => partition\_index partition\_size offset\_lag is\_future\_key TAG\_BUFFER 
        partition_index => INT32
        partition_size => INT64
        offset_lag => INT64
        is\_future\_key => BOOLEAN

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The log directories. |
| error_code | The error code, or 0 if there was no error. |
| log_dir | The absolute log directory path. |
| topics | Each topic. |
| name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| partition_size | The size of the log segments in this partition in bytes. |
| offset_lag | The lag of the log's LEO w.r.t. partition's HW (if it is the current log for the partition) or current replica's LEO (if it is the future log for the partition) |
| is\_future\_key | True if this log is created by AlterReplicaLogDirsRequest and will replace the current log of the replica in the future. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DescribeLogDirs Response (Version: 3) => throttle\_time\_ms error\_code \[results\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  results => error\_code log\_dir \[topics\] TAG_BUFFER 
    error_code => INT16
    log\_dir => COMPACT\_STRING
    topics => name \[partitions\] TAG_BUFFER 
      name => COMPACT_STRING
      partitions => partition\_index partition\_size offset\_lag is\_future\_key TAG\_BUFFER 
        partition_index => INT32
        partition_size => INT64
        offset_lag => INT64
        is\_future\_key => BOOLEAN

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| results | The log directories. |
| error_code | The error code, or 0 if there was no error. |
| log_dir | The absolute log directory path. |
| topics | Each topic. |
| name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| partition_size | The size of the log segments in this partition in bytes. |
| offset_lag | The lag of the log's LEO w.r.t. partition's HW (if it is the current log for the partition) or current replica's LEO (if it is the future log for the partition) |
| is\_future\_key | True if this log is created by AlterReplicaLogDirsRequest and will replace the current log of the replica in the future. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DescribeLogDirs Response (Version: 4) => throttle\_time\_ms error\_code \[results\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  results => error\_code log\_dir \[topics\] total\_bytes usable\_bytes TAG_BUFFER 
    error_code => INT16
    log\_dir => COMPACT\_STRING
    topics => name \[partitions\] TAG_BUFFER 
      name => COMPACT_STRING
      partitions => partition\_index partition\_size offset\_lag is\_future\_key TAG\_BUFFER 
        partition_index => INT32
        partition_size => INT64
        offset_lag => INT64
        is\_future\_key => BOOLEAN
    total_bytes => INT64
    usable_bytes => INT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| results | The log directories. |
| error_code | The error code, or 0 if there was no error. |
| log_dir | The absolute log directory path. |
| topics | Each topic. |
| name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| partition_size | The size of the log segments in this partition in bytes. |
| offset_lag | The lag of the log's LEO w.r.t. partition's HW (if it is the current log for the partition) or current replica's LEO (if it is the future log for the partition) |
| is\_future\_key | True if this log is created by AlterReplicaLogDirsRequest and will replace the current log of the replica in the future. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| total_bytes | The total size in bytes of the volume the log directory is in. |
| usable_bytes | The usable size in bytes of the volume the log directory is in. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### SaslAuthenticate API (Key: 36):

**Requests:**  

SaslAuthenticate Request (Version: 0) => auth_bytes 
  auth_bytes => BYTES

| Field | Description |
| --- | --- |
| auth_bytes | The SASL authentication bytes from the client, as defined by the SASL mechanism. |

SaslAuthenticate Request (Version: 1) => auth_bytes 
  auth_bytes => BYTES

| Field | Description |
| --- | --- |
| auth_bytes | The SASL authentication bytes from the client, as defined by the SASL mechanism. |

SaslAuthenticate Request (Version: 2) => auth\_bytes TAG\_BUFFER 
  auth\_bytes => COMPACT\_BYTES

| Field | Description |
| --- | --- |
| auth_bytes | The SASL authentication bytes from the client, as defined by the SASL mechanism. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

SaslAuthenticate Response (Version: 0) => error\_code error\_message auth_bytes 
  error_code => INT16
  error\_message => NULLABLE\_STRING
  auth_bytes => BYTES

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| auth_bytes | The SASL authentication bytes from the server, as defined by the SASL mechanism. |

SaslAuthenticate Response (Version: 1) => error\_code error\_message auth\_bytes session\_lifetime_ms 
  error_code => INT16
  error\_message => NULLABLE\_STRING
  auth_bytes => BYTES
  session\_lifetime\_ms => INT64

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| auth_bytes | The SASL authentication bytes from the server, as defined by the SASL mechanism. |
| session\_lifetime\_ms | Number of milliseconds after which only re-authentication over the existing connection to create a new session can occur. |

SaslAuthenticate Response (Version: 2) => error\_code error\_message auth\_bytes session\_lifetime\_ms TAG\_BUFFER 
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  auth\_bytes => COMPACT\_BYTES
  session\_lifetime\_ms => INT64

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| error_message | The error message, or null if there was no error. |
| auth_bytes | The SASL authentication bytes from the server, as defined by the SASL mechanism. |
| session\_lifetime\_ms | Number of milliseconds after which only re-authentication over the existing connection to create a new session can occur. |
| \_tagged\_fields | The tagged fields |

##### CreatePartitions API (Key: 37):

**Requests:**  

CreatePartitions Request (Version: 0) => \[topics\] timeout\_ms validate\_only 
  topics => name count \[assignments\] 
    name => STRING
    count => INT32
    assignments => \[broker_ids\] 
      broker_ids => INT32
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | Each topic that we want to create new partitions inside. |
| name | The topic name. |
| count | The new partition count. |
| assignments | The new partition assignments. |
| broker_ids | The assigned broker IDs. |
| timeout_ms | The time in ms to wait for the partitions to be created. |
| validate_only | If true, then validate the request, but don't actually increase the number of partitions. |

CreatePartitions Request (Version: 1) => \[topics\] timeout\_ms validate\_only 
  topics => name count \[assignments\] 
    name => STRING
    count => INT32
    assignments => \[broker_ids\] 
      broker_ids => INT32
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | Each topic that we want to create new partitions inside. |
| name | The topic name. |
| count | The new partition count. |
| assignments | The new partition assignments. |
| broker_ids | The assigned broker IDs. |
| timeout_ms | The time in ms to wait for the partitions to be created. |
| validate_only | If true, then validate the request, but don't actually increase the number of partitions. |

CreatePartitions Request (Version: 2) => \[topics\] timeout\_ms validate\_only TAG_BUFFER 
  topics => name count \[assignments\] TAG_BUFFER 
    name => COMPACT_STRING
    count => INT32
    assignments => \[broker\_ids\] TAG\_BUFFER 
      broker_ids => INT32
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | Each topic that we want to create new partitions inside. |
| name | The topic name. |
| count | The new partition count. |
| assignments | The new partition assignments. |
| broker_ids | The assigned broker IDs. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| timeout_ms | The time in ms to wait for the partitions to be created. |
| validate_only | If true, then validate the request, but don't actually increase the number of partitions. |
| \_tagged\_fields | The tagged fields |

CreatePartitions Request (Version: 3) => \[topics\] timeout\_ms validate\_only TAG_BUFFER 
  topics => name count \[assignments\] TAG_BUFFER 
    name => COMPACT_STRING
    count => INT32
    assignments => \[broker\_ids\] TAG\_BUFFER 
      broker_ids => INT32
  timeout_ms => INT32
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| topics | Each topic that we want to create new partitions inside. |
| name | The topic name. |
| count | The new partition count. |
| assignments | The new partition assignments. |
| broker_ids | The assigned broker IDs. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| timeout_ms | The time in ms to wait for the partitions to be created. |
| validate_only | If true, then validate the request, but don't actually increase the number of partitions. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

CreatePartitions Response (Version: 0) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => name error\_code error\_message 
    name => STRING
    error_code => INT16
    error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The partition creation results for each topic. |
| name | The topic name. |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |

CreatePartitions Response (Version: 1) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => name error\_code error\_message 
    name => STRING
    error_code => INT16
    error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The partition creation results for each topic. |
| name | The topic name. |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |

CreatePartitions Response (Version: 2) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => name error\_code error\_message TAG_BUFFER 
    name => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The partition creation results for each topic. |
| name | The topic name. |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

CreatePartitions Response (Version: 3) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => name error\_code error\_message TAG_BUFFER 
    name => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The partition creation results for each topic. |
| name | The topic name. |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### CreateDelegationToken API (Key: 38):

**Requests:**  

CreateDelegationToken Request (Version: 0) => \[renewers\] max\_lifetime\_ms 
  renewers => principal\_type principal\_name 
    principal_type => STRING
    principal_name => STRING
  max\_lifetime\_ms => INT64

| Field | Description |
| --- | --- |
| renewers | A list of those who are allowed to renew this token before it expires. |
| principal_type | The type of the Kafka principal. |
| principal_name | The name of the Kafka principal. |
| max\_lifetime\_ms | The maximum lifetime of the token in milliseconds, or -1 to use the server side default. |

CreateDelegationToken Request (Version: 1) => \[renewers\] max\_lifetime\_ms 
  renewers => principal\_type principal\_name 
    principal_type => STRING
    principal_name => STRING
  max\_lifetime\_ms => INT64

| Field | Description |
| --- | --- |
| renewers | A list of those who are allowed to renew this token before it expires. |
| principal_type | The type of the Kafka principal. |
| principal_name | The name of the Kafka principal. |
| max\_lifetime\_ms | The maximum lifetime of the token in milliseconds, or -1 to use the server side default. |

CreateDelegationToken Request (Version: 2) => \[renewers\] max\_lifetime\_ms TAG_BUFFER 
  renewers => principal\_type principal\_name TAG_BUFFER 
    principal\_type => COMPACT\_STRING
    principal\_name => COMPACT\_STRING
  max\_lifetime\_ms => INT64

| Field | Description |
| --- | --- |
| renewers | A list of those who are allowed to renew this token before it expires. |
| principal_type | The type of the Kafka principal. |
| principal_name | The name of the Kafka principal. |
| \_tagged\_fields | The tagged fields |
| max\_lifetime\_ms | The maximum lifetime of the token in milliseconds, or -1 to use the server side default. |
| \_tagged\_fields | The tagged fields |

CreateDelegationToken Request (Version: 3) => owner\_principal\_type owner\_principal\_name \[renewers\] max\_lifetime\_ms TAG_BUFFER 
  owner\_principal\_type => COMPACT\_NULLABLE\_STRING
  owner\_principal\_name => COMPACT\_NULLABLE\_STRING
  renewers => principal\_type principal\_name TAG_BUFFER 
    principal\_type => COMPACT\_STRING
    principal\_name => COMPACT\_STRING
  max\_lifetime\_ms => INT64

| Field | Description |
| --- | --- |
| owner\_principal\_type | The principal type of the owner of the token. If it's null it defaults to the token request principal. |
| owner\_principal\_name | The principal name of the owner of the token. If it's null it defaults to the token request principal. |
| renewers | A list of those who are allowed to renew this token before it expires. |
| principal_type | The type of the Kafka principal. |
| principal_name | The name of the Kafka principal. |
| \_tagged\_fields | The tagged fields |
| max\_lifetime\_ms | The maximum lifetime of the token in milliseconds, or -1 to use the server side default. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

CreateDelegationToken Response (Version: 0) => error\_code principal\_type principal\_name issue\_timestamp\_ms expiry\_timestamp\_ms max\_timestamp\_ms token\_id hmac throttle\_time\_ms 
  error_code => INT16
  principal_type => STRING
  principal_name => STRING
  issue\_timestamp\_ms => INT64
  expiry\_timestamp\_ms => INT64
  max\_timestamp\_ms => INT64
  token_id => STRING
  hmac => BYTES
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error, or zero if there was no error. |
| principal_type | The principal type of the token owner. |
| principal_name | The name of the token owner. |
| issue\_timestamp\_ms | When this token was generated. |
| expiry\_timestamp\_ms | When this token expires. |
| max\_timestamp\_ms | The maximum lifetime of this token. |
| token_id | The token UUID. |
| hmac | HMAC of the delegation token. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

CreateDelegationToken Response (Version: 1) => error\_code principal\_type principal\_name issue\_timestamp\_ms expiry\_timestamp\_ms max\_timestamp\_ms token\_id hmac throttle\_time\_ms 
  error_code => INT16
  principal_type => STRING
  principal_name => STRING
  issue\_timestamp\_ms => INT64
  expiry\_timestamp\_ms => INT64
  max\_timestamp\_ms => INT64
  token_id => STRING
  hmac => BYTES
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error, or zero if there was no error. |
| principal_type | The principal type of the token owner. |
| principal_name | The name of the token owner. |
| issue\_timestamp\_ms | When this token was generated. |
| expiry\_timestamp\_ms | When this token expires. |
| max\_timestamp\_ms | The maximum lifetime of this token. |
| token_id | The token UUID. |
| hmac | HMAC of the delegation token. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

CreateDelegationToken Response (Version: 2) => error\_code principal\_type principal\_name issue\_timestamp\_ms expiry\_timestamp\_ms max\_timestamp\_ms token\_id hmac throttle\_time\_ms TAG_BUFFER 
  error_code => INT16
  principal\_type => COMPACT\_STRING
  principal\_name => COMPACT\_STRING
  issue\_timestamp\_ms => INT64
  expiry\_timestamp\_ms => INT64
  max\_timestamp\_ms => INT64
  token\_id => COMPACT\_STRING
  hmac => COMPACT_BYTES
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error, or zero if there was no error. |
| principal_type | The principal type of the token owner. |
| principal_name | The name of the token owner. |
| issue\_timestamp\_ms | When this token was generated. |
| expiry\_timestamp\_ms | When this token expires. |
| max\_timestamp\_ms | The maximum lifetime of this token. |
| token_id | The token UUID. |
| hmac | HMAC of the delegation token. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| \_tagged\_fields | The tagged fields |

CreateDelegationToken Response (Version: 3) => error\_code principal\_type principal\_name token\_requester\_principal\_type token\_requester\_principal\_name issue\_timestamp\_ms expiry\_timestamp\_ms max\_timestamp\_ms token\_id hmac throttle\_time\_ms TAG_BUFFER 
  error_code => INT16
  principal\_type => COMPACT\_STRING
  principal\_name => COMPACT\_STRING
  token\_requester\_principal\_type => COMPACT\_STRING
  token\_requester\_principal\_name => COMPACT\_STRING
  issue\_timestamp\_ms => INT64
  expiry\_timestamp\_ms => INT64
  max\_timestamp\_ms => INT64
  token\_id => COMPACT\_STRING
  hmac => COMPACT_BYTES
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The top-level error, or zero if there was no error. |
| principal_type | The principal type of the token owner. |
| principal_name | The name of the token owner. |
| token\_requester\_principal_type | The principal type of the requester of the token. |
| token\_requester\_principal_name | The principal type of the requester of the token. |
| issue\_timestamp\_ms | When this token was generated. |
| expiry\_timestamp\_ms | When this token expires. |
| max\_timestamp\_ms | The maximum lifetime of this token. |
| token_id | The token UUID. |
| hmac | HMAC of the delegation token. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| \_tagged\_fields | The tagged fields |

##### RenewDelegationToken API (Key: 39):

**Requests:**  

RenewDelegationToken Request (Version: 0) => hmac renew\_period\_ms 
  hmac => BYTES
  renew\_period\_ms => INT64

| Field | Description |
| --- | --- |
| hmac | The HMAC of the delegation token to be renewed. |
| renew\_period\_ms | The renewal time period in milliseconds. |

RenewDelegationToken Request (Version: 1) => hmac renew\_period\_ms 
  hmac => BYTES
  renew\_period\_ms => INT64

| Field | Description |
| --- | --- |
| hmac | The HMAC of the delegation token to be renewed. |
| renew\_period\_ms | The renewal time period in milliseconds. |

RenewDelegationToken Request (Version: 2) => hmac renew\_period\_ms TAG_BUFFER 
  hmac => COMPACT_BYTES
  renew\_period\_ms => INT64

| Field | Description |
| --- | --- |
| hmac | The HMAC of the delegation token to be renewed. |
| renew\_period\_ms | The renewal time period in milliseconds. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

RenewDelegationToken Response (Version: 0) => error\_code expiry\_timestamp\_ms throttle\_time_ms 
  error_code => INT16
  expiry\_timestamp\_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| expiry\_timestamp\_ms | The timestamp in milliseconds at which this token expires. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

RenewDelegationToken Response (Version: 1) => error\_code expiry\_timestamp\_ms throttle\_time_ms 
  error_code => INT16
  expiry\_timestamp\_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| expiry\_timestamp\_ms | The timestamp in milliseconds at which this token expires. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

RenewDelegationToken Response (Version: 2) => error\_code expiry\_timestamp\_ms throttle\_time\_ms TAG\_BUFFER 
  error_code => INT16
  expiry\_timestamp\_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| expiry\_timestamp\_ms | The timestamp in milliseconds at which this token expires. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| \_tagged\_fields | The tagged fields |

##### ExpireDelegationToken API (Key: 40):

**Requests:**  

ExpireDelegationToken Request (Version: 0) => hmac expiry\_time\_period_ms 
  hmac => BYTES
  expiry\_time\_period_ms => INT64

| Field | Description |
| --- | --- |
| hmac | The HMAC of the delegation token to be expired. |
| expiry\_time\_period_ms | The expiry time period in milliseconds. |

ExpireDelegationToken Request (Version: 1) => hmac expiry\_time\_period_ms 
  hmac => BYTES
  expiry\_time\_period_ms => INT64

| Field | Description |
| --- | --- |
| hmac | The HMAC of the delegation token to be expired. |
| expiry\_time\_period_ms | The expiry time period in milliseconds. |

ExpireDelegationToken Request (Version: 2) => hmac expiry\_time\_period\_ms TAG\_BUFFER 
  hmac => COMPACT_BYTES
  expiry\_time\_period_ms => INT64

| Field | Description |
| --- | --- |
| hmac | The HMAC of the delegation token to be expired. |
| expiry\_time\_period_ms | The expiry time period in milliseconds. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ExpireDelegationToken Response (Version: 0) => error\_code expiry\_timestamp\_ms throttle\_time_ms 
  error_code => INT16
  expiry\_timestamp\_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| expiry\_timestamp\_ms | The timestamp in milliseconds at which this token expires. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

ExpireDelegationToken Response (Version: 1) => error\_code expiry\_timestamp\_ms throttle\_time_ms 
  error_code => INT16
  expiry\_timestamp\_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| expiry\_timestamp\_ms | The timestamp in milliseconds at which this token expires. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

ExpireDelegationToken Response (Version: 2) => error\_code expiry\_timestamp\_ms throttle\_time\_ms TAG\_BUFFER 
  error_code => INT16
  expiry\_timestamp\_ms => INT64
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| expiry\_timestamp\_ms | The timestamp in milliseconds at which this token expires. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| \_tagged\_fields | The tagged fields |

##### DescribeDelegationToken API (Key: 41):

**Requests:**  

DescribeDelegationToken Request (Version: 0) => \[owners\] 
  owners => principal\_type principal\_name 
    principal_type => STRING
    principal_name => STRING

| Field | Description |
| --- | --- |
| owners | Each owner that we want to describe delegation tokens for, or null to describe all tokens. |
| principal_type | The owner principal type. |
| principal_name | The owner principal name. |

DescribeDelegationToken Request (Version: 1) => \[owners\] 
  owners => principal\_type principal\_name 
    principal_type => STRING
    principal_name => STRING

| Field | Description |
| --- | --- |
| owners | Each owner that we want to describe delegation tokens for, or null to describe all tokens. |
| principal_type | The owner principal type. |
| principal_name | The owner principal name. |

DescribeDelegationToken Request (Version: 2) => \[owners\] TAG_BUFFER 
  owners => principal\_type principal\_name TAG_BUFFER 
    principal\_type => COMPACT\_STRING
    principal\_name => COMPACT\_STRING

| Field | Description |
| --- | --- |
| owners | Each owner that we want to describe delegation tokens for, or null to describe all tokens. |
| principal_type | The owner principal type. |
| principal_name | The owner principal name. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DescribeDelegationToken Request (Version: 3) => \[owners\] TAG_BUFFER 
  owners => principal\_type principal\_name TAG_BUFFER 
    principal\_type => COMPACT\_STRING
    principal\_name => COMPACT\_STRING

| Field | Description |
| --- | --- |
| owners | Each owner that we want to describe delegation tokens for, or null to describe all tokens. |
| principal_type | The owner principal type. |
| principal_name | The owner principal name. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeDelegationToken Response (Version: 0) => error\_code \[tokens\] throttle\_time_ms 
  error_code => INT16
  tokens => principal\_type principal\_name issue\_timestamp expiry\_timestamp max\_timestamp token\_id hmac \[renewers\] 
    principal_type => STRING
    principal_name => STRING
    issue_timestamp => INT64
    expiry_timestamp => INT64
    max_timestamp => INT64
    token_id => STRING
    hmac => BYTES
    renewers => principal\_type principal\_name 
      principal_type => STRING
      principal_name => STRING
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| tokens | The tokens. |
| principal_type | The token principal type. |
| principal_name | The token principal name. |
| issue_timestamp | The token issue timestamp in milliseconds. |
| expiry_timestamp | The token expiry timestamp in milliseconds. |
| max_timestamp | The token maximum timestamp length in milliseconds. |
| token_id | The token ID. |
| hmac | The token HMAC. |
| renewers | Those who are able to renew this token before it expires. |
| principal_type | The renewer principal type |
| principal_name | The renewer principal name |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

DescribeDelegationToken Response (Version: 1) => error\_code \[tokens\] throttle\_time_ms 
  error_code => INT16
  tokens => principal\_type principal\_name issue\_timestamp expiry\_timestamp max\_timestamp token\_id hmac \[renewers\] 
    principal_type => STRING
    principal_name => STRING
    issue_timestamp => INT64
    expiry_timestamp => INT64
    max_timestamp => INT64
    token_id => STRING
    hmac => BYTES
    renewers => principal\_type principal\_name 
      principal_type => STRING
      principal_name => STRING
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| tokens | The tokens. |
| principal_type | The token principal type. |
| principal_name | The token principal name. |
| issue_timestamp | The token issue timestamp in milliseconds. |
| expiry_timestamp | The token expiry timestamp in milliseconds. |
| max_timestamp | The token maximum timestamp length in milliseconds. |
| token_id | The token ID. |
| hmac | The token HMAC. |
| renewers | Those who are able to renew this token before it expires. |
| principal_type | The renewer principal type |
| principal_name | The renewer principal name |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |

DescribeDelegationToken Response (Version: 2) => error\_code \[tokens\] throttle\_time\_ms TAG\_BUFFER 
  error_code => INT16
  tokens => principal\_type principal\_name issue\_timestamp expiry\_timestamp max\_timestamp token\_id hmac \[renewers\] TAG_BUFFER 
    principal\_type => COMPACT\_STRING
    principal\_name => COMPACT\_STRING
    issue_timestamp => INT64
    expiry_timestamp => INT64
    max_timestamp => INT64
    token\_id => COMPACT\_STRING
    hmac => COMPACT_BYTES
    renewers => principal\_type principal\_name TAG_BUFFER 
      principal\_type => COMPACT\_STRING
      principal\_name => COMPACT\_STRING
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| tokens | The tokens. |
| principal_type | The token principal type. |
| principal_name | The token principal name. |
| issue_timestamp | The token issue timestamp in milliseconds. |
| expiry_timestamp | The token expiry timestamp in milliseconds. |
| max_timestamp | The token maximum timestamp length in milliseconds. |
| token_id | The token ID. |
| hmac | The token HMAC. |
| renewers | Those who are able to renew this token before it expires. |
| principal_type | The renewer principal type |
| principal_name | The renewer principal name |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| \_tagged\_fields | The tagged fields |

DescribeDelegationToken Response (Version: 3) => error\_code \[tokens\] throttle\_time\_ms TAG\_BUFFER 
  error_code => INT16
  tokens => principal\_type principal\_name token\_requester\_principal\_type token\_requester\_principal\_name issue\_timestamp expiry\_timestamp max\_timestamp token\_id hmac \[renewers\] TAG_BUFFER 
    principal\_type => COMPACT\_STRING
    principal\_name => COMPACT\_STRING
    token\_requester\_principal\_type => COMPACT\_STRING
    token\_requester\_principal\_name => COMPACT\_STRING
    issue_timestamp => INT64
    expiry_timestamp => INT64
    max_timestamp => INT64
    token\_id => COMPACT\_STRING
    hmac => COMPACT_BYTES
    renewers => principal\_type principal\_name TAG_BUFFER 
      principal\_type => COMPACT\_STRING
      principal\_name => COMPACT\_STRING
  throttle\_time\_ms => INT32

| Field | Description |
| --- | --- |
| error_code | The error code, or 0 if there was no error. |
| tokens | The tokens. |
| principal_type | The token principal type. |
| principal_name | The token principal name. |
| token\_requester\_principal_type | The principal type of the requester of the token. |
| token\_requester\_principal_name | The principal type of the requester of the token. |
| issue_timestamp | The token issue timestamp in milliseconds. |
| expiry_timestamp | The token expiry timestamp in milliseconds. |
| max_timestamp | The token maximum timestamp length in milliseconds. |
| token_id | The token ID. |
| hmac | The token HMAC. |
| renewers | Those who are able to renew this token before it expires. |
| principal_type | The renewer principal type |
| principal_name | The renewer principal name |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| \_tagged\_fields | The tagged fields |

##### DeleteGroups API (Key: 42):

**Requests:**  

DeleteGroups Request (Version: 0) => \[groups_names\] 
  groups_names => STRING

| Field | Description |
| --- | --- |
| groups_names | The group names to delete. |

DeleteGroups Request (Version: 1) => \[groups_names\] 
  groups_names => STRING

| Field | Description |
| --- | --- |
| groups_names | The group names to delete. |

DeleteGroups Request (Version: 2) => \[groups\_names\] TAG\_BUFFER 
  groups\_names => COMPACT\_STRING

| Field | Description |
| --- | --- |
| groups_names | The group names to delete. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DeleteGroups Response (Version: 0) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => group\_id error\_code 
    group_id => STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The deletion results |
| group_id | The group id |
| error_code | The deletion error, or 0 if the deletion succeeded. |

DeleteGroups Response (Version: 1) => throttle\_time\_ms \[results\] 
  throttle\_time\_ms => INT32
  results => group\_id error\_code 
    group_id => STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The deletion results |
| group_id | The group id |
| error_code | The deletion error, or 0 if the deletion succeeded. |

DeleteGroups Response (Version: 2) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => group\_id error\_code TAG_BUFFER 
    group\_id => COMPACT\_STRING
    error_code => INT16

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The deletion results |
| group_id | The group id |
| error_code | The deletion error, or 0 if the deletion succeeded. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### ElectLeaders API (Key: 43):

**Requests:**  

ElectLeaders Request (Version: 0) => \[topic\_partitions\] timeout\_ms 
  topic_partitions => topic \[partitions\] 
    topic => STRING
    partitions => INT32
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| topic_partitions | The topic partitions to elect leaders. |
| topic | The name of a topic. |
| partitions | The partitions of this topic whose leader should be elected. |
| timeout_ms | The time in ms to wait for the election to complete. |

ElectLeaders Request (Version: 1) => election\_type \[topic\_partitions\] timeout_ms 
  election_type => INT8
  topic_partitions => topic \[partitions\] 
    topic => STRING
    partitions => INT32
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| election_type | Type of elections to conduct for the partition. A value of '0' elects the preferred replica. A value of '1' elects the first live replica if there are no in-sync replica. |
| topic_partitions | The topic partitions to elect leaders. |
| topic | The name of a topic. |
| partitions | The partitions of this topic whose leader should be elected. |
| timeout_ms | The time in ms to wait for the election to complete. |

ElectLeaders Request (Version: 2) => election\_type \[topic\_partitions\] timeout\_ms TAG\_BUFFER 
  election_type => INT8
  topic\_partitions => topic \[partitions\] TAG\_BUFFER 
    topic => COMPACT_STRING
    partitions => INT32
  timeout_ms => INT32

| Field | Description |
| --- | --- |
| election_type | Type of elections to conduct for the partition. A value of '0' elects the preferred replica. A value of '1' elects the first live replica if there are no in-sync replica. |
| topic_partitions | The topic partitions to elect leaders. |
| topic | The name of a topic. |
| partitions | The partitions of this topic whose leader should be elected. |
| \_tagged\_fields | The tagged fields |
| timeout_ms | The time in ms to wait for the election to complete. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ElectLeaders Response (Version: 0) => throttle\_time\_ms \[replica\_election\_results\] 
  throttle\_time\_ms => INT32
  replica\_election\_results => topic \[partition_result\] 
    topic => STRING
    partition\_result => partition\_id error\_code error\_message 
      partition_id => INT32
      error_code => INT16
      error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| replica\_election\_results | The election results, or an empty array if the requester did not have permission and the request asks for all partitions. |
| topic | The topic name |
| partition_result | The results for each partition |
| partition_id | The partition id |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |

ElectLeaders Response (Version: 1) => throttle\_time\_ms error\_code \[replica\_election_results\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  replica\_election\_results => topic \[partition_result\] 
    topic => STRING
    partition\_result => partition\_id error\_code error\_message 
      partition_id => INT32
      error_code => INT16
      error\_message => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| replica\_election\_results | The election results, or an empty array if the requester did not have permission and the request asks for all partitions. |
| topic | The topic name |
| partition_result | The results for each partition |
| partition_id | The partition id |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |

ElectLeaders Response (Version: 2) => throttle\_time\_ms error\_code \[replica\_election\_results\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  replica\_election\_results => topic \[partition\_result\] TAG\_BUFFER 
    topic => COMPACT_STRING
    partition\_result => partition\_id error\_code error\_message TAG_BUFFER 
      partition_id => INT32
      error_code => INT16
      error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code. |
| replica\_election\_results | The election results, or an empty array if the requester did not have permission and the request asks for all partitions. |
| topic | The topic name |
| partition_result | The results for each partition |
| partition_id | The partition id |
| error_code | The result error, or zero if there was no error. |
| error_message | The result message, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### IncrementalAlterConfigs API (Key: 44):

**Requests:**  

IncrementalAlterConfigs Request (Version: 0) => \[resources\] validate_only 
  resources => resource\_type resource\_name \[configs\] 
    resource_type => INT8
    resource_name => STRING
    configs => name config_operation value 
      name => STRING
      config_operation => INT8
      value => NULLABLE_STRING
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The incremental updates for each resource. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | The configurations. |
| name | The configuration key name. |
| config_operation | The type (Set, Delete, Append, Subtract) of operation. |
| value | The value to set for the configuration key. |
| validate_only | True if we should validate the request, but not change the configurations. |

IncrementalAlterConfigs Request (Version: 1) => \[resources\] validate\_only TAG\_BUFFER 
  resources => resource\_type resource\_name \[configs\] TAG_BUFFER 
    resource_type => INT8
    resource\_name => COMPACT\_STRING
    configs => name config\_operation value TAG\_BUFFER 
      name => COMPACT_STRING
      config_operation => INT8
      value => COMPACT\_NULLABLE\_STRING
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| resources | The incremental updates for each resource. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| configs | The configurations. |
| name | The configuration key name. |
| config_operation | The type (Set, Delete, Append, Subtract) of operation. |
| value | The value to set for the configuration key. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| validate_only | True if we should validate the request, but not change the configurations. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

IncrementalAlterConfigs Response (Version: 0) => throttle\_time\_ms \[responses\] 
  throttle\_time\_ms => INT32
  responses => error\_code error\_message resource\_type resource\_name 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    resource_type => INT8
    resource_name => STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The responses for each resource. |
| error_code | The resource error code. |
| error_message | The resource error message, or null if there was no error. |
| resource_type | The resource type. |
| resource_name | The resource name. |

IncrementalAlterConfigs Response (Version: 1) => throttle\_time\_ms \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  responses => error\_code error\_message resource\_type resource\_name TAG_BUFFER 
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    resource_type => INT8
    resource\_name => COMPACT\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| responses | The responses for each resource. |
| error_code | The resource error code. |
| error_message | The resource error message, or null if there was no error. |
| resource_type | The resource type. |
| resource_name | The resource name. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AlterPartitionReassignments API (Key: 45):

**Requests:**  

AlterPartitionReassignments Request (Version: 0) => timeout\_ms \[topics\] TAG\_BUFFER 
  timeout_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index \[replicas\] TAG\_BUFFER 
      partition_index => INT32
      replicas => INT32

| Field | Description |
| --- | --- |
| timeout_ms | The time in ms to wait for the request to complete. |
| topics | The topics to reassign. |
| name | The topic name. |
| partitions | The partitions to reassign. |
| partition_index | The partition index. |
| replicas | The replicas to place the partitions on, or null to cancel a pending reassignment for this partition. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AlterPartitionReassignments Response (Version: 0) => throttle\_time\_ms error\_code error\_message \[responses\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  responses => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index error\_code error\_message TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16
      error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top-level error code, or 0 if there was no error. |
| error_message | The top-level error message, or null if there was no error. |
| responses | The responses to topics to reassign. |
| name | The topic name |
| partitions | The responses to partitions to reassign |
| partition_index | The partition index. |
| error_code | The error code for this partition, or 0 if there was no error. |
| error_message | The error message for this partition, or null if there was no error. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### ListPartitionReassignments API (Key: 46):

**Requests:**  

ListPartitionReassignments Request (Version: 0) => timeout\_ms \[topics\] TAG\_BUFFER 
  timeout_ms => INT32
  topics => name \[partition\_indexes\] TAG\_BUFFER 
    name => COMPACT_STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| timeout_ms | The time in ms to wait for the request to complete. |
| topics | The topics to list partition reassignments for, or null to list everything. |
| name | The topic name |
| partition_indexes | The partitions to list partition reassignments for. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ListPartitionReassignments Response (Version: 0) => throttle\_time\_ms error\_code error\_message \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index \[replicas\] \[adding\_replicas\] \[removing\_replicas\] TAG\_BUFFER 
      partition_index => INT32
      replicas => INT32
      adding_replicas => INT32
      removing_replicas => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top-level error code, or 0 if there was no error |
| error_message | The top-level error message, or null if there was no error. |
| topics | The ongoing reassignments for each topic. |
| name | The topic name. |
| partitions | The ongoing reassignments for each partition. |
| partition_index | The index of the partition. |
| replicas | The current replica set. |
| adding_replicas | The set of replicas we are currently adding. |
| removing_replicas | The set of replicas we are currently removing. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### OffsetDelete API (Key: 47):

**Requests:**  

OffsetDelete Request (Version: 0) => group_id \[topics\] 
  group_id => STRING
  topics => name \[partitions\] 
    name => STRING
    partitions => partition_index 
      partition_index => INT32

| Field | Description |
| --- | --- |
| group_id | The unique group identifier. |
| topics | The topics to delete offsets for |
| name | The topic name. |
| partitions | Each partition to delete offsets for. |
| partition_index | The partition index. |

**Responses:**  

OffsetDelete Response (Version: 0) => error\_code throttle\_time_ms \[topics\] 
  error_code => INT16
  throttle\_time\_ms => INT32
  topics => name \[partitions\] 
    name => STRING
    partitions => partition\_index error\_code 
      partition_index => INT32
      error_code => INT16

| Field | Description |
| --- | --- |
| error_code | The top-level error code, or 0 if there was no error. |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | The responses for each topic. |
| name | The topic name. |
| partitions | The responses for each partition in the topic. |
| partition_index | The partition index. |
| error_code | The error code, or 0 if there was no error. |

##### DescribeClientQuotas API (Key: 48):

**Requests:**  

DescribeClientQuotas Request (Version: 0) => \[components\] strict 
  components => entity\_type match\_type match 
    entity_type => STRING
    match_type => INT8
    match => NULLABLE_STRING
  strict => BOOLEAN

| Field | Description |
| --- | --- |
| components | Filter components to apply to quota entities. |
| entity_type | The entity type that the filter component applies to. |
| match_type | How to match the entity {0 = exact name, 1 = default name, 2 = any specified name}. |
| match | The string to match against, or null if unused for the match type. |
| strict | Whether the match is strict, i.e. should exclude entities with unspecified entity types. |

DescribeClientQuotas Request (Version: 1) => \[components\] strict TAG_BUFFER 
  components => entity\_type match\_type match TAG_BUFFER 
    entity\_type => COMPACT\_STRING
    match_type => INT8
    match => COMPACT\_NULLABLE\_STRING
  strict => BOOLEAN

| Field | Description |
| --- | --- |
| components | Filter components to apply to quota entities. |
| entity_type | The entity type that the filter component applies to. |
| match_type | How to match the entity {0 = exact name, 1 = default name, 2 = any specified name}. |
| match | The string to match against, or null if unused for the match type. |
| \_tagged\_fields | The tagged fields |
| strict | Whether the match is strict, i.e. should exclude entities with unspecified entity types. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeClientQuotas Response (Version: 0) => throttle\_time\_ms error\_code error\_message \[entries\] 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => NULLABLE\_STRING
  entries => \[entity\] \[values\] 
    entity => entity\_type entity\_name 
      entity_type => STRING
      entity\_name => NULLABLE\_STRING
    values => key value 
      key => STRING
      value => FLOAT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or \`0\` if the quota description succeeded. |
| error_message | The error message, or \`null\` if the quota description succeeded. |
| entries | A result entry. |
| entity | The quota entity description. |
| entity_type | The entity type. |
| entity_name | The entity name, or null if the default. |
| values | The quota values for the entity. |
| key | The quota configuration key. |
| value | The quota configuration value. |

DescribeClientQuotas Response (Version: 1) => throttle\_time\_ms error\_code error\_message \[entries\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  entries => \[entity\] \[values\] TAG_BUFFER 
    entity => entity\_type entity\_name TAG_BUFFER 
      entity\_type => COMPACT\_STRING
      entity\_name => COMPACT\_NULLABLE_STRING
    values => key value TAG_BUFFER 
      key => COMPACT_STRING
      value => FLOAT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or \`0\` if the quota description succeeded. |
| error_message | The error message, or \`null\` if the quota description succeeded. |
| entries | A result entry. |
| entity | The quota entity description. |
| entity_type | The entity type. |
| entity_name | The entity name, or null if the default. |
| \_tagged\_fields | The tagged fields |
| values | The quota values for the entity. |
| key | The quota configuration key. |
| value | The quota configuration value. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AlterClientQuotas API (Key: 49):

**Requests:**  

AlterClientQuotas Request (Version: 0) => \[entries\] validate_only 
  entries => \[entity\] \[ops\] 
    entity => entity\_type entity\_name 
      entity_type => STRING
      entity\_name => NULLABLE\_STRING
    ops => key value remove 
      key => STRING
      value => FLOAT64
      remove => BOOLEAN
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| entries | The quota configuration entries to alter. |
| entity | The quota entity to alter. |
| entity_type | The entity type. |
| entity_name | The name of the entity, or null if the default. |
| ops | An individual quota configuration entry to alter. |
| key | The quota configuration key. |
| value | The value to set, otherwise ignored if the value is to be removed. |
| remove | Whether the quota configuration value should be removed, otherwise set. |
| validate_only | Whether the alteration should be validated, but not performed. |

AlterClientQuotas Request (Version: 1) => \[entries\] validate\_only TAG\_BUFFER 
  entries => \[entity\] \[ops\] TAG_BUFFER 
    entity => entity\_type entity\_name TAG_BUFFER 
      entity\_type => COMPACT\_STRING
      entity\_name => COMPACT\_NULLABLE_STRING
    ops => key value remove TAG_BUFFER 
      key => COMPACT_STRING
      value => FLOAT64
      remove => BOOLEAN
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| entries | The quota configuration entries to alter. |
| entity | The quota entity to alter. |
| entity_type | The entity type. |
| entity_name | The name of the entity, or null if the default. |
| \_tagged\_fields | The tagged fields |
| ops | An individual quota configuration entry to alter. |
| key | The quota configuration key. |
| value | The value to set, otherwise ignored if the value is to be removed. |
| remove | Whether the quota configuration value should be removed, otherwise set. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| validate_only | Whether the alteration should be validated, but not performed. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AlterClientQuotas Response (Version: 0) => throttle\_time\_ms \[entries\] 
  throttle\_time\_ms => INT32
  entries => error\_code error\_message \[entity\] 
    error_code => INT16
    error\_message => NULLABLE\_STRING
    entity => entity\_type entity\_name 
      entity_type => STRING
      entity\_name => NULLABLE\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| entries | The quota configuration entries to alter. |
| error_code | The error code, or \`0\` if the quota alteration succeeded. |
| error_message | The error message, or \`null\` if the quota alteration succeeded. |
| entity | The quota entity to alter. |
| entity_type | The entity type. |
| entity_name | The name of the entity, or null if the default. |

AlterClientQuotas Response (Version: 1) => throttle\_time\_ms \[entries\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  entries => error\_code error\_message \[entity\] TAG_BUFFER 
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    entity => entity\_type entity\_name TAG_BUFFER 
      entity\_type => COMPACT\_STRING
      entity\_name => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| entries | The quota configuration entries to alter. |
| error_code | The error code, or \`0\` if the quota alteration succeeded. |
| error_message | The error message, or \`null\` if the quota alteration succeeded. |
| entity | The quota entity to alter. |
| entity_type | The entity type. |
| entity_name | The name of the entity, or null if the default. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### DescribeUserScramCredentials API (Key: 50):

**Requests:**  

DescribeUserScramCredentials Request (Version: 0) => \[users\] TAG_BUFFER 
  users => name TAG_BUFFER 
    name => COMPACT_STRING

| Field | Description |
| --- | --- |
| users | The users to describe, or null/empty to describe all users. |
| name | The user name. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeUserScramCredentials Response (Version: 0) => throttle\_time\_ms error\_code error\_message \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  results => user error\_code error\_message \[credential\_infos\] TAG\_BUFFER 
    user => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING
    credential\_infos => mechanism iterations TAG\_BUFFER 
      mechanism => INT8
      iterations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The message-level error code, 0 except for user authorization or infrastructure issues. |
| error_message | The message-level error message, if any. |
| results | The results for descriptions, one per user. |
| user | The user name. |
| error_code | The user-level error code. |
| error_message | The user-level error message, if any. |
| credential_infos | The mechanism and related information associated with the user's SCRAM credentials. |
| mechanism | The SCRAM mechanism. |
| iterations | The number of iterations used in the SCRAM credential. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AlterUserScramCredentials API (Key: 51):

**Requests:**  

AlterUserScramCredentials Request (Version: 0) => \[deletions\] \[upsertions\] TAG_BUFFER 
  deletions => name mechanism TAG_BUFFER 
    name => COMPACT_STRING
    mechanism => INT8
  upsertions => name mechanism iterations salt salted\_password TAG\_BUFFER 
    name => COMPACT_STRING
    mechanism => INT8
    iterations => INT32
    salt => COMPACT_BYTES
    salted\_password => COMPACT\_BYTES

| Field | Description |
| --- | --- |
| deletions | The SCRAM credentials to remove. |
| name | The user name. |
| mechanism | The SCRAM mechanism. |
| \_tagged\_fields | The tagged fields |
| upsertions | The SCRAM credentials to update/insert. |
| name | The user name. |
| mechanism | The SCRAM mechanism. |
| iterations | The number of iterations. |
| salt | A random salt generated by the client. |
| salted_password | The salted password. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AlterUserScramCredentials Response (Version: 0) => throttle\_time\_ms \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  results => user error\_code error\_message TAG_BUFFER 
    user => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| results | The results for deletions and alterations, one per affected user. |
| user | The user name. |
| error_code | The error code. |
| error_message | The error message, if any. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### DescribeQuorum API (Key: 55):

**Requests:**  

DescribeQuorum Request (Version: 0) => \[topics\] TAG_BUFFER 
  topics => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index TAG\_BUFFER 
      partition_index => INT32

| Field | Description |
| --- | --- |
| topics |     |
| topic_name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DescribeQuorum Request (Version: 1) => \[topics\] TAG_BUFFER 
  topics => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index TAG\_BUFFER 
      partition_index => INT32

| Field | Description |
| --- | --- |
| topics |     |
| topic_name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeQuorum Response (Version: 0) => error\_code \[topics\] TAG\_BUFFER 
  error_code => INT16
  topics => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index error\_code leader\_id leader\_epoch high\_watermark \[current\_voters\] \[observers\] TAG_BUFFER 
      partition_index => INT32
      error_code => INT16
      leader_id => INT32
      leader_epoch => INT32
      high_watermark => INT64
      current\_voters => replica\_id log\_end\_offset TAG_BUFFER 
        replica_id => INT32
        log\_end\_offset => INT64
      observers => replica\_id log\_end\_offset TAG\_BUFFER 
        replica_id => INT32
        log\_end\_offset => INT64

| Field | Description |
| --- | --- |
| error_code | The top level error code. |
| topics |     |
| topic_name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| error_code |     |
| leader_id | The ID of the current leader or -1 if the leader is unknown. |
| leader_epoch | The latest known leader epoch |
| high_watermark |     |
| current_voters |     |
| replica_id |     |
| log\_end\_offset | The last known log end offset of the follower or -1 if it is unknown |
| \_tagged\_fields | The tagged fields |
| observers |     |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

DescribeQuorum Response (Version: 1) => error\_code \[topics\] TAG\_BUFFER 
  error_code => INT16
  topics => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index error\_code leader\_id leader\_epoch high\_watermark \[current\_voters\] \[observers\] TAG_BUFFER 
      partition_index => INT32
      error_code => INT16
      leader_id => INT32
      leader_epoch => INT32
      high_watermark => INT64
      current\_voters => replica\_id log\_end\_offset last\_fetch\_timestamp last\_caught\_up\_timestamp TAG\_BUFFER 
        replica_id => INT32
        log\_end\_offset => INT64
        last\_fetch\_timestamp => INT64
        last\_caught\_up_timestamp => INT64
      observers => replica\_id log\_end\_offset last\_fetch\_timestamp last\_caught\_up\_timestamp TAG_BUFFER 
        replica_id => INT32
        log\_end\_offset => INT64
        last\_fetch\_timestamp => INT64
        last\_caught\_up_timestamp => INT64

| Field | Description |
| --- | --- |
| error_code | The top level error code. |
| topics |     |
| topic_name | The topic name. |
| partitions |     |
| partition_index | The partition index. |
| error_code |     |
| leader_id | The ID of the current leader or -1 if the leader is unknown. |
| leader_epoch | The latest known leader epoch |
| high_watermark |     |
| current_voters |     |
| replica_id |     |
| log\_end\_offset | The last known log end offset of the follower or -1 if it is unknown |
| last\_fetch\_timestamp | The last known leader wall clock time time when a follower fetched from the leader. This is reported as -1 both for the current leader or if it is unknown for a voter |
| last\_caught\_up_timestamp | The leader wall clock append time of the offset for which the follower made the most recent fetch request. This is reported as the current time for the leader and -1 if unknown for a voter |
| \_tagged\_fields | The tagged fields |
| observers |     |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AlterPartition API (Key: 56):

**Requests:**  

AlterPartition Request (Version: 0) => broker\_id broker\_epoch \[topics\] TAG_BUFFER 
  broker_id => INT32
  broker_epoch => INT64
  topics => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index leader\_epoch \[new\_isr\] partition\_epoch TAG_BUFFER 
      partition_index => INT32
      leader_epoch => INT32
      new_isr => INT32
      partition_epoch => INT32

| Field | Description |
| --- | --- |
| broker_id | The ID of the requesting broker |
| broker_epoch | The epoch of the requesting broker |
| topics |     |
| topic_name | The name of the topic to alter ISRs for |
| partitions |     |
| partition_index | The partition index |
| leader_epoch | The leader epoch of this partition |
| new_isr | The ISR for this partition. Deprecated since version 3. |
| partition_epoch | The expected epoch of the partition which is being updated. For legacy cluster this is the ZkVersion in the LeaderAndIsr request. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

AlterPartition Request (Version: 1) => broker\_id broker\_epoch \[topics\] TAG_BUFFER 
  broker_id => INT32
  broker_epoch => INT64
  topics => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index leader\_epoch \[new\_isr\] leader\_recovery\_state partition\_epoch TAG_BUFFER 
      partition_index => INT32
      leader_epoch => INT32
      new_isr => INT32
      leader\_recovery\_state => INT8
      partition_epoch => INT32

| Field | Description |
| --- | --- |
| broker_id | The ID of the requesting broker |
| broker_epoch | The epoch of the requesting broker |
| topics |     |
| topic_name | The name of the topic to alter ISRs for |
| partitions |     |
| partition_index | The partition index |
| leader_epoch | The leader epoch of this partition |
| new_isr | The ISR for this partition. Deprecated since version 3. |
| leader\_recovery\_state | 1 if the partition is recovering from an unclean leader election; 0 otherwise. |
| partition_epoch | The expected epoch of the partition which is being updated. For legacy cluster this is the ZkVersion in the LeaderAndIsr request. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

AlterPartition Request (Version: 2) => broker\_id broker\_epoch \[topics\] TAG_BUFFER 
  broker_id => INT32
  broker_epoch => INT64
  topics => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition\_index leader\_epoch \[new\_isr\] leader\_recovery\_state partition\_epoch TAG_BUFFER 
      partition_index => INT32
      leader_epoch => INT32
      new_isr => INT32
      leader\_recovery\_state => INT8
      partition_epoch => INT32

| Field | Description |
| --- | --- |
| broker_id | The ID of the requesting broker |
| broker_epoch | The epoch of the requesting broker |
| topics |     |
| topic_id | The ID of the topic to alter ISRs for |
| partitions |     |
| partition_index | The partition index |
| leader_epoch | The leader epoch of this partition |
| new_isr | The ISR for this partition. Deprecated since version 3. |
| leader\_recovery\_state | 1 if the partition is recovering from an unclean leader election; 0 otherwise. |
| partition_epoch | The expected epoch of the partition which is being updated. For legacy cluster this is the ZkVersion in the LeaderAndIsr request. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

AlterPartition Request (Version: 3) => broker\_id broker\_epoch \[topics\] TAG_BUFFER 
  broker_id => INT32
  broker_epoch => INT64
  topics => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition\_index leader\_epoch \[new\_isr\_with\_epochs\] leader\_recovery\_state partition\_epoch TAG_BUFFER 
      partition_index => INT32
      leader_epoch => INT32
      new\_isr\_with\_epochs => broker\_id broker\_epoch TAG\_BUFFER 
        broker_id => INT32
        broker_epoch => INT64
      leader\_recovery\_state => INT8
      partition_epoch => INT32

| Field | Description |
| --- | --- |
| broker_id | The ID of the requesting broker |
| broker_epoch | The epoch of the requesting broker |
| topics |     |
| topic_id | The ID of the topic to alter ISRs for |
| partitions |     |
| partition_index | The partition index |
| leader_epoch | The leader epoch of this partition |
| new\_isr\_with_epochs |     |
| broker_id | The ID of the broker. |
| broker_epoch | The epoch of the broker. It will be -1 if the epoch check is not supported. |
| \_tagged\_fields | The tagged fields |
| leader\_recovery\_state | 1 if the partition is recovering from an unclean leader election; 0 otherwise. |
| partition_epoch | The expected epoch of the partition which is being updated. For legacy cluster this is the ZkVersion in the LeaderAndIsr request. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AlterPartition Response (Version: 0) => throttle\_time\_ms error\_code \[topics\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  topics => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index error\_code leader\_id leader\_epoch \[isr\] partition\_epoch TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16
      leader_id => INT32
      leader_epoch => INT32
      isr => INT32
      partition_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code |
| topics |     |
| topic_name | The name of the topic |
| partitions |     |
| partition_index | The partition index |
| error_code | The partition level error code |
| leader_id | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| partition_epoch | The current epoch for the partition for KRaft controllers. The current ZK version for the legacy controllers. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

AlterPartition Response (Version: 1) => throttle\_time\_ms error\_code \[topics\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  topics => topic\_name \[partitions\] TAG\_BUFFER 
    topic\_name => COMPACT\_STRING
    partitions => partition\_index error\_code leader\_id leader\_epoch \[isr\] leader\_recovery\_state partition\_epoch TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16
      leader_id => INT32
      leader_epoch => INT32
      isr => INT32
      leader\_recovery\_state => INT8
      partition_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code |
| topics |     |
| topic_name | The name of the topic |
| partitions |     |
| partition_index | The partition index |
| error_code | The partition level error code |
| leader_id | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| leader\_recovery\_state | 1 if the partition is recovering from an unclean leader election; 0 otherwise. |
| partition_epoch | The current epoch for the partition for KRaft controllers. The current ZK version for the legacy controllers. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

AlterPartition Response (Version: 2) => throttle\_time\_ms error\_code \[topics\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  topics => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition\_index error\_code leader\_id leader\_epoch \[isr\] leader\_recovery\_state partition\_epoch TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16
      leader_id => INT32
      leader_epoch => INT32
      isr => INT32
      leader\_recovery\_state => INT8
      partition_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code |
| topics |     |
| topic_id | The ID of the topic |
| partitions |     |
| partition_index | The partition index |
| error_code | The partition level error code |
| leader_id | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| leader\_recovery\_state | 1 if the partition is recovering from an unclean leader election; 0 otherwise. |
| partition_epoch | The current epoch for the partition for KRaft controllers. The current ZK version for the legacy controllers. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

AlterPartition Response (Version: 3) => throttle\_time\_ms error\_code \[topics\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  topics => topic\_id \[partitions\] TAG\_BUFFER 
    topic_id => UUID
    partitions => partition\_index error\_code leader\_id leader\_epoch \[isr\] leader\_recovery\_state partition\_epoch TAG\_BUFFER 
      partition_index => INT32
      error_code => INT16
      leader_id => INT32
      leader_epoch => INT32
      isr => INT32
      leader\_recovery\_state => INT8
      partition_epoch => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code |
| topics |     |
| topic_id | The ID of the topic |
| partitions |     |
| partition_index | The partition index |
| error_code | The partition level error code |
| leader_id | The broker ID of the leader. |
| leader_epoch | The leader epoch. |
| isr | The in-sync replica IDs. |
| leader\_recovery\_state | 1 if the partition is recovering from an unclean leader election; 0 otherwise. |
| partition_epoch | The current epoch for the partition for KRaft controllers. The current ZK version for the legacy controllers. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### UpdateFeatures API (Key: 57):

**Requests:**  

UpdateFeatures Request (Version: 0) => timeout\_ms \[feature\_updates\] TAG_BUFFER 
  timeout_ms => INT32
  feature\_updates => feature max\_version\_level allow\_downgrade TAG_BUFFER 
    feature => COMPACT_STRING
    max\_version\_level => INT16
    allow_downgrade => BOOLEAN

| Field | Description |
| --- | --- |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| feature_updates | The list of updates to finalized features. |
| feature | The name of the finalized feature to be updated. |
| max\_version\_level | The new maximum version level for the finalized feature. A value >= 1 is valid. A value < 1, is special, and can be used to request the deletion of the finalized feature. |
| allow_downgrade | DEPRECATED in version 1 (see DowngradeType). When set to true, the finalized feature version level is allowed to be downgraded/deleted. The downgrade request will fail if the new maximum version level is a value that's not lower than the existing maximum finalized version level. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

UpdateFeatures Request (Version: 1) => timeout\_ms \[feature\_updates\] validate\_only TAG\_BUFFER 
  timeout_ms => INT32
  feature\_updates => feature max\_version\_level upgrade\_type TAG_BUFFER 
    feature => COMPACT_STRING
    max\_version\_level => INT16
    upgrade_type => INT8
  validate_only => BOOLEAN

| Field | Description |
| --- | --- |
| timeout_ms | How long to wait in milliseconds before timing out the request. |
| feature_updates | The list of updates to finalized features. |
| feature | The name of the finalized feature to be updated. |
| max\_version\_level | The new maximum version level for the finalized feature. A value >= 1 is valid. A value < 1, is special, and can be used to request the deletion of the finalized feature. |
| upgrade_type | Determine which type of upgrade will be performed: 1 will perform an upgrade only (default), 2 is safe downgrades only (lossless), 3 is unsafe downgrades (lossy). |
| \_tagged\_fields | The tagged fields |
| validate_only | True if we should validate the request, but not perform the upgrade or downgrade. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

UpdateFeatures Response (Version: 0) => throttle\_time\_ms error\_code error\_message \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  results => feature error\_code error\_message TAG_BUFFER 
    feature => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top-level error code, or \`0\` if there was no top-level error. |
| error_message | The top-level error message, or \`null\` if there was no top-level error. |
| results | Results for each feature update. |
| feature | The name of the finalized feature. |
| error_code | The feature update error code or \`0\` if the feature update succeeded. |
| error_message | The feature update error, or \`null\` if the feature update succeeded. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

UpdateFeatures Response (Version: 1) => throttle\_time\_ms error\_code error\_message \[results\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  results => feature error\_code error\_message TAG_BUFFER 
    feature => COMPACT_STRING
    error_code => INT16
    error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top-level error code, or \`0\` if there was no top-level error. |
| error_message | The top-level error message, or \`null\` if there was no top-level error. |
| results | Results for each feature update. |
| feature | The name of the finalized feature. |
| error_code | The feature update error code or \`0\` if the feature update succeeded. |
| error_message | The feature update error, or \`null\` if the feature update succeeded. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### Envelope API (Key: 58):

**Requests:**  

Envelope Request (Version: 0) => request\_data request\_principal client\_host\_address TAG_BUFFER 
  request\_data => COMPACT\_BYTES
  request\_principal => COMPACT\_NULLABLE_BYTES
  client\_host\_address => COMPACT_BYTES

| Field | Description |
| --- | --- |
| request_data | The embedded request header and data. |
| request_principal | Value of the initial client principal when the request is redirected by a broker. |
| client\_host\_address | The original client's address in bytes. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

Envelope Response (Version: 0) => response\_data error\_code TAG_BUFFER 
  response\_data => COMPACT\_NULLABLE_BYTES
  error_code => INT16

| Field | Description |
| --- | --- |
| response_data | The embedded response header and data. |
| error_code | The error code, or 0 if there was no error. |
| \_tagged\_fields | The tagged fields |

##### DescribeCluster API (Key: 60):

**Requests:**  

DescribeCluster Request (Version: 0) => include\_cluster\_authorized\_operations TAG\_BUFFER 
  include\_cluster\_authorized_operations => BOOLEAN

| Field | Description |
| --- | --- |
| include\_cluster\_authorized_operations | Whether to include cluster authorized operations. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeCluster Response (Version: 0) => throttle\_time\_ms error\_code error\_message cluster\_id controller\_id \[brokers\] cluster\_authorized\_operations TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  cluster\_id => COMPACT\_STRING
  controller_id => INT32
  brokers => broker\_id host port rack TAG\_BUFFER 
    broker_id => INT32
    host => COMPACT_STRING
    port => INT32
    rack => COMPACT\_NULLABLE\_STRING
  cluster\_authorized\_operations => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top-level error code, or 0 if there was no error |
| error_message | The top-level error message, or null if there was no error. |
| cluster_id | The cluster ID that responding broker belongs to. |
| controller_id | The ID of the controller broker. |
| brokers | Each broker in the response. |
| broker_id | The broker ID. |
| host | The broker hostname. |
| port | The broker port. |
| rack | The rack of the broker, or null if it has not been assigned to a rack. |
| \_tagged\_fields | The tagged fields |
| cluster\_authorized\_operations | 32-bit bitfield to represent authorized operations for this cluster. |
| \_tagged\_fields | The tagged fields |

##### DescribeProducers API (Key: 61):

**Requests:**  

DescribeProducers Request (Version: 0) => \[topics\] TAG_BUFFER 
  topics => name \[partition\_indexes\] TAG\_BUFFER 
    name => COMPACT_STRING
    partition_indexes => INT32

| Field | Description |
| --- | --- |
| topics |     |
| name | The topic name. |
| partition_indexes | The indexes of the partitions to list producers for. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeProducers Response (Version: 0) => throttle\_time\_ms \[topics\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  topics => name \[partitions\] TAG_BUFFER 
    name => COMPACT_STRING
    partitions => partition\_index error\_code error\_message \[active\_producers\] TAG_BUFFER 
      partition_index => INT32
      error_code => INT16
      error\_message => COMPACT\_NULLABLE_STRING
      active\_producers => producer\_id producer\_epoch last\_sequence last\_timestamp coordinator\_epoch current\_txn\_start\_offset TAG\_BUFFER 
        producer_id => INT64
        producer_epoch => INT32
        last_sequence => INT32
        last_timestamp => INT64
        coordinator_epoch => INT32
        current\_txn\_start_offset => INT64

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| topics | Each topic in the response. |
| name | The topic name |
| partitions | Each partition in the response. |
| partition_index | The partition index. |
| error_code | The partition error code, or 0 if there was no error. |
| error_message | The partition error message, which may be null if no additional details are available |
| active_producers |     |
| producer_id |     |
| producer_epoch |     |
| last_sequence |     |
| last_timestamp |     |
| coordinator_epoch |     |
| current\_txn\_start_offset |     |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### UnregisterBroker API (Key: 64):

**Requests:**  

UnregisterBroker Request (Version: 0) => broker\_id TAG\_BUFFER 
  broker_id => INT32

| Field | Description |
| --- | --- |
| broker_id | The broker ID to unregister. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

UnregisterBroker Response (Version: 0) => throttle\_time\_ms error\_code error\_message TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | Duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The error code, or 0 if there was no error. |
| error_message | The top-level error message, or \`null\` if there was no top-level error. |
| \_tagged\_fields | The tagged fields |

##### DescribeTransactions API (Key: 65):

**Requests:**  

DescribeTransactions Request (Version: 0) => \[transactional\_ids\] TAG\_BUFFER 
  transactional\_ids => COMPACT\_STRING

| Field | Description |
| --- | --- |
| transactional_ids | Array of transactionalIds to include in describe results. If empty, then no results will be returned. |
| \_tagged\_fields | The tagged fields |

**Responses:**  

DescribeTransactions Response (Version: 0) => throttle\_time\_ms \[transaction\_states\] TAG\_BUFFER 
  throttle\_time\_ms => INT32
  transaction\_states => error\_code transactional\_id transaction\_state transaction\_timeout\_ms transaction\_start\_time\_ms producer\_id producer\_epoch \[topics\] TAG\_BUFFER 
    error_code => INT16
    transactional\_id => COMPACT\_STRING
    transaction\_state => COMPACT\_STRING
    transaction\_timeout\_ms => INT32
    transaction\_start\_time_ms => INT64
    producer_id => INT64
    producer_epoch => INT16
    topics => topic \[partitions\] TAG_BUFFER 
      topic => COMPACT_STRING
      partitions => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| transaction_states |     |
| error_code |     |
| transactional_id |     |
| transaction_state |     |
| transaction\_timeout\_ms |     |
| transaction\_start\_time_ms |     |
| producer_id |     |
| producer_epoch |     |
| topics | The set of partitions included in the current transaction (if active). When a transaction is preparing to commit or abort, this will include only partitions which do not have markers. |
| topic |     |
| partitions |     |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### ListTransactions API (Key: 66):

**Requests:**  

ListTransactions Request (Version: 0) => \[state\_filters\] \[producer\_id\_filters\] TAG\_BUFFER 
  state\_filters => COMPACT\_STRING
  producer\_id\_filters => INT64

| Field | Description |
| --- | --- |
| state_filters | The transaction states to filter by: if empty, all transactions are returned; if non-empty, then only transactions matching one of the filtered states will be returned |
| producer\_id\_filters | The producerIds to filter by: if empty, all transactions will be returned; if non-empty, only transactions which match one of the filtered producerIds will be returned |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ListTransactions Response (Version: 0) => throttle\_time\_ms error\_code \[unknown\_state\_filters\] \[transaction\_states\] TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  unknown\_state\_filters => COMPACT_STRING
  transaction\_states => transactional\_id producer\_id transaction\_state TAG_BUFFER 
    transactional\_id => COMPACT\_STRING
    producer_id => INT64
    transaction\_state => COMPACT\_STRING

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code |     |
| unknown\_state\_filters | Set of state filters provided in the request which were unknown to the transaction coordinator |
| transaction_states |     |
| transactional_id |     |
| producer_id |     |
| transaction_state | The current transaction state of the producer |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

##### AllocateProducerIds API (Key: 67):

**Requests:**  

AllocateProducerIds Request (Version: 0) => broker\_id broker\_epoch TAG_BUFFER 
  broker_id => INT32
  broker_epoch => INT64

| Field | Description |
| --- | --- |
| broker_id | The ID of the requesting broker |
| broker_epoch | The epoch of the requesting broker |
| \_tagged\_fields | The tagged fields |

**Responses:**  

AllocateProducerIds Response (Version: 0) => throttle\_time\_ms error\_code producer\_id\_start producer\_id\_len TAG\_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  producer\_id\_start => INT64
  producer\_id\_len => INT32

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top level response error code |
| producer\_id\_start | The first producer ID in this range, inclusive |
| producer\_id\_len | The number of producer IDs in this range |
| \_tagged\_fields | The tagged fields |

##### ConsumerGroupHeartbeat API (Key: 68):

**Requests:**  

ConsumerGroupHeartbeat Request (Version: 0) => group\_id member\_id member\_epoch instance\_id rack\_id rebalance\_timeout\_ms \[subscribed\_topic\_names\] subscribed\_topic\_regex server\_assignor \[client\_assignors\] \[topic\_partitions\] TAG_BUFFER 
  group\_id => COMPACT\_STRING
  member\_id => COMPACT\_STRING
  member_epoch => INT32
  instance\_id => COMPACT\_NULLABLE_STRING
  rack\_id => COMPACT\_NULLABLE_STRING
  rebalance\_timeout\_ms => INT32
  subscribed\_topic\_names => COMPACT_STRING
  subscribed\_topic\_regex => COMPACT\_NULLABLE\_STRING
  server\_assignor => COMPACT\_NULLABLE_STRING
  client\_assignors => name minimum\_version maximum\_version reason metadata\_version metadata\_bytes TAG\_BUFFER 
    name => COMPACT_STRING
    minimum_version => INT16
    maximum_version => INT16
    reason => INT8
    metadata_version => INT16
    metadata\_bytes => COMPACT\_BYTES
  topic\_partitions => topic\_id \[partitions\] TAG_BUFFER 
    topic_id => UUID
    partitions => INT32

| Field | Description |
| --- | --- |
| group_id | The group identifier. |
| member_id | The member id generated by the coordinator. The member id must be kept during the entire lifetime of the member. |
| member_epoch | The current member epoch; 0 to join the group; -1 to leave the group; -2 to indicate that the static member will rejoin. |
| instance_id | null if not provided or if it didn't change since the last heartbeat; the instance Id otherwise. |
| rack_id | null if not provided or if it didn't change since the last heartbeat; the rack ID of consumer otherwise. |
| rebalance\_timeout\_ms | -1 if it didn't chance since the last heartbeat; the maximum time in milliseconds that the coordinator will wait on the member to revoke its partitions otherwise. |
| subscribed\_topic\_names | null if it didn't change since the last heartbeat; the subscribed topic names otherwise. |
| subscribed\_topic\_regex | null if it didn't change since the last heartbeat; the subscribed topic regex otherwise |
| server_assignor | null if not used or if it didn't change since the last heartbeat; the server side assignor to use otherwise. |
| client_assignors | null if not used or if it didn't change since the last heartbeat; the list of client-side assignors otherwise. |
| name | The name of the assignor. |
| minimum_version | The minimum supported version for the metadata. |
| maximum_version | The maximum supported version for the metadata. |
| reason | The reason of the metadata update. |
| metadata_version | The version of the metadata. |
| metadata_bytes | The metadata. |
| \_tagged\_fields | The tagged fields |
| topic_partitions | null if it didn't change since the last heartbeat; the partitions owned by the member. |
| topic_id | The topic ID. |
| partitions | The partitions. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

**Responses:**  

ConsumerGroupHeartbeat Response (Version: 0) => throttle\_time\_ms error\_code error\_message member\_id member\_epoch should\_compute\_assignment heartbeat\_interval\_ms assignment TAG_BUFFER 
  throttle\_time\_ms => INT32
  error_code => INT16
  error\_message => COMPACT\_NULLABLE_STRING
  member\_id => COMPACT\_NULLABLE_STRING
  member_epoch => INT32
  should\_compute\_assignment => BOOLEAN
  heartbeat\_interval\_ms => INT32
  assignment => error \[assigned\_topic\_partitions\] \[pending\_topic\_partitions\] metadata\_version metadata\_bytes TAG_BUFFER 
    error => INT8
    assigned\_topic\_partitions => topic\_id \[partitions\] TAG\_BUFFER 
      topic_id => UUID
      partitions => INT32
    pending\_topic\_partitions => topic\_id \[partitions\] TAG\_BUFFER 
      topic_id => UUID
      partitions => INT32
    metadata_version => INT16
    metadata\_bytes => COMPACT\_BYTES

| Field | Description |
| --- | --- |
| throttle\_time\_ms | The duration in milliseconds for which the request was throttled due to a quota violation, or zero if the request did not violate any quota. |
| error_code | The top-level error code, or 0 if there was no error |
| error_message | The top-level error message, or null if there was no error. |
| member_id | The member id generated by the coordinator. Only provided when the member joins with MemberEpoch == 0. |
| member_epoch | The member epoch. |
| should\_compute\_assignment | True if the member should compute the assignment for the group. |
| heartbeat\_interval\_ms | The heartbeat interval in milliseconds. |
| assignment | null if not provided; the assignment otherwise. |
| error | The assigned error. |
| assigned\_topic\_partitions | The partitions assigned to the member that can be used immediately. |
| topic_id | The topic ID. |
| partitions | The partitions. |
| \_tagged\_fields | The tagged fields |
| pending\_topic\_partitions | The partitions assigned to the member that cannot be used because they are not released by their former owners yet. |
| metadata_version | The version of the metadata. |
| metadata_bytes | The assigned metadata. |
| \_tagged\_fields | The tagged fields |
| \_tagged\_fields | The tagged fields |

#### [Some Common Philosophical Questions](#protocol_philosophy)

Some people have asked why we don't use HTTP. There are a number of reasons, the best is that client implementors can make use of some of the more advanced TCP features--the ability to multiplex requests, the ability to simultaneously poll many connections, etc. We have also found HTTP libraries in many languages to be surprisingly shabby.

Others have asked if maybe we shouldn't support many different protocols. Prior experience with this was that it makes it very hard to add and test new features if they have to be ported across many protocol implementations. Our feeling is that most users don't really see multiple protocols as a feature, they just want a good reliable client in the language of their choice.

Another question is why we don't adopt XMPP, STOMP, AMQP or an existing protocol. The answer to this varies by protocol, but in general the problem is that the protocol does determine large parts of the implementation and we couldn't do what we are doing if we didn't have control over the protocol. Our belief is that it is possible to do better than existing messaging systems have in providing a truly distributed messaging system, and to do this we need to build something that works differently.

A final question is why we don't use a system like Protocol Buffers or Thrift to define our request messages. These packages excel at helping you to managing lots and lots of serialized messages. However we have only a few messages. Support across languages is somewhat spotty (depending on the package). Finally the mapping between binary log format and wire protocol is something we manage somewhat carefully and this would not be possible with these systems. Finally we prefer the style of versioning APIs explicitly and checking this to inferring new values as nulls as it allows more nuanced control of compatibility.

The contents of this website are © 2023 [Apache Software Foundation](https://www.apache.org/) under the terms of the [Apache License v2](https://www.apache.org/licenses/LICENSE-2.0.html). Apache Kafka, Kafka, and the Kafka logo are either registered trademarks or trademarks of The Apache Software Foundation in the United States and other countries.

[Security](https://kafka.apache.org/project-security) |  [Donate](https://www.apache.org/foundation/sponsorship.html) |  [Thanks](https://www.apache.org/foundation/thanks.html) |  [Events](https://apache.org/events/current-event) |  [License](https://apache.org/licenses/) |  [Privacy](https://privacy.apache.org/policies/privacy-policy-public.html)

[![Apache Feather](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAPcAAABGCAYAAAFnHTIVAAAAGXRFWHRTb2Z0d2FyZQBBZG9iZSBJbWFnZVJlYWR5ccllPAAAAyJpVFh0WE1MOmNvbS5hZG9iZS54bXAAAAAAADw/eHBhY2tldCBiZWdpbj0i77u/IiBpZD0iVzVNME1wQ2VoaUh6cmVTek5UY3prYzlkIj8+IDx4OnhtcG1ldGEgeG1sbnM6eD0iYWRvYmU6bnM6bWV0YS8iIHg6eG1wdGs9IkFkb2JlIFhNUCBDb3JlIDUuMC1jMDYxIDY0LjE0MDk0OSwgMjAxMC8xMi8wNy0xMDo1NzowMSAgICAgICAgIj4gPHJkZjpSREYgeG1sbnM6cmRmPSJodHRwOi8vd3d3LnczLm9yZy8xOTk5LzAyLzIyLXJkZi1zeW50YXgtbnMjIj4gPHJkZjpEZXNjcmlwdGlvbiByZGY6YWJvdXQ9IiIgeG1sbnM6eG1wPSJodHRwOi8vbnMuYWRvYmUuY29tL3hhcC8xLjAvIiB4bWxuczp4bXBNTT0iaHR0cDovL25zLmFkb2JlLmNvbS94YXAvMS4wL21tLyIgeG1sbnM6c3RSZWY9Imh0dHA6Ly9ucy5hZG9iZS5jb20veGFwLzEuMC9zVHlwZS9SZXNvdXJjZVJlZiMiIHhtcDpDcmVhdG9yVG9vbD0iQWRvYmUgUGhvdG9zaG9wIENTNS4xIFdpbmRvd3MiIHhtcE1NOkluc3RhbmNlSUQ9InhtcC5paWQ6MTYyMzQwM0U5NzUwMTFFMTg1ODBGOEUwQTk1QUEzRTciIHhtcE1NOkRvY3VtZW50SUQ9InhtcC5kaWQ6MTYyMzQwM0Y5NzUwMTFFMTg1ODBGOEUwQTk1QUEzRTciPiA8eG1wTU06RGVyaXZlZEZyb20gc3RSZWY6aW5zdGFuY2VJRD0ieG1wLmlpZDoxNjIzNDAzQzk3NTAxMUUxODU4MEY4RTBBOTVBQTNFNyIgc3RSZWY6ZG9jdW1lbnRJRD0ieG1wLmRpZDoxNjIzNDAzRDk3NTAxMUUxODU4MEY4RTBBOTVBQTNFNyIvPiA8L3JkZjpEZXNjcmlwdGlvbj4gPC9yZGY6UkRGPiA8L3g6eG1wbWV0YT4gPD94cGFja2V0IGVuZD0iciI/PgE7krcAAHbTSURBVHjatJJdaFt1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)](http://www.apache.org/)
