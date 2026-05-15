package master

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"nut-server/internal/protocol"
)

var (
	metricUPSPolls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nut_master_ups_poll_total",
		Help: "Total UPS SNMP polls partitioned by result.",
	}, []string{"result"})

	metricShutdownIssued = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nut_master_shutdowns_issued_total",
		Help: "Total shutdown commands issued (auto + manual).",
	})

	metricShutdownAcks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nut_master_shutdown_acks_total",
		Help: "Shutdown ack messages received from slaves, partitioned by status.",
	}, []string{"status"})

	metricRegisterAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nut_master_register_attempts_total",
		Help: "Slave register attempts partitioned by result.",
	}, []string{"result"})

	metricBuildInfo = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nut_master_build_info",
		Help: "Constant 1 for the running master process.",
	})

	masterRegistry     *prometheus.Registry
	masterRegistryOnce sync.Once
)

func masterPromRegistry(s *Server) *prometheus.Registry {
	masterRegistryOnce.Do(func() {
		masterRegistry = buildMasterRegistry(s)
	})
	return masterRegistry
}

func buildMasterRegistry(s *Server) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		metricUPSPolls,
		metricShutdownIssued,
		metricShutdownAcks,
		metricRegisterAttempts,
		metricBuildInfo,
		newMasterSnapshotCollector(s),
	)
	metricBuildInfo.Set(1)
	return reg
}

func recordUPSPollResult(success bool) {
	if success {
		metricUPSPolls.WithLabelValues("success").Inc()
	} else {
		metricUPSPolls.WithLabelValues("error").Inc()
	}
}

func recordShutdownIssued() {
	metricShutdownIssued.Inc()
}

func recordShutdownAck(status string) {
	if status == "" {
		return
	}
	switch status {
	case protocol.ShutdownStatusAccepted,
		protocol.ShutdownStatusExecuting,
		protocol.ShutdownStatusExecuted,
		protocol.ShutdownStatusFailed,
		protocol.ShutdownStatusTimeout:
		metricShutdownAcks.WithLabelValues(status).Inc()
	default:
		metricShutdownAcks.WithLabelValues("unknown").Inc()
	}
}

func recordRegisterAttempt(result string) {
	metricRegisterAttempts.WithLabelValues(result).Inc()
}

type masterSnapshotCollector struct {
	server *Server

	upsOnBattery      *prometheus.Desc
	upsBatteryCharge  *prometheus.Desc
	upsRuntimeMinutes *prometheus.Desc
	registeredSlaves  *prometheus.Desc
	nodesByState      *prometheus.Desc
	shutdownActive    *prometheus.Desc
	localShutdownPh   *prometheus.Desc
}

func newMasterSnapshotCollector(s *Server) *masterSnapshotCollector {
	return &masterSnapshotCollector{
		server: s,
		upsOnBattery: prometheus.NewDesc(
			"nut_master_ups_on_battery",
			"1 when the UPS is on battery, 0 when on line power.",
			nil, nil,
		),
		upsBatteryCharge: prometheus.NewDesc(
			"nut_master_ups_battery_charge_percent",
			"Latest UPS battery charge percentage from SNMP.",
			nil, nil,
		),
		upsRuntimeMinutes: prometheus.NewDesc(
			"nut_master_ups_runtime_minutes",
			"Latest UPS remaining runtime in minutes from SNMP.",
			nil, nil,
		),
		registeredSlaves: prometheus.NewDesc(
			"nut_master_registered_slaves",
			"Number of slaves with an active connection right now.",
			nil, nil,
		),
		nodesByState: prometheus.NewDesc(
			"nut_master_nodes",
			"Number of known nodes in the directory partitioned by state.",
			[]string{"state"}, nil,
		),
		shutdownActive: prometheus.NewDesc(
			"nut_master_shutdown_active",
			"1 while a shutdown command is in flight, 0 otherwise.",
			nil, nil,
		),
		localShutdownPh: prometheus.NewDesc(
			"nut_master_local_shutdown_phase",
			"Indicator gauge for the local-shutdown state machine phase.",
			[]string{"phase"}, nil,
		),
	}
}

func (c *masterSnapshotCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.upsOnBattery
	ch <- c.upsBatteryCharge
	ch <- c.upsRuntimeMinutes
	ch <- c.registeredSlaves
	ch <- c.nodesByState
	ch <- c.shutdownActive
	ch <- c.localShutdownPh
}

func (c *masterSnapshotCollector) Collect(ch chan<- prometheus.Metric) {
	if ups := c.server.currentUPSStatus(); ups != nil {
		if ups.OnBattery != nil {
			ch <- prometheus.MustNewConstMetric(c.upsOnBattery, prometheus.GaugeValue, boolToFloat(*ups.OnBattery))
		}
		if ups.BatteryCharge != nil {
			ch <- prometheus.MustNewConstMetric(c.upsBatteryCharge, prometheus.GaugeValue, float64(*ups.BatteryCharge))
		}
		if ups.RuntimeMinutes != nil {
			ch <- prometheus.MustNewConstMetric(c.upsRuntimeMinutes, prometheus.GaugeValue, float64(*ups.RuntimeMinutes))
		}
	}

	ch <- prometheus.MustNewConstMetric(c.registeredSlaves, prometheus.GaugeValue, float64(len(c.server.registry.List())))

	status := c.server.Status()
	stateCounts := map[string]int{
		protocol.NodeStateOnline:    0,
		protocol.NodeStateOffline:   0,
		protocol.NodeStateNeverSeen: 0,
	}
	for _, n := range status.Nodes {
		stateCounts[n.State]++
	}
	for state, count := range stateCounts {
		ch <- prometheus.MustNewConstMetric(c.nodesByState, prometheus.GaugeValue, float64(count), state)
	}

	ch <- prometheus.MustNewConstMetric(c.shutdownActive, prometheus.GaugeValue, boolToFloat(c.server.shutdownIssued.Load()))

	if status.LocalShutdown != nil {
		for _, phase := range []string{
			protocol.LocalShutdownPhaseIdle,
			protocol.LocalShutdownPhaseWaitingRemote,
			protocol.LocalShutdownPhaseWaitExpired,
			protocol.LocalShutdownPhaseEmergency,
			protocol.LocalShutdownPhaseExecuting,
			protocol.LocalShutdownPhaseCompleted,
			protocol.LocalShutdownPhaseFailed,
		} {
			value := 0.0
			if status.LocalShutdown.Phase == phase {
				value = 1
			}
			ch <- prometheus.MustNewConstMetric(c.localShutdownPh, prometheus.GaugeValue, value, phase)
		}
	}
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
