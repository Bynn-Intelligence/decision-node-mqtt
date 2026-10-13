package bridge

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/config"
)

var gate = config.Profile{
	State:     "Gate camera 2.",
	Questions: config.Questions{{ID: "vehicle", JSON: json.RawMessage(`{"type":"truth","instructions":"Is a vehicle at the gate?"}`)}},
	ImageID:   "gate",
}

func TestJSONRequest(t *testing.T) {
	req, id, err := buildRequest([]byte(`{"id":"r-7","state":{"temp":81},"questions":{"z":{"type":"truth","instructions":"Too hot?"},"a":{"type":"truth","instructions":"Rising?"}},
		"images":[{"url":"https://files.example.com/a.jpg"}],"boosters":["age"],"model":"decisionnode-flash"}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(id) != `"r-7"` || len(req.Questions) != 2 || req.Questions[0].ID != "z" || req.Questions[1].ID != "a" || req.Model != "decisionnode-flash" ||
		len(req.Images) != 1 || req.Images[0].ID() != "image1" || len(req.Boosters) != 1 {
		t.Fatalf("%+v %s", req, id)
	}
}

func TestProfileFillsIn(t *testing.T) {
	req, _, err := buildRequest([]byte(`{"state":"Gate camera 2 at night."}`), &gate, nil)
	if err != nil || req.State.(json.RawMessage) == nil || len(req.Questions) != 1 || req.Questions[0].ID != "vehicle" {
		t.Fatalf("%+v %v", req, err)
	}
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 16)...)
	req, id, err := buildRequest(jpeg, &gate, nil)
	if err != nil || id != nil || len(req.Images) != 1 || req.Images[0].ID() != "gate" || req.State != "Gate camera 2." {
		t.Fatalf("%+v %v", req, err)
	}
	req, _, err = buildRequest([]byte(`{"profile":"gate","images":[{"id":"gate","data":"`+b64(jpeg)+`"}]}`), nil, map[string]config.Profile{"gate": gate})
	if err != nil || len(req.Questions) != 1 || len(req.Images) != 1 {
		t.Fatalf("%+v %v", req, err)
	}
}

func TestPayloadRefused(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}
	for name, c := range map[string]struct {
		payload string
		prof    *config.Profile
		kind    string
		has     string
	}{
		"image without profile": {string(jpeg), nil, "no_profile", "needs a profile"},
		"not JSON":              {"hello", nil, "invalid_payload", "neither"},
		"unknown field":         {`{"question":{}}`, nil, "invalid_payload", "question"},
		"no questions":          {`{"state":"s"}`, nil, "invalid_payload", "no questions"},
		"two values":            {`{"state":"s"} {}`, &gate, "invalid_payload", "more than one"},
		"unknown profile":       {`{"profile":"x"}`, nil, "no_profile", `"x"`},
		"url and data":          {`{"questions":{"q":{}},"images":[{"url":"https://a/b.jpg","data":"AA=="}]}`, nil, "invalid_payload", "not both"},
		"bad base64":            {`{"questions":{"q":{}},"images":[{"data":"%%%"}]}`, nil, "invalid_payload", "image image1"},
		"questions not object":  {`{"questions":[1]}`, nil, "invalid_payload", "object"},
	} {
		_, _, err := buildRequest([]byte(c.payload), c.prof, nil)
		pe, ok := asPayloadError(err)
		if !ok || pe.kind != c.kind || !strings.Contains(pe.message, c.has) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTopicLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newTopicLimiter(1, 2, 2)
	l.now = func() time.Time { return now }
	for i, want := range []bool{true, true, false} {
		if ok, _ := l.allow("a"); ok != want {
			t.Fatalf("a #%d: %v", i, ok)
		}
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("b")
	}
	if ok, full := l.allow("c"); ok || !full {
		t.Fatal("c should find no room")
	}
	now = now.Add(idleAfter + time.Second)
	if ok, _ := l.allow("c"); !ok {
		t.Fatal("c after the others went idle")
	}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
