//go:build linux

package keiser

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"
)

func TestRawHCIFilterBytes(t *testing.T) {
	filter := rawHCIFilterBytes()
	if len(filter) != rawHCIFilterSize {
		t.Fatalf("filter length = %d, want %d", len(filter), rawHCIFilterSize)
	}
	if got, want := binary.LittleEndian.Uint32(filter[0:4]), uint32(1<<hciEventPacket); got != want {
		t.Fatalf("type mask = 0x%08x, want 0x%08x", got, want)
	}
	eventMask0 := binary.LittleEndian.Uint32(filter[4:8])
	for _, eventCode := range []byte{hciEventCommandComplete, hciEventCommandStatus} {
		if eventMask0&(1<<eventCode) == 0 {
			t.Fatalf("event mask[0] missing event 0x%02x: 0x%08x", eventCode, eventMask0)
		}
	}
	eventMask1 := binary.LittleEndian.Uint32(filter[8:12])
	if eventMask1&(1<<(hciEventLEMeta-32)) == 0 {
		t.Fatalf("event mask[1] missing LE meta event: 0x%08x", eventMask1)
	}
	if opcode := binary.LittleEndian.Uint16(filter[12:14]); opcode != 0 {
		t.Fatalf("opcode = 0x%04x, want 0", opcode)
	}
	if filter[14] != 0 || filter[15] != 0 {
		t.Fatalf("filter padding = [%d %d], want [0 0]", filter[14], filter[15])
	}
}

func TestHCICommandStatusIs(t *testing.T) {
	opcode := hciOpcode(hciOGFLEControl, hciOCFLESetScanEnable)
	err := hciCommandError{opcode: opcode, status: hciStatusCommandDisallowed}

	if !hciCommandStatusIs(err, opcode, hciStatusCommandDisallowed) {
		t.Fatal("direct command status was not matched")
	}
	if !hciCommandStatusIs(fmt.Errorf("wrapped: %w", err), opcode, hciStatusCommandDisallowed) {
		t.Fatal("wrapped command status was not matched")
	}
	if hciCommandStatusIs(err, opcode, 0x00) {
		t.Fatal("wrong status matched")
	}
	if hciCommandStatusIs(err, hciOpcode(hciOGFLEControl, hciOCFLESetScanParameters), hciStatusCommandDisallowed) {
		t.Fatal("wrong opcode matched")
	}
	if hciCommandStatusIs(nil, opcode, hciStatusCommandDisallowed) {
		t.Fatal("nil error matched")
	}
}

func TestParseAdvertisingData_KeiserManufacturerPayload(t *testing.T) {
	keiserPayload := build(func(b []byte) {
		b[2], b[3] = 6, 32
		b[4] = 0
		binary.LittleEndian.PutUint16(b[6:], 905)
		binary.LittleEndian.PutUint16(b[10:], 215)
	})
	data := []byte{
		2, 0x01, 0x06,
		3, 0x03, 0x18, 0x18,
		3, 0x09, 'M', '3',
		byte(1 + len(keiserPayload)), 0xff,
	}
	data = append(data, keiserPayload...)

	name, manufacturerPayloads, companyIDs, payloads := parseAdvertisingData(data)
	if name != "M3" {
		t.Fatalf("name = %q, want M3", name)
	}
	if manufacturerPayloads != 1 {
		t.Fatalf("manufacturerPayloads = %d, want 1", manufacturerPayloads)
	}
	if len(companyIDs) != 1 || companyIDs[0] != "0x0102" {
		t.Fatalf("companyIDs = %+v, want [0x0102]", companyIDs)
	}
	if len(payloads) != 1 {
		t.Fatalf("payload count = %d, want 1", len(payloads))
	}
	got, err := Parse(payloads[0], time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("Parse payload: %v", err)
	}
	if got.PowerWatts != 215 || got.CadenceRPM != 90 {
		t.Fatalf("metrics = power:%d cadence:%d, want 215/90", got.PowerWatts, got.CadenceRPM)
	}
}

func TestParseAdvertisingData_NonKeiserManufacturerPayload(t *testing.T) {
	data := []byte{
		4, 0x09, 'n', 'e', 't',
		5, 0xff, 0xa8, 0x06, 0x01, 0x02,
	}

	name, manufacturerPayloads, companyIDs, payloads := parseAdvertisingData(data)
	if name != "net" {
		t.Fatalf("name = %q, want net", name)
	}
	if manufacturerPayloads != 1 {
		t.Fatalf("manufacturerPayloads = %d, want 1", manufacturerPayloads)
	}
	if len(companyIDs) != 1 || companyIDs[0] != "0x06a8" {
		t.Fatalf("companyIDs = %+v, want [0x06a8]", companyIDs)
	}
	if len(payloads) != 0 {
		t.Fatalf("payload count = %d, want 0", len(payloads))
	}
}
