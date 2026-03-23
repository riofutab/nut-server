package master

import "nut-server/internal/config"

func ShouldShutdown(status UPSStatus, policy config.ShutdownPolicy) bool {
	if policy.RequireOnBattery && !status.OnBattery {
		return false
	}
	if policy.MinBatteryCharge > 0 && status.BatteryCharge > policy.MinBatteryCharge {
		return false
	}
	if policy.MinRuntimeSeconds > 0 && status.RuntimeSeconds > policy.MinRuntimeSeconds {
		return false
	}
	return true
}
