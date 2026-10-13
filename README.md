# decision-node-mqtt

An MQTT bridge for DecisionNode. DecisionNode is a decision engine: you ask it a yes-or-no, choice, score, number, point or box question about text, JSON, images or video, and it answers with calibrated probabilities. The bridge lets any MQTT device ask those questions without an SDK of its own: an ESP32, an industrial gateway, a Node-RED flow or a camera that can only publish JPEG bytes.

A device publishes a request on a topic. The bridge sends it to the DecisionNode API and publishes the answer on a response topic.

```
device ── decisionnode/request/<id> ──▶ broker ──▶ decision-node-mqtt ──▶ DecisionNode API
device ◀── decisionnode/response/<id> ── broker ◀──────────┘
```

One static binary for Linux (x86-64, arm64, armv7, armv6), macOS and Windows, or a container image for linux/amd64, arm64 and arm/v7.

## Quickstart with mosquitto

You need an API key from the DecisionNode console and Docker.

```sh
cd examples
DECISIONNODE_API_KEY=dn_live_... docker compose up --build
```

That starts a mosquitto broker on port 1883 and the bridge beside it. In another terminal, listen for answers and ask a question:

```sh
mosquitto_sub -h localhost -t 'decisionnode/response/#' -v &

mosquitto_pub -h localhost -t decisionnode/request/sensor-7 -m '{
  "id": "r-1",
  "state": {"temperature_c": 81, "trend": "rising", "limit_c": 85},
  "questions": {
    "overheating": {"type": "truth", "instructions": "Will this machine overheat within the hour?"}
  }
}'
```

The answer arrives on `decisionnode/response/sensor-7`:

```json
{"id":"r-1","request_id":"req_...","decision":{"model":"decisionnode-moe-...","answers":{"overheating":{"type":"truth","truth":0.87}},"usage":{"input_tokens":96,"output_tokens":0,"cost":{"usd":0.000004,"model_usd":0.000004,"boosters_usd":0,"free_tier":false}}}}
```

Without Docker, run the binary against any broker:

```sh
export DECISIONNODE_API_KEY=dn_live_...
export DN_MQTT_URL=mqtt://localhost:1883
decision-node-mqtt -config config.yaml
```

## Topics

- **Requests** arrive on the topic filters in `mqtt.subscribe`, `decisionnode/request/+` by default.
- **Answers** go to `<response_prefix>/<id>`, `decisionnode/response/<id>` by default. `<id>` is the part of the request topic the subscription's wildcards matched: a request on `decisionnode/request/sensor-7` is answered on `decisionnode/response/sensor-7`.
- **MQTT 5 clients** may set the request's response topic and correlation data. The answer then goes to that response topic, with the same correlation data, when the topic matches one of `mqtt.response_topics` (by default `decisionnode/response/#`), so a device can never make the bridge publish anywhere else. A response topic the bridge itself subscribes to is never used, so an answer can never come back as a request. Otherwise the answer goes to the default topic, still with the correlation data.
- **MQTT 3.1.1 clients** put an `id` in the JSON request. The answer carries the same `id`.

The bridge connects to the broker over MQTT 5 (mosquitto 1.6 or later, EMQX, HiveMQ, VerneMQ and others). Devices may use MQTT 3.1.1 or 5.

## Requests

A request payload is either a JSON request or the bytes of an image.

**JSON.** Every field is optional, except that a request needs questions, its own or its topic's profile's.

| Field | What it is |
|---|---|
| `id` | Any JSON value, echoed in the answer |
| `state` | What is judged: a string, or any JSON object or array |
| `questions` | The questions, by id, as the DecisionNode API takes them: `truth`, `choice`, `score`, `number`, `point` and `box` |
| `images` | Up to 16 images: `{"id": "gate", "url": "https://..."}`, or `{"id": "gate", "data": "<base64>", "media_type": "image/jpeg"}` |
| `model` | `decisionnode-moe` (the default) or `decisionnode-flash` |
| `boosters` | Specialist readings of the images, added as evidence, such as `["age"]` |
| `fallback` | `false` to wait for the requested model rather than be answered by another |
| `profile` | The name of a profile to start from, instead of the topic's |

An unknown field is refused, so a typo is never silently ignored.

**An image.** A payload that starts like a JPEG, PNG or WebP file is sent as the image of its topic's profile. A camera that can only publish JPEG bytes is answered with the profile's questions.

### Profiles

A profile is a named request that a topic's payloads fill in. An image payload becomes its image; a JSON payload's fields replace its own.

```yaml
profiles:
  gate:
    state: Camera 2 at the north gate of the loading yard.
    image_id: gate
    questions:
      vehicle:
        type: truth
        instructions: Is a vehicle waiting at the gate?
      people:
        type: number
        instructions: How many people are in the picture?
        min: 0
        max: 20

topics:
  - filter: decisionnode/request/gate-north
    profile: gate
```

```sh
mosquitto_pub -h localhost -t decisionnode/request/gate-north -f snapshot.jpg
```

## Answers

An answer is one JSON object:

- `id`: the request's `id`, or `null`.
- `request_id`: the API's request id, to quote to support.
- `decision`: the API's response body, exactly as it sent it: `answers`, `model` and `usage`, with `evidence` when the request named boosters.

A request that could not be answered gets `error` instead of `decision`:

```json
{"id":"r-1","error":{"status":null,"error_type":"rate_limited","message":"too many requests on this topic: try again later","request_id":null}}
```

`status`, `error_type` and `message` are the API's own, exactly as it sent them. Where the API was not reached, `status` is `null` and `error_type` is one of the bridge's:

| `error_type` | Why |
|---|---|
| `payload_too_large` | The payload is over `limits.max_payload_bytes` |
| `rate_limited` | The request topic is over `limits.rate_per_second` and `limits.burst` |
| `overloaded` | `limits.max_in_flight` requests are being answered already |
| `invalid_payload` | The payload is neither a JSON request nor an image, or the JSON is not a request the bridge can send |
| `no_profile` | An image payload on a topic without a profile, or a `profile` that does not exist |
| `timeout_error`, `connection_error` | The API did not answer in time, or could not be reached, after the retries |

Over a limit a request is answered at once with its error and dropped. Nothing is queued without bound.

## An ESP32 example

With the Arduino `PubSubClient` library (MQTT 3.1.1), a device publishes its reading with an `id` and listens on its own response topic:

```cpp
#include <WiFi.h>
#include <PubSubClient.h>

WiFiClient net;
PubSubClient mqtt(net);

void onAnswer(char* topic, byte* payload, unsigned int length) {
  // {"id":"r-42","request_id":"req_...","decision":{"answers":{"open":{"type":"truth","truth":0.91}}}}
  Serial.write(payload, length);
  Serial.println();
}

void setup() {
  Serial.begin(115200);
  WiFi.begin("my-network", "my-password");
  while (WiFi.status() != WL_CONNECTED) delay(250);
  mqtt.setServer("192.168.1.20", 1883);
  mqtt.setBufferSize(1024);
  mqtt.setCallback(onAnswer);
}

void loop() {
  if (!mqtt.connected()) {
    mqtt.connect("esp32-door-3");
    mqtt.subscribe("decisionnode/response/esp32-door-3");
  }
  mqtt.loop();

  static unsigned long last = 0;
  if (millis() - last > 60000) {
    last = millis();
    char body[256];
    snprintf(body, sizeof body,
      "{\"id\":\"r-%lu\",\"state\":{\"door_sensor\":%d,\"lux\":%d},"
      "\"questions\":{\"open\":{\"type\":\"truth\",\"instructions\":\"Is the door open?\"}}}",
      last / 1000, digitalRead(4), analogRead(34));
    mqtt.publish("decisionnode/request/esp32-door-3", body);
  }
}
```

## Configuration

Settings come from a YAML file (`-config`, or `DN_MQTT_CONFIG`) and the environment, the environment winning. [`config.example.yaml`](config.example.yaml) lists every setting with its default.

| Environment variable | Setting |
|---|---|
| `DECISIONNODE_API_KEY` | The API key (else `api.key_file`) |
| `DECISIONNODE_BASE_URL` | `api.base_url`, for a self-hosted server |
| `DECISIONNODE_MODEL` | `api.model` |
| `DN_MQTT_URL` | `mqtt.url`: `mqtt://` or `tcp://`, `mqtts://`, `ssl://` or `tls://`, `ws://` or `wss://` |
| `DN_MQTT_CLIENT_ID`, `DN_MQTT_USERNAME` | `mqtt.client_id`, `mqtt.username` |
| `DN_MQTT_PASSWORD` | The broker password (else `mqtt.password_file`) |
| `DN_MQTT_HTTP_LISTEN` | `http.listen` |

The API key and the broker password come only from the environment or a file. The YAML has no key for either, and an unknown key is an error, so a key pasted into the file is refused rather than used. Neither is ever read from a topic or written to a log.

**TLS.** An `mqtts://`, `ssl://`, `tls://` or `wss://` URL connects over TLS 1.2 or later, verifying the broker's certificate against the system's roots, or against `mqtt.ca_file`. `mqtt.cert_file` and `mqtt.key_file` add a client certificate. Certificate checks cannot be turned off.

## Health and metrics

The bridge serves on `http.listen` (`:8080`):

- `/healthz`: `200 {"status":"ok"}` while it is connected to the broker and subscribed, else `503`.
- `/metrics`: Prometheus metrics: `decisionnode_mqtt_requests_total{result}`, `decisionnode_mqtt_in_flight`, `decisionnode_mqtt_decision_seconds`, `decisionnode_mqtt_connected` and `decisionnode_mqtt_publish_errors_total`, with the Go runtime's.

Logs are JSON lines on stderr: the request topic, the response topic, the result, the time taken and the API's request id. Payloads are never logged.

On `SIGTERM` or `SIGINT` the bridge answers the requests it has taken, for up to a minute, then disconnects.

## Build from source

Go 1.26 or later:

```sh
go build -o decision-node-mqtt ./cmd/decision-node-mqtt
go test ./...
```

## License

MIT. See [LICENSE](LICENSE).
