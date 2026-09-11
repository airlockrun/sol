package session

import "testing"

func TestModelLimitsValidate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		limits  ModelLimits
		auto    bool
		wantErr bool
	}{
		{"missing auto", ModelLimits{}, true, true},
		{"disabled", ModelLimits{}, false, false},
		{"input only", ModelLimits{Input: 1000}, true, false},
		{"context", ModelLimits{Context: 100000, Output: 4000}, true, false},
		{"negative input", ModelLimits{Input: -1}, false, true},
		{"negative context", ModelLimits{Context: -1}, true, true},
		{"negative output", ModelLimits{Context: 8192, Output: -1}, true, true},
		{"output equals context", ModelLimits{Context: 8192, Output: 8192}, true, false},
		{"output exceeds context", ModelLimits{Context: 8192, Output: 32768}, true, false},
		{"unknown output", ModelLimits{Context: 8192}, true, false},
		{"minimal context", ModelLimits{Context: 1, Output: 1}, true, false},
		{"output only", ModelLimits{Output: 8192}, true, true},
		{"input beyond context", ModelLimits{Context: 100, Input: 200}, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.limits.Validate(tt.auto); (err != nil) != tt.wantErr {
				t.Fatalf("Validate = %v", err)
			}
		})
	}
}
