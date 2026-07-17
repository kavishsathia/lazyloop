package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	request := Request{Version: Version, RequestID: "abc", Method: "ping", Params: json.RawMessage(`{"ok":true}`)}
	var buffer bytes.Buffer
	if err := Write(&buffer, request); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	var decoded Request
	if err := Read(bufio.NewReader(&buffer), &decoded); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if decoded.RequestID != request.RequestID || decoded.Method != request.Method {
		t.Fatalf("decoded = %#v", decoded)
	}
}
