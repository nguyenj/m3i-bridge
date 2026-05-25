package antplus

import "encoding/binary"

// ANT+ common data pages used by device profiles to publish identity/version
// data alongside profile-specific measurement pages.
const (
	CommonPageManufacturer uint8 = 0x50
	CommonPageProduct      uint8 = 0x51
	CommonPageRequest      uint8 = 0x46

	HardwareRevision      uint8  = 1
	ManufacturerID        uint16 = 0x00FF // FIT manufacturer 255: development.
	InvalidCommonPageByte uint8  = 0xFF
)

// EncodeCommonPage80 builds the ANT+ manufacturer's identification page.
func EncodeCommonPage80(modelNumber uint16) [8]byte {
	var p [8]byte
	p[0] = CommonPageManufacturer
	p[1], p[2] = InvalidCommonPageByte, InvalidCommonPageByte
	p[3] = HardwareRevision
	binary.LittleEndian.PutUint16(p[4:6], ManufacturerID)
	binary.LittleEndian.PutUint16(p[6:8], modelNumber)
	return p
}

// EncodeCommonPage81 builds the ANT+ product information page.
func EncodeCommonPage81(softwareRevision uint8, serialNumber uint32) [8]byte {
	var p [8]byte
	p[0] = CommonPageProduct
	p[1], p[2] = InvalidCommonPageByte, InvalidCommonPageByte
	p[3] = softwareRevision
	binary.LittleEndian.PutUint32(p[4:8], serialNumber)
	return p
}

// CommonInterleavedPage returns a common page at the profile background-page
// cadence. Each common page is sent twice in a row, both early in the stream
// and periodically after that, so receivers joining mid-stream can learn the
// device identity without delaying measurement pages for long.
func CommonInterleavedPage(count uint64, modelNumber uint16, softwareRevision uint8, serialNumber uint32) ([8]byte, bool) {
	switch count {
	case 1, 2:
		return EncodeCommonPage80(modelNumber), true
	case 3, 4:
		return EncodeCommonPage81(softwareRevision, serialNumber), true
	}

	switch count % 132 {
	case 65, 66:
		return EncodeCommonPage80(modelNumber), true
	case 131, 0:
		return EncodeCommonPage81(softwareRevision, serialNumber), true
	default:
		return [8]byte{}, false
	}
}
