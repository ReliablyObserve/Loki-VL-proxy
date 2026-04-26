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
// Representative of Grafana Explore label browser + small panel refreshes.
func Small(now time.Time) Workload {
	start5m := fmt.Sprintf("%d", now.Add(-5*time.Minute).UnixNano())
	end := fmt.Sprintf("%d", now.UnixNano())

	return Workload{Name: "small", Queries: []Query{
		{
			Name: "labels",
			Path: "/loki/api/v1/labels",
			Params: url.Values{"start": {start5m}, "end": {end}},
		},
		{
			Name:   "label_values_app",
			Path:   "/loki/api/v1/label/app/values",
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
			Name: "query_range_simple",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"}`},
				"start": {start5m},
				"end":   {end},
				"limit": {"100"},
			},
		},
		{
			Name: "query_range_filter",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"} |= "GET"`},
				"start": {start5m},
				"end":   {end},
				"limit": {"100"},
			},
		},
		{
			Name: "query_instant",
			Path: "/loki/api/v1/query",
			Params: url.Values{
				"query": {`count_over_time({app="api-gateway"}[5m])`},
				"time":  {end},
			},
		},
		{
			Name: "detected_fields",
			Path: "/loki/api/v1/detected_fields",
			Params: url.Values{
				"query": {`{app="api-gateway"}`},
				"start": {start5m},
				"end":   {end},
			},
		},
	}}
}

// Heavy: json parse, multi-stage pipelines, metric aggregations.
// Representative of Grafana Drilldown service view + metric panels.
func Heavy(now time.Time) Workload {
	start30m := fmt.Sprintf("%d", now.Add(-30*time.Minute).UnixNano())
	start1h := fmt.Sprintf("%d", now.Add(-1*time.Hour).UnixNano())
	end := fmt.Sprintf("%d", now.UnixNano())

	return Workload{Name: "heavy", Queries: []Query{
		{
			Name: "query_range_json_parse",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"} | json | status >= 400`},
				"start": {start30m},
				"end":   {end},
				"limit": {"500"},
			},
		},
		{
			Name: "query_range_logfmt",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app=~"auth.*"} | logfmt | level="error"`},
				"start": {start30m},
				"end":   {end},
				"limit": {"500"},
			},
		},
		{
			Name: "metric_rate",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (app) (rate({app=~".+"}[5m]))`},
				"start": {start1h},
				"end":   {end},
				"step":  {"60"},
			},
		},
		{
			Name: "metric_count_over_time",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (level) (count_over_time({app=~".+"}[5m]))`},
				"start": {start1h},
				"end":   {end},
				"step":  {"60"},
			},
		},
		{
			Name: "metric_bytes_rate",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum(bytes_rate({app=~".+"}[5m]))`},
				"start": {start1h},
				"end":   {end},
				"step":  {"60"},
			},
		},
		{
			Name: "query_range_multi_stage",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"} | json | status >= 200 | status < 500 | line_format "{{.method}} {{.path}} {{.status}}"`},
				"start": {start30m},
				"end":   {end},
				"limit": {"200"},
			},
		},
		{
			Name: "detected_fields_heavy",
			Path: "/loki/api/v1/detected_fields",
			Params: url.Values{
				"query": {`{app=~".+"} | json`},
				"start": {start30m},
				"end":   {end},
			},
		},
		{
			Name: "patterns",
			Path: "/loki/api/v1/patterns",
			Params: url.Values{
				"query": {`{app=~".+"}`},
				"start": {start1h},
				"end":   {end},
			},
		},
	}}
}

// LongRange: 6h, 24h, 48h windows — exercises proxy query-range windowing,
// prefilter, adaptive parallelism, and historical window cache.
func LongRange(now time.Time) Workload {
	start6h := fmt.Sprintf("%d", now.Add(-6*time.Hour).UnixNano())
	start24h := fmt.Sprintf("%d", now.Add(-24*time.Hour).UnixNano())
	start48h := fmt.Sprintf("%d", now.Add(-48*time.Hour).UnixNano())
	end := fmt.Sprintf("%d", now.UnixNano())

	return Workload{Name: "long_range", Queries: []Query{
		{
			Name: "query_range_6h_simple",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app="api-gateway"}`},
				"start": {start6h},
				"end":   {end},
				"limit": {"1000"},
			},
		},
		{
			Name: "metric_rate_6h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (app) (rate({app=~".+"}[5m]))`},
				"start": {start6h},
				"end":   {end},
				"step":  {"300"},
			},
		},
		{
			Name: "query_range_24h_simple",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`{app=~".+"}`},
				"start": {start24h},
				"end":   {end},
				"limit": {"1000"},
			},
		},
		{
			Name: "metric_rate_24h",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (app) (rate({app=~".+"}[5m]))`},
				"start": {start24h},
				"end":   {end},
				"step":  {"300"},
			},
		},
		{
			Name: "query_range_48h_metric",
			Path: "/loki/api/v1/query_range",
			Params: url.Values{
				"query": {`sum by (level) (count_over_time({app=~".+"}[1h]))`},
				"start": {start48h},
				"end":   {end},
				"step":  {"3600"},
			},
		},
		{
			Name: "labels_24h",
			Path: "/loki/api/v1/labels",
			Params: url.Values{
				"start": {start24h},
				"end":   {end},
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
