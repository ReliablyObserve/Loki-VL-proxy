// seed: ingest dense historical log data into both Loki and VictoriaLogs for benchmarking.
//
// Generates N days of realistic multi-service logs with production-like metadata,
// back-filling from (now - N days) to now. Both Loki and VictoriaLogs receive
// identical streams so loki-bench comparison runs have the same data on both sides.
//
// Usage:
//
//	go run ./cmd/seed/ \
//	  --loki=http://localhost:3101 \
//	  --vl=http://localhost:9428 \
//	  --days=3 \
//	  --lines-per-batch=200 \
//	  --batch-interval=30s
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

// service describes one workload stream.
type service struct {
	app       string
	namespace string
	env       string
	region    string
	cluster   string
	version   string
	format    string // "json" | "logfmt" | "nginx" | "postgres"
}

var services = []service{
	{"api-gateway", "prod", "production", "us-east-1", "eks-prod-a", "v2.14.3", "json"},
	{"api-gateway", "prod", "production", "us-west-2", "eks-prod-b", "v2.14.3", "json"},
	{"payment-service", "prod", "production", "us-east-1", "eks-prod-a", "v1.8.0", "logfmt"},
	{"auth-service", "prod", "production", "us-east-1", "eks-prod-a", "v3.2.1", "json"},
	{"nginx-ingress", "ingress-nginx", "production", "us-east-1", "eks-prod-a", "1.9.4", "nginx"},
	{"worker-service", "prod", "production", "us-east-1", "eks-prod-a", "v0.9.2", "logfmt"},
	{"db-postgres", "data", "production", "us-east-1", "eks-data-a", "15.3", "postgres"},
	{"cache-redis", "data", "production", "us-east-1", "eks-data-a", "7.2.0", "logfmt"},
	{"frontend-ssr", "prod", "production", "us-east-1", "eks-prod-a", "v4.1.0", "json"},
	{"frontend-ssr", "prod", "production", "us-west-2", "eks-prod-b", "v4.1.0", "json"},
	{"batch-etl", "batch", "production", "us-east-1", "eks-batch-a", "v2.0.5", "json"},
	{"ml-serving", "ml", "production", "us-east-1", "eks-ml-a", "v1.3.0", "json"},
}

var levels = []string{"debug", "info", "info", "info", "info", "warn", "warn", "error"}

var httpMethods = []string{"GET", "GET", "GET", "GET", "POST", "POST", "PUT", "DELETE", "PATCH"}
var apiPaths = []string{
	"/api/v1/users", "/api/v1/users/{id}", "/api/v1/orders", "/api/v1/orders/{id}",
	"/api/v1/products", "/api/v2/events", "/api/v2/analytics", "/health", "/metrics",
	"/api/v1/payments", "/api/v1/sessions", "/api/v1/search",
}
var httpStatuses = []int{200, 200, 200, 200, 200, 201, 204, 400, 401, 403, 404, 429, 500, 502, 503}
var statusWeights = []int{40, 10, 5, 5, 5, 5, 2, 2, 2, 1, 5, 1, 1, 1, 1}

var workers = []string{"worker-0", "worker-1", "worker-2", "worker-3", "worker-4"}
var jobNames = []string{"process-orders", "sync-inventory", "send-emails", "cleanup-sessions", "update-metrics", "export-reports"}
var dbTables = []string{"users", "orders", "payments", "sessions", "products", "events", "logs"}
var cacheOps = []string{"GET", "SET", "DEL", "EXPIRE", "INCR", "HGET", "HSET", "ZADD", "ZRANGE"}
var mlModels = []string{"bert-large", "gpt-small", "clip-v2", "classification-v3", "embedding-v1"}
var etlJobs = []string{"raw-to-parquet", "daily-aggregation", "feature-extraction", "model-training", "data-export"}

var client = &http.Client{Timeout: 30 * time.Second}

func randID(n int) string {
	const charset = "abcdef0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

func randPod(app string) string {
	return fmt.Sprintf("%s-%s-%s", app, randID(5), randID(4))
}

func randIP() string {
	return fmt.Sprintf("10.%d.%d.%d", rand.Intn(10), rand.Intn(254), rand.Intn(254)+1)
}

func weightedStatus() int {
	total := 0
	for _, w := range statusWeights {
		total += w
	}
	r := rand.Intn(total)
	for i, w := range statusWeights {
		r -= w
		if r < 0 {
			return httpStatuses[i]
		}
	}
	return 200
}

func genJSONLine(svc service, ts time.Time) string {
	level := levels[rand.Intn(len(levels))]
	method := httpMethods[rand.Intn(len(httpMethods))]
	path := apiPaths[rand.Intn(len(apiPaths))]
	status := weightedStatus()
	latency := rand.Intn(800) + 1
	if status >= 500 {
		latency += rand.Intn(3000)
	}
	traceID := randID(32)
	spanID := randID(16)
	userID := fmt.Sprintf("usr_%d", rand.Intn(100000))
	pod := randPod(svc.app)

	entry := map[string]interface{}{
		"level":      level,
		"msg":        fmt.Sprintf("%s %s %d %dms", method, path, status, latency),
		"ts":         ts.Format(time.RFC3339Nano),
		"method":     method,
		"path":       path,
		"status":     status,
		"latency_ms": latency,
		"trace_id":   traceID,
		"span_id":    spanID,
		"user_id":    userID,
		"pod":        pod,
		"service":    svc.app,
		"version":    svc.version,
		"region":     svc.region,
		"cluster":    svc.cluster,
	}
	if status >= 400 {
		errs := []string{"connection refused", "timeout", "invalid token", "rate limited", "upstream unavailable"}
		entry["error"] = errs[rand.Intn(len(errs))]
	}

	b, _ := json.Marshal(entry)
	// Inject _msg for VictoriaLogs (stores original JSON string as message).
	full := map[string]interface{}{}
	_ = json.Unmarshal(b, &full)
	full["_msg"] = string(b)
	out, _ := json.Marshal(full)
	return string(out)
}

func genLogfmtLine(svc service, ts time.Time) string {
	level := levels[rand.Intn(len(levels))]
	switch svc.app {
	case "worker-service":
		job := jobNames[rand.Intn(len(jobNames))]
		worker := workers[rand.Intn(len(workers))]
		dur := rand.Intn(30000) + 100
		queued := rand.Intn(500)
		return fmt.Sprintf(`level=%s ts=%s msg="job completed" job=%s worker=%s duration_ms=%d queued=%d trace_id=%s service=%s version=%s`,
			level, ts.Format(time.RFC3339), job, worker, dur, queued, randID(32), svc.app, svc.version)
	case "payment-service":
		methods := []string{"card", "bank_transfer", "crypto", "paypal"}
		payMethod := methods[rand.Intn(len(methods))]
		amount := rand.Intn(100000) + 100
		status := []string{"authorized", "authorized", "authorized", "declined", "pending"}
		s := status[rand.Intn(len(status))]
		return fmt.Sprintf(`level=%s ts=%s msg="payment processed" method=%s amount_cents=%d status=%s user_id=usr_%d trace_id=%s service=%s version=%s`,
			level, ts.Format(time.RFC3339), payMethod, amount, s, rand.Intn(100000), randID(32), svc.app, svc.version)
	case "cache-redis":
		op := cacheOps[rand.Intn(len(cacheOps))]
		key := fmt.Sprintf("cache:%s:%s", dbTables[rand.Intn(len(dbTables))], randID(8))
		latency := rand.Intn(50) + 1
		hit := rand.Intn(10) > 2
		return fmt.Sprintf(`level=%s ts=%s msg="cache op" op=%s key=%s hit=%v latency_us=%d service=%s version=%s`,
			level, ts.Format(time.RFC3339), op, key, hit, latency, svc.app, svc.version)
	case "db-postgres":
		return genPostgresLine(svc, ts)
	default:
		return fmt.Sprintf(`level=%s ts=%s msg="event" service=%s version=%s trace_id=%s`,
			level, ts.Format(time.RFC3339), svc.app, svc.version, randID(32))
	}
}

func genPostgresLine(svc service, ts time.Time) string {
	ops := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "VACUUM", "ANALYZE"}
	op := ops[rand.Intn(len(ops))]
	table := dbTables[rand.Intn(len(dbTables))]
	rows := rand.Intn(10000)
	dur := rand.Intn(5000) + 1
	level := "info"
	if dur > 3000 {
		level = "warn"
	}
	pid := rand.Intn(32768) + 1000
	return fmt.Sprintf(`level=%s ts=%s pid=%d op=%s table=%s rows=%d duration_ms=%d db=app_production service=%s`,
		level, ts.Format(time.RFC3339), pid, op, table, rows, dur, svc.app)
}

func genNginxLine(svc service, ts time.Time) string {
	method := httpMethods[rand.Intn(len(httpMethods))]
	path := apiPaths[rand.Intn(len(apiPaths))]
	status := weightedStatus()
	size := rand.Intn(50000) + 100
	reqTime := rand.Float64() * 2.0
	upstream := fmt.Sprintf("10.0.%d.%d:8080", rand.Intn(10), rand.Intn(254))
	return fmt.Sprintf(`%s - - [%s] "%s %s HTTP/1.1" %d %d "https://app.example.com" "Mozilla/5.0 (compatible)" %.3f %s`,
		randIP(), ts.Format("02/Jan/2006:15:04:05 -0700"),
		method, path, status, size, reqTime, upstream)
}

func genMLLine(svc service, ts time.Time) string {
	model := mlModels[rand.Intn(len(mlModels))]
	batchSize := []int{1, 8, 16, 32, 64}[rand.Intn(5)]
	latency := rand.Intn(2000) + 10
	tokens := rand.Intn(4096) + 1
	level := "info"
	if latency > 1500 {
		level = "warn"
	}
	entry := map[string]interface{}{
		"level":    level,
		"msg":      fmt.Sprintf("inference completed model=%s batch=%d latency=%dms tokens=%d", model, batchSize, latency, tokens),
		"ts":       ts.Format(time.RFC3339Nano),
		"model":    model,
		"batch":    batchSize,
		"latency":  latency,
		"tokens":   tokens,
		"trace_id": randID(32),
		"service":  svc.app,
		"version":  svc.version,
		"pod":      randPod(svc.app),
	}
	b, _ := json.Marshal(entry)
	full := map[string]interface{}{}
	_ = json.Unmarshal(b, &full)
	full["_msg"] = string(b)
	out, _ := json.Marshal(full)
	return string(out)
}

func genETLLine(svc service, ts time.Time) string {
	job := etlJobs[rand.Intn(len(etlJobs))]
	records := rand.Intn(1000000) + 1000
	dur := rand.Intn(600) + 10
	level := "info"
	if dur > 300 {
		level = "warn"
	}
	entry := map[string]interface{}{
		"level":      level,
		"msg":        fmt.Sprintf("etl job %s completed: %d records in %ds", job, records, dur),
		"ts":         ts.Format(time.RFC3339Nano),
		"job":        job,
		"records":    records,
		"duration_s": dur,
		"source":     fmt.Sprintf("s3://data-lake/raw/%s", ts.Format("2006/01/02")),
		"dest":       fmt.Sprintf("s3://data-warehouse/%s/v1", job),
		"trace_id":   randID(32),
		"service":    svc.app,
		"version":    svc.version,
	}
	b, _ := json.Marshal(entry)
	full := map[string]interface{}{}
	_ = json.Unmarshal(b, &full)
	full["_msg"] = string(b)
	out, _ := json.Marshal(full)
	return string(out)
}

func genLine(svc service, ts time.Time) string {
	switch svc.format {
	case "json":
		switch svc.app {
		case "ml-serving":
			return genMLLine(svc, ts)
		case "batch-etl":
			return genETLLine(svc, ts)
		default:
			return genJSONLine(svc, ts)
		}
	case "logfmt", "postgres":
		return genLogfmtLine(svc, ts)
	case "nginx":
		return genNginxLine(svc, ts)
	default:
		return fmt.Sprintf(`level=info ts=%s msg=event service=%s`, ts.Format(time.RFC3339), svc.app)
	}
}

func buildStreams(ts time.Time, linesPerService int) []map[string]interface{} {
	streams := make([]map[string]interface{}, 0, len(services))
	for _, svc := range services {
		values := make([][]string, 0, linesPerService)
		for i := 0; i < linesPerService; i++ {
			jitter := time.Duration(rand.Int63n(int64(time.Second * 29)))
			entryTS := fmt.Sprintf("%d", ts.Add(jitter).UnixNano())
			line := genLine(svc, ts.Add(jitter))
			values = append(values, []string{entryTS, line})
		}
		streams = append(streams, map[string]interface{}{
			"stream": map[string]string{
				"app":        svc.app,
				"namespace":  svc.namespace,
				"job":        svc.namespace + "/" + svc.app,
				"env":        svc.env,
				"region":     svc.region,
				"cluster":    svc.cluster,
				"version":    svc.version,
				"log_format": svc.format,
			},
			"values": values,
		})
	}
	return streams
}

func pushLoki(lokiURL string, streams []map[string]interface{}) error {
	payload := map[string]interface{}{"streams": streams}
	body, _ := json.Marshal(payload)
	resp, err := client.Post(lokiURL+"/loki/api/v1/push", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func pushVL(vlURL string, streams []map[string]interface{}) error {
	payload := map[string]interface{}{"streams": streams}
	body, _ := json.Marshal(payload)
	resp, err := client.Post(vlURL+"/insert/loki/api/v1/push", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func main() {
	lokiURL := flag.String("loki", "http://localhost:3101", "Loki push URL")
	vlURL := flag.String("vl", "http://localhost:9428", "VictoriaLogs push URL")
	days := flag.Int("days", 3, "Number of historical days to seed")
	linesPerBatch := flag.Int("lines-per-batch", 200, "Log lines per service per time step")
	batchInterval := flag.Duration("batch-interval", 30*time.Second, "Time step between batches")
	skipLoki := flag.Bool("skip-loki", false, "Skip Loki ingestion")
	skipVL := flag.Bool("skip-vl", false, "Skip VictoriaLogs ingestion")
	flag.Parse()

	end := time.Now().Add(-time.Minute)
	start := end.Add(-time.Duration(*days) * 24 * time.Hour)

	totalBatches := int(end.Sub(start) / *batchInterval)
	totalLines := totalBatches * *linesPerBatch * len(services)
	fmt.Printf("Seeding %d days from %s to %s\n", *days, start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04"))
	fmt.Printf("  %d batches × %d lines × %d streams = ~%d total log lines\n",
		totalBatches, *linesPerBatch, len(services), totalLines)
	fmt.Printf("  targets: loki=%v  vl=%v\n\n", !*skipLoki, !*skipVL)

	var pushed, errCount, batchesOK int
	reportEvery := totalBatches / 20
	if reportEvery < 1 {
		reportEvery = 1
	}

	for ts := start; ts.Before(end); ts = ts.Add(*batchInterval) {
		streams := buildStreams(ts, *linesPerBatch)

		if !*skipLoki {
			if err := pushLoki(*lokiURL, streams); err != nil {
				fmt.Fprintf(os.Stderr, "warn: loki push at %s: %v\n", ts.Format(time.RFC3339), err)
				errCount++
			}
		}
		if !*skipVL {
			if err := pushVL(*vlURL, streams); err != nil {
				fmt.Fprintf(os.Stderr, "warn: vl push at %s: %v\n", ts.Format(time.RFC3339), err)
				errCount++
			}
		}

		pushed += *linesPerBatch * len(services)
		batchesOK++

		if batchesOK%reportEvery == 0 {
			pct := float64(ts.Sub(start)) / float64(end.Sub(start)) * 100
			rate := float64(pushed) / time.Since(start.Add(-*batchInterval*time.Duration(batchesOK))).Seconds()
			fmt.Printf("  %.0f%% done — %s — %d lines pushed, %.0f lines/s, %d errors\n",
				pct, ts.Format("2006-01-02 15:04"), pushed, rate, errCount)
		}
	}

	fmt.Printf("\nDone. %d lines pushed across %d batches. Errors: %d.\n",
		pushed, batchesOK, errCount)
	fmt.Printf("Both backends have %s of dense data from %d service streams (%d services × %d regions).\n",
		strings.TrimSuffix((*batchInterval * time.Duration(batchesOK)).Truncate(time.Minute).String(), "0s"),
		len(services), 10, 2)
	fmt.Println("\nNow run: ./run-comparison.sh --workloads=small,heavy,long_range")
}
