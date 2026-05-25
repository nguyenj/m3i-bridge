//go:build linux

package keiser

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	hciDeviceID = 0

	hciCommandPacket = 0x01
	hciEventPacket   = 0x04

	hciEventCommandComplete = 0x0e
	hciEventCommandStatus   = 0x0f
	hciEventLEMeta          = 0x3e

	hciSubeventLEAdvertisingReport         = 0x02
	hciSubeventLEExtendedAdvertisingReport = 0x0d

	hciFilterOption = 2

	hciOGFLEControl           = 0x08
	hciOCFLESetScanParameters = 0x000b
	hciOCFLESetScanEnable     = 0x000c
	hciCommandResponseTimeout = 2 * time.Second
	rawHCIPollTimeoutMillis   = 500
	rawHCIScanHealthInterval  = 15 * time.Second
	rawHCIScanSummaryInterval = 30 * time.Second
)

func (s *Scanner) runRawHCI(ctx context.Context) error {
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	if s.Out == nil {
		return errors.New("keiser: Scanner.Out must be set")
	}

	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		return fmt.Errorf("open raw HCI socket: %w", err)
	}
	defer unix.Close(fd)

	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: hciDeviceID, Channel: unix.HCI_CHANNEL_RAW}); err != nil {
		return fmt.Errorf("bind raw HCI socket to hci%d: %w", hciDeviceID, err)
	}
	if err := setRawHCIFilter(fd); err != nil {
		return fmt.Errorf("set raw HCI filter: %w", err)
	}
	if err := configureRawHCIScan(fd, true); err != nil {
		return fmt.Errorf("start raw HCI LE scan: %w", err)
	}
	defer configureRawHCIScan(fd, false)

	var filter DropoutFilter
	stats := scanStats{started: time.Now()}
	buf := make([]byte, 512)
	pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	summaryTicker := time.NewTicker(rawHCIScanSummaryInterval)
	defer summaryTicker.Stop()
	healthTicker := time.NewTicker(rawHCIScanHealthInterval)
	defer healthTicker.Stop()

	log.Info("BLE raw HCI scan starting", "hci", hciDeviceID, "duplicate_data", true, "scan_type", "passive")

	for {
		select {
		case <-ctx.Done():
			return nil
		case reason, ok := <-s.Restart:
			if !ok {
				s.Restart = nil
				continue
			}
			if err := restartRawHCIScan(fd, log, reason); err != nil {
				return err
			}
			stats.restarts++
			continue
		case <-summaryTicker.C:
			stats.log(log)
			continue
		case <-healthTicker.C:
			if stats.payloads == 0 {
				if err := restartRawHCIScan(fd, log, "no Keiser payloads observed"); err != nil {
					return err
				}
				stats.restarts++
			}
			continue
		default:
		}

		n, err := unix.Poll(pollFDs, rawHCIPollTimeoutMillis)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("poll raw HCI socket: %w", err)
		}
		if n == 0 || pollFDs[0].Revents&unix.POLLIN == 0 {
			continue
		}

		read, err := unix.Read(fd, buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return fmt.Errorf("read raw HCI socket: %w", err)
		}
		s.processHCIFrame(buf[:read], &filter, &stats)
	}
}

func restartRawHCIScan(fd int, log *slog.Logger, reason string) error {
	if reason == "" {
		reason = "requested"
	}
	log.Info("BLE raw HCI scan restarting", "reason", reason)
	if err := configureRawHCIScan(fd, true); err != nil {
		return fmt.Errorf("restart raw HCI LE scan: %w", err)
	}
	log.Info("BLE raw HCI scan restarted", "reason", reason)
	return nil
}

func configureRawHCIScan(fd int, enable bool) error {
	if err := sendRawHCICommand(fd, hciOpcode(hciOGFLEControl, hciOCFLESetScanEnable), []byte{0x00, 0x00}); err != nil {
		return err
	}
	if !enable {
		return nil
	}

	// Passive continuous LE scan. Filter duplicates is disabled in the enable
	// command so every controller-delivered M3 advertisement can update ANT+.
	if err := sendRawHCICommand(fd, hciOpcode(hciOGFLEControl, hciOCFLESetScanParameters), []byte{
		0x00,       // scan type: passive
		0x10, 0x00, // interval: 10 ms
		0x10, 0x00, // window: 10 ms
		0x00, // own address type: public
		0x00, // filter policy: accept all
	}); err != nil {
		return err
	}
	return sendRawHCICommand(fd, hciOpcode(hciOGFLEControl, hciOCFLESetScanEnable), []byte{
		0x01, // enable
		0x00, // do not filter duplicates
	})
}

func sendRawHCICommand(fd int, opcode uint16, params []byte) error {
	packet := make([]byte, 4+len(params))
	packet[0] = hciCommandPacket
	binary.LittleEndian.PutUint16(packet[1:3], opcode)
	packet[3] = byte(len(params))
	copy(packet[4:], params)

	if _, err := unix.Write(fd, packet); err != nil {
		return fmt.Errorf("write HCI command 0x%04x: %w", opcode, err)
	}

	deadline := time.Now().Add(hciCommandResponseTimeout)
	buf := make([]byte, 512)
	pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		timeout := int(remaining / time.Millisecond)
		if timeout < 1 {
			timeout = 1
		}
		n, err := unix.Poll(pollFDs, timeout)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		if n == 0 || pollFDs[0].Revents&unix.POLLIN == 0 {
			continue
		}
		read, err := unix.Read(fd, buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return err
		}
		done, status := hciCommandStatus(buf[:read], opcode)
		if !done {
			continue
		}
		if status != 0x00 {
			return fmt.Errorf("HCI command 0x%04x status 0x%02x", opcode, status)
		}
		return nil
	}
	return fmt.Errorf("timeout waiting for HCI command 0x%04x response", opcode)
}

func setRawHCIFilter(fd int) error {
	var filter [14]byte
	binary.LittleEndian.PutUint32(filter[0:4], 1<<hciEventPacket)
	setHCIEventBit(filter[4:12], hciEventCommandComplete)
	setHCIEventBit(filter[4:12], hciEventCommandStatus)
	setHCIEventBit(filter[4:12], hciEventLEMeta)

	_, _, errno := unix.Syscall6(
		unix.SYS_SETSOCKOPT,
		uintptr(fd),
		uintptr(unix.SOL_HCI),
		uintptr(hciFilterOption),
		uintptr(unsafe.Pointer(&filter[0])),
		uintptr(len(filter)),
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func setHCIEventBit(mask []byte, eventCode byte) {
	bit := uint(eventCode)
	offset := (bit / 32) * 4
	if int(offset)+4 > len(mask) {
		return
	}
	current := binary.LittleEndian.Uint32(mask[offset : offset+4])
	current |= 1 << (bit % 32)
	binary.LittleEndian.PutUint32(mask[offset:offset+4], current)
}

func hciOpcode(ogf, ocf uint16) uint16 {
	return (ocf & 0x03ff) | (ogf << 10)
}

func hciCommandStatus(frame []byte, wantOpcode uint16) (bool, byte) {
	eventCode, payload, ok := hciEvent(frame)
	if !ok {
		return false, 0
	}
	switch eventCode {
	case hciEventCommandComplete:
		if len(payload) < 3 {
			return false, 0
		}
		opcode := binary.LittleEndian.Uint16(payload[1:3])
		if opcode != wantOpcode {
			return false, 0
		}
		if len(payload) >= 4 {
			return true, payload[3]
		}
		return true, 0
	case hciEventCommandStatus:
		if len(payload) < 4 {
			return false, 0
		}
		opcode := binary.LittleEndian.Uint16(payload[2:4])
		if opcode != wantOpcode {
			return false, 0
		}
		return true, payload[0]
	default:
		return false, 0
	}
}

func hciEvent(frame []byte) (byte, []byte, bool) {
	if len(frame) < 2 {
		return 0, nil, false
	}
	if frame[0] == hciEventPacket {
		if len(frame) < 3 {
			return 0, nil, false
		}
		payloadLen := int(frame[2])
		if len(frame) < 3+payloadLen {
			payloadLen = len(frame) - 3
		}
		return frame[1], frame[3 : 3+payloadLen], true
	}

	payloadLen := int(frame[1])
	if len(frame) < 2+payloadLen {
		payloadLen = len(frame) - 2
	}
	return frame[0], frame[2 : 2+payloadLen], true
}

func (s *Scanner) processHCIFrame(frame []byte, filter *DropoutFilter, stats *scanStats) {
	eventCode, payload, ok := hciEvent(frame)
	if !ok || eventCode != hciEventLEMeta || len(payload) < 1 {
		return
	}
	switch payload[0] {
	case hciSubeventLEAdvertisingReport:
		s.processLegacyAdvertisingReports(payload[1:], filter, stats)
	case hciSubeventLEExtendedAdvertisingReport:
		s.processExtendedAdvertisingReports(payload[1:], filter, stats)
	}
}

func (s *Scanner) processLegacyAdvertisingReports(payload []byte, filter *DropoutFilter, stats *scanStats) {
	if len(payload) < 1 {
		return
	}
	reports := int(payload[0])
	offset := 1
	for i := 0; i < reports; i++ {
		if len(payload)-offset < 10 {
			return
		}
		dataLen := int(payload[offset+8])
		offset += 9
		if len(payload)-offset < dataLen+1 {
			return
		}
		advertisingData := payload[offset : offset+dataLen]
		offset += dataLen + 1 // RSSI follows data.
		s.processAdvertisingData(advertisingData, filter, stats)
	}
}

func (s *Scanner) processExtendedAdvertisingReports(payload []byte, filter *DropoutFilter, stats *scanStats) {
	if len(payload) < 1 {
		return
	}
	reports := int(payload[0])
	offset := 1
	for i := 0; i < reports; i++ {
		if len(payload)-offset < 24 {
			return
		}
		dataLen := int(payload[offset+23])
		offset += 24
		if len(payload)-offset < dataLen {
			return
		}
		advertisingData := payload[offset : offset+dataLen]
		offset += dataLen
		s.processAdvertisingData(advertisingData, filter, stats)
	}
}

func (s *Scanner) processAdvertisingData(data []byte, filter *DropoutFilter, stats *scanStats) {
	stats.observed++

	localName, manufacturerPayloads, companyIDs, keiserPayloads := parseAdvertisingData(data)
	if len(keiserPayloads) == 0 {
		if manufacturerPayloads > 0 {
			stats.manufacturerPayloads += uint64(manufacturerPayloads)
			stats.lastNonKeiserName = localName
			stats.lastCompanyIDs = strings.Join(companyIDs, ",")
		}
		return
	}
	for _, payload := range keiserPayloads {
		s.processKeiserPayload(payload, localName, filter, stats)
	}
}

func parseAdvertisingData(data []byte) (string, int, []string, [][]byte) {
	var localName string
	var manufacturerPayloads int
	var companyIDs []string
	var keiserPayloads [][]byte

	for offset := 0; offset < len(data); {
		fieldLen := int(data[offset])
		offset++
		if fieldLen == 0 {
			break
		}
		if fieldLen > len(data)-offset {
			break
		}
		fieldType := data[offset]
		field := data[offset+1 : offset+fieldLen]
		offset += fieldLen

		switch fieldType {
		case 0x08, 0x09: // Shortened / Complete Local Name
			localName = string(field)
		case 0xff: // Manufacturer Specific Data
			manufacturerPayloads++
			if len(field) >= 2 {
				companyID := binary.LittleEndian.Uint16(field[:2])
				companyIDs = append(companyIDs, fmt.Sprintf("0x%04x", companyID))
			}
			if len(field) >= PayloadLen && field[0] == Magic[0] && field[1] == Magic[1] {
				payload := make([]byte, PayloadLen)
				copy(payload, field[:PayloadLen])
				keiserPayloads = append(keiserPayloads, payload)
			}
		}
	}

	return localName, manufacturerPayloads, companyIDs, keiserPayloads
}
