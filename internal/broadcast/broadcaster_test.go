package broadcast

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nguyenj/m3i-bridge/internal/antplus"
	"github.com/nguyenj/m3i-bridge/internal/session"
)

type sentBroadcast struct {
	channel byte
	data    [8]byte
}

type fakeANTController struct {
	events chan antEvent
	data   chan antDataMessage
	sent   []sentBroadcast
	ids    []channelID
}

type channelID struct {
	channel         byte
	deviceNumber    uint16
	deviceType      byte
	transmissionTyp byte
}

func newFakeANTController() *fakeANTController {
	return &fakeANTController{
		events: make(chan antEvent, 8),
		data:   make(chan antDataMessage, 8),
	}
}

func (f *fakeANTController) Close() error { return nil }
func (f *fakeANTController) ResetSystem(context.Context) error {
	return nil
}
func (f *fakeANTController) SetNetworkKey(context.Context, byte, [8]byte) error {
	return nil
}
func (f *fakeANTController) AssignChannel(context.Context, byte, byte, byte) error {
	return nil
}
func (f *fakeANTController) SetChannelID(_ context.Context, channel byte, deviceNumber uint16, deviceType, transmissionType byte) error {
	f.ids = append(f.ids, channelID{
		channel:         channel,
		deviceNumber:    deviceNumber,
		deviceType:      deviceType,
		transmissionTyp: transmissionType,
	})
	return nil
}
func (f *fakeANTController) SetChannelRFFrequency(context.Context, byte, byte) error {
	return nil
}
func (f *fakeANTController) SetChannelTransmitPower(context.Context, byte, byte) error {
	return nil
}
func (f *fakeANTController) SetChannelPeriod(context.Context, byte, uint16) error {
	return nil
}
func (f *fakeANTController) OpenChannel(context.Context, byte) error {
	return nil
}
func (f *fakeANTController) CloseChannel(context.Context, byte) error {
	return nil
}
func (f *fakeANTController) UnassignChannel(context.Context, byte) error {
	return nil
}
func (f *fakeANTController) SendBroadcastData(_ context.Context, channel byte, data []byte) error {
	var copied [8]byte
	copy(copied[:], data)
	f.sent = append(f.sent, sentBroadcast{channel: channel, data: copied})
	return nil
}
func (f *fakeANTController) ChannelEvents() <-chan antEvent {
	return f.events
}
func (f *fakeANTController) DataMessages() <-chan antDataMessage {
	return f.data
}

func TestBroadcasterStateSendsPowerPageOnTXEvent(t *testing.T) {
	fake := newFakeANTController()
	state := broadcasterState{log: discardLogger(), dev: fake}
	ctx := context.Background()

	if err := state.startSession(ctx); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	state.applyStats(session.Event{
		At:             time.Unix(10, 0),
		Power:          123,
		Cadence:        87,
		DistanceValid:  true,
		DistanceTenths: 5,
		DistanceMetric: true,
	})
	for range 5 {
		if err := state.handleANTEvent(ctx, antEvent{channel: powerChannelNumber, code: eventTx}); err != nil {
			t.Fatalf("handleANTEvent: %v", err)
		}
	}

	got := lastBroadcast(t, fake, powerChannelNumber)
	if got[0] != antplus.PowerPageStandard {
		t.Fatalf("power page = 0x%02x, want 0x10", got[0])
	}
	if got[3] != 87 {
		t.Fatalf("cadence = %d, want 87", got[3])
	}
	if power := binary.LittleEndian.Uint16(got[6:8]); power != 123 {
		t.Fatalf("instant power = %d, want 123", power)
	}
	if state.txEvents != 5 || state.powerBroadcasts != 5 || state.commonBroadcasts != 4 || state.nonZeroBroadcasts != 1 {
		t.Fatalf("counters tx=%d power=%d common=%d nonzero=%d, want 5/5/4/1", state.txEvents, state.powerBroadcasts, state.commonBroadcasts, state.nonZeroBroadcasts)
	}
}

func TestBroadcasterStateUsesGlobalDataPageTransmissionType(t *testing.T) {
	fake := newFakeANTController()
	state := broadcasterState{log: discardLogger(), dev: fake}
	ctx := context.Background()

	if err := state.startSession(ctx); err != nil {
		t.Fatalf("startSession: %v", err)
	}

	if len(fake.ids) != 2 {
		t.Fatalf("channel ids = %d, want 2", len(fake.ids))
	}
	for _, id := range fake.ids {
		want := powerTransmission
		if id.channel == speedChannelNumber {
			want = speedTransmission
		}
		if id.transmissionTyp != want {
			t.Fatalf("channel %d transmission type = %#x, want %#x", id.channel, id.transmissionTyp, want)
		}
	}
}

func TestBroadcasterStateSendsSpeedPageOnTXEvent(t *testing.T) {
	fake := newFakeANTController()
	state := broadcasterState{log: discardLogger(), dev: fake}
	ctx := context.Background()

	if err := state.startSession(ctx); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	state.applyStats(session.Event{
		At:             time.Unix(10, 0),
		DistanceValid:  true,
		DistanceTenths: 5,
		DistanceMetric: true,
	})
	if err := state.handleANTEvent(ctx, antEvent{channel: speedChannelNumber, code: eventTx}); err != nil {
		t.Fatalf("handleANTEvent: %v", err)
	}

	got := lastBroadcast(t, fake, speedChannelNumber)
	if got[0] != 0x00 {
		t.Fatalf("speed page = 0x%02x, want 0x00", got[0])
	}
	if revs := binary.LittleEndian.Uint16(got[6:8]); revs != 250 {
		t.Fatalf("speed revs = %d, want 250", revs)
	}
	if state.txEvents != 1 || state.speedBroadcasts != 1 {
		t.Fatalf("counters tx=%d speed=%d, want 1/1", state.txEvents, state.speedBroadcasts)
	}
}

func TestBroadcasterStateInterleavesCommonPowerPages(t *testing.T) {
	fake := newFakeANTController()
	state := broadcasterState{log: discardLogger(), dev: fake}
	ctx := context.Background()

	if err := state.startSession(ctx); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	state.applyStats(session.Event{
		At:      time.Unix(10, 0),
		Power:   123,
		Cadence: 87,
	})
	for range 1 {
		if err := state.handleANTEvent(ctx, antEvent{channel: powerChannelNumber, code: eventTx}); err != nil {
			t.Fatalf("handleANTEvent: %v", err)
		}
	}

	got := lastBroadcast(t, fake, powerChannelNumber)
	if got[0] != antplus.CommonPageManufacturer {
		t.Fatalf("power page = 0x%02x, want common page 80", got[0])
	}
	if state.powerBroadcasts != 1 || state.commonBroadcasts != 1 || state.nonZeroBroadcasts != 0 {
		t.Fatalf("counters power=%d common=%d nonzero=%d, want 1/1/0", state.powerBroadcasts, state.commonBroadcasts, state.nonZeroBroadcasts)
	}
}

func TestBroadcasterStateDoesNotInterleaveCommonSpeedPages(t *testing.T) {
	fake := newFakeANTController()
	state := broadcasterState{log: discardLogger(), dev: fake}
	ctx := context.Background()

	if err := state.startSession(ctx); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	for range 8 {
		if err := state.handleANTEvent(ctx, antEvent{channel: speedChannelNumber, code: eventTx}); err != nil {
			t.Fatalf("handleANTEvent: %v", err)
		}
	}

	got := lastBroadcast(t, fake, speedChannelNumber)
	if got[0] != 0x00 {
		t.Fatalf("speed page = 0x%02x, want page 0", got[0])
	}
	if state.speedBroadcasts != 8 || state.commonBroadcasts != 0 {
		t.Fatalf("counters speed=%d common=%d, want 8/0", state.speedBroadcasts, state.commonBroadcasts)
	}
}

func TestBroadcasterStateQueuesPowerCalibrationResponse(t *testing.T) {
	fake := newFakeANTController()
	state := broadcasterState{log: discardLogger(), dev: fake}
	ctx := context.Background()

	if err := state.startSession(ctx); err != nil {
		t.Fatalf("startSession: %v", err)
	}
	state.handleANTData(antDataMessage{
		channel: powerChannelNumber,
		data: [8]byte{
			antplus.PowerPageCalibration,
			antplus.CalibrationRequest,
			0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		},
		acknowledged: true,
	})
	if err := state.handleANTEvent(ctx, antEvent{channel: powerChannelNumber, code: eventTx}); err != nil {
		t.Fatalf("handleANTEvent: %v", err)
	}

	got := lastBroadcast(t, fake, powerChannelNumber)
	if got[0] != antplus.PowerPageCalibration || got[1] != antplus.CalibrationSuccess {
		t.Fatalf("power response = % x, want calibration success", got)
	}
	if state.calibrationReqs != 1 || state.responseBroadcasts != 1 {
		t.Fatalf("counters calibration=%d response=%d, want 1/1", state.calibrationReqs, state.responseBroadcasts)
	}
}

func lastBroadcast(t *testing.T, fake *fakeANTController, channel byte) []byte {
	t.Helper()
	for i := len(fake.sent) - 1; i >= 0; i-- {
		if fake.sent[i].channel == channel {
			return fake.sent[i].data[:]
		}
	}
	t.Fatalf("no broadcast sent on channel %d", channel)
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
