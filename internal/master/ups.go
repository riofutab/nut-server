package master

import (
	"context"
	"log/slog"
	"time"

	"nut-server/internal/protocol"
)

func (s *Server) runPollingLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval.Duration)
	defer ticker.Stop()

	for {
		if err := s.evaluateUPS(time.Now().UTC()); err != nil {
			slog.Error("poll UPS failed", "target", s.cfg.SNMP.Target, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) evaluateUPS(polledAt time.Time) error {
	started := time.Now()
	status, err := s.upsReader(s.cfg.SNMP)
	recordUPSPollDuration(time.Since(started).Seconds())
	if err != nil {
		s.recordUPSError(err, polledAt)
		recordUPSPollResult(false)
		s.evaluateEpisodePolicies(nil, polledAt)
		return err
	}
	s.recordUPSSuccess(status, polledAt)
	recordUPSPollResult(true)
	// upsConfirmed is set only after a restored episode was ended or kept, so
	// a reconnecting slave never sees a stale episode as confirmed.
	if !status.OnBattery {
		s.endEpisodeOnLinePower()
		s.upsConfirmed.Store(true)
		return nil
	}
	// Policies are only evaluated while on battery: on line power the nodes are
	// powered, and a charge/runtime threshold there would shut down nodes that
	// boot back up while the battery recharges.
	s.startEpisodeIfNeeded(polledAt)
	s.upsConfirmed.Store(true)
	s.evaluateEpisodePolicies(&status, polledAt)
	return nil
}

// upsSilentFor returns how long the UPS has not answered when the latest poll
// failed. Silence counts from the last successful poll, or from process start
// when the UPS has not answered since: a master restarted mid-outage must not
// wait forever for a reading that never comes.
func (s *Server) upsSilentFor(now time.Time) (time.Duration, bool) {
	view := s.currentUPSStatus()
	if view == nil || view.LastErrorAt == nil {
		return 0, false
	}
	since := s.startedAt
	if view.LastSuccessAt != nil {
		if !view.LastErrorAt.After(*view.LastSuccessAt) {
			return 0, false
		}
		since = *view.LastSuccessAt
	}
	return now.Sub(since), true
}

func (s *Server) latestSuccessfulUPSStatus() *UPSStatus {
	view := s.currentUPSStatus()
	if view == nil || view.LastSuccessAt == nil || view.RuntimeMinutes == nil {
		return nil
	}
	if view.LastErrorAt != nil && !view.LastSuccessAt.After(*view.LastErrorAt) {
		return nil
	}
	status := &UPSStatus{
		RuntimeMinutes: *view.RuntimeMinutes,
	}
	if view.OnBattery != nil {
		status.OnBattery = *view.OnBattery
	}
	if view.BatteryCharge != nil {
		status.BatteryCharge = *view.BatteryCharge
	}
	return status
}

func (s *Server) recordUPSSuccess(status UPSStatus, polledAt time.Time) {
	s.upsMu.Lock()
	defer s.upsMu.Unlock()

	onBattery := status.OnBattery
	batteryCharge := status.BatteryCharge
	runtimeMinutes := status.RuntimeMinutes

	if s.upsStatus == nil {
		s.upsStatus = &protocol.UPSStatusView{}
	}
	s.upsStatus.Target = s.cfg.SNMP.Target
	s.upsStatus.OnBattery = &onBattery
	s.upsStatus.BatteryCharge = &batteryCharge
	s.upsStatus.RuntimeMinutes = &runtimeMinutes
	s.upsStatus.LastSuccessAt = &polledAt
	s.upsStatus.LastError = ""
	s.upsStatus.LastErrorAt = nil

	if s.cfg.LogUPSStatus {
		slog.Info("ups status",
			"target", s.cfg.SNMP.Target,
			"on_battery", status.OnBattery,
			"charge", status.BatteryCharge,
			"runtime_minutes", status.RuntimeMinutes)
	}
}

func (s *Server) recordUPSError(err error, polledAt time.Time) {
	s.upsMu.Lock()
	defer s.upsMu.Unlock()

	if s.upsStatus == nil {
		s.upsStatus = &protocol.UPSStatusView{}
	}
	s.upsStatus.Target = s.cfg.SNMP.Target
	s.upsStatus.LastError = err.Error()
	s.upsStatus.LastErrorAt = &polledAt
}

func (s *Server) currentUPSStatus() *protocol.UPSStatusView {
	s.upsMu.RLock()
	defer s.upsMu.RUnlock()

	if s.upsStatus == nil {
		if s.cfg.SNMP.Target == "" {
			return nil
		}
		return &protocol.UPSStatusView{Target: s.cfg.SNMP.Target}
	}

	view := &protocol.UPSStatusView{
		Target:    s.upsStatus.Target,
		LastError: s.upsStatus.LastError,
	}
	if s.upsStatus.OnBattery != nil {
		onBattery := *s.upsStatus.OnBattery
		view.OnBattery = &onBattery
	}
	if s.upsStatus.BatteryCharge != nil {
		batteryCharge := *s.upsStatus.BatteryCharge
		view.BatteryCharge = &batteryCharge
	}
	if s.upsStatus.RuntimeMinutes != nil {
		runtimeMinutes := *s.upsStatus.RuntimeMinutes
		view.RuntimeMinutes = &runtimeMinutes
	}
	if s.upsStatus.LastSuccessAt != nil {
		lastSuccessAt := *s.upsStatus.LastSuccessAt
		view.LastSuccessAt = &lastSuccessAt
	}
	if s.upsStatus.LastErrorAt != nil {
		lastErrorAt := *s.upsStatus.LastErrorAt
		view.LastErrorAt = &lastErrorAt
	}
	return view
}
