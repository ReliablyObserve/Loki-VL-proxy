package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func FuzzNormalizeMetadataPairsFromJSON(f *testing.F) {
	seeds := []string{
		`{"service.name":"orders","env":"prod"}`,
		`[["service.name","orders"],["env","prod"]]`,
		`[{"name":"service.name","value":"orders"},{"name":"env","value":"prod"}]`,
		`{"":"skip-empty","ok":"v"}`,
		`null`,
		`[]`,
		`{}`,
		`[1,true,{"name":"k","value":"v"}]`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, rawJSON string) {
		var raw interface{}
		if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
			return
		}
		normalized := normalizeMetadataPairs(raw)
		for key := range normalized {
			if key == "" {
				t.Fatalf("normalizeMetadataPairs must not emit empty keys; input=%s output=%#v", rawJSON, normalized)
			}
		}
	})
}

func FuzzMergeLokiQueryResponsesStructuredMetadataContract(f *testing.F) {
	seeds := []struct {
		structured string
		parsed     string
	}{
		{`{"service.name":"orders"}`, `{"status":"200"}`},
		{`[["service.name","orders"]]`, `[["status","200"]]`},
		{`[{"name":"service.name","value":"orders"}]`, `null`},
		{`null`, `{"status":"500"}`},
		{`{"":"drop-empty","ok":"v"}`, `[]`},
	}
	for _, seed := range seeds {
		f.Add(seed.structured, seed.parsed)
	}

	f.Fuzz(func(t *testing.T, structuredJSON string, parsedJSON string) {
		var structured interface{}
		if err := json.Unmarshal([]byte(structuredJSON), &structured); err != nil {
			return
		}
		var parsed interface{}
		if err := json.Unmarshal([]byte(parsedJSON), &parsed); err != nil {
			return
		}

		stream := map[string]interface{}{
			"stream": map[string]string{"app": "api"},
			"values": []interface{}{
				[]interface{}{
					"1775848431328318291",
					"log line",
					map[string]interface{}{
						"structuredMetadata": structured,
						"parsed":             parsed,
					},
				},
			},
		}
		body, err := json.Marshal(map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "streams",
				"result":     []interface{}{stream},
			},
		})
		if err != nil {
			t.Fatalf("marshal seed response: %v", err)
		}

		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		rec.Write(body)

		mergedBody, _, err := mergeLokiQueryResponses([]string{"tenant-a"}, []*httptest.ResponseRecorder{rec})
		if err != nil {
			t.Fatalf("mergeLokiQueryResponses failed for structured=%s parsed=%s: %v", structuredJSON, parsedJSON, err)
		}

		var payload struct {
			Status string `json:"status"`
			Data   struct {
				EncodingFlags []string `json:"encodingFlags"`
				Result        []struct {
					Values [][]interface{} `json:"values"`
				} `json:"result"`
			} `json:"data"`
		}
		if err := json.Unmarshal(mergedBody, &payload); err != nil {
			t.Fatalf("merged response must be valid json: %v body=%s", err, string(mergedBody))
		}
		if payload.Status != "success" {
			t.Fatalf("expected success status, got %q body=%s", payload.Status, string(mergedBody))
		}
		hasCategorized := false
		for _, flag := range payload.Data.EncodingFlags {
			if flag == "categorize-labels" {
				hasCategorized = true
				break
			}
		}
		if !hasCategorized {
			t.Fatalf("expected categorize-labels encoding flag, got %#v body=%s", payload.Data.EncodingFlags, string(mergedBody))
		}
		if len(payload.Data.Result) == 0 || len(payload.Data.Result[0].Values) == 0 {
			t.Fatalf("expected merged stream values, body=%s", string(mergedBody))
		}
		tuple := payload.Data.Result[0].Values[0]
		if len(tuple) != 3 {
			t.Fatalf("expected 3-tuple, got %#v body=%s", tuple, string(mergedBody))
		}
		meta, ok := tuple[2].(map[string]interface{})
		if !ok {
			t.Fatalf("expected metadata object at tuple[2], got %T body=%s", tuple[2], string(mergedBody))
		}
		if raw, ok := meta["structuredMetadata"]; ok {
			if _, ok := raw.(map[string]interface{}); !ok {
				t.Fatalf("expected structuredMetadata object map after normalization, got %T (%#v)", raw, raw)
			}
		}
		if raw, ok := meta["parsed"]; ok {
			if _, ok := raw.(map[string]interface{}); !ok {
				t.Fatalf("expected parsed object map after normalization, got %T (%#v)", raw, raw)
			}
		}
	})
}
