package ipc

import (
	"bytes"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf)
	env := Envelope{Type: TypePing, ID: "1", Payload: MustPayload(map[string]string{"a": "b"})}
	if err := c.Write(env); err != nil {
		t.Fatal(err)
	}
	got, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypePing || got.ID != "1" {
		t.Fatalf("%+v", got)
	}
}
