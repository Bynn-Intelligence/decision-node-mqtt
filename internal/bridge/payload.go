package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	dn "github.com/Bynn-Intelligence/decision-node-go"

	"github.com/Bynn-Intelligence/decision-node-mqtt/internal/config"
)

// wireRequest is a JSON request payload. Every field is optional; a topic's
// profile fills in what a payload leaves out.
type wireRequest struct {
	// ID is echoed in the answer, for clients without MQTT 5's correlation
	// data.
	ID json.RawMessage `json:"id"`
	// Profile names the profile to start from instead of the topic's.
	Profile   string            `json:"profile"`
	State     json.RawMessage   `json:"state"`
	Questions json.RawMessage   `json:"questions"`
	Model     string            `json:"model"`
	Boosters  []json.RawMessage `json:"boosters"`
	Images    []wireImage       `json:"images"`
	Fallback  *bool             `json:"fallback"`
}

// wireImage is an image by URL, or its bytes as base64 in data.
type wireImage struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Data      string `json:"data"`
	MediaType string `json:"media_type"`
}

// payloadError is a payload the bridge cannot turn into a request.
type payloadError struct {
	kind    string
	message string
}

func (e *payloadError) Error() string { return e.message }

func invalid(format string, args ...any) error {
	return &payloadError{kind: "invalid_payload", message: fmt.Sprintf(format, args...)}
}

// isImage reports a JPEG, PNG or WebP payload, which is sent as the image
// of the topic's profile.
func isImage(b []byte) bool {
	return bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) ||
		bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) ||
		len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP"))
}

// buildRequest turns a payload into a request. prof is the topic's profile,
// nil when the topic has none.
func buildRequest(payload []byte, prof *config.Profile, profiles map[string]config.Profile) (dn.Request, json.RawMessage, error) {
	var req dn.Request
	if isImage(payload) {
		if prof == nil {
			return req, nil, &payloadError{kind: "no_profile", message: "an image payload needs a profile for its topic: set one under topics in the configuration"}
		}
		req = fromProfile(prof)
		id := prof.ImageID
		if id == "" {
			id = "image"
		}
		req.Images = []dn.Image{dn.ImageData(id, payload, "")}
		return req, nil, nil
	}
	var w wireRequest
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return req, nil, invalid("the payload is neither a JPEG, PNG or WebP image nor a JSON request: %v", err)
	}
	if dec.More() {
		return req, nil, invalid("the payload holds more than one JSON value")
	}
	if w.Profile != "" {
		p, ok := profiles[w.Profile]
		if !ok {
			return req, w.ID, &payloadError{kind: "no_profile", message: fmt.Sprintf("no profile %q", w.Profile)}
		}
		prof = &p
	}
	if prof != nil {
		req = fromProfile(prof)
	}
	if len(w.State) > 0 {
		req.State = w.State
	}
	if len(w.Questions) > 0 {
		qs, err := orderedQuestions(w.Questions)
		if err != nil {
			return req, w.ID, err
		}
		req.Questions = qs
	}
	if len(req.Questions) == 0 {
		return req, w.ID, invalid("no questions: send questions, or set a profile for this topic")
	}
	if w.Model != "" {
		req.Model = w.Model
	}
	if w.Boosters != nil {
		req.Boosters = make([]any, len(w.Boosters))
		for i, b := range w.Boosters {
			req.Boosters[i] = b
		}
	}
	if w.Fallback != nil {
		req.Fallback = w.Fallback
	}
	for i, img := range w.Images {
		id := img.ID
		if id == "" {
			id = fmt.Sprintf("image%d", i+1)
		}
		switch {
		case img.URL != "" && img.Data != "":
			return req, w.ID, invalid("image %s: give url or data, not both", id)
		case img.URL != "":
			req.Images = append(req.Images, dn.ImageURL(id, img.URL))
		case img.Data != "":
			im, err := dn.ImageBase64(id, img.Data, img.MediaType)
			if err != nil {
				return req, w.ID, invalid("image %s: %v", id, err)
			}
			req.Images = append(req.Images, im)
		default:
			return req, w.ID, invalid("image %s: give url or data", id)
		}
	}
	return req, w.ID, nil
}

func fromProfile(p *config.Profile) dn.Request {
	req := dn.Request{State: p.State, Model: p.Model, Boosters: p.Boosters, Fallback: p.Fallback}
	for _, q := range p.Questions {
		req.Questions = append(req.Questions, dn.Q(q.ID, q.JSON))
	}
	return req
}

// orderedQuestions reads a JSON object of questions in the order written.
func orderedQuestions(raw json.RawMessage) (dn.Questions, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if d, ok := tok.(json.Delim); err != nil || !ok || d != '{' {
		return nil, invalid("questions must be an object of question id to question")
	}
	var out dn.Questions
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, invalid("questions: %v", err)
		}
		id, _ := tok.(string)
		var q json.RawMessage
		if err := dec.Decode(&q); err != nil {
			return nil, invalid("question %q: %v", id, err)
		}
		out = append(out, dn.Q(id, q))
	}
	return out, nil
}

// asPayloadError reports a payload fault and its kind.
func asPayloadError(err error) (*payloadError, bool) {
	var pe *payloadError
	return pe, errors.As(err, &pe)
}
