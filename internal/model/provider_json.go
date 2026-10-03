package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Confidence is explanatory metadata, never an authorization or verification
// result. Providers commonly use either a label or a numeric score; normalize
// both into display text while rejecting unrelated JSON shapes.
func confidenceText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", fmt.Errorf("confidence must be text or a numeric score")
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || value < 0 || value > 100 {
		return "", fmt.Errorf("confidence score must be between 0 and 100")
	}
	return number.String(), nil
}

func (a *Analysis) UnmarshalJSON(data []byte) error {
	type plain Analysis
	v := struct {
		*plain
		Confidence json.RawMessage `json:"confidence"`
	}{plain: (*plain)(a)}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	var err error
	a.Confidence, err = confidenceText(v.Confidence)
	return err
}

func (p *RepairProposal) UnmarshalJSON(data []byte) error {
	type plain RepairProposal
	v := struct {
		*plain
		Confidence json.RawMessage `json:"confidence"`
	}{plain: (*plain)(p)}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	var err error
	p.Confidence, err = confidenceText(v.Confidence)
	return err
}
