# syntax=docker/dockerfile:1
# A static binary on a distroless base, for linux/amd64, linux/arm64 and
# linux/arm/v7, cross-compiled on the build machine's own platform.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS build
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/decision-node-mqtt ./cmd/decision-node-mqtt

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source=https://github.com/Bynn-Intelligence/decision-node-mqtt \
      org.opencontainers.image.description="MQTT bridge for DecisionNode, the decision engine" \
      org.opencontainers.image.licenses=MIT
COPY --from=build /out/decision-node-mqtt /usr/local/bin/decision-node-mqtt
COPY config.example.yaml /etc/decision-node-mqtt/config.yaml
ENV DN_MQTT_CONFIG=/etc/decision-node-mqtt/config.yaml
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/decision-node-mqtt"]
