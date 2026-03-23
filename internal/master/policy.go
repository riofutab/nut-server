package master

import "nut-server/internal/config"

func ShouldShutdown(status UPSStatus, policy config.ShutdownPolicy) bool {
	if policy.RequireOnBattery && !status.OnBattery {
		return false
	}
	if policy.MinBatteryCharge > 0 && status.BatteryCharge > policy.MinBatteryCharge {
		return false
	}
	if policy.MinRuntimeMinutes > 0 && status.RuntimeMinutes > policy.MinRuntimeMinutes {
		return false
	}
	return true
}
