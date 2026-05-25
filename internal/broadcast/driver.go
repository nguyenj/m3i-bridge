package broadcast

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/gousb"
)

const (
	antSync byte = 0xA4

	msgResponseEvent        byte = 0x40
	msgUnassignChannel      byte = 0x41
	msgAssignChannel        byte = 0x42
	msgChannelPeriod        byte = 0x43
	msgChannelRFFrequency   byte = 0x45
	msgNetworkKey           byte = 0x46
	msgChannelEvent         byte = 0x01
	msgSystemReset          byte = 0x4A
	msgOpenChannel          byte = 0x4B
	msgCloseChannel         byte = 0x4C
	msgBroadcastData        byte = 0x4E
	msgChannelID            byte = 0x51
	msgChannelTransmitPower byte = 0x60
	msgStartup              byte = 0x6F
	responseNoError         byte = 0x00
	eventTx                 byte = 0x03
	channelTypeTransmit     byte = 0x10
	radioTransmitPowerMax   byte = 0x03
	antUSBTransferTimeout        = 750 * time.Millisecond
	antSetupResponseTimeout      = 2 * time.Second
	antReadErrorBackoff          = 25 * time.Millisecond
)

var antPlusNetworkKey = [8]byte{0xB9, 0xA5, 0x21, 0xFB, 0xBD, 0x72, 0xC3, 0x45}

type antController interface {
	Close() error
	ResetSystem(context.Context) error
	SetNetworkKey(context.Context, byte, [8]byte) error
	AssignChannel(context.Context, byte, byte, byte) error
	SetChannelID(context.Context, byte, uint16, byte, byte) error
	SetChannelRFFrequency(context.Context, byte, byte) error
	SetChannelTransmitPower(context.Context, byte, byte) error
	SetChannelPeriod(context.Context, byte, uint16) error
	OpenChannel(context.Context, byte) error
	CloseChannel(context.Context, byte) error
	UnassignChannel(context.Context, byte) error
	SendBroadcastData(context.Context, byte, []byte) error
	ChannelEvents() <-chan antEvent
}

type antOpenFunc func(context.Context, *slog.Logger) (antController, error)

type antUSBStick struct {
	log *slog.Logger

	ctx        *gousb.Context
	device     *gousb.Device
	closeIface func()
	in         *gousb.InEndpoint
	out        *gousb.OutEndpoint

	readCtx    context.Context
	readCancel context.CancelFunc
	readDone   chan struct{}
	responses  chan antMessage
	events     chan antEvent

	writeMu sync.Mutex
}

type antUSBCandidate struct {
	name string
	vid  gousb.ID
	pid  gousb.ID
}

type antMessage struct {
	ID   byte
	Data []byte
}

type antEvent struct {
	channel byte
	code    byte
}

func openANTUSB(ctx context.Context, log *slog.Logger) (antController, error) {
	if log == nil {
		log = slog.Default()
	}

	candidates := []antUSBCandidate{
		{name: "ANTUSB2", vid: 0x0FCF, pid: 0x1008},
		{name: "ANTUSB-m", vid: 0x0FCF, pid: 0x1009},
	}

	var errs []string
	for _, candidate := range candidates {
		stick, err := openANTUSBCandidate(ctx, log, candidate)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s %04x:%04x: %v", candidate.name, candidate.vid, candidate.pid, err))
			continue
		}
		log.Info("ant usb opened", "device", candidate.name, "vid", fmt.Sprintf("%04x", candidate.vid), "pid", fmt.Sprintf("%04x", candidate.pid))
		return stick, nil
	}

	return nil, fmt.Errorf("open ANT USB device: %s", strings.Join(errs, "; "))
}

func openANTUSBCandidate(parent context.Context, log *slog.Logger, candidate antUSBCandidate) (*antUSBStick, error) {
	usbCtx := gousb.NewContext()
	device, err := usbCtx.OpenDeviceWithVIDPID(candidate.vid, candidate.pid)
	if err != nil {
		_ = usbCtx.Close()
		return nil, err
	}
	if device == nil {
		_ = usbCtx.Close()
		return nil, errors.New("not found")
	}

	cleanup := func() {
		_ = device.Close()
		_ = usbCtx.Close()
	}

	if err := device.SetAutoDetach(true); err != nil {
		cleanup()
		return nil, err
	}

	intf, closeIface, err := device.DefaultInterface()
	if err != nil {
		cleanup()
		return nil, err
	}

	cleanupInterface := func() {
		closeIface()
		cleanup()
	}

	out, err := intf.OutEndpoint(1)
	if err != nil {
		cleanupInterface()
		return nil, err
	}
	in, err := intf.InEndpoint(1)
	if err != nil {
		cleanupInterface()
		return nil, err
	}

	readCtx, readCancel := context.WithCancel(parent)
	stick := &antUSBStick{
		log:        log,
		ctx:        usbCtx,
		device:     device,
		closeIface: closeIface,
		in:         in,
		out:        out,
		readCtx:    readCtx,
		readCancel: readCancel,
		readDone:   make(chan struct{}),
		responses:  make(chan antMessage, 128),
		events:     make(chan antEvent, 512),
	}
	go stick.readLoop()
	return stick, nil
}

func (s *antUSBStick) Close() error {
	s.StopResponses()
	if s.closeIface != nil {
		s.closeIface()
	}
	if s.device != nil {
		_ = s.device.Close()
	}
	if s.ctx != nil {
		return s.ctx.Close()
	}
	return nil
}

func (s *antUSBStick) StopResponses() {
	if s.readCancel != nil {
		s.readCancel()
		<-s.readDone
		s.readCancel = nil
	}
}

func (s *antUSBStick) ResetSystem(ctx context.Context) error {
	if err := s.send(ctx, msgSystemReset, []byte{0}); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, antSetupResponseTimeout)
	defer cancel()
	for {
		msg, err := s.nextMessage(waitCtx)
		if err != nil {
			s.log.Warn("ant reset startup response not observed", "err", err)
			return nil
		}
		if msg.ID == msgStartup {
			return nil
		}
	}
}

func (s *antUSBStick) SetNetworkKey(ctx context.Context, network byte, key [8]byte) error {
	payload := make([]byte, 9)
	payload[0] = network
	copy(payload[1:], key[:])
	return s.command(ctx, msgNetworkKey, payload)
}

func (s *antUSBStick) AssignChannel(ctx context.Context, channel, channelType, network byte) error {
	return s.command(ctx, msgAssignChannel, []byte{channel, channelType, network})
}

func (s *antUSBStick) SetChannelID(ctx context.Context, channel byte, deviceNumber uint16, deviceType, transmissionType byte) error {
	payload := []byte{channel, 0, 0, deviceType, transmissionType}
	binary.LittleEndian.PutUint16(payload[1:3], deviceNumber)
	return s.command(ctx, msgChannelID, payload)
}

func (s *antUSBStick) SetChannelRFFrequency(ctx context.Context, channel, frequency byte) error {
	return s.command(ctx, msgChannelRFFrequency, []byte{channel, frequency})
}

func (s *antUSBStick) SetChannelTransmitPower(ctx context.Context, channel, power byte) error {
	return s.command(ctx, msgChannelTransmitPower, []byte{channel, power & 0x03})
}

func (s *antUSBStick) SetChannelPeriod(ctx context.Context, channel byte, period uint16) error {
	payload := []byte{channel, 0, 0}
	binary.LittleEndian.PutUint16(payload[1:3], period)
	return s.command(ctx, msgChannelPeriod, payload)
}

func (s *antUSBStick) OpenChannel(ctx context.Context, channel byte) error {
	return s.command(ctx, msgOpenChannel, []byte{channel})
}

func (s *antUSBStick) CloseChannel(ctx context.Context, channel byte) error {
	return s.command(ctx, msgCloseChannel, []byte{channel})
}

func (s *antUSBStick) UnassignChannel(ctx context.Context, channel byte) error {
	return s.command(ctx, msgUnassignChannel, []byte{channel})
}

func (s *antUSBStick) SendBroadcastData(ctx context.Context, channel byte, data []byte) error {
	if len(data) != 8 {
		return fmt.Errorf("ANT broadcast data length = %d, want 8", len(data))
	}
	payload := make([]byte, 9)
	payload[0] = channel
	copy(payload[1:], data)
	return s.send(ctx, msgBroadcastData, payload)
}

func (s *antUSBStick) ChannelEvents() <-chan antEvent {
	return s.events
}

func (s *antUSBStick) command(ctx context.Context, messageID byte, payload []byte) error {
	if err := s.send(ctx, messageID, payload); err != nil {
		return err
	}

	waitCtx, cancel := context.WithTimeout(ctx, antSetupResponseTimeout)
	defer cancel()
	for {
		msg, err := s.nextMessage(waitCtx)
		if err != nil {
			return fmt.Errorf("wait for ANT response to 0x%02x: %w", messageID, err)
		}
		if msg.ID != msgResponseEvent || len(msg.Data) < 3 || msg.Data[1] != messageID {
			continue
		}
		if msg.Data[2] == responseNoError {
			return nil
		}
		return fmt.Errorf("ANT response to 0x%02x on channel %d: 0x%02x", messageID, msg.Data[0], msg.Data[2])
	}
}

func (s *antUSBStick) send(ctx context.Context, messageID byte, payload []byte) error {
	frame := encodeANTFrame(messageID, payload)
	writeCtx, cancel := context.WithTimeout(ctx, antUSBTransferTimeout)
	defer cancel()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	n, err := s.out.WriteContext(writeCtx, frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return fmt.Errorf("short ANT write: %d/%d bytes", n, len(frame))
	}
	return nil
}

func (s *antUSBStick) nextMessage(ctx context.Context) (antMessage, error) {
	select {
	case <-ctx.Done():
		return antMessage{}, ctx.Err()
	case msg := <-s.responses:
		return msg, nil
	}
}

func (s *antUSBStick) readLoop() {
	defer close(s.readDone)

	var decoder antDecoder
	buf := make([]byte, 64)
	for {
		readCtx, cancel := context.WithTimeout(s.readCtx, antUSBTransferTimeout)
		n, err := s.in.ReadContext(readCtx, buf)
		cancel()

		if n > 0 {
			for _, msg := range decoder.feed(buf[:n]) {
				if ev, ok := parseANTEvent(msg); ok {
					select {
					case s.events <- ev:
					default:
						s.log.Warn("ant event dropped: channel full", "channel", ev.channel, "code", fmt.Sprintf("0x%02x", ev.code))
					}
				}
				select {
				case s.responses <- msg:
				default:
				}
				s.log.Debug("ant rx", "message_id", fmt.Sprintf("0x%02x", msg.ID), "data", fmt.Sprintf("% x", msg.Data))
			}
		}
		if n == 0 && err == nil {
			sleepOrDone(s.readCtx, antReadErrorBackoff)
			continue
		}
		if err == nil {
			continue
		}
		if s.readCtx.Err() != nil {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, gousb.TransferTimedOut) ||
			errors.Is(err, gousb.TransferCancelled) {
			sleepOrDone(s.readCtx, antReadErrorBackoff)
			continue
		}
		s.log.Debug("ant usb read error", "err", err)
		sleepOrDone(s.readCtx, antReadErrorBackoff)
	}
}

func parseANTEvent(msg antMessage) (antEvent, bool) {
	if msg.ID != msgResponseEvent || len(msg.Data) < 3 || msg.Data[1] != msgChannelEvent {
		return antEvent{}, false
	}
	return antEvent{channel: msg.Data[0], code: msg.Data[2]}, true
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

type antDecoder struct {
	buf []byte
}

func (d *antDecoder) feed(data []byte) []antMessage {
	d.buf = append(d.buf, data...)
	var messages []antMessage

	for {
		for len(d.buf) > 0 && d.buf[0] != antSync {
			d.buf = d.buf[1:]
		}
		if len(d.buf) < 4 {
			return messages
		}

		frameLen := int(d.buf[1]) + 4
		if len(d.buf) < frameLen {
			return messages
		}

		frame := d.buf[:frameLen]
		d.buf = d.buf[frameLen:]
		if !validANTChecksum(frame) {
			continue
		}

		payload := make([]byte, int(frame[1]))
		copy(payload, frame[3:frameLen-1])
		messages = append(messages, antMessage{ID: frame[2], Data: payload})
	}
}

func encodeANTFrame(messageID byte, payload []byte) []byte {
	frame := make([]byte, len(payload)+4)
	frame[0] = antSync
	frame[1] = byte(len(payload))
	frame[2] = messageID
	copy(frame[3:], payload)
	frame[len(frame)-1] = antChecksum(frame[:len(frame)-1])
	return frame
}

func validANTChecksum(frame []byte) bool {
	if len(frame) < 4 {
		return false
	}
	return antChecksum(frame[:len(frame)-1]) == frame[len(frame)-1]
}

func antChecksum(frame []byte) byte {
	var checksum byte
	for _, b := range frame {
		checksum ^= b
	}
	return checksum
}
