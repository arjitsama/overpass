package signals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/agentnameservice/agent-trust-discovery/internal/domain"
	"github.com/agentnameservice/agent-trust-discovery/internal/port"
)

// RiskBehaviorAuditFailure is raised when a pass_delivery observation records at
// least one audit failure (a canary acceptance or a chain mismatch). It caps the
// behavior score so a station that betrays a booking cannot hold a fiduciary tier.
const RiskBehaviorAuditFailure = "BEHAVIOR_AUDIT_FAILURE"

// auditFailureCap is the highest behavior score a station with any audit failure
// may score, regardless of its delivery ratio (master plan §11.4).
const auditFailureCap = 40

// PassDelivery scores the behavior dimension from Overpass's own auditor: how
// many booked passes the station actually delivered, and whether any audited
// pass failed (a canary acceptance or a chain mismatch). It is an Overpass
// addition to the upstream built-ins; see third_party/PATCHES.md.
//
// A missing observation scores 0 ("no audited passes yet") and counts toward the
// dimension: a station with no audited history cannot reach a fiduciary tier.
// This cold-start behavior is deliberate (master plan §11), so PassDelivery does
// not implement port.AbsenceAware.
type PassDelivery struct{}

func (PassDelivery) ID() domain.SignalID         { return "pass_delivery" }
func (PassDelivery) Dimension() domain.Dimension { return domain.DimensionBehavior }
func (PassDelivery) Derived() bool               { return false }

type passDeliveryValue struct {
	Booked        int `json:"booked"`
	Delivered     int `json:"delivered"`
	AuditFailures int `json:"auditFailures"`
}

func decodePassDelivery(value json.RawMessage) (passDeliveryValue, error) {
	// DisallowUnknownFields keeps the schema tight: the signal is the schema
	// (design §5.2.1), so a typo'd field is a validation error, not silently 0.
	dec := json.NewDecoder(bytes.NewReader(value))
	dec.DisallowUnknownFields()
	var v passDeliveryValue
	if err := dec.Decode(&v); err != nil {
		return passDeliveryValue{}, fmt.Errorf("pass_delivery: invalid value: %w", err)
	}
	return v, nil
}

// Validate enforces non-negative integers with delivered <= booked.
func (PassDelivery) Validate(value json.RawMessage) error {
	v, err := decodePassDelivery(value)
	if err != nil {
		return err
	}
	if v.Booked < 0 || v.Delivered < 0 || v.AuditFailures < 0 {
		return fmt.Errorf("pass_delivery: booked, delivered and auditFailures must be non-negative")
	}
	if v.Delivered > v.Booked {
		return fmt.Errorf("pass_delivery: delivered (%d) must not exceed booked (%d)", v.Delivered, v.Booked)
	}
	return nil
}

// Evaluate scores the delivery ratio, capped on any audit failure.
func (PassDelivery) Evaluate(_ context.Context, _ domain.Agent, obs *domain.SignalObservation) (port.SignalResult, error) {
	if obs == nil {
		return port.SignalResult{
			Raw:         0,
			Explanation: "no audited passes yet",
			Attestation: domain.AttestationUnattested,
		}, nil
	}
	v, err := decodePassDelivery(obs.Value)
	if err != nil {
		return port.SignalResult{}, err
	}
	if v.Booked == 0 {
		return port.SignalResult{
			Raw:         0,
			Explanation: "no audited passes yet",
			Attestation: domain.AttestationUnattested,
		}, nil
	}
	raw := int((100*int64(v.Delivered) + int64(v.Booked)/2) / int64(v.Booked)) // round half up
	var risks []string
	explanation := fmt.Sprintf("delivered %d of %d booked; raw %d", v.Delivered, v.Booked, raw)
	if v.AuditFailures > 0 {
		if raw > auditFailureCap {
			raw = auditFailureCap
		}
		risks = []string{RiskBehaviorAuditFailure}
		explanation = fmt.Sprintf("delivered %d of %d booked with %d audit failure(s); capped at %d",
			v.Delivered, v.Booked, v.AuditFailures, raw)
	}
	return port.SignalResult{
		Raw:         raw,
		Explanation: explanation,
		Attestation: domain.AttestationUnattested,
		RiskCodes:   risks,
	}, nil
}
