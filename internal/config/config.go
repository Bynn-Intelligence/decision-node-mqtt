// Package config reads the bridge's settings from a YAML file and the
// environment. The API key is read from the environment or from a file,
// never from the YAML itself, a topic or a log.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/topic"
)

// Config is the bridge's whole configuration.
type Config struct {
	API      API                `yaml:"api"`
	MQTT     MQTT               `yaml:"mqtt"`
	Limits   Limits             `yaml:"limits"`
	HTTP     HTTP               `yaml:"http"`
	Profiles map[string]Profile `yaml:"profiles"`
	// Topics maps request topics to profiles, first match first.
	Topics []TopicProfile `yaml:"topics"`

	// APIKey is read from DECISIONNODE_API_KEY or API.KeyFile.
	APIKey string `yaml:"-"`
	// Password is read from DN_MQTT_PASSWORD or MQTT.PasswordFile.
	Password string `yaml:"-"`
}

// API is how the bridge reaches DecisionNode.
type API struct {
	// BaseURL is the API's address; empty means the hosted API.
	BaseURL string `yaml:"base_url"`
	// KeyFile holds the API key, used when DECISIONNODE_API_KEY is unset.
	KeyFile string `yaml:"key_file"`
	// Model is the model a request without one is sent to.
	Model string `yaml:"model"`
	// Timeout is the time allowed for each attempt.
	Timeout Duration `yaml:"timeout"`
	// Retries is how often a temporary failure is tried again.
	Retries *int `yaml:"retries"`
}

// MQTT is the broker connection.
type MQTT struct {
	// URL is the broker: mqtt:// or tcp:// (1883), mqtts://, ssl:// or tls://
	// (8883), ws:// or wss://.
	URL      string `yaml:"url"`
	ClientID string `yaml:"client_id"`
	Username string `yaml:"username"`
	// PasswordFile holds the password, used when DN_MQTT_PASSWORD is unset.
	PasswordFile string `yaml:"password_file"`
	// CAFile verifies the broker's certificate instead of the system's roots.
	CAFile string `yaml:"ca_file"`
	// CertFile and KeyFile are the bridge's client certificate.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// KeepAlive is the MQTT keep-alive interval.
	KeepAlive Duration `yaml:"keep_alive"`
	// QoS is used for the subscriptions and the responses, 0 or 1.
	QoS byte `yaml:"qos"`
	// Subscribe are the request topic filters.
	Subscribe []string `yaml:"subscribe"`
	// ResponsePrefix is where answers go: <prefix>/<id>.
	ResponsePrefix string `yaml:"response_prefix"`
	// ResponseTopics are the filters an MQTT 5 request's own response topic
	// must match to be used; empty means <response_prefix>/#. A device can
	// thus never make the bridge publish anywhere else.
	ResponseTopics []string `yaml:"response_topics"`
}

// Limits bound what the bridge takes on. Over a limit a request is dropped
// with an error response; nothing is ever queued without bound.
type Limits struct {
	// MaxPayloadBytes is the largest request payload taken.
	MaxPayloadBytes int `yaml:"max_payload_bytes"`
	// MaxInFlight is how many requests run at once across all topics.
	MaxInFlight int `yaml:"max_in_flight"`
	// RatePerSecond and Burst limit each request topic.
	RatePerSecond float64 `yaml:"rate_per_second"`
	Burst         int     `yaml:"burst"`
	// MaxTopics is how many request topics have a rate limit held at once.
	MaxTopics int `yaml:"max_topics"`
	// RequestTimeout bounds one request, its retries included.
	RequestTimeout Duration `yaml:"request_timeout"`
}

// HTTP serves /healthz and /metrics.
type HTTP struct {
	Listen string `yaml:"listen"`
}

// Profile is a named request a topic's payloads fill in: an image payload
// becomes its image, a JSON payload's fields replace its own.
type Profile struct {
	State     any       `yaml:"state"`
	Questions Questions `yaml:"questions"`
	Model     string    `yaml:"model"`
	Boosters  []any     `yaml:"boosters"`
	ImageID   string    `yaml:"image_id"`
	Fallback  *bool     `yaml:"fallback"`
}

// TopicProfile uses a profile for the request topics a filter matches.
type TopicProfile struct {
	Filter  string `yaml:"filter"`
	Profile string `yaml:"profile"`
}

// Question is one question of a profile, in its JSON wire form.
type Question struct {
	ID   string
	JSON json.RawMessage
}

// Questions are a profile's questions in the order written.
type Questions []Question

// UnmarshalYAML keeps the questions' order and turns each into JSON.
func (q *Questions) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return errors.New("questions must be a mapping of question id to question")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		var v any
		if err := n.Content[i+1].Decode(&v); err != nil {
			return err
		}
		b, err := json.Marshal(jsonable(v))
		if err != nil {
			return fmt.Errorf("question %q: %w", n.Content[i].Value, err)
		}
		*q = append(*q, Question{ID: n.Content[i].Value, JSON: b})
	}
	return nil
}

// jsonable turns YAML's decoded maps into ones JSON can encode.
func jsonable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = jsonable(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = jsonable(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = jsonable(e)
		}
	}
	return v
}

// Duration is a time.Duration written as "30s" or "1m".
type Duration time.Duration

// UnmarshalYAML reads a Go duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("%q is not a duration such as 30s", n.Value)
	}
	*d = Duration(v)
	return nil
}

// D returns the duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Load reads the file (optional: "" uses the defaults and the environment
// only), applies the environment and checks the result.
func Load(path string, getenv func(string) string) (*Config, error) {
	c := Defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read the configuration: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		// An unknown key is an error, so a key written into the file by
		// mistake (api_key, password) is refused rather than ignored.
		dec.KnownFields(true)
		if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	for _, e := range []struct {
		name string
		dst  *string
	}{
		{"DECISIONNODE_BASE_URL", &c.API.BaseURL},
		{"DECISIONNODE_MODEL", &c.API.Model},
		{"DN_MQTT_URL", &c.MQTT.URL},
		{"DN_MQTT_CLIENT_ID", &c.MQTT.ClientID},
		{"DN_MQTT_USERNAME", &c.MQTT.Username},
		{"DN_MQTT_HTTP_LISTEN", &c.HTTP.Listen},
	} {
		if v := getenv(e.name); v != "" {
			*e.dst = v
		}
	}
	key, err := secret(getenv("DECISIONNODE_API_KEY"), c.API.KeyFile, "the API key")
	if err != nil {
		return nil, err
	}
	c.APIKey = key
	pass, err := secret(getenv("DN_MQTT_PASSWORD"), c.MQTT.PasswordFile, "the MQTT password")
	if err != nil {
		return nil, err
	}
	c.Password = pass
	if len(c.MQTT.ResponseTopics) == 0 {
		c.MQTT.ResponseTopics = []string{c.MQTT.ResponsePrefix + "/#"}
	}
	if c.MQTT.ClientID == "" {
		host, _ := os.Hostname()
		c.MQTT.ClientID = "decision-node-mqtt-" + strings.ToLower(host)
	}
	return c, c.check()
}

// secret is the environment's value, else the file's, trimmed.
func secret(env, file, what string) (string, error) {
	if env != "" {
		return strings.TrimSpace(env), nil
	}
	if file == "" {
		return "", nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read %s from %s: %w", what, file, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Defaults are the settings before the file and the environment.
func Defaults() *Config {
	return &Config{
		API: API{Timeout: Duration(30 * time.Second)},
		MQTT: MQTT{
			URL:            "mqtt://localhost:1883",
			KeepAlive:      Duration(30 * time.Second),
			QoS:            1,
			Subscribe:      []string{"decisionnode/request/+"},
			ResponsePrefix: "decisionnode/response",
		},
		Limits: Limits{
			MaxPayloadBytes: 4 << 20,
			MaxInFlight:     16,
			RatePerSecond:   1,
			Burst:           5,
			MaxTopics:       10000,
			RequestTimeout:  Duration(90 * time.Second),
		},
		HTTP: HTTP{Listen: ":8080"},
	}
}

func (c *Config) check() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if c.APIKey == "" {
		add("no API key: set DECISIONNODE_API_KEY or api.key_file")
	}
	if c.MQTT.QoS > 1 {
		add("mqtt.qos must be 0 or 1, got %d", c.MQTT.QoS)
	}
	if len(c.MQTT.Subscribe) == 0 {
		add("mqtt.subscribe needs at least one topic filter")
	}
	if !topic.ValidName(c.MQTT.ResponsePrefix) || c.MQTT.ResponsePrefix == "" {
		add("mqtt.response_prefix %q is not a topic name", c.MQTT.ResponsePrefix)
	}
	for _, f := range c.MQTT.Subscribe {
		if !topic.ValidFilter(f) {
			add("mqtt.subscribe: %q is not a topic filter", f)
			continue
		}
		// An answer must never arrive back as a request.
		if topic.Match(f, c.MQTT.ResponsePrefix+"/x") {
			add("mqtt.subscribe: %q also matches the answers under %q", f, c.MQTT.ResponsePrefix)
		}
	}
	for _, f := range c.MQTT.ResponseTopics {
		if !topic.ValidFilter(f) {
			add("mqtt.response_topics: %q is not a topic filter", f)
		}
	}
	if (c.MQTT.CertFile == "") != (c.MQTT.KeyFile == "") {
		add("mqtt.cert_file and mqtt.key_file go together")
	}
	l := c.Limits
	if l.MaxPayloadBytes < 1 || l.MaxInFlight < 1 || l.RatePerSecond <= 0 || l.Burst < 1 || l.MaxTopics < 1 || l.RequestTimeout <= 0 {
		add("limits: max_payload_bytes, max_in_flight, rate_per_second, burst, max_topics and request_timeout must all be above 0")
	}
	if c.API.Timeout <= 0 {
		add("api.timeout must be above 0")
	}
	if c.API.Retries != nil && (*c.API.Retries < 0 || *c.API.Retries > 10) {
		add("api.retries must be 0 to 10, got %d", *c.API.Retries)
	}
	for name, p := range c.Profiles {
		if len(p.Questions) == 0 {
			add("profile %q has no questions", name)
		}
	}
	for i, t := range c.Topics {
		if !topic.ValidFilter(t.Filter) {
			add("topics[%d]: %q is not a topic filter", i, t.Filter)
		}
		if _, ok := c.Profiles[t.Profile]; !ok {
			add("topics[%d]: no profile %q", i, t.Profile)
		}
	}
	if len(problems) > 0 {
		return errors.New("configuration: " + strings.Join(problems, "; "))
	}
	return nil
}

// ProfileFor is the profile a request topic uses, or nil.
func (c *Config) ProfileFor(name string) (*Profile, string) {
	for _, t := range c.Topics {
		if topic.Match(t.Filter, name) {
			p := c.Profiles[t.Profile]
			return &p, t.Profile
		}
	}
	return nil, ""
}
