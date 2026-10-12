package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Profile names accepted by snmp.profile.
const (
	SNMPProfileUPSMIB     = "ups-mib"
	SNMPProfileAPC        = "apc"
	SNMPProfileCyberPower = "cyberpower"
	SNMPProfileEaton      = "eaton"
)

// SNMPv3 security levels accepted by snmp.security_level.
const (
	SNMPSecurityNoAuthNoPriv = "noAuthNoPriv"
	SNMPSecurityAuthNoPriv   = "authNoPriv"
	SNMPSecurityAuthPriv     = "authPriv"
)

// minUSMPassphraseLength is the RFC 3414 minimum; agents reject shorter ones.
const minUSMPassphraseLength = 8

// snmpAuthProtocols and snmpPrivProtocols are the USM protocols gosnmp
// supports; internal/master maps the same names onto gosnmp constants.
var (
	snmpAuthProtocols = []string{"MD5", "SHA", "SHA224", "SHA256", "SHA384", "SHA512"}
	snmpPrivProtocols = []string{"DES", "AES", "AES192", "AES256", "AES192C", "AES256C"}
)

type snmpProfile struct {
	onBattery SNMPStateVar
	charge    SNMPValueVar
	runtime   SNMPValueVar
}

// snmpProfiles are the built-in OID presets. The vendor entries follow each
// vendor's MIB but must be verified against the MIB files and real hardware
// before a release relies on them.
var snmpProfiles = map[string]snmpProfile{
	// RFC 1628 UPS-MIB: upsOutputSource battery(5), upsEstimatedChargeRemaining,
	// upsEstimatedMinutesRemaining.
	SNMPProfileUPSMIB: {
		onBattery: SNMPStateVar{OID: ".1.3.6.1.2.1.33.1.4.1.0", Values: []int{5}},
		charge:    SNMPValueVar{OID: ".1.3.6.1.2.1.33.1.2.4.0"},
		runtime:   SNMPValueVar{OID: ".1.3.6.1.2.1.33.1.2.3.0", Unit: RuntimeUnitMinutes},
	},
	// PowerNet-MIB: upsBasicOutputStatus onBattery(3), upsAdvBatteryCapacity,
	// upsAdvBatteryRunTimeRemaining.
	SNMPProfileAPC: {
		onBattery: SNMPStateVar{OID: ".1.3.6.1.4.1.318.1.1.1.4.1.1.0", Values: []int{3}},
		charge:    SNMPValueVar{OID: ".1.3.6.1.4.1.318.1.1.1.2.2.1.0"},
		runtime:   SNMPValueVar{OID: ".1.3.6.1.4.1.318.1.1.1.2.2.3.0", Unit: RuntimeUnitTimeTicks},
	},
	// CPS-MIB: upsBaseOutputStatus onBattery(3), upsAdvanceBatteryCapacity,
	// upsAdvanceBatteryRunTimeRemaining.
	SNMPProfileCyberPower: {
		onBattery: SNMPStateVar{OID: ".1.3.6.1.4.1.3808.1.1.1.4.1.1.0", Values: []int{3}},
		charge:    SNMPValueVar{OID: ".1.3.6.1.4.1.3808.1.1.1.2.2.1.0"},
		runtime:   SNMPValueVar{OID: ".1.3.6.1.4.1.3808.1.1.1.2.2.4.0", Unit: RuntimeUnitTimeTicks},
	},
	// XUPS-MIB: xupsOutputSource battery(5), xupsBatCapacity,
	// xupsBatTimeRemaining.
	SNMPProfileEaton: {
		onBattery: SNMPStateVar{OID: ".1.3.6.1.4.1.534.1.4.5.0", Values: []int{5}},
		charge:    SNMPValueVar{OID: ".1.3.6.1.4.1.534.1.2.4.0"},
		runtime:   SNMPValueVar{OID: ".1.3.6.1.4.1.534.1.2.1.0", Unit: RuntimeUnitSeconds},
	},
}

// ResolveSNMPVars fills OnBattery / Charge / Runtime from the selected profile
// (ups-mib when empty), keeping and validating any explicit overrides.
func ResolveSNMPVars(snmp *SNMPConfig) error {
	if snmp.Profile == "" {
		snmp.Profile = SNMPProfileUPSMIB
	}
	profile, ok := snmpProfiles[snmp.Profile]
	if !ok {
		return fmt.Errorf("snmp.profile %q is unknown; use one of %s", snmp.Profile, strings.Join(slices.Sorted(maps.Keys(snmpProfiles)), ", "))
	}

	if snmp.OnBattery == nil {
		snmp.OnBattery = &SNMPStateVar{OID: profile.onBattery.OID, Values: slices.Clone(profile.onBattery.Values)}
	} else {
		if snmp.OnBattery.OID == "" {
			return fmt.Errorf("snmp.on_battery.oid must not be empty")
		}
		if len(snmp.OnBattery.Values) == 0 {
			return fmt.Errorf("snmp.on_battery.values must list at least one value meaning on battery")
		}
	}

	if snmp.Charge == nil {
		charge := profile.charge
		snmp.Charge = &charge
	} else {
		if snmp.Charge.OID == "" {
			return fmt.Errorf("snmp.charge.oid must not be empty")
		}
		if snmp.Charge.Unit != "" {
			return fmt.Errorf("snmp.charge.unit is not supported; charge is always a percentage")
		}
	}

	if snmp.Runtime == nil {
		runtime := profile.runtime
		snmp.Runtime = &runtime
		return nil
	}
	if snmp.Runtime.OID == "" {
		return fmt.Errorf("snmp.runtime.oid must not be empty")
	}
	switch snmp.Runtime.Unit {
	case "":
		snmp.Runtime.Unit = RuntimeUnitMinutes
	case RuntimeUnitMinutes, RuntimeUnitSeconds, RuntimeUnitTimeTicks:
	default:
		return fmt.Errorf("snmp.runtime.unit %q is invalid; use %q, %q or %q", snmp.Runtime.Unit, RuntimeUnitMinutes, RuntimeUnitSeconds, RuntimeUnitTimeTicks)
	}
	return nil
}

func normalizeSNMPVersion(snmp *SNMPConfig) error {
	switch snmp.Version {
	case "":
		snmp.Version = "2c"
	case "1", "2c", "3":
	case "v3":
		snmp.Version = "3"
	default:
		return fmt.Errorf("snmp.version %q is invalid; use \"1\", \"2c\" or \"3\"", snmp.Version)
	}
	if snmp.Version != "3" {
		return rejectUSMSettings(snmp)
	}
	return validateUSM(snmp)
}

func rejectUSMSettings(snmp *SNMPConfig) error {
	set := []struct{ key, value string }{
		{"username", snmp.Username},
		{"security_level", snmp.SecurityLevel},
		{"auth_protocol", snmp.AuthProtocol},
		{"auth_passphrase", snmp.AuthPassphrase},
		{"priv_protocol", snmp.PrivProtocol},
		{"priv_passphrase", snmp.PrivPassphrase},
	}
	for _, field := range set {
		if field.value != "" {
			return fmt.Errorf("snmp.%s is only used by SNMPv3; remove it or set snmp.version: \"3\"", field.key)
		}
	}
	return nil
}

func validateUSM(snmp *SNMPConfig) error {
	if snmp.Community != "" {
		return fmt.Errorf("snmp.community is not used by SNMPv3; remove it and configure snmp.username / security_level")
	}
	if snmp.Username == "" {
		return fmt.Errorf("snmp.username must not be empty for SNMPv3")
	}
	var usesAuth, usesPriv bool
	switch snmp.SecurityLevel {
	case SNMPSecurityNoAuthNoPriv:
	case SNMPSecurityAuthNoPriv:
		usesAuth = true
	case SNMPSecurityAuthPriv:
		usesAuth, usesPriv = true, true
	default:
		return fmt.Errorf("snmp.security_level %q is invalid; use %q, %q or %q", snmp.SecurityLevel, SNMPSecurityNoAuthNoPriv, SNMPSecurityAuthNoPriv, SNMPSecurityAuthPriv)
	}
	if err := validateUSMSecret(usesAuth, snmp.SecurityLevel, "auth", snmp.AuthProtocol, snmp.AuthPassphrase, snmpAuthProtocols); err != nil {
		return err
	}
	return validateUSMSecret(usesPriv, snmp.SecurityLevel, "priv", snmp.PrivProtocol, snmp.PrivPassphrase, snmpPrivProtocols)
}

// validateUSMSecret checks one auth_* / priv_* pair. Settings the security
// level does not use are rejected so a config never looks encrypted when it
// is not.
func validateUSMSecret(isUsed bool, level, prefix, protocol, passphrase string, protocols []string) error {
	if !isUsed {
		if protocol != "" || passphrase != "" {
			return fmt.Errorf("snmp.%s_protocol / %s_passphrase are not used with security_level %q", prefix, prefix, level)
		}
		return nil
	}
	if !slices.Contains(protocols, protocol) {
		return fmt.Errorf("snmp.%s_protocol %q is invalid for security_level %q; use one of %s", prefix, protocol, level, strings.Join(protocols, ", "))
	}
	if len(passphrase) < minUSMPassphraseLength {
		return fmt.Errorf("snmp.%s_passphrase must be at least %d characters", prefix, minUSMPassphraseLength)
	}
	return nil
}
