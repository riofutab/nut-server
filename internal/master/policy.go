package master

import (
	"slices"
	"sort"
	"strings"

	"nut-server/internal/config"
	"nut-server/internal/protocol"
)

type PolicyMatch struct {
	Names   []string
	Reason  string
	All     bool
	Tags    []string
	NodeIDs []string
}

// buildPolicyMatch combines the targets and reasons of the named policies.
func buildPolicyMatch(policies []config.ShutdownPolicySpec, names []string) PolicyMatch {
	match := PolicyMatch{Names: append([]string(nil), names...)}
	tagSet := map[string]struct{}{}
	nodeSet := map[string]struct{}{}
	for _, p := range policies {
		if !slices.Contains(names, p.Name) {
			continue
		}
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
	match.Tags = sortedKeys(tagSet)
	match.NodeIDs = sortedKeys(nodeSet)
	match.Reason = buildPolicyReason(policies, names)
	return match
}

// policyMatches reports whether an on-battery reading satisfies a policy's
// reading conditions. ups_unreachable_for policies are decided by the absence
// of readings and never match one. A condition on a value the UPS does not
// report never holds.
func policyMatches(status UPSStatus, when config.ShutdownPolicyWhen) bool {
	if when.UPSUnreachableFor.Duration > 0 {
		return false
	}
	if when.ChargeBelow > 0 && (status.BatteryCharge == nil || *status.BatteryCharge > when.ChargeBelow) {
		return false
	}
	if when.RuntimeBelow > 0 && (status.RuntimeMinutes == nil || *status.RuntimeMinutes > when.RuntimeBelow) {
		return false
	}
	if when.LowBattery && !status.LowBattery {
		return false
	}
	return true
}

// Target is the shutdown target covering every matched policy.
func (m PolicyMatch) Target() protocol.ShutdownTarget {
	if m.All {
		return protocol.ShutdownTarget{All: true}
	}
	return protocol.ShutdownTarget{
		Tags:    append([]string(nil), m.Tags...),
		NodeIDs: append([]string(nil), m.NodeIDs...),
	}
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
