package proxy

import (
	"encoding/json"
	"log/slog"
	"testing"
)

func TestParseValueToFloat_RegressionCases(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want float64
	}{
		{name: "string", in: "12.5", want: 12.5},
		{name: "float64", in: 7.25, want: 7.25},
		{name: "json number", in: json.Number("42"), want: 42},
		{name: "invalid", in: "not-a-number", want: 0},
		{name: "unsupported", in: true, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseValueToFloat(tt.in); got != tt.want {
				t.Fatalf("parseValueToFloat(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyConstantBinaryOp_RegressionCases(t *testing.T) {
	tests := []struct {
		name   string
		left   float64
		right  float64
		op     string
		want   float64
		wantOK bool
	}{
		{name: "add", left: 2, right: 3, op: "+", want: 5, wantOK: true},
		{name: "subtract", left: 7, right: 4, op: "-", want: 3, wantOK: true},
		{name: "multiply", left: 6, right: 5, op: "*", want: 30, wantOK: true},
		{name: "divide", left: 8, right: 2, op: "/", want: 4, wantOK: true},
		{name: "divide by zero", left: 8, right: 0, op: "/", want: 0, wantOK: false},
		{name: "unsupported", left: 8, right: 2, op: "%", want: 0, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := applyConstantBinaryOp(tt.left, tt.right, tt.op)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("applyConstantBinaryOp(%v, %v, %q) = (%v, %v), want (%v, %v)", tt.left, tt.right, tt.op, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestTruncateQuery_RegressionCases(t *testing.T) {
	if got := truncateQuery("short", 10); got != "short" {
		t.Fatalf("unexpected short truncation: %q", got)
	}

	if got := truncateQuery("exact", 5); got != "exact" {
		t.Fatalf("unexpected exact truncation: %q", got)
	}

	if got := truncateQuery("0123456789", 5); got != "01234..." {
		t.Fatalf("unexpected truncated query: %q", got)
	}
}

func TestRedactAttr_RegressionCases(t *testing.T) {
	t.Run("redacts sensitive key entirely", func(t *testing.T) {
		attr := redactAttr(slog.String("Authorization", "Bearer secret-token"))
		if got := attr.Value.String(); got != redactReplacement {
			t.Fatalf("expected replacement for sensitive key, got %q", got)
		}
	})

	t.Run("redacts secrets inside string values", func(t *testing.T) {
		attr := redactAttr(slog.String("body", "Authorization: Bearer secret-token"))
		if got := attr.Value.String(); got == "Authorization: Bearer secret-token" {
			t.Fatalf("expected string value to be redacted, got %q", got)
		}
	})

	t.Run("redacts nested group attrs", func(t *testing.T) {
		attr := redactAttr(slog.Group("request",
			slog.String("body", "api_key=secret"),
			slog.String("password", "super-secret"),
		))
		group := attr.Value.Group()
		if len(group) != 2 {
			t.Fatalf("expected 2 group attrs, got %d", len(group))
		}
		if group[0].Value.String() == "api_key=secret" {
			t.Fatalf("expected nested string value to be redacted, got %q", group[0].Value.String())
		}
		if group[1].Value.String() != redactReplacement {
			t.Fatalf("expected nested sensitive key to be fully redacted, got %q", group[1].Value.String())
		}
	})
}
