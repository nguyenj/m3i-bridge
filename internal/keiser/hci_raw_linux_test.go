//go:build linux

package keiser

import (
	"encoding/binary"
	"testing"
	"time"
)

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
