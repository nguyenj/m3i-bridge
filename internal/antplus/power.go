// Package antplus builds the ANT+ broadcast frames for the Keiser bridge:
// Bicycle Power for power/cadence and Bicycle Speed for distance/speed.
//
// We broadcast as an ANT+ Power Meter (device type 0x0B) using page 0x10
// "Standard Power-Only", which carries instantaneous power AND cadence in a
// single 8-byte payload. The Garmin Forerunner 955 pairs this as a Power
// Meter and reads cadence from the same channel, so no separate Bike Cadence
// Sensor channel is needed.
//
// References:
//   - ANT+ Device Profile: Bicycle Power (D00001086)
package antplus

import "encoding/binary"

// Power Meter channel constants per ANT+ device profile 0x0B.
const (
	PowerDeviceType    uint8  = 0x0B
	PowerChannelPeriod uint16 = 8182 // 8182/32768 Hz ~= 4.0049 Hz
	RFFrequency        uint8  = 57   // 2457 MHz, the ANT+ "public" frequency

	PowerPageCalibration uint8 = 0x01
	PowerPageStandard    uint8 = 0x10

	CalibrationRequest uint8 = 0xAA
	CalibrationSuccess uint8 = 0xAC
)

// PowerEncoder produces ANT+ Standard Power-Only broadcast payloads (page
// 0x10) and tracks the rolling event counter and accumulated power that the
// page format requires.
//
// Methods are not goroutine-safe; the broadcaster owns a single instance.
type PowerEncoder struct {
	eventCount       uint8
	accumulatedPower uint16
}

// EncodePage10 builds an 8-byte page 0x10 Standard Power-Only payload from
// the current instantaneous power and cadence values. Caller invokes this
// once per channel-period tick.
//
// Wire format (8 bytes):
//
//	[0] 0x10                page number
//	[1] event_count         increments every transmission
//	[2] 0xFF                pedal power not supported by the bike
//	[3] cadence rpm         instantaneous cadence; 0xFF = invalid
//	[4-5] accumulated_power LE u16, wraps at 0xFFFF
//	[6-7] instantaneous_power LE u16
func (e *PowerEncoder) EncodePage10(power, cadence uint16) [8]byte {
	e.eventCount++
	e.accumulatedPower += power // u16 wraparound is intentional per spec

	cadenceByte := uint8(0xFF) // "invalid" sentinel per spec
	if cadence <= 0xFE {
		cadenceByte = uint8(cadence)
	}

	var p [8]byte
	p[0] = PowerPageStandard
	p[1] = e.eventCount
	p[2] = 0xFF
	p[3] = cadenceByte
	binary.LittleEndian.PutUint16(p[4:6], e.accumulatedPower)
	binary.LittleEndian.PutUint16(p[6:8], power)
	return p
}

// EncodeCalibrationResponse builds a successful general calibration response.
// The bridge has no torque zero-offset to perform, so it reports success,
// auto-zero unsupported, and a zero manufacturer-specific calibration value.
func EncodeCalibrationResponse() [8]byte {
	var p [8]byte
	p[0] = PowerPageCalibration
	p[1] = CalibrationSuccess
	p[2] = InvalidCommonPageByte
	p[3], p[4], p[5] = InvalidCommonPageByte, InvalidCommonPageByte, InvalidCommonPageByte
	return p
}

// EventCount returns the next event_count value that will be transmitted. For
// tests only.
func (e *PowerEncoder) EventCount() uint8 { return e.eventCount }

// AccumulatedPower returns the current accumulated power register. For tests
// only.
func (e *PowerEncoder) AccumulatedPower() uint16 { return e.accumulatedPower }
