package signals_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentnameservice/agent-trust-discovery/internal/domain"
	"github.com/agentnameservice/agent-trust-discovery/internal/scoring/signals"
)

// TestPassDeliveryEvaluate covers the master-plan §11.4 cases: no data, a
// perfect record, a partial record, and the audit-failure cap.
func TestPassDeliveryEvaluate(t *testing.T) {
	s := signals.PassDelivery{}
	cases := []struct {
		name     string
		obs      *domain.SignalObservation
		wantRaw  int
		wantRisk bool
		wantExpl string
	}{
		{"no observation", nil, 0, false, "no audited passes yet"},
		{"zero booked", obsOf(`{"booked":0,"delivered":0,"auditFailures":0}`), 0, false, "no audited passes yet"},
		{"perfect", obsOf(`{"booked":10,"delivered":10,"auditFailures":0}`), 100, false, ""},
		{"partial rounds half up", obsOf(`{"booked":8,"delivered":7,"auditFailures":0}`), 88, false, ""}, // 87.5 -> 88
		{"partial rounds down", obsOf(`{"booked":3,"delivered":1,"auditFailures":0}`), 33, false, ""},    // 33.3 -> 33
		{"audit failure caps a perfect record", obsOf(`{"booked":10,"delivered":10,"auditFailures":1}`), 40, true, ""},
		{"audit failure below cap keeps its score", obsOf(`{"booked":10,"delivered":3,"auditFailures":2}`), 30, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.Evaluate(context.Background(), domain.Agent{}, tc.obs)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if res.Raw != tc.wantRaw {
				t.Errorf("raw = %d, want %d", res.Raw, tc.wantRaw)
			}
			hasRisk := len(res.RiskCodes) == 1 && res.RiskCodes[0] == signals.RiskBehaviorAuditFailure
			if hasRisk != tc.wantRisk {
				t.Errorf("risk = %v, want %v (codes=%v)", hasRisk, tc.wantRisk, res.RiskCodes)
			}
			if tc.wantExpl != "" && res.Explanation != tc.wantExpl {
				t.Errorf("explanation = %q, want %q", res.Explanation, tc.wantExpl)
			}
		})
	}
}

// TestPassDeliveryValidate covers the invalid-payload rules.
func TestPassDeliveryValidate(t *testing.T) {
	s := signals.PassDelivery{}
	good := []string{
		`{"booked":0,"delivered":0,"auditFailures":0}`,
		`{"booked":5,"delivered":5,"auditFailures":0}`,
		`{"booked":5,"delivered":0,"auditFailures":3}`,
	}
	for _, v := range good {
		if err := s.Validate(json.RawMessage(v)); err != nil {
			t.Errorf("Validate(%s) = %v, want nil", v, err)
		}
	}
	bad := []struct {
		v, wantSub string
	}{
		{`{"booked":-1,"delivered":0,"auditFailures":0}`, "non-negative"},
		{`{"booked":1,"delivered":-1,"auditFailures":0}`, "non-negative"},
		{`{"booked":1,"delivered":0,"auditFailures":-2}`, "non-negative"},
		{`{"booked":2,"delivered":3,"auditFailures":0}`, "must not exceed"},
		{`{"booked":"two","delivered":1,"auditFailures":0}`, "invalid value"},
		{`{"booked":1,"delivered":1,"auditFailures":0,"extra":9}`, "invalid value"},
		{`not json`, "invalid value"},
	}
	for _, tc := range bad {
		err := s.Validate(json.RawMessage(tc.v))
		if err == nil {
			t.Errorf("Validate(%s) = nil, want error", tc.v)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("Validate(%s) error = %q, want substring %q", tc.v, err.Error(), tc.wantSub)
		}
	}
}
