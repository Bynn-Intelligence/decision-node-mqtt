package bridge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	dn "github.com/Bynn-Intelligence/decision-node-go"

	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/config"
)

type sent struct {
	topic       string
	payload     envelope
	correlation string
}

type fakePub struct {
	mu  sync.Mutex
	out []sent
	got chan struct{}
}

func (f *fakePub) Publish(_ context.Context, topic string, payload, correlation []byte) error {
	var e envelope
	_ = json.Unmarshal(payload, &e)
	f.mu.Lock()
	f.out = append(f.out, sent{topic, e, string(correlation)})
	f.mu.Unlock()
	f.got <- struct{}{}
	return nil
}

// blockingAPI answers once release is closed.
type blockingAPI struct{ release chan struct{} }

func (b *blockingAPI) Decide(ctx context.Context, _ dn.Request) (*dn.Decision, error) {
	<-b.release
	if p, ok := ctx.Value(captureKey{}).(*[]byte); ok {
		*p = []byte(`{"answers":{}}`)
	}
	return &dn.Decision{RequestID: "req_1"}, nil
}

func newTestBridge(maxInFlight int) (*Bridge, *fakePub, *blockingAPI) {
	cfg := config.Defaults()
	cfg.Limits.MaxInFlight = maxInFlight
	cfg.Limits.RatePerSecond, cfg.Limits.Burst = 100, 100
	cfg.MQTT.ResponseTopics = []string{"decisionnode/response/#", "app/#"}
	pub := &fakePub{got: make(chan struct{}, 16)}
	api := &blockingAPI{release: make(chan struct{})}
	return New(cfg, api, pub, slog.New(slog.NewTextHandler(io.Discard, nil)), NewMetrics()), pub, api
}

func wait(t *testing.T, p *fakePub, n int) []sent {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-p.got:
		case <-time.After(5 * time.Second):
			t.Fatalf("%d of %d answers", i, n)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sent(nil), p.out...)
}

func TestOverloadedIsAnsweredAtOnce(t *testing.T) {
	b, pub, api := newTestBridge(1)
	q := []byte(`{"questions":{"q":{"type":"truth","instructions":"?"}}}`)
	b.Handle(Message{Topic: "decisionnode/request/a", Payload: q})
	b.Handle(Message{Topic: "decisionnode/request/b", Payload: q})
	out := wait(t, pub, 1)
	if out[0].topic != "decisionnode/response/b" || out[0].payload.Error == nil || *out[0].payload.Error.ErrorType != "overloaded" {
		t.Fatalf("%+v", out[0])
	}
	close(api.release)
	b.Wait()
	out = wait(t, pub, 1)
	if out[1].topic != "decisionnode/response/a" || out[1].payload.Error != nil || string(out[1].payload.Decision) != `{"answers":{}}` {
		t.Fatalf("%+v", out[1])
	}
}

func TestResponseTopicNeverARequestTopic(t *testing.T) {
	b, pub, api := newTestBridge(4)
	close(api.release)
	q := []byte(`{"questions":{"q":{"type":"truth","instructions":"?"}}}`)
	// A response topic the bridge itself subscribes to would loop: the
	// answer goes to the default topic instead.
	b.Handle(Message{Topic: "decisionnode/request/a", Payload: q, ResponseTopic: "decisionnode/request/x", CorrelationData: []byte("c")})
	b.Handle(Message{Topic: "decisionnode/request/b", Payload: q, ResponseTopic: "app/b", CorrelationData: []byte("d")})
	// A response topic outside mqtt.response_topics is not used either.
	b.Handle(Message{Topic: "decisionnode/request/c", Payload: q, ResponseTopic: "home/door/command", CorrelationData: []byte("e")})
	b.Wait()
	out := wait(t, pub, 3)
	topics := map[string]string{}
	for _, s := range out {
		topics[s.topic] = s.correlation
	}
	if topics["decisionnode/response/a"] != "c" || topics["app/b"] != "d" || topics["decisionnode/response/c"] != "e" || len(topics) != 3 {
		t.Fatalf("%+v", out)
	}
	// An answer arriving on a request filter is never taken as a request.
	b.Handle(Message{Topic: "decisionnode/response/a", Payload: q})
	b.Wait()
	select {
	case <-pub.got:
		t.Fatal("answered an answer")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestClosedBridgeTakesNothing(t *testing.T) {
	b, pub, api := newTestBridge(4)
	close(api.release)
	b.Close()
	b.Handle(Message{Topic: "decisionnode/request/a", Payload: []byte(`{"questions":{"q":{}}}`)})
	out := wait(t, pub, 1)
	if out[0].payload.Error == nil || *out[0].payload.Error.ErrorType != "overloaded" {
		t.Fatalf("%+v", out[0])
	}
}
