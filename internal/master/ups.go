package master

import (
	"context"
	"log/slog"
	"time"

	"nut-server/internal/protocol"
)

func (s *Server) readUPS() (UPSStatus, error) {
	if s.cfg.UsesNUT() {
		return ReadNUTStatus(s.cfg.NUT)
	}
	return ReadSNMPStatus(s.cfg.SNMP)
}

func (s *Server) upsSource() string {
	if s.cfg.UsesNUT() {
		return protocol.UPSSourceNUT
	}
	return protocol.UPSSourceSNMP
}

// upsTarget names the polled UPS in logs and /status; "" when unconfigured.
func (s *Server) upsTarget() string {
	if s.cfg.UsesNUT() {
		return s.cfg.NUT.Address()
	}
	return s.cfg.SNMP.Target
}

func (s *Server) runPollingLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval.Duration)
	defer ticker.Stop()

	for {
		if err := s.evaluateUPS(time.Now().UTC()); err != nil {
			slog.Error("poll UPS failed", "target", s.upsTarget(), "err", err)
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
	status, err := s.upsReader()
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
	if view == nil || view.LastSuccessAt == nil {
		return nil
	}
	if view.LastErrorAt != nil && !view.LastSuccessAt.After(*view.LastErrorAt) {
		return nil
	}
	// currentUPSStatus returns copies, so the pointers are not shared.
	status := &UPSStatus{
		BatteryCharge:  view.BatteryCharge,
		RuntimeMinutes: view.RuntimeMinutes,
	}
	if view.OnBattery != nil {
		status.OnBattery = *view.OnBattery
	}
	if view.LowBattery != nil {
		status.LowBattery = *view.LowBattery
	}
	return status
}

func (s *Server) recordUPSSuccess(status UPSStatus, polledAt time.Time) {
	s.upsMu.Lock()
	defer s.upsMu.Unlock()

	onBattery := status.OnBattery

	if s.upsStatus == nil {
		s.upsStatus = &protocol.UPSStatusView{}
	}
	s.upsStatus.Source = s.upsSource()
	s.upsStatus.Target = s.upsTarget()
	s.upsStatus.OnBattery = &onBattery
	s.upsStatus.LowBattery = nil
	if s.cfg.UsesNUT() {
		lowBattery := status.LowBattery
		s.upsStatus.LowBattery = &lowBattery
	}
	s.upsStatus.BatteryCharge = copyInt(status.BatteryCharge)
	s.upsStatus.RuntimeMinutes = copyInt(status.RuntimeMinutes)
	s.upsStatus.LastSuccessAt = &polledAt
	s.upsStatus.LastError = ""
	s.upsStatus.LastErrorAt = nil
	s.warnMissingUPSVarsLocked(status)

	if s.cfg.LogUPSStatus {
		slog.Info("ups status",
			"target", s.upsTarget(),
			"on_battery", status.OnBattery,
			"low_battery", status.LowBattery,
			"charge", optionalInt(status.BatteryCharge),
			"runtime_minutes", optionalInt(status.RuntimeMinutes))
	}
}

// warnMissingUPSVarsLocked logs once per variable the UPS does not report,
// since every condition on it silently never fires.
func (s *Server) warnMissingUPSVarsLocked(status UPSStatus) {
	missing := []struct {
		name     string
		isAbsent bool
		effect   string
	}{
		{"battery.charge", status.BatteryCharge == nil, "charge_below policies will not fire"},
		{"battery.runtime", status.RuntimeMinutes == nil, "runtime_below policies and local_shutdown.emergency_runtime_minutes will not fire"},
	}
	for _, m := range missing {
		if !m.isAbsent || s.upsMissingWarned[m.name] {
			continue
		}
		if s.upsMissingWarned == nil {
			s.upsMissingWarned = make(map[string]bool)
		}
		s.upsMissingWarned[m.name] = true
		slog.Warn("UPS does not report "+m.name+"; "+m.effect, "target", s.upsTarget())
	}
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// optionalInt renders a missing reading as "-" in logs instead of a pointer.
func optionalInt(value *int) any {
	if value == nil {
		return "-"
	}
	return *value
}

func (s *Server) recordUPSError(err error, polledAt time.Time) {
	s.upsMu.Lock()
	defer s.upsMu.Unlock()

	if s.upsStatus == nil {
		s.upsStatus = &protocol.UPSStatusView{}
	}
	s.upsStatus.Source = s.upsSource()
	s.upsStatus.Target = s.upsTarget()
	s.upsStatus.LastError = err.Error()
	s.upsStatus.LastErrorAt = &polledAt
}

func (s *Server) currentUPSStatus() *protocol.UPSStatusView {
	s.upsMu.RLock()
	defer s.upsMu.RUnlock()

	if s.upsStatus == nil {
		target := s.upsTarget()
		if target == "" {
			return nil
		}
		return &protocol.UPSStatusView{Source: s.upsSource(), Target: target}
	}

	view := &protocol.UPSStatusView{
		Source:         s.upsStatus.Source,
		Target:         s.upsStatus.Target,
		LastError:      s.upsStatus.LastError,
		BatteryCharge:  copyInt(s.upsStatus.BatteryCharge),
		RuntimeMinutes: copyInt(s.upsStatus.RuntimeMinutes),
	}
	if s.upsStatus.OnBattery != nil {
		onBattery := *s.upsStatus.OnBattery
		view.OnBattery = &onBattery
	}
	if s.upsStatus.LowBattery != nil {
		lowBattery := *s.upsStatus.LowBattery
		view.LowBattery = &lowBattery
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
