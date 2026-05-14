package testutil

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

type RecordedUpdate struct {
	Timestamp string        `json:"ts"`
	UpdateID  int           `json:"update_id"`
	Raw       telego.Update `json:"update"`
}

type Recorder struct {
	mu      sync.Mutex
	file    *os.File
	encoder *json.Encoder
	count   int
}

func NewRecorder(path string) (*Recorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open recorder file: %w", err)
	}
	return &Recorder{
		file:    f,
		encoder: json.NewEncoder(f),
	}, nil
}

func (r *Recorder) Middleware() th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		r.mu.Lock()
		r.count++
		r.encoder.Encode(RecordedUpdate{
			Timestamp: time.Now().Format(time.RFC3339Nano),
			UpdateID:  update.UpdateID,
			Raw:       update,
		})
		r.mu.Unlock()

		return ctx.Next(update)
	}
}

func (r *Recorder) Close() error {
	return r.file.Close()
}

func (r *Recorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func LoadRecording(path string) ([]RecordedUpdate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var updates []RecordedUpdate
	dec := json.NewDecoder(f)
	for dec.More() {
		var u RecordedUpdate
		if err := dec.Decode(&u); err != nil {
			return updates, err
		}
		updates = append(updates, u)
	}
	return updates, nil
}
