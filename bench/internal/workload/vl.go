package workload

import (
	"fmt"
	"net/url"
	"time"
)

// VLSmall mirrors Small but uses VictoriaLogs native LogsQL API endpoints.
// Queries hit /select/logsql/query, /select/logsql/stats_query,
// /select/logsql/field_names, and /select/logsql/field_values directly.
func VLSmall(now time.Time) Workload {
	start5m := ns(now.Add(-5 * time.Minute))
	start1m := ns(now.Add(-1 * time.Minute))
	end := ns(now)
	step5m := "5m"

	return Workload{Name: "small", Queries: []Query{
		{
			Name:   "field_names",
			Path:   "/select/logsql/field_names",
			Params: url.Values{"query": {"*"}, "start": {start5m}, "end": {end}},
		},
		{
			Name:   "field_values_app",
			Path:   "/select/logsql/field_values",
			Params: url.Values{"fieldName": {"app"}, "query": {"*"}, "start": {start5m}, "end": {end}},
		},
		{
			Name:   "field_values_namespace",
			Path:   "/select/logsql/field_values",
			Params: url.Values{"fieldName": {"namespace"}, "query": {"*"}, "start": {start5m}, "end": {end}},
		},
		{
			Name:   "field_values_level",
			Path:   "/select/logsql/field_values",
			Params: url.Values{"fieldName": {"level"}, "query": {"*"}, "start": {start5m}, "end": {end}},
		},
		{
			Name:   "stream_ids",
			Path:   "/select/logsql/stream_ids",
			Params: url.Values{"query": {`app:*`}, "start": {start5m}, "end": {end}},
		},
		{
			Name:   "log_simple_1m",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"api-gateway"`}, "start": {start1m}, "end": {end}, "limit": {"200"}},
		},
		{
			Name:   "log_simple_5m",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`namespace:"prod"`}, "start": {start5m}, "end": {end}, "limit": {"500"}},
		},
		{
			Name:   "log_filter_error_1m",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"api-gateway" "error"`}, "start": {start1m}, "end": {end}, "limit": {"100"}},
		},
		{
			Name: "stats_count_5m",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`app:"api-gateway" | count()`},
				"start": {start5m}, "end": {end}, "step": {step5m},
			},
		},
		{
			Name: "stats_count_by_app",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (app)`},
				"start": {start5m}, "end": {end}, "step": {step5m},
			},
		},
		{
			Name:   "hits_5m",
			Path:   "/select/logsql/hits",
			Params: url.Values{"query": {`namespace:"prod"`}, "start": {start5m}, "end": {end}, "step": {"1m"}},
		},
		{
			Name:   "stream_ids_ns",
			Path:   "/select/logsql/stream_ids",
			Params: url.Values{"query": {`namespace:"prod"`}, "start": {start5m}, "end": {end}},
		},
	}}
}

// VLHeavy mirrors Heavy but uses VictoriaLogs native LogsQL.
func VLHeavy(now time.Time) Workload {
	start15m := ns(now.Add(-15 * time.Minute))
	start30m := ns(now.Add(-30 * time.Minute))
	start1h := ns(now.Add(-1 * time.Hour))
	start2h := ns(now.Add(-2 * time.Hour))
	end := ns(now)
	step1m := "1m"

	return Workload{Name: "heavy", Queries: []Query{
		{
			Name:   "log_json_status_filter",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"api-gateway" | json | status:>=400`}, "start": {start30m}, "end": {end}, "limit": {"1000"}},
		},
		{
			Name:   "log_json_multi_field",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"api-gateway" | json | status:>=200 | status:<500 | latency_ms:>100`}, "start": {start30m}, "end": {end}, "limit": {"500"}},
		},
		{
			Name:   "log_json_line_format",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"api-gateway" | json`}, "start": {start15m}, "end": {end}, "limit": {"200"}},
		},
		{
			Name:   "log_logfmt_error",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"payment-service" | logfmt | level:"error"`}, "start": {start30m}, "end": {end}, "limit": {"500"}},
		},
		{
			Name:   "log_logfmt_latency",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"worker-service" | logfmt | duration_ms:>5000`}, "start": {start1h}, "end": {end}, "limit": {"200"}},
		},
		{
			Name:   "log_regex_filter",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`namespace:"prod" ~"status=(4|5)[0-9][0-9]"`}, "start": {start30m}, "end": {end}, "limit": {"500"}},
		},
		{
			Name: "stats_count_by_app_1h",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (app)`},
				"start": {start1h}, "end": {end}, "step": {step1m},
			},
		},
		{
			Name: "stats_count_errors_by_app",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`app:"api-gateway" | json | status:>=400 | count() by (app)`},
				"start": {start1h}, "end": {end}, "step": {step1m},
			},
		},
		{
			Name: "stats_count_by_level_2h",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (level)`},
				"start": {start2h}, "end": {end}, "step": {step1m},
			},
		},
		{
			Name: "stats_count_bytes_approx",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (app)`},
				"start": {start1h}, "end": {end}, "step": {step1m},
			},
		},
		{
			Name: "stats_count_topk_approx",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (app)`},
				"start": {start1h}, "end": {end}, "step": {step1m},
			},
		},
		{
			Name:   "field_names_json",
			Path:   "/select/logsql/field_names",
			Params: url.Values{"query": {`namespace:"prod" | json`}, "start": {start30m}, "end": {end}},
		},
		{
			Name:   "hits_1h",
			Path:   "/select/logsql/hits",
			Params: url.Values{"query": {`namespace:"prod"`}, "start": {start1h}, "end": {end}, "step": {"5m"}},
		},
		{
			Name:   "log_full_volume_1h",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"api-gateway"`}, "start": {start1h}, "end": {end}, "limit": {"5000"}},
		},
		{
			Name: "stats_volume_range",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (app)`},
				"start": {start1h}, "end": {end}, "step": {step1m},
			},
		},
	}}
}

// VLLongRange mirrors LongRange but uses VictoriaLogs native LogsQL.
func VLLongRange(now time.Time) Workload {
	start6h := ns(now.Add(-6 * time.Hour))
	start24h := ns(now.Add(-24 * time.Hour))
	start48h := ns(now.Add(-48 * time.Hour))
	start72h := ns(now.Add(-72 * time.Hour))
	end := ns(now)

	return Workload{Name: "long_range", Queries: []Query{
		{
			Name:   "log_select_6h",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"api-gateway"`}, "start": {start6h}, "end": {end}, "limit": {"2000"}},
		},
		{
			Name:   "log_select_24h",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`namespace:"prod"`}, "start": {start24h}, "end": {end}, "limit": {"2000"}},
		},
		{
			Name:   "log_error_48h",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`namespace:"prod" "error"`}, "start": {start48h}, "end": {end}, "limit": {"1000"}},
		},
		{
			Name: "stats_count_by_app_6h",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (app)`},
				"start": {start6h}, "end": {end}, "step": {"5m"},
			},
		},
		{
			Name: "stats_count_by_app_24h",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (app)`},
				"start": {start24h}, "end": {end}, "step": {"5m"},
			},
		},
		{
			Name: "stats_count_by_level_48h",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (level)`},
				"start": {start48h}, "end": {end}, "step": {"1h"},
			},
		},
		{
			Name: "stats_count_by_app_72h",
			Path: "/select/logsql/stats_query",
			Params: url.Values{
				"query": {`namespace:"prod" | count() by (app)`},
				"start": {start72h}, "end": {end}, "step": {"1h"},
			},
		},
		{
			Name:   "field_names_24h",
			Path:   "/select/logsql/field_names",
			Params: url.Values{"query": {"*"}, "start": {start24h}, "end": {end}},
		},
		{
			Name:   "stream_ids_24h",
			Path:   "/select/logsql/stream_ids",
			Params: url.Values{"query": {`namespace:"prod"`}, "start": {start24h}, "end": {end}},
		},
		{
			Name:   "log_json_errors_24h",
			Path:   "/select/logsql/query",
			Params: url.Values{"query": {`app:"api-gateway" | json | status:>=400`}, "start": {start24h}, "end": {end}, "limit": {"5000"}},
		},
	}}
}

// VLAll returns all VL-native workloads for the given time reference.
func VLAll(now time.Time) []Workload {
	return []Workload{VLSmall(now), VLHeavy(now), VLLongRange(now)}
}

// VLByName returns the named VL-native workloads.
func VLByName(names []string, now time.Time) []Workload {
	all := VLAll(now)
	if len(names) == 0 {
		return all
	}
	m := make(map[string]Workload, len(all))
	for _, w := range all {
		m[w.Name] = w
	}
	var result []Workload
	for _, n := range names {
		if w, ok := m[n]; ok {
			result = append(result, w)
		}
	}
	return result
}

// vlStep converts a seconds integer to a VL-compatible duration string.
func vlStep(seconds int) string {
	if seconds%3600 == 0 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}
