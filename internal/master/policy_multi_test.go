package master

import (
	"reflect"
	"testing"

	"nut-server/internal/config"
)

func boolPtr(b bool) *bool { return &b }

func TestEvaluatePoliciesSingleANDCondition(t *testing.T) {
	policies := []config.ShutdownPolicySpec{
		{
			Name: "charge+runtime",
			When: config.ShutdownPolicyWhen{
				OnBattery:    boolPtr(true),
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
		{"all conditions met", UPSStatus{OnBattery: true, BatteryCharge: 25, RuntimeMinutes: 10}, true},
		{"not on battery", UPSStatus{OnBattery: false, BatteryCharge: 25, RuntimeMinutes: 10}, false},
		{"charge above", UPSStatus{OnBattery: true, BatteryCharge: 40, RuntimeMinutes: 10}, false},
		{"runtime above", UPSStatus{OnBattery: true, BatteryCharge: 25, RuntimeMinutes: 20}, false},
		{"boundary equal triggers", UPSStatus{OnBattery: true, BatteryCharge: 30, RuntimeMinutes: 15}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := EvaluatePolicies(tc.status, policies)
			if ok != tc.trigger {
				t.Fatalf("trigger=%v want %v", ok, tc.trigger)
			}
		})
	}
}

func TestEvaluatePoliciesOnBatterySymmetric(t *testing.T) {
	cases := []struct {
		name      string
		want      *bool
		status    UPSStatus
		triggered bool
	}{
		{"nil ignores battery state on=true", nil, UPSStatus{OnBattery: true, BatteryCharge: 10}, true},
		{"nil ignores battery state on=false", nil, UPSStatus{OnBattery: false, BatteryCharge: 10}, true},
		{"require on_battery=true matches", boolPtr(true), UPSStatus{OnBattery: true, BatteryCharge: 10}, true},
		{"require on_battery=true rejects line power", boolPtr(true), UPSStatus{OnBattery: false, BatteryCharge: 10}, false},
		{"require on_battery=false matches line power", boolPtr(false), UPSStatus{OnBattery: false, BatteryCharge: 10}, true},
		{"require on_battery=false rejects battery", boolPtr(false), UPSStatus{OnBattery: true, BatteryCharge: 10}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policies := []config.ShutdownPolicySpec{{
				Name: "x", When: config.ShutdownPolicyWhen{OnBattery: tc.want, ChargeBelow: 20},
				Target: config.ShutdownPolicyTarget{All: true},
			}}
			_, ok := EvaluatePolicies(tc.status, policies)
			if ok != tc.triggered {
				t.Fatalf("triggered=%v want %v", ok, tc.triggered)
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
		match, ok := EvaluatePolicies(UPSStatus{BatteryCharge: 40}, policies)
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
		match, ok := EvaluatePolicies(UPSStatus{BatteryCharge: 10}, policies)
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
	match, ok := EvaluatePolicies(UPSStatus{BatteryCharge: 40}, policies)
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
	match, ok := EvaluatePolicies(UPSStatus{BatteryCharge: 10}, policies)
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
	if _, ok := EvaluatePolicies(UPSStatus{BatteryCharge: 50}, policies); ok {
		t.Fatal("should not trigger")
	}
}

func TestPolicyMatchToRequestAllOmitsTagsAndNodes(t *testing.T) {
	m := PolicyMatch{Reason: "r", All: true, Tags: []string{"web"}, NodeIDs: []string{"db"}}
	req := m.ToRequest()
	if len(req.Tags) != 0 || len(req.NodeIDs) != 0 {
		t.Fatalf("All=true should drop tags/node_ids; got %+v", req)
	}
	if req.Reason != "r" {
		t.Fatalf("reason=%q", req.Reason)
	}
}

func TestPolicyMatchToRequestScopedKeepsFilters(t *testing.T) {
	m := PolicyMatch{Reason: "r", Tags: []string{"web"}, NodeIDs: []string{"db"}}
	req := m.ToRequest()
	if !reflect.DeepEqual(req.Tags, []string{"web"}) {
		t.Fatalf("tags=%v", req.Tags)
	}
	if !reflect.DeepEqual(req.NodeIDs, []string{"db"}) {
		t.Fatalf("node_ids=%v", req.NodeIDs)
	}
}
