package master

import (
	"sort"
	"strings"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

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

type PolicyMatch struct {
	Names   []string
	Reason  string
	All     bool
	Tags    []string
	NodeIDs []string
}

func EvaluatePolicies(status UPSStatus, policies []config.ShutdownPolicySpec) (PolicyMatch, bool) {
	var match PolicyMatch
	tagSet := map[string]struct{}{}
	nodeSet := map[string]struct{}{}
	for _, p := range policies {
		if !policyMatches(status, p.When) {
			continue
		}
		match.Names = append(match.Names, p.Name)
		if p.Target.All {
			match.All = true
		}
		for _, t := range p.Target.Tags {
			tagSet[t] = struct{}{}
		}
		for _, n := range p.Target.NodeIDs {
			nodeSet[n] = struct{}{}
		}
	}
	if len(match.Names) == 0 {
		return PolicyMatch{}, false
	}
	match.Tags = sortedKeys(tagSet)
	match.NodeIDs = sortedKeys(nodeSet)
	match.Reason = buildPolicyReason(policies, match.Names)
	return match, true
}

func policyMatches(status UPSStatus, when config.ShutdownPolicyWhen) bool {
	if when.OnBattery != nil && *when.OnBattery != status.OnBattery {
		return false
	}
	if when.ChargeBelow > 0 && status.BatteryCharge > when.ChargeBelow {
		return false
	}
	if when.RuntimeBelow > 0 && status.RuntimeMinutes > when.RuntimeBelow {
		return false
	}
	return true
}

func (m PolicyMatch) ToRequest() protocol.ShutdownRequest {
	req := protocol.ShutdownRequest{Reason: m.Reason}
	if !m.All {
		req.Tags = append([]string(nil), m.Tags...)
		req.NodeIDs = append([]string(nil), m.NodeIDs...)
	}
	return req
}

func buildPolicyReason(policies []config.ShutdownPolicySpec, names []string) string {
	byName := make(map[string]string, len(policies))
	for _, p := range policies {
		byName[p.Name] = p.Reason
	}
	parts := make([]string, 0, len(names))
	for _, n := range names {
		reason := byName[n]
		if reason == "" {
			reason = n
		}
		parts = append(parts, reason)
	}
	return strings.Join(parts, "; ")
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
