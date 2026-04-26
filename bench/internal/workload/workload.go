// Package workload defines the query workloads used in benchmarks.
package workload

import (
	"fmt"
	"net/url"
	"time"
)

// Query is a single HTTP request definition.
type Query struct {
	Name   string
	Method string // GET or POST
	Path   string // URL path
	Params url.Values
}

func (q Query) URL(base string) string {
	u := base + q.Path
	if len(q.Params) > 0 {
		u += "?" + q.Params.Encode()
	}
	return u
}

// Workload is a named collection of queries.
type Workload struct {
	Name    string
	Queries []Query
}

// Small: metadata + short log selects (≤5 min window).
// Exercises Grafana Explore label browser, small panel refreshes, metadata cache (T0/L1).
func Small(now time.Time) Workload {
	start5m := ns(now.Add(-5 * time.Minute))
	start1m := ns(now.Add(-1 * time.Minute))
	end := ns(now)

	return Workload{Name: "small", Queries: []Query{
		{
			Name:   "labels",
			Path:   "/loki/api/v1/labels",
			Params: url.Values{"start": {start5m}, "end": {end}},
		},
		{
			Name:   "label_values_app",
			Path:   "/loki/api/v1/label/app/values",
			Params: url.Values{"start": {start5m}, "end": {end}},
		},
		{
			Name:   "label_values_namespace",
			Path:   "/loki/api/v1/label/namespace/values",
			Params: url.Values{"start": {start5m}, "end": {end}},
		},
		{
			Name:   "label_values_level",
			Path:   "/loki/api/v1/label/level/values",
			Params: url.Values{"start": {start5m}, "end": {end}},
		},
		{
			Name: "series",
			Path: "/loki/api/v1/series",
			Params: url.Values{
				"match[]": {`{app=~".+"}`},
				"start":   {start5m},
				"end":     {end},
			},
		},
		{
			Name: "query_range_simple_1m",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"}`},
				"start": {start1m},
				"end":   {end},
				"limit": {"200"},
			},
		},
		{
			Name: "query_range_simple_5m",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{namespace="prod"}`},
				"start": {start5m},
				"end":   {end},
				"limit": {"500"},
			},
		},
		{
			Name: "query_range_filter_1m",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"} |= "error"`},
				"start": {start1m},
				"end":   {end},
				"limit": {"100"},
			},
		},
		{
			Name: "query_instant_count",
			Path: "/loki/api/v1/query",
			Params: url.Values{
				"query": {`count_over_time({app="api-gateway"}[5m])`},
				"time":  {end},
			},
		},
		{
			Name: "query_instant_rate",
			Path: "/loki/api/v1/query",
			Params: url.Values{
				"query": {`sum by (app) (rate({namespace="prod"}[1m]))`},
				"time":  {end},
			},
		},
		{
			Name: "detected_fields_small",
			Path: "/loki/api/v1/detected_fields",
			Params: url.Values{
				"query": {`{app="api-gateway"}`},
				"start": {start5m},
				"end":   {end},
			},
		},
		{
			Name: "index_stats",
			Path: "/loki/api/v1/index/stats",
			Params: url.Values{
				"query": {`{namespace="prod"}`},
				"start": {start5m},
				"end":   {end},
			},
		},
	}}
}

// Heavy: complex pipelines, metric aggregations, full-volume log returns.
// Exercises proxy translation overhead, VL field indexing, metric shaping.
func Heavy(now time.Time) Workload {
	start15m := ns(now.Add(-15 * time.Minute))
	start30m := ns(now.Add(-30 * time.Minute))
	start1h := ns(now.Add(-1 * time.Hour))
	start2h := ns(now.Add(-2 * time.Hour))
	end := ns(now)

	return Workload{Name: "heavy", Queries: []Query{
		// JSON parse + filter — exercises proxy | json translation + VL field search.
		{
			Name: "json_parse_filter_status",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"} | json | status >= 400`},
				"start": {start30m},
				"end":   {end},
				"limit": {"1000"},
			},
		},
		{
			Name: "json_parse_multi_field",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"} | json | status >= 200 | status < 500 | latency_ms > 100`},
				"start": {start30m},
				"end":   {end},
				"limit": {"500"},
			},
		},
		{
			Name: "json_line_format",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"} | json | line_format "{{.method}} {{.path}} {{.status}} {{.latency_ms}}ms"`},
				"start": {start15m},
				"end":   {end},
				"limit": {"200"},
			},
		},
		// Logfmt parse + filter.
		{
			Name: "logfmt_parse_error",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="payment-service"} | logfmt | level="error"`},
				"start": {start30m},
				"end":   {end},
				"limit": {"500"},
			},
		},
		{
			Name: "logfmt_latency_filter",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="worker-service"} | logfmt | duration_ms > 5000`},
				"start": {start1h},
				"end":   {end},
				"limit": {"200"},
			},
		},
		// Regex line filter.
		{
			Name: "regex_filter",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{namespace="prod"} |~ "status=(4|5)[0-9][0-9]"`},
				"start": {start30m},
				"end":   {end},
				"limit": {"500"},
			},
		},
		// Metric aggregations — rate/count/bytes over various windows.
		{
			Name: "metric_rate_by_app",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (app) (rate({namespace="prod"}[5m]))`},
				"start": {start1h},
				"end":   {end},
				"step":  {"60"},
			},
		},
		{
			Name: "metric_rate_by_status",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (app) (rate({app="api-gateway"} | json | status >= 400 [5m]))`},
				"start": {start1h},
				"end":   {end},
				"step":  {"60"},
			},
		},
		{
			Name: "metric_count_by_level",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (level) (count_over_time({namespace="prod"}[5m]))`},
				"start": {start2h},
				"end":   {end},
				"step":  {"60"},
			},
		},
		{
			Name: "metric_bytes_rate",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (app) (bytes_rate({namespace="prod"}[5m]))`},
				"start": {start1h},
				"end":   {end},
				"step":  {"60"},
			},
		},
		// Topk + quantile — complex aggregation shapes.
		{
			Name: "topk_apps",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`topk(5, sum by (app) (rate({namespace="prod"}[5m])))`},
				"start": {start1h},
				"end":   {end},
				"step":  {"60"},
			},
		},
		// Full detected_fields over all services.
		{
			Name: "detected_fields_all",
			Path: "/loki/api/v1/detected_fields",
			Params: url.Values{
				"query": {`{namespace="prod"} | json`},
				"start": {start30m},
				"end":   {end},
			},
		},
		// Patterns — proxy clustering.
		{
			Name: "patterns_prod",
			Path: "/loki/api/v1/patterns",
			Params: url.Values{
				"query": {`{namespace="prod"}`},
				"start": {start1h},
				"end":   {end},
			},
		},
		// Full volume log return — large response payload.
		{
			Name: "full_volume_1h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"}`},
				"start": {start1h},
				"end":   {end},
				"limit": {"5000"},
			},
		},
		// Volume endpoint.
		{
			Name: "volume_range",
			Path: "/loki/api/v1/index/volume_range",
			Params: url.Values{
				"query": {`{namespace="prod"}`},
				"start": {start1h},
				"end":   {end},
				"step":  {"60"},
			},
		},
	}}
}

// LongRange: 6h, 24h, 48h, 72h windows.
// Exercises proxy query-range windowing, prefilter, adaptive parallelism, historical cache.
func LongRange(now time.Time) Workload {
	start6h := ns(now.Add(-6 * time.Hour))
	start24h := ns(now.Add(-24 * time.Hour))
	start48h := ns(now.Add(-48 * time.Hour))
	start72h := ns(now.Add(-72 * time.Hour))
	end := ns(now)

	return Workload{Name: "long_range", Queries: []Query{
		// Simple log selects over long windows — tests windowing + cache.
		{
			Name: "log_select_6h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"}`},
				"start": {start6h},
				"end":   {end},
				"limit": {"2000"},
			},
		},
		{
			Name: "log_select_24h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{namespace="prod"}`},
				"start": {start24h},
				"end":   {end},
				"limit": {"2000"},
			},
		},
		{
			Name: "log_select_error_48h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{namespace="prod"} |= "error"`},
				"start": {start48h},
				"end":   {end},
				"limit": {"1000"},
			},
		},
		// Metric rate over long windows — many windows × step points.
		{
			Name: "rate_by_app_6h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (app) (rate({namespace="prod"}[5m]))`},
				"start": {start6h},
				"end":   {end},
				"step":  {"300"},
			},
		},
		{
			Name: "rate_by_app_24h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (app) (rate({namespace="prod"}[5m]))`},
				"start": {start24h},
				"end":   {end},
				"step":  {"300"},
			},
		},
		{
			Name: "count_by_level_48h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (level) (count_over_time({namespace="prod"}[1h]))`},
				"start": {start48h},
				"end":   {end},
				"step":  {"3600"},
			},
		},
		{
			Name: "bytes_rate_72h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum(bytes_rate({namespace="prod"}[1h]))`},
				"start": {start72h},
				"end":   {end},
				"step":  {"3600"},
			},
		},
		// Metadata over long windows.
		{
			Name:   "labels_24h",
			Path:   "/loki/api/v1/labels",
			Params: url.Values{"start": {start24h}, "end": {end}},
		},
		{
			Name: "series_24h",
			Path: "/loki/api/v1/series",
			Params: url.Values{
				"match[]": {`{namespace="prod"}`},
				"start":   {start24h},
				"end":     {end},
			},
		},
		// Full volume — large response, stresses windowing + merge.
		{
			Name: "full_volume_json_24h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"} | json | status >= 400`},
				"start": {start24h},
				"end":   {end},
				"limit": {"5000"},
			},
		},
	}}
}

// All returns all workloads for the given time reference.
func All(now time.Time) []Workload {
	return []Workload{Small(now), Heavy(now), LongRange(now)}
}

// ByName returns the named workloads.
func ByName(names []string, now time.Time) []Workload {
	all := All(now)
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

func ns(t time.Time) string {
	return fmt.Sprintf("%d", t.UnixNano())
}
