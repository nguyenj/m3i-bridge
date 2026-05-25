package antplus

import (
	"encoding/binary"
	"testing"
)

func TestEncodeCommonPage80(t *testing.T) {
	page := EncodeCommonPage80(0x1234)

	if page[0] != CommonPageManufacturer {
		t.Fatalf("page = %#x, want %#x", page[0], CommonPageManufacturer)
	}
	if page[1] != 0xFF || page[2] != 0xFF {
		t.Fatalf("reserved bytes = %#x %#x, want 0xff 0xff", page[1], page[2])
	}
	if page[3] != HardwareRevision {
		t.Fatalf("hardware revision = %d, want %d", page[3], HardwareRevision)
	}
	if got := binary.LittleEndian.Uint16(page[4:6]); got != ManufacturerID {
		t.Fatalf("manufacturer = %#x, want %#x", got, ManufacturerID)
	}
	if got := binary.LittleEndian.Uint16(page[6:8]); got != 0x1234 {
		t.Fatalf("model = %#x, want 0x1234", got)
	}
}

func TestEncodeCommonPage81(t *testing.T) {
	page := EncodeCommonPage81(7, 0x12345678)

	if page[0] != CommonPageProduct {
		t.Fatalf("page = %#x, want %#x", page[0], CommonPageProduct)
	}
	if page[1] != 0xFF || page[2] != 0xFF {
		t.Fatalf("reserved bytes = %#x %#x, want 0xff 0xff", page[1], page[2])
	}
	if page[3] != 7 {
		t.Fatalf("software revision = %d, want 7", page[3])
	}
	if got := binary.LittleEndian.Uint32(page[4:8]); got != 0x12345678 {
		t.Fatalf("serial = %#x, want 0x12345678", got)
	}
}

func TestCommonInterleavedPage(t *testing.T) {
	tests := []struct {
		count uint64
		page  uint8
		ok    bool
	}{
		{count: 1, ok: false},
		{count: 4, page: CommonPageManufacturer, ok: true},
		{count: 5, page: CommonPageManufacturer, ok: true},
		{count: 8, page: CommonPageProduct, ok: true},
		{count: 9, page: CommonPageProduct, ok: true},
		{count: 65, page: CommonPageManufacturer, ok: true},
		{count: 66, page: CommonPageManufacturer, ok: true},
		{count: 131, page: CommonPageProduct, ok: true},
		{count: 132, page: CommonPageProduct, ok: true},
		{count: 133, ok: false},
	}

	for _, tc := range tests {
		page, ok := CommonInterleavedPage(tc.count, 0x1234, 7, 0x12345678)
		if ok != tc.ok {
			t.Fatalf("count %d ok=%v, want %v", tc.count, ok, tc.ok)
		}
		if ok && page[0] != tc.page {
			t.Fatalf("count %d page=%#x, want %#x", tc.count, page[0], tc.page)
		}
	}
}
