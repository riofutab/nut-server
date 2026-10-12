package master

import (
	"reflect"
	"testing"

	"nut-server/internal/config"
)

func boolPtr(b bool) *bool { return &b }

// evaluatePolicies applies one reading to every policy, ignoring debounce.
func evaluatePolicies(status UPSStatus, policies []config.ShutdownPolicySpec) (PolicyMatch, bool) {
	var names []string
	for _, p := range policies {
		if policyMatches(status, p.When) {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return PolicyMatch{}, false
	}
	return buildPolicyMatch(policies, names), true
}

func TestEvaluatePoliciesSingleANDCondition(t *testing.T) {
	policies := []config.ShutdownPolicySpec{
		{
			Name: "charge+runtime",
			When: config.ShutdownPolicyWhen{
				ChargeBelow:  30,
				RuntimeBelow: 15,
			},
			Target: config.ShutdownPolicyTarget{All: true},
			Reason: "low UPS",
		},
	}

	cases := []struct {
		name    string
		status  UPSStatus
		trigger bool
	}{
		{"all conditions met", UPSStatus{OnBattery: true, BatteryCharge: new(25), RuntimeMinutes: new(10)}, true},
		{"charge above", UPSStatus{OnBattery: true, BatteryCharge: new(40), RuntimeMinutes: new(10)}, false},
		{"runtime above", UPSStatus{OnBattery: true, BatteryCharge: new(25), RuntimeMinutes: new(20)}, false},
		{"boundary equal triggers", UPSStatus{OnBattery: true, BatteryCharge: new(30), RuntimeMinutes: new(15)}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := evaluatePolicies(tc.status, policies)
			if ok != tc.trigger {
				t.Fatalf("trigger=%v want %v", ok, tc.trigger)
			}
		})
	}
}

func TestEvaluatePoliciesMultiOR(t *testing.T) {
	policies := []config.ShutdownPolicySpec{
		{
			Name:   "dev-early",
			When:   config.ShutdownPolicyWhen{ChargeBelow: 50},
			Target: config.ShutdownPolicyTarget{Tags: []string{"dev"}},
			Reason: "dev early",
		},
		{
			Name:   "critical-all",
			When:   config.ShutdownPolicyWhen{ChargeBelow: 20},
			Target: config.ShutdownPolicyTarget{All: true},
			Reason: "critical",
		},
	}

	t.Run("only dev fires", func(t *testing.T) {
		match, ok := evaluatePolicies(UPSStatus{BatteryCharge: new(40)}, policies)
		if !ok || len(match.Names) != 1 || match.Names[0] != "dev-early" {
			t.Fatalf("names=%v ok=%v", match.Names, ok)
		}
		if match.All {
			t.Fatalf("should not be All when only dev policy fires")
		}
		if !reflect.DeepEqual(match.Tags, []string{"dev"}) {
			t.Fatalf("tags=%v", match.Tags)
		}
	})

	t.Run("both fire union to all", func(t *testing.T) {
		match, ok := evaluatePolicies(UPSStatus{BatteryCharge: new(10)}, policies)
		if !ok {
			t.Fatal("should trigger")
		}
		if !match.All {
			t.Fatalf("expected All=true after union, got %+v", match)
		}
		if len(match.Names) != 2 {
			t.Fatalf("names=%v want both", match.Names)
		}
	})
}

func TestEvaluatePoliciesTargetUnionByTagsAndNodeIDs(t *testing.T) {
	policies := []config.ShutdownPolicySpec{
		{
			Name:   "a",
			When:   config.ShutdownPolicyWhen{ChargeBelow: 50},
			Target: config.ShutdownPolicyTarget{Tags: []string{"web"}, NodeIDs: []string{"db-01"}},
		},
		{
			Name:   "b",
			When:   config.ShutdownPolicyWhen{ChargeBelow: 50},
			Target: config.ShutdownPolicyTarget{Tags: []string{"db", "web"}, NodeIDs: []string{"cache-01"}},
		},
	}
	match, ok := evaluatePolicies(UPSStatus{BatteryCharge: new(40)}, policies)
	if !ok {
		t.Fatal("should trigger")
	}
	if match.All {
		t.Fatalf("should not be All")
	}
	if !reflect.DeepEqual(match.Tags, []string{"db", "web"}) {
		t.Fatalf("tags=%v want sorted dedup [db web]", match.Tags)
	}
	if !reflect.DeepEqual(match.NodeIDs, []string{"cache-01", "db-01"}) {
		t.Fatalf("node_ids=%v", match.NodeIDs)
	}
}

func TestEvaluatePoliciesReasonJoinsMatches(t *testing.T) {
	policies := []config.ShutdownPolicySpec{
		{Name: "a", When: config.ShutdownPolicyWhen{ChargeBelow: 50}, Reason: "alpha"},
		{Name: "b", When: config.ShutdownPolicyWhen{ChargeBelow: 50}, Reason: "beta"},
	}
	match, ok := evaluatePolicies(UPSStatus{BatteryCharge: new(10)}, policies)
	if !ok {
		t.Fatal("should trigger")
	}
	if match.Reason != "alpha; beta" {
		t.Fatalf("reason=%q want %q", match.Reason, "alpha; beta")
	}
}

func TestEvaluatePoliciesNoMatchReturnsFalse(t *testing.T) {
	policies := []config.ShutdownPolicySpec{
		{Name: "a", When: config.ShutdownPolicyWhen{ChargeBelow: 10}, Target: config.ShutdownPolicyTarget{All: true}},
	}
	if _, ok := evaluatePolicies(UPSStatus{BatteryCharge: new(50)}, policies); ok {
		t.Fatal("should not trigger")
	}
}

func TestPolicyMatchTargetAllOmitsTagsAndNodes(t *testing.T) {
	m := PolicyMatch{Reason: "r", All: true, Tags: []string{"web"}, NodeIDs: []string{"db"}}
	target := m.Target()
	if !target.All || len(target.Tags) != 0 || len(target.NodeIDs) != 0 {
		t.Fatalf("All=true should drop tags/node_ids; got %+v", target)
	}
}

func TestPolicyMatchTargetScopedKeepsFilters(t *testing.T) {
	m := PolicyMatch{Reason: "r", Tags: []string{"web"}, NodeIDs: []string{"db"}}
	target := m.Target()
	if target.All {
		t.Fatal("scoped match must not target all")
	}
	if !reflect.DeepEqual(target.Tags, []string{"web"}) {
		t.Fatalf("tags=%v", target.Tags)
	}
	if !reflect.DeepEqual(target.NodeIDs, []string{"db"}) {
		t.Fatalf("node_ids=%v", target.NodeIDs)
	}
}
