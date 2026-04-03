package translator

import (
	"testing"
)

func TestTranslateLogQLWithLabels(t *testing.T) {
	// Simulate a label translator that converts underscore labels to dotted VL fields
	labelFn := func(label string) string {
		mapping := map[string]string{
			"service_name":      "service.name",
			"k8s_pod_name":      "k8s.pod.name",
			"k8s_namespace_name": "k8s.namespace.name",
			"host_name":         "host.name",
		}
		if mapped, ok := mapping[label]; ok {
			return mapped
		}
		return label
	}

	tests := []struct {
		name  string
		logql string
		want  string
	}{
		{
			name:  "simple label translated and quoted",
			logql: `{service_name="auth"}`,
			want:  `"service.name":=auth`,
		},
		{
			name:  "multiple labels, one translated",
			logql: `{service_name="auth",level="error"}`,
			want:  `"service.name":=auth level:=error`,
		},
		{
			name:  "k8s label translated",
			logql: `{k8s_pod_name="my-pod"}`,
			want:  `"k8s.pod.name":=my-pod`,
		},
		{
			name:  "non-mapped label passes through",
			logql: `{app="nginx"}`,
			want:  `app:=nginx`,
		},
		{
			name:  "regex matcher with translated label",
			logql: `{service_name=~"auth.*"}`,
			want:  `"service.name":~"auth.*"`,
		},
		{
			name:  "negated matcher with translated label",
			logql: `{service_name!="auth"}`,
			want:  `-"service.name":=auth`,
		},
		{
			name:  "negated regex with translated label",
			logql: `{service_name!~"auth.*"}`,
			want:  `-"service.name":~"auth.*"`,
		},
		{
			name:  "label with line filter",
			logql: `{service_name="auth"} |= "error"`,
			want:  `"service.name":=auth ~"error"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TranslateLogQLWithLabels(tt.logql, labelFn)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("TranslateLogQLWithLabels(%q) =\n  got:  %q\n  want: %q", tt.logql, got, tt.want)
			}
		})
	}
}

func TestTranslateLogQLWithLabels_NilFn(t *testing.T) {
	// With nil labelFn, should behave like TranslateLogQL
	got, err := TranslateLogQLWithLabels(`{app="nginx"}`, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `app:=nginx` {
		t.Errorf("nil labelFn: got %q, want %q", got, `app:=nginx`)
	}
}
