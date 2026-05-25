package broadcast

import (
	"bytes"
	"testing"
)

func TestEncodeANTFrame(t *testing.T) {
	got := encodeANTFrame(msgSystemReset, []byte{0})
	want := []byte{0xA4, 0x01, 0x4A, 0x00, 0xEF}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeANTFrame() = % x, want % x", got, want)
	}
}

func TestANTDecoderSplitFramesAndNoise(t *testing.T) {
	frame1 := encodeANTFrame(msgStartup, []byte{0x00})
	frame2 := encodeANTFrame(msgResponseEvent, []byte{0x01, msgOpenChannel, responseNoError})

	var d antDecoder
	if got := d.feed([]byte{0x00, 0x11, frame1[0]}); len(got) != 0 {
		t.Fatalf("first partial feed produced %d messages, want 0", len(got))
	}

	got := d.feed(append(frame1[1:], frame2...))
	if len(got) != 2 {
		t.Fatalf("second feed produced %d messages, want 2", len(got))
	}
	if got[0].ID != msgStartup || !bytes.Equal(got[0].Data, []byte{0x00}) {
		t.Fatalf("first decoded message = %#v", got[0])
	}
	if got[1].ID != msgResponseEvent || !bytes.Equal(got[1].Data, []byte{0x01, msgOpenChannel, responseNoError}) {
		t.Fatalf("second decoded message = %#v", got[1])
	}
}

func TestANTDecoderDropsBadChecksum(t *testing.T) {
	bad := encodeANTFrame(msgStartup, []byte{0x00})
	bad[len(bad)-1] ^= 0xFF
	good := encodeANTFrame(msgResponseEvent, []byte{0x00, msgNetworkKey, responseNoError})

	var d antDecoder
	got := d.feed(append(bad, good...))
	if len(got) != 1 {
		t.Fatalf("decoded %d messages, want only the valid frame", len(got))
	}
	if got[0].ID != msgResponseEvent {
		t.Fatalf("decoded message id = 0x%02x, want 0x%02x", got[0].ID, msgResponseEvent)
	}
}

func TestParseANTEventTX(t *testing.T) {
	ev, ok := parseANTEvent(antMessage{
		ID:   msgResponseEvent,
		Data: []byte{0x02, msgChannelEvent, eventTx},
	})
	if !ok {
		t.Fatal("EVENT_TX was not parsed")
	}
	if ev.channel != 0x02 || ev.code != eventTx {
		t.Fatalf("event = %+v, want channel 2 code EVENT_TX", ev)
	}
}

func TestParseANTEventIgnoresCommandResponse(t *testing.T) {
	if _, ok := parseANTEvent(antMessage{
		ID:   msgResponseEvent,
		Data: []byte{0x00, msgOpenChannel, responseNoError},
	}); ok {
		t.Fatal("command response parsed as channel event")
	}
}
