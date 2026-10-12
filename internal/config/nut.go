package config

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	defaultNUTPort           = 3493
	defaultNUTTimeoutSeconds = 2
	maxNUTTimeoutSeconds     = 60
)

// nutUPSNamePattern is the character set upsd accepts in ups.conf section
// names. It also keeps the name from breaking the line-based protocol.
var nutUPSNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// NUTConfig reads the UPS from a NUT upsd. The master only lists variables,
// which upsd serves without LOGIN, so no credentials are configured.
type NUTConfig struct {
	Host           string `yaml:"host"`
	Port           uint16 `yaml:"port"`
	UPS            string `yaml:"ups"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// Address is the NUT notation for the UPS, e.g. "myups@10.0.0.5:3493".
func (c NUTConfig) Address() string {
	return fmt.Sprintf("%s@%s:%d", c.UPS, c.Host, c.Port)
}

func normalizeNUT(nut *NUTConfig) error {
	if nut.Host == "" {
		return fmt.Errorf("nut.host must not be empty")
	}
	if strings.ContainsAny(nut.Host, " \t\r\n") {
		return fmt.Errorf("nut.host %q must not contain whitespace", nut.Host)
	}
	if nut.UPS == "" {
		return fmt.Errorf("nut.ups must not be empty; use the UPS name from upsd's ups.conf")
	}
	if !nutUPSNamePattern.MatchString(nut.UPS) {
		return fmt.Errorf("nut.ups %q may only contain letters, digits, '.', '_' and '-'", nut.UPS)
	}
	if nut.Port == 0 {
		nut.Port = defaultNUTPort
	}
	if nut.TimeoutSeconds == 0 {
		nut.TimeoutSeconds = defaultNUTTimeoutSeconds
	}
	if nut.TimeoutSeconds < 1 || nut.TimeoutSeconds > maxNUTTimeoutSeconds {
		return fmt.Errorf("nut.timeout_seconds must be between 1 and %d", maxNUTTimeoutSeconds)
	}
	return nil
}
