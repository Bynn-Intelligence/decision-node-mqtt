// Package bridge turns MQTT request messages into DecisionNode decisions
// and publishes the answers.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dn "github.com/Bynn-Intelligence/decision-node-go"

	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/config"
	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/topic"
)

// Message is one request as the broker delivered it.
type Message struct {
	Topic   string
	Payload []byte
	// ResponseTopic and CorrelationData are the MQTT 5 request's, when set.
	ResponseTopic   string
	CorrelationData []byte
}

// Publisher sends an answer.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload, correlation []byte) error
}

// Decider answers a request: a *dn.Client built with ResponseHook.
type Decider interface {
	Decide(ctx context.Context, req dn.Request) (*dn.Decision, error)
}

// publishTimeout bounds the publishing of one answer.
const publishTimeout = 10 * time.Second

// Bridge answers request messages.
type Bridge struct {
	cfg     *config.Config
	api     Decider
	pub     Publisher
	log     *slog.Logger
	metrics *Metrics
	sem     chan struct{}
	limits  *topicLimiter
	wg      sync.WaitGroup
	up      atomic.Bool
	closing atomic.Bool
}

// New builds a bridge. api must be a client built with ResponseHook, so the
// answers are published exactly as the API sent them.
func New(cfg *config.Config, api Decider, pub Publisher, log *slog.Logger, m *Metrics) *Bridge {
	return &Bridge{
		cfg: cfg, api: api, pub: pub, log: log, metrics: m,
		sem:    make(chan struct{}, cfg.Limits.MaxInFlight),
		limits: newTopicLimiter(cfg.Limits.RatePerSecond, cfg.Limits.Burst, cfg.Limits.MaxTopics),
	}
}

// SetConnected records whether the broker connection is up.
func (b *Bridge) SetConnected(up bool) {
	b.up.Store(up)
	if up {
		b.metrics.connected.Set(1)
	} else {
		b.metrics.connected.Set(0)
	}
}

// Connected reports whether the broker connection is up.
func (b *Bridge) Connected() bool { return b.up.Load() }

// Wait returns once every request taken has been answered.
func (b *Bridge) Wait() { b.wg.Wait() }

// Close stops taking requests: every one after it is answered at once with
// overloaded, while the ones taken before it are answered as usual.
func (b *Bridge) Close() { b.closing.Store(true) }

// Handle takes one message. It never blocks on the API: a request over a
// limit is answered at once with an error, and one within them is answered
// in its own goroutine.
func (b *Bridge) Handle(m Message) {
	if strings.HasPrefix(m.Topic, b.cfg.MQTT.ResponsePrefix+"/") {
		return
	}
	start := time.Now()
	reply := b.replyTo(m)
	if b.closing.Load() {
		b.fail(reply, m, nil, "overloaded", "the bridge is shutting down: try again shortly", start)
		return
	}
	if len(m.Payload) > b.cfg.Limits.MaxPayloadBytes {
		b.fail(reply, m, nil, "payload_too_large", "the payload is larger than the bridge takes", start)
		return
	}
	if ok, full := b.limits.allow(m.Topic); !ok {
		msg := "too many requests on this topic: try again later"
		if full {
			msg = "too many request topics are active: try again later"
		}
		b.fail(reply, m, nil, "rate_limited", msg, start)
		return
	}
	select {
	case b.sem <- struct{}{}:
	default:
		b.fail(reply, m, nil, "overloaded", "the bridge is answering as many requests as it takes: try again shortly", start)
		return
	}
	b.wg.Add(1)
	b.metrics.inFlight.Inc()
	go func() {
		defer func() {
			<-b.sem
			b.metrics.inFlight.Dec()
			b.wg.Done()
		}()
		b.process(reply, m, start)
	}()
}

func (b *Bridge) process(reply string, m Message, start time.Time) {
	prof, _ := b.cfg.ProfileFor(m.Topic)
	req, id, err := buildRequest(m.Payload, prof, b.cfg.Profiles)
	if err != nil {
		pe, _ := asPayloadError(err)
		b.fail(reply, m, id, pe.kind, pe.message, start)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.Limits.RequestTimeout.D())
	defer cancel()
	var body []byte
	ctx = context.WithValue(ctx, captureKey{}, &body)
	d, err := b.api.Decide(ctx, req)
	if err != nil {
		b.apiError(reply, m, id, err, start)
		return
	}
	out, _ := json.Marshal(envelope{ID: id, RequestID: d.RequestID, Decision: json.RawMessage(body)})
	b.send(reply, m, out, "answered", start, d.RequestID)
}

// replyTo is the topic an answer goes to: the MQTT 5 request's response
// topic when it matches mqtt.response_topics and no subscription, else
// <response_prefix>/<id>, the id being the levels the subscription's
// wildcards matched (or the topic's last level).
func (b *Bridge) replyTo(m Message) string {
	if rt := m.ResponseTopic; topic.ValidName(rt) && !b.subscribed(rt) && b.allowedReply(rt) {
		return rt
	}
	id := m.Topic[strings.LastIndex(m.Topic, "/")+1:]
	for _, f := range b.cfg.MQTT.Subscribe {
		if caps, ok := topic.Captures(f, m.Topic); ok && len(caps) > 0 {
			id = strings.Join(caps, "/")
			break
		}
	}
	return b.cfg.MQTT.ResponsePrefix + "/" + id
}

func (b *Bridge) allowedReply(name string) bool {
	for _, f := range b.cfg.MQTT.ResponseTopics {
		if topic.Match(f, name) {
			return true
		}
	}
	return false
}

func (b *Bridge) subscribed(name string) bool {
	for _, f := range b.cfg.MQTT.Subscribe {
		if topic.Match(f, name) {
			return true
		}
	}
	return false
}

// envelope is what the bridge publishes: the decision exactly as the API
// sent it, or an error.
type envelope struct {
	ID        json.RawMessage `json:"id"`
	RequestID string          `json:"request_id,omitempty"`
	Decision  json.RawMessage `json:"decision,omitempty"`
	Error     *errorBody      `json:"error,omitempty"`
}

type errorBody struct {
	Status    *int    `json:"status"`
	ErrorType *string `json:"error_type"`
	Message   string  `json:"message"`
	RequestID *string `json:"request_id"`
}

// fail answers with one of the bridge's own errors.
func (b *Bridge) fail(reply string, m Message, id json.RawMessage, kind, message string, start time.Time) {
	out, _ := json.Marshal(envelope{ID: id, Error: &errorBody{ErrorType: &kind, Message: message}})
	b.send(reply, m, out, kind, start, "")
}

// apiError answers with the API's error, or the SDK's when nothing came
// back: its status, error_type and message as sent.
func (b *Bridge) apiError(reply string, m Message, id json.RawMessage, err error, start time.Time) {
	e := &errorBody{Message: err.Error()}
	var de *dn.Error
	if errors.As(err, &de) {
		if de.Status != 0 {
			s := de.Status
			e.Status = &s
		}
		if de.ErrorType != "" {
			t := de.ErrorType
			e.ErrorType = &t
		}
		if de.RequestID != "" {
			r := de.RequestID
			e.RequestID = &r
		}
	}
	var te *dn.TimeoutError
	var ce *dn.ConnectionError
	switch {
	case errors.As(err, &te):
		t := "timeout_error"
		e.ErrorType = &t
	case errors.As(err, &ce):
		t := "connection_error"
		e.ErrorType = &t
	case de == nil:
		t := "invalid_payload"
		e.ErrorType = &t
	}
	if e.Message == "" {
		e.Message = "the API answered with an error"
	}
	out, _ := json.Marshal(envelope{ID: id, Error: e})
	rid := ""
	if e.RequestID != nil {
		rid = *e.RequestID
	}
	b.send(reply, m, out, "api_error", start, rid)
}

func (b *Bridge) send(reply string, m Message, payload []byte, result string, start time.Time, requestID string) {
	b.metrics.messages.WithLabelValues(result).Inc()
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	err := b.pub.Publish(ctx, reply, payload, m.CorrelationData)
	elapsed := time.Since(start)
	b.metrics.latency.Observe(elapsed.Seconds())
	attrs := []any{"topic", m.Topic, "reply", reply, "result", result, "ms", elapsed.Milliseconds()}
	if requestID != "" {
		attrs = append(attrs, "request_id", requestID)
	}
	if err != nil {
		b.metrics.pubErrors.Inc()
		b.log.Warn("could not publish the answer", append(attrs, "error", err.Error())...)
		return
	}
	b.log.Info("request", attrs...)
}

type captureKey struct{}

// ResponseHook keeps the body of a decision's response for the request
// whose context asked for it. Pass it to dn.WithResponseHook.
func ResponseHook(ctx context.Context, r dn.ResponseInfo) {
	if p, ok := ctx.Value(captureKey{}).(*[]byte); ok && r.Status >= 200 && r.Status < 300 && r.Path == "/v1/decide" {
		*p = r.Body
	}
}
