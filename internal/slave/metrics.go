package slave

import (
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	metricConnectAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nut_slave_connect_attempts_total",
		Help: "Total dial+register attempts partitioned by result (success/dial_error/register_error).",
	}, []string{"result"})

	metricShutdownsReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "nut_slave_shutdowns_received_total",
		Help: "Total shutdown commands received from master.",
	})

	metricShutdownStatus = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nut_slave_shutdown_status_total",
		Help: "Shutdown status transitions partitioned by final status emitted by slave.",
	}, []string{"status"})

	metricBuildInfoSlave = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "nut_slave_build_info",
		Help: "Constant 1 for the running slave process.",
	})

	slaveConnected atomic.Bool

	slaveRegistry     *prometheus.Registry
	slaveRegistryOnce sync.Once
)

func slavePromRegistry() *prometheus.Registry {
	slaveRegistryOnce.Do(func() {
		slaveRegistry = buildSlaveRegistry()
	})
	return slaveRegistry
}

func buildSlaveRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		metricConnectAttempts,
		metricShutdownsReceived,
		metricShutdownStatus,
		metricBuildInfoSlave,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "nut_slave_connected",
			Help: "1 while the slave has an active session with the master, 0 otherwise.",
		}, func() float64 {
			if slaveConnected.Load() {
				return 1
			}
			return 0
		}),
	)
	metricBuildInfoSlave.Set(1)
	return reg
}

func recordConnectAttempt(result string) {
	if result == "" {
		return
	}
	metricConnectAttempts.WithLabelValues(result).Inc()
}

func recordShutdownReceived() {
	metricShutdownsReceived.Inc()
}

func recordShutdownStatus(status string) {
	if status == "" {
		return
	}
	metricShutdownStatus.WithLabelValues(status).Inc()
}

func setConnected(v bool) {
	slaveConnected.Store(v)
}
