package master

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"nut-server/internal/config"
)

// upsd answers LIST VAR with one line per variable; the caps bound what a
// misbehaving server can make the master buffer.
const (
	maxNUTLineBytes = 4096
	maxNUTVars      = 1024
)

// ReadNUTStatus lists the UPS variables from a NUT upsd. It does not LOGIN, so
// upsmon on the upsd host does not wait for the master before powering off.
func ReadNUTStatus(cfg config.NUTConfig) (UPSStatus, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return UPSStatus{}, fmt.Errorf("connect upsd %s: %w", addr, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return UPSStatus{}, fmt.Errorf("set upsd deadline: %w", err)
	}
	if _, err := fmt.Fprintf(conn, "LIST VAR %s\n", cfg.UPS); err != nil {
		return UPSStatus{}, fmt.Errorf("send LIST VAR to upsd %s: %w", addr, err)
	}
	vars, err := readNUTVarList(bufio.NewReaderSize(conn, maxNUTLineBytes), cfg.UPS)
	if err != nil {
		return UPSStatus{}, fmt.Errorf("upsd %s: %w", cfg.Address(), err)
	}
	// Best effort: without LOGOUT upsd only logs a dropped connection.
	_, _ = fmt.Fprint(conn, "LOGOUT\n")
	return decodeNUTVars(vars)
}

// readNUTVarList reads the reply to "LIST VAR <ups>":
//
//	BEGIN LIST VAR <ups>
//	VAR <ups> <name> "<value>"
//	END LIST VAR <ups>
func readNUTVarList(reader *bufio.Reader, ups string) (map[string]string, error) {
	line, err := readNUTLine(reader)
	if err != nil {
		return nil, err
	}
	if err := nutError(line, ups); err != nil {
		return nil, err
	}
	if line != "BEGIN LIST VAR "+ups {
		return nil, fmt.Errorf("unexpected reply %q to LIST VAR", line)
	}
	vars := make(map[string]string)
	varPrefix := "VAR " + ups + " "
	for {
		line, err := readNUTLine(reader)
		if err != nil {
			return nil, err
		}
		if line == "END LIST VAR "+ups {
			return vars, nil
		}
		rest, ok := strings.CutPrefix(line, varPrefix)
		if !ok {
			return nil, fmt.Errorf("unexpected line %q in LIST VAR reply", line)
		}
		name, value, err := parseNUTVar(rest)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", line, err)
		}
		if len(vars) >= maxNUTVars {
			return nil, fmt.Errorf("LIST VAR reply has more than %d variables", maxNUTVars)
		}
		vars[name] = value
	}
}

func readNUTLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		return "", fmt.Errorf("reply line longer than %d bytes", maxNUTLineBytes)
	}
	if err != nil {
		return "", fmt.Errorf("read reply: %w", err)
	}
	return strings.TrimRight(string(line), "\r\n"), nil
}

// nutError turns an "ERR <code>" reply into an error, naming the fix for the
// codes a misconfiguration or a stopped driver produce.
func nutError(line, ups string) error {
	code, ok := strings.CutPrefix(line, "ERR ")
	if !ok {
		return nil
	}
	code, _, _ = strings.Cut(code, " ")
	switch code {
	case "UNKNOWN-UPS":
		return fmt.Errorf("upsd does not serve a UPS named %q; check nut.ups against ups.conf", ups)
	case "DATA-STALE", "DRIVER-NOT-CONNECTED":
		return fmt.Errorf("upsd reports %s; the NUT driver for %q is not delivering data", code, ups)
	case "ACCESS-DENIED":
		return fmt.Errorf("upsd denied access (ACCESS-DENIED); allow this host in upsd.conf LISTEN / firewall")
	default:
		return fmt.Errorf("upsd error %s", code)
	}
}

// parseNUTVar splits `<name> "<value>"`; values escape '"' and '\' with '\'.
func parseNUTVar(rest string) (string, string, error) {
	name, quoted, ok := strings.Cut(rest, " ")
	if !ok || name == "" {
		return "", "", fmt.Errorf("missing variable value")
	}
	if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return "", "", fmt.Errorf("value is not quoted")
	}
	var value strings.Builder
	body := quoted[1 : len(quoted)-1]
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '"' {
			return "", "", fmt.Errorf("unescaped quote in value")
		}
		if c == '\\' {
			i++
			if i == len(body) {
				return "", "", fmt.Errorf("dangling escape in value")
			}
			c = body[i]
		}
		value.WriteByte(c)
	}
	return name, value.String(), nil
}

// decodeNUTVars maps NUT variables onto a reading. ups.status is required and
// must say OL or OB: during driver startup it can hold neither, and reading
// that as line power would end an outage. battery.charge and battery.runtime
// are optional because many USB UPSes do not report them.
func decodeNUTVars(vars map[string]string) (UPSStatus, error) {
	statusValue, ok := vars["ups.status"]
	if !ok {
		return UPSStatus{}, fmt.Errorf("upsd did not report ups.status")
	}
	flags := strings.Fields(statusValue)
	var status UPSStatus
	var isOnline bool
	for _, flag := range flags {
		switch flag {
		case "OB":
			status.OnBattery = true
		case "OL":
			isOnline = true
		case "LB":
			status.LowBattery = true
		}
	}
	if !status.OnBattery && !isOnline {
		return UPSStatus{}, fmt.Errorf("ups.status %q has neither OL nor OB", statusValue)
	}
	if value, ok := vars["battery.charge"]; ok {
		charge, err := parseNUTNumber("battery.charge", value)
		if err != nil {
			return UPSStatus{}, err
		}
		status.BatteryCharge = &charge
	}
	if value, ok := vars["battery.runtime"]; ok {
		seconds, err := parseNUTNumber("battery.runtime", value)
		if err != nil {
			return UPSStatus{}, err
		}
		minutes := runtimeToMinutes(seconds, config.RuntimeUnitSeconds)
		status.RuntimeMinutes = &minutes
	}
	return status, nil
}

// parseNUTNumber rounds down: drivers may report decimals ("87.5"), and
// rounding down makes a threshold fire no later than the reading would.
func parseNUTNumber(name, value string) (int, error) {
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("%s %q is not a number", name, value)
	}
	// A negative reading would trigger every threshold, like on SNMP.
	if number < 0 {
		return 0, fmt.Errorf("%s returned negative value %s", name, value)
	}
	if number > math.MaxInt32 {
		return 0, fmt.Errorf("%s %q is out of range", name, value)
	}
	return int(math.Floor(number)), nil
}
