package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/config"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// broker starts an embedded MQTT 5 broker that lets every client in.
func broker(t *testing.T) string {
	t.Helper()
	addr := freeAddr(t)
	s := mqtt.New(&mqtt.Options{InlineClient: false, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := s.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddListener(listeners.NewTCP(listeners.Config{ID: "t", Address: addr})); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })
	return addr
}

// api is a mock DecisionNode API: a truth answer for every question, and
// the API's 422 for a request to the model "bad".
type api struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.bodies = append(a.bodies, body)
	a.mu.Unlock()
	var req struct {
		Model     string                     `json:"model"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	_ = json.Unmarshal(body, &req)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-request-id", "req_0000000000000000000000000001")
	if req.Model == "bad" {
		w.WriteHeader(422)
		_, _ = io.WriteString(w, `{"detail":[{"type":"missing","loc":["body","model"],"msg":"Unknown model","input":"bad"}]}`)
		return
	}
	answers := map[string]any{}
	for id := range req.Questions {
		answers[id] = map[string]any{"type": "truth", "truth": 0.94}
	}
	out, _ := json.Marshal(map[string]any{"model": "decisionnode-moe-0.9.0", "answers": answers, "usage": map[string]any{"input_tokens": 120, "output_tokens": 0}})
	_, _ = w.Write(out)
}

func (a *api) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.bodies)
}

func (a *api) last() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.bodies) == 0 {
		return nil
	}
	return a.bodies[len(a.bodies)-1]
}

// client is a test MQTT 5 client that collects what arrives on a filter.
type client struct {
	c   *paho.Client
	got chan *paho.Publish
}

func connectClient(t *testing.T, addr, id string, filter string) *client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	cl := &client{got: make(chan *paho.Publish, 16)}
	cl.c = paho.NewClient(paho.ClientConfig{
		Conn: conn, ClientID: id,
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(pr paho.PublishReceived) (bool, error) {
			cl.got <- pr.Packet
			return true, nil
		}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cl.c.Connect(ctx, &paho.Connect{ClientID: id, KeepAlive: 30, CleanStart: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.c.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: filter, QoS: 1}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.c.Disconnect(&paho.Disconnect{}) })
	return cl
}

func (cl *client) publish(t *testing.T, topic string, payload []byte, props *paho.PublishProperties) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cl.c.Publish(ctx, &paho.Publish{Topic: topic, QoS: 1, Payload: payload, Properties: props}); err != nil {
		t.Fatal(err)
	}
}

type answer struct {
	ID        json.RawMessage `json:"id"`
	RequestID string          `json:"request_id"`
	Decision  struct {
		Answers map[string]struct {
			Truth float64 `json:"truth"`
		} `json:"answers"`
	} `json:"decision"`
	Error *struct {
		Status    *int    `json:"status"`
		ErrorType *string `json:"error_type"`
		Message   string  `json:"message"`
	} `json:"error"`
}

func (cl *client) next(t *testing.T) (*paho.Publish, answer) {
	t.Helper()
	select {
	case p := <-cl.got:
		var a answer
		if err := json.Unmarshal(p.Payload, &a); err != nil {
			t.Fatalf("answer %s: %v", p.Payload, err)
		}
		return p, a
	case <-time.After(10 * time.Second):
		t.Fatal("no answer")
	}
	return nil, answer{}
}

func smallJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBridgeEndToEnd(t *testing.T) {
	addr := broker(t)
	mock := &api{}
	srv := httptest.NewServer(mock)
	defer srv.Close()
	httpAddr := freeAddr(t)

	cfg := config.Defaults()
	cfg.APIKey = "dn_test_key"
	cfg.API.BaseURL = srv.URL
	cfg.MQTT.URL = "mqtt://" + addr
	cfg.MQTT.ClientID = "bridge"
	cfg.HTTP.Listen = httpAddr
	cfg.MQTT.ResponseTopics = []string{"decisionnode/response/#", "app/replies/#"}
	cfg.Limits.MaxPayloadBytes = 64 << 10
	cfg.Limits.RatePerSecond = 0.001
	cfg.Limits.Burst = 2
	cfg.Profiles = map[string]config.Profile{"gate": {
		State:     "Gate camera 2.",
		Questions: config.Questions{{ID: "vehicle", JSON: json.RawMessage(`{"type":"truth","instructions":"Is a vehicle at the gate?"}`)}},
		ImageID:   "gate",
	}}
	cfg.Topics = []config.TopicProfile{{Filter: "decisionnode/request/gate2", Profile: "gate"}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test") }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitHealthy(t, httpAddr)

	dev := connectClient(t, addr, "device", "decisionnode/response/#")
	app := connectClient(t, addr, "app", "app/replies/#")

	// MQTT 5: the request's response topic and correlation data.
	dev.publish(t, "decisionnode/request/dev1", []byte(`{"state":"Order 4411: I want my money back.","questions":{"refund":{"type":"truth","instructions":"Is this a refund request?"}}}`),
		&paho.PublishProperties{ResponseTopic: "app/replies/1", CorrelationData: []byte("c-1")})
	p, a := app.next(t)
	if p.Topic != "app/replies/1" || p.Properties == nil || string(p.Properties.CorrelationData) != "c-1" || a.Decision.Answers["refund"].Truth != 0.94 || a.RequestID == "" {
		t.Fatalf("MQTT 5 answer on %s: %s", p.Topic, p.Payload)
	}
	if !strings.Contains(string(mock.last()), `"questions":{"refund"`) {
		t.Fatalf("request sent: %s", mock.last())
	}

	// No response topic: the id field, answered on decisionnode/response/<id>.
	dev.publish(t, "decisionnode/request/dev2", []byte(`{"id":"r-7","questions":{"q":{"type":"truth","instructions":"?"}}}`), nil)
	p, a = dev.next(t)
	if p.Topic != "decisionnode/response/dev2" || string(a.ID) != `"r-7"` || a.Error != nil {
		t.Fatalf("answer on %s: %s", p.Topic, p.Payload)
	}

	// A camera publishes only JPEG bytes; the topic's profile asks.
	dev.publish(t, "decisionnode/request/gate2", smallJPEG(t), nil)
	p, a = dev.next(t)
	if p.Topic != "decisionnode/response/gate2" || a.Decision.Answers["vehicle"].Truth != 0.94 {
		t.Fatalf("camera answer on %s: %s", p.Topic, p.Payload)
	}
	if body := string(mock.last()); !strings.Contains(body, `"id":"gate"`) || !strings.Contains(body, `"data":"`) || !strings.Contains(body, "Gate camera 2.") {
		t.Fatalf("camera request sent: %.300s", body)
	}

	// The API's error, as sent.
	dev.publish(t, "decisionnode/request/dev3", []byte(`{"model":"bad","questions":{"q":{"type":"truth","instructions":"?"}}}`), nil)
	_, a = dev.next(t)
	if a.Error == nil || a.Error.Status == nil || *a.Error.Status != 422 {
		t.Fatalf("API error: %+v", a)
	}

	// Over the size limit: refused without calling the API.
	sent := mock.count()
	dev.publish(t, "decisionnode/request/dev4", bytes.Repeat([]byte("x"), 65<<10), nil)
	_, a = dev.next(t)
	if a.Error == nil || *a.Error.ErrorType != "payload_too_large" || a.Error.Status != nil || mock.count() != sent {
		t.Fatalf("too large: %+v", a)
	}

	// The third request on one topic within the burst of two.
	for i := 0; i < 3; i++ {
		dev.publish(t, "decisionnode/request/dev5", []byte(`{"questions":{"q":{"type":"truth","instructions":"?"}}}`), nil)
	}
	kinds := map[string]int{}
	for i := 0; i < 3; i++ {
		_, a = dev.next(t)
		if a.Error != nil {
			kinds[*a.Error.ErrorType]++
		} else {
			kinds["answered"]++
		}
	}
	if kinds["answered"] != 2 || kinds["rate_limited"] != 1 {
		t.Fatalf("rate limit: %v", kinds)
	}

	// The image payload on a topic without a profile.
	dev.publish(t, "decisionnode/request/dev6", smallJPEG(t), nil)
	_, a = dev.next(t)
	if a.Error == nil || *a.Error.ErrorType != "no_profile" {
		t.Fatalf("no profile: %+v", a)
	}

	metrics := get(t, "http://"+httpAddr+"/metrics")
	for _, want := range []string{`decisionnode_mqtt_requests_total{result="answered"} 5`, `decisionnode_mqtt_requests_total{result="rate_limited"} 1`, "decisionnode_mqtt_connected 1"} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

func waitHealthy(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := http.Get("http://" + addr + "/healthz"); err == nil {
			_ = r.Body.Close()
			if r.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the bridge never became healthy")
}

func get(t *testing.T, url string) string {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}
