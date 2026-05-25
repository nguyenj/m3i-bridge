// Command m3i-bridge bridges a Keiser M3i indoor bike to a Garmin watch.
//
// It scans BLE for Keiser advertisement beacons, classifies session state
// (no_bike / active / stale), and rebroadcasts power + cadence as an ANT+
// Power Meter plus Keiser distance as an ANT+ Bike Speed sensor.
//
// See PLAN.md / the design doc for architecture rationale.
package main

import (
	"context"
	"flag"
	"io"
	stdlog "log"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/nguyenj/m3i-bridge/internal/broadcast"
	"github.com/nguyenj/m3i-bridge/internal/keiser"
	"github.com/nguyenj/m3i-bridge/internal/session"
)

func main() {
	verbose := flag.Bool("verbose", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	if !*verbose {
		stdlog.SetOutput(io.Discard)
	}
	log.Info("m3i-bridge starting", buildInfoAttrs()...)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func buildInfoAttrs() []any {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}

	attrs := []any{
		"go_version", info.GoVersion,
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		attrs = append(attrs, "module_version", info.Main.Version)
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			attrs = append(attrs, "vcs_revision", setting.Value)
		case "vcs.time":
			attrs = append(attrs, "vcs_time", setting.Value)
		case "vcs.modified":
			attrs = append(attrs, "vcs_modified", setting.Value)
		}
	}
	return attrs
}

func run(ctx context.Context, log *slog.Logger) error {
	adverts := make(chan keiser.Advert, 16)
	events := make(chan session.Event, 16)
	scanRestarts := make(chan string, 4)

	scanner := &keiser.Scanner{Logger: log.With("component", "scanner"), Out: adverts, Restart: scanRestarts}
	broadcaster := &broadcast.Broadcaster{Logger: log.With("component", "broadcaster"), Events: events}

	scanErr := make(chan error, 1)
	broadErr := make(chan error, 1)

	go func() { scanErr <- scanner.Run(ctx) }()
	go func() { broadErr <- broadcaster.Run(ctx) }()

	go runFSM(ctx, log.With("component", "fsm"), adverts, events, scanRestarts)

	select {
	case <-ctx.Done():
		log.Info("shutting down on signal")
	case err := <-scanErr:
		if err != nil {
			return err
		}
	case err := <-broadErr:
		if err != nil {
			return err
		}
	}

	// Give the broadcaster a moment to clean up its channel close + USB reset
	// after we cancel the context, so the watch sees a clean sensor-disconnect.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-scanErr:
	case <-shutdownCtx.Done():
	}
	select {
	case <-broadErr:
	case <-shutdownCtx.Done():
	}
	return nil
}

// runFSM drives the session state machine from incoming adverts and forwards
// the resulting events to the broadcaster. It also ticks the FSM periodically
// so timeouts fire even when adverts have stopped arriving.
func runFSM(ctx context.Context, log *slog.Logger, adverts <-chan keiser.Advert, events chan<- session.Event, scanRestarts chan<- string) {
	fsm := session.New(nil)

	// Ticker resolution: at 4 Hz the FSM never delays a state-change by more
	// than 250ms, which is fine for stats/bike timeouts measured in seconds.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case a := <-adverts:
			emit(log, events, scanRestarts, fsm.Observe(a), fsm.State())
		case <-ticker.C:
			emit(log, events, scanRestarts, fsm.Tick(), fsm.State())
		}
	}
}

func emit(log *slog.Logger, events chan<- session.Event, scanRestarts chan<- string, ev []session.Event, state session.State) {
	for _, e := range ev {
		switch e.Type {
		case session.EventSessionStarted:
			log.Info("session started", "state", state, "power", e.Power, "cadence", e.Cadence, "distance_tenths", e.DistanceTenths, "distance_metric", e.DistanceMetric)
		case session.EventSessionEnded:
			log.Info("session ended", "state", state)
		case session.EventSessionStale:
			log.Info("session stale: emitting zeros and restarting BLE discovery", "state", state)
			select {
			case scanRestarts <- "session stale":
			default:
				log.Warn("BLE restart request dropped: scanner channel full")
			}
		case session.EventStatsUpdated:
			log.Debug("stats", "power", e.Power, "cadence", e.Cadence, "distance_valid", e.DistanceValid, "distance_tenths", e.DistanceTenths, "distance_metric", e.DistanceMetric)
		}
		select {
		case events <- e:
		default:
			log.Warn("event dropped: broadcaster channel full", "type", e.Type)
		}
	}
}
