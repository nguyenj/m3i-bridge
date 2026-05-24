// Package keiser parses Keiser M Series indoor bike BLE advertisement data.
//
// Wire format reference: https://dev.keiser.com/mseries/direct/
package keiser

import (
	"encoding/binary"
	"errors"
	"time"
)

// Magic prefix that identifies a Keiser M Series advertisement.
// The Keiser docs call this a "company identifier prefix" but it does not
// correspond to a Bluetooth SIG-assigned company ID — it is a protocol magic.
var Magic = []byte{0x02, 0x01}

// PayloadLen is the full length of a Keiser advertisement payload (including
// the 2-byte magic prefix) per the v6.32+ data structure.
const PayloadLen = 19

// NewFirmwareMinor is the version-minor threshold at which the bike switched
// to ~318.75ms broadcast intervals from the older ~2s interval.
const NewFirmwareMinor = 30

// DataType classifies a Keiser advert per byte 4 of the payload.
//
// See https://dev.keiser.com/mseries/direct/#data-type.
type DataType uint8

// Classify returns the high-level mode the bike is in.
func (d DataType) Classify() Mode {
	switch {
	case d == 0:
		return ModeRealtimeMain
	case d >= 1 && d <= 32:
		return ModeReview
	case d >= 128 && d <= 227:
		return ModeRealtimeInterval
	default:
		return ModeUnknown
	}
}

// Mode is the high-level classification of an advert's Data Type byte.
type Mode uint8

const (
	ModeUnknown Mode = iota
	ModeRealtimeMain
	ModeReview
	ModeRealtimeInterval
)

// IsRealtime reports whether the advert carries live data (vs review playback
// of a past interval). Only realtime adverts should drive ANT+ broadcasting.
func (m Mode) IsRealtime() bool {
	return m == ModeRealtimeMain || m == ModeRealtimeInterval
}

// Advert is a fully parsed Keiser M Series advertisement.
type Advert struct {
	Received time.Time

	VersionMajor uint8
	VersionMinor uint8
	DataType     DataType
	EquipmentID  uint8

	CadenceRPM   uint16 // raw bike value already divided by 10
	HeartRateBPM uint16 // raw bike value already divided by 10; 0 when no strap
	PowerWatts   uint16

	Calories       uint16
	DurationMin    uint8
	DurationSec    uint8
	DistanceTenths uint16 // raw bike value in tenths of km/miles
	DistanceUnits  uint16 // integer km/miles, truncated for display/logging
	DistanceMetric bool   // true = km, false = miles

	Gear uint8 // 1-24, 0 when braking or firmware < 4.21
}

// IsNewFirmware reports whether the bike is on firmware ≥6.x with the faster
// broadcast interval and shorter stats freshness window.
func (a Advert) IsNewFirmware() bool {
	return a.VersionMajor == 6 && a.VersionMinor >= NewFirmwareMinor
}

// Errors returned by Parse.
var (
	ErrShortPayload = errors.New("keiser: payload shorter than 19 bytes")
	ErrBadMagic     = errors.New("keiser: missing 0x02 0x01 magic prefix")
)

// Parse decodes the manufacturer-specific bytes of a Keiser M Series advert.
// The buffer must be the full 19-byte payload including the magic prefix.
//
// Parse does no filtering or smoothing — review-mode adverts are returned
// with their DataType intact so callers can decide what to do with them.
func Parse(buf []byte, received time.Time) (Advert, error) {
	if len(buf) < PayloadLen {
		return Advert{}, ErrShortPayload
	}
	if buf[0] != Magic[0] || buf[1] != Magic[1] {
		return Advert{}, ErrBadMagic
	}

	dist := binary.LittleEndian.Uint16(buf[16:18])

	return Advert{
		Received:       received,
		VersionMajor:   buf[2],
		VersionMinor:   buf[3],
		DataType:       DataType(buf[4]),
		EquipmentID:    buf[5],
		CadenceRPM:     binary.LittleEndian.Uint16(buf[6:8]) / 10,
		HeartRateBPM:   binary.LittleEndian.Uint16(buf[8:10]) / 10,
		PowerWatts:     binary.LittleEndian.Uint16(buf[10:12]),
		Calories:       binary.LittleEndian.Uint16(buf[12:14]),
		DurationMin:    buf[14],
		DurationSec:    buf[15],
		DistanceTenths: dist & 0x7FFF,
		DistanceUnits:  (dist & 0x7FFF) / 10,
		DistanceMetric: dist&0x8000 == 0,
		Gear:           buf[18],
	}, nil
}
