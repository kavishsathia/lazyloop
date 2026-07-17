package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func Load(directory string) (*Fixture, error) {
	manifestPayload, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return nil, err
	}
	fixture := &Fixture{}
	if err := json.Unmarshal(manifestPayload, &fixture.Manifest); err != nil {
		return nil, err
	}
	if fixture.Manifest.Schema != SchemaVersion {
		return nil, fmt.Errorf("unsupported trace schema %q", fixture.Manifest.Schema)
	}
	events, err := os.Open(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer events.Close()
	scanner := bufio.NewScanner(events)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode trace event %d: %w", len(fixture.Records)+1, err)
		}
		fixture.Records = append(fixture.Records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if fixture.Manifest.EventCount != len(fixture.Records) {
		return nil, fmt.Errorf("manifest has %d events but fixture contains %d", fixture.Manifest.EventCount, len(fixture.Records))
	}
	return fixture, nil
}

func Write(directory string, fixture Fixture) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	fixture.Manifest.Schema = SchemaVersion
	fixture.Manifest.EventCount = len(fixture.Records)
	manifest, err := json.MarshalIndent(fixture.Manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(directory, "manifest.json"), append(manifest, '\n')); err != nil {
		return err
	}
	var events []byte
	for _, record := range fixture.Records {
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		events = append(events, line...)
		events = append(events, '\n')
	}
	return writeFile(filepath.Join(directory, "events.jsonl"), events)
}

func writeFile(path string, payload []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".trace-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

type legacyRecord struct {
	CapturedAt time.Time       `json:"captured_at"`
	Input      json.RawMessage `json:"input"`
}

func ImportLegacy(reader io.Reader, sessionID string, manifest Manifest) (*Fixture, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var captured []legacyRecord
	for scanner.Scan() {
		var record legacyRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		var identity struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(record.Input, &identity) != nil || identity.SessionID != sessionID {
			continue
		}
		captured = append(captured, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(captured) == 0 {
		return nil, fmt.Errorf("capture contains no events for session %s", sessionID)
	}
	sort.SliceStable(captured, func(i, j int) bool { return captured[i].CapturedAt.Before(captured[j].CapturedAt) })
	redactor := NewRedactor()
	fixture := &Fixture{Manifest: manifest}
	fixture.Manifest.Schema = SchemaVersion
	fixture.Manifest.Source = "live"
	fixture.Manifest.RecordedAt = captured[0].CapturedAt
	fixture.Manifest.Sanitized = true
	start := captured[0].CapturedAt
	for index, capturedRecord := range captured {
		input, err := redactor.Redact(capturedRecord.Input)
		if err != nil {
			return nil, err
		}
		fixture.Records = append(fixture.Records, Record{
			Sequence: index + 1, ElapsedMS: capturedRecord.CapturedAt.Sub(start).Milliseconds(), Input: input,
		})
	}
	fixture.Manifest.EventCount = len(fixture.Records)
	return fixture, nil
}
