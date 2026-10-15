# Changelog

All notable changes to decision-node-mqtt are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Each release's notes are its
section below.

## [0.1.2] - 2026-10-15

Documentation for boosters; the bridge itself is unchanged.

- The README lists every booster a request or a profile can name, with their options: `age`, `ai_generated`, `face_detection`, `face_recognition` (`collection` and `threshold`), `ocr`, and for robots and drones Depth Vision (`bynn_depth`) and Segment Vision (`bynn_segment`).
- A booster with settings is an object, `{"name", "images", "require", "options", "timeout_ms"}`, in a JSON request or in a profile's `boosters` in the configuration file; `config.example.yaml` has a `drone` profile with Depth Vision and Segment Vision.
- The container image is built without a build provenance attestation, as in 0.1.1. The image and the release archives are signed keylessly with cosign.

## [0.1.1] - 2026-10-14

A maintenance release; the bridge itself is unchanged.

- The container image is built without a build provenance attestation. The image and the release archives are still signed keylessly with cosign, as in 0.1.0.

## [0.1.0] - 2026-10-13

The first release of decision-node-mqtt, the MQTT bridge for DecisionNode.

- Answers requests published on MQTT topics, `decisionnode/request/+` by default: JSON requests with a state, questions, images by URL or base64, a model and boosters, or the bytes of a JPEG, PNG or WebP image asked about by its topic's profile.
- Publishes each answer on `decisionnode/response/<id>`, or on an MQTT 5 request's own response topic with its correlation data, the API's decision exactly as sent, or an error with its `error_type`.
- Limits on payload size, requests in flight and requests per topic; a request over a limit is answered at once with an error, never queued.
- TLS with a CA file and a client certificate, a broker user name and password, and the API key from the environment or a file only.
- `/healthz` and Prometheus metrics on `/metrics`.
- Static binaries for Linux (x86-64, arm64, armv7, armv6), macOS and Windows, with checksums and a keyless signature, and a container image for linux/amd64, linux/arm64 and linux/arm/v7.
