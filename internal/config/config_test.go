package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoad(t *testing.T) {
	key := write(t, "key", "dn_live_test\n")
	p := write(t, "c.yaml", `
api:
  key_file: `+key+`
  model: decisionnode-flash
mqtt:
  url: mqtts://broker.example.com:8883
  subscribe: [factory/+/request]
  response_prefix: factory/answers
profiles:
  gate:
    state: Gate camera 2.
    questions:
      vehicle: {type: truth, instructions: "Is a vehicle at the gate?"}
      count: {type: number, instructions: "How many people?", min: 0, max: 20}
    image_id: gate
topics:
  - filter: factory/gate2/request
    profile: gate
`)
	c, err := Load(p, env(map[string]string{"DN_MQTT_URL": "mqtt://other:1883"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "dn_live_test" || c.API.Model != "decisionnode-flash" || c.MQTT.URL != "mqtt://other:1883" {
		t.Fatalf("%+v", c)
	}
	prof, name := c.ProfileFor("factory/gate2/request")
	if prof == nil || name != "gate" || len(prof.Questions) != 2 || prof.Questions[0].ID != "vehicle" || prof.Questions[1].ID != "count" {
		t.Fatalf("profile: %+v %q", prof, name)
	}
	if got := string(prof.Questions[1].JSON); got != `{"instructions":"How many people?","max":20,"min":0,"type":"number"}` {
		t.Fatalf("question JSON: %s", got)
	}
	if p, _ := c.ProfileFor("factory/gate3/request"); p != nil {
		t.Fatal("a topic without a profile")
	}
}

func TestLoadRefuses(t *testing.T) {
	withKey := map[string]string{"DECISIONNODE_API_KEY": "dn_live_test"}
	for name, c := range map[string]struct {
		yaml string
		env  map[string]string
		want string
	}{
		"no key":             {"", nil, "no API key"},
		"key in the file":    {"api:\n  api_key: dn_live_x\n", withKey, "api_key"},
		"answers as request": {"mqtt:\n  subscribe: ['decisionnode/#']\n", withKey, "also matches the answers"},
		"bad filter":         {"mqtt:\n  subscribe: ['a/#/b']\n", withKey, "not a topic filter"},
		"unknown profile":    {"topics:\n  - {filter: a/b, profile: nope}\n", withKey, `no profile "nope"`},
		"qos 2":              {"mqtt:\n  qos: 2\n", withKey, "qos"},
		"cert without key":   {"mqtt:\n  cert_file: c.pem\n", withKey, "go together"},
	} {
		_, err := Load(write(t, "c.yaml", c.yaml), env(c.env))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEmptyFileUsesDefaults(t *testing.T) {
	c, err := Load(write(t, "c.yaml", ""), env(map[string]string{"DECISIONNODE_API_KEY": " k "}))
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "k" || c.MQTT.Subscribe[0] != "decisionnode/request/+" || c.Limits.MaxInFlight != 16 || !strings.HasPrefix(c.MQTT.ClientID, "decision-node-mqtt-") {
		t.Fatalf("%+v", c)
	}
}

func TestProfileBoosters(t *testing.T) {
	p := write(t, "c.yaml", `
profiles:
  drone:
    image_id: front
    boosters:
      - bynn_depth
      - name: bynn_segment
        options:
          prompt: [purple gate, orange H landing pad]
          max_instances: 4
    questions:
      clear: {type: truth, instructions: "Is the path straight ahead clear for 2 meters?"}
`)
	c, err := Load(p, env(map[string]string{"DECISIONNODE_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c.Profiles["drone"].Boosters)
	if err != nil {
		t.Fatal(err)
	}
	want := `["bynn_depth",{"name":"bynn_segment","options":{"max_instances":4,"prompt":["purple gate","orange H landing pad"]}}]`
	if string(b) != want {
		t.Fatalf("boosters: %s", b)
	}
}

func TestExampleLoads(t *testing.T) {
	c, err := Load("../../config.example.yaml", env(map[string]string{"DECISIONNODE_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if p, name := c.ProfileFor("decisionnode/request/gate-north"); p == nil || name != "gate" || len(p.Questions) != 2 {
		t.Fatalf("%+v", p)
	}
}
