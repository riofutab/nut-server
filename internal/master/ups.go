package master

import (
	"context"
	"log"
	"time"

	"nut-server/internal/protocol"
)

func (s *Server) runPollingLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval.Duration)
	defer ticker.Stop()

	for {
		if err := s.evaluateUPS(); err != nil {
			log.Printf("poll UPS failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) evaluateUPS() error {
	polledAt := time.Now().UTC()
	status, err := ReadUPSStatus(s.cfg.SNMP)
	if err != nil {
		s.recordUPSError(err, polledAt)
		return err
	}
	s.recordUPSSuccess(status, polledAt)
	if !ShouldShutdown(status, s.cfg.ShutdownPolicy) {
		return nil
	}
	_, _, err = s.triggerShutdown(protocol.ShutdownRequest{Reason: s.cfg.ShutdownPolicy.ShutdownReason}, true)
	if err != nil && err == errShutdownAlreadyActive {
		return nil
	}
	return err
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
		log.Printf(
			"ups status target=%s on_battery=%t charge=%d runtime_minutes=%d",
			s.cfg.SNMP.Target,
			status.OnBattery,
			status.BatteryCharge,
			status.RuntimeMinutes,
		)
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
