// Package service runs the bridge: the broker connection, the API client
// and the health and metrics server.
package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	dn "github.com/Bynn-Intelligence/decision-node-go"

	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/bridge"
	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/config"
)

// drainTimeout is how long a shutdown waits for the requests already taken.
const drainTimeout = 60 * time.Second

// Run answers requests until ctx ends, then answers the requests already
// taken (for up to a minute) and disconnects.
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger, version string) error {
	opts := []dn.Option{
		dn.WithAPIKey(cfg.APIKey),
		dn.WithTimeout(cfg.API.Timeout.D()),
		dn.WithResponseHook(bridge.ResponseHook),
		dn.WithHeaders(map[string]string{"User-Agent": "decision-node-mqtt/" + version + " " + dn.UserAgent}),
	}
	if cfg.API.BaseURL != "" {
		opts = append(opts, dn.WithBaseURL(cfg.API.BaseURL))
	}
	if cfg.API.Model != "" {
		opts = append(opts, dn.WithModel(cfg.API.Model))
	}
	if cfg.API.Retries != nil {
		opts = append(opts, dn.WithMaxRetries(*cfg.API.Retries))
	}
	api := dn.NewClient(opts...)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	metrics := bridge.NewMetrics()
	pub := &publisher{qos: cfg.MQTT.QoS}
	b := bridge.New(cfg, api, pub, log, metrics)

	cm, err := connect(ctx, cfg, b, log)
	if err != nil {
		return err
	}
	pub.cm = cm

	srv := &http.Server{Addr: cfg.HTTP.Listen, Handler: handler(b, metrics), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("the health and metrics server stopped", "error", err.Error())
			stop()
		}
	}()
	log.Info("started", "version", version, "broker", redact(cfg.MQTT.URL), "subscribe", cfg.MQTT.Subscribe, "http", cfg.HTTP.Listen)

	<-ctx.Done()
	b.Close()
	log.Info("stopping: answering the requests already taken")
	done := make(chan struct{})
	go func() { b.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(drainTimeout):
		log.Warn("stopping before every request was answered")
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = cm.Disconnect(shut)
	_ = srv.Shutdown(shut)
	return nil
}

// connect opens the broker connection, which reconnects by itself, and
// subscribes to the request topics on every connection.
func connect(ctx context.Context, cfg *config.Config, b *bridge.Bridge, log *slog.Logger) (*autopaho.ConnectionManager, error) {
	u, err := url.Parse(cfg.MQTT.URL)
	if err != nil {
		return nil, fmt.Errorf("mqtt.url: %w", err)
	}
	tlsCfg, err := tlsConfig(cfg.MQTT)
	if err != nil {
		return nil, err
	}
	subs := make([]paho.SubscribeOptions, len(cfg.MQTT.Subscribe))
	for i, f := range cfg.MQTT.Subscribe {
		subs[i] = paho.SubscribeOptions{Topic: f, QoS: cfg.MQTT.QoS}
	}
	cc := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{u},
		TlsCfg:                        tlsCfg,
		KeepAlive:                     uint16(cfg.MQTT.KeepAlive.D().Seconds()),
		CleanStartOnInitialConnection: true,
		ConnectUsername:               cfg.MQTT.Username,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			go func() {
				sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if _, err := cm.Subscribe(sctx, &paho.Subscribe{Subscriptions: subs}); err != nil {
					log.Error("could not subscribe", "error", err.Error())
					return
				}
				b.SetConnected(true)
				log.Info("connected and subscribed")
			}()
		},
		OnConnectionDown: func() bool {
			b.SetConnected(false)
			log.Warn("the broker connection is down; reconnecting")
			return true
		},
		OnConnectError: func(err error) { log.Warn("could not connect to the broker", "error", err.Error()) },
		ClientConfig: paho.ClientConfig{
			ClientID: cfg.MQTT.ClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					m := bridge.Message{Topic: pr.Packet.Topic, Payload: pr.Packet.Payload}
					if p := pr.Packet.Properties; p != nil {
						m.ResponseTopic, m.CorrelationData = p.ResponseTopic, p.CorrelationData
					}
					b.Handle(m)
					return true, nil
				},
			},
			OnClientError: func(err error) { log.Warn("broker connection error", "error", err.Error()) },
		},
	}
	if cfg.Password != "" {
		cc.ConnectPassword = []byte(cfg.Password)
	}
	return autopaho.NewConnection(ctx, cc)
}

// tlsConfig is the broker connection's TLS: the CA file instead of the
// system's roots when given, and the client certificate when given.
func tlsConfig(m config.MQTT) (*tls.Config, error) {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if m.CAFile != "" {
		pem, err := os.ReadFile(m.CAFile)
		if err != nil {
			return nil, fmt.Errorf("mqtt.ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("mqtt.ca_file: no certificate in %s", m.CAFile)
		}
		c.RootCAs = pool
	}
	if m.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(m.CertFile, m.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("mqtt.cert_file and key_file: %w", err)
		}
		c.Certificates = []tls.Certificate{cert}
	}
	return c, nil
}

// publisher publishes answers on the connection.
type publisher struct {
	cm  *autopaho.ConnectionManager
	qos byte
}

func (p *publisher) Publish(ctx context.Context, topic string, payload, correlation []byte) error {
	if p.cm == nil {
		return errors.New("not connected")
	}
	_, err := p.cm.Publish(ctx, &paho.Publish{
		Topic: topic, QoS: p.qos, Payload: payload,
		Properties: &paho.PublishProperties{ContentType: "application/json", CorrelationData: correlation},
	})
	return err
}

// handler serves /healthz (200 while connected to the broker, else 503) and
// /metrics.
func handler(b *bridge.Bridge, m *bridge.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !b.Connected() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"disconnected"}` + "\n"))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	return mux
}

// redact drops a URL's user information before it is logged.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unreadable)"
	}
	u.User = nil
	return u.String()
}
