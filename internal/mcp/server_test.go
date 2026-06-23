package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func mcpRoundtrip(t *testing.T, req map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	in := bytes.NewReader(append(data, '\n'))
	var out bytes.Buffer

	srv := &server{cache: make(map[string]*cachedScan)}
	if err := srv.loop(in, &out); err != nil {
		t.Fatal(err)
	}

	// Parse first non-notification response.
	var resp map[string]interface{}
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var candidate map[string]interface{}
		if err := json.Unmarshal([]byte(line), &candidate); err != nil {
			continue
		}
		if candidate["method"] != nil {
			continue // skip notifications
		}
		resp = candidate
		break
	}
	if resp == nil {
		t.Fatal("no response received")
	}
	return resp
}

func TestMCP_Initialize(t *testing.T) {
	resp := mcpRoundtrip(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})

	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected result object, got: %v", resp)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("unexpected protocolVersion: %v", result["protocolVersion"])
	}
}

func TestMCP_ToolsList(t *testing.T) {
	resp := mcpRoundtrip(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	})

	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected result: %v", resp)
	}
	tools, _ := result["tools"].([]interface{})
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	names := map[string]bool{}
	for _, tool := range tools {
		m, _ := tool.(map[string]interface{})
		names[m["name"].(string)] = true
	}

	required := []string{"scan", "price", "price_compare", "inspect_policy_detail", "inspect_top_savings", "inspect_resources", "inspect_diagnostics"}
	for _, name := range required {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestMCP_Price(t *testing.T) {
	resp := mcpRoundtrip(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "price",
			"arguments": map[string]interface{}{
				"iac": `resource "aws_kms_key" "k" {}`,
			},
		},
	})

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	summary, _ := result["summary"].(map[string]interface{})
	if summary == nil {
		t.Fatalf("expected summary in result: %v", result)
	}
	cost, _ := summary["monthly_cost"].(float64)
	if cost <= 0 {
		t.Errorf("expected positive monthly cost for KMS key, got %v", cost)
	}
}

func TestMCP_PriceCompare(t *testing.T) {
	resp := mcpRoundtrip(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "price_compare",
			"arguments": map[string]interface{}{
				"iac_a":   `resource "aws_kms_key" "k1" {}`,
				"iac_b":   `resource "aws_kms_key" "k1" {} ` + "\n" + `resource "aws_kms_key" "k2" {}`,
				"label_a": "single key",
				"label_b": "two keys",
			},
		},
	})

	if resp["error"] != nil {
		t.Fatalf("unexpected error: %v", resp["error"])
	}
	result, _ := resp["result"].(map[string]interface{})
	delta, _ := result["delta"].(float64)
	if delta <= 0 {
		t.Errorf("expected positive delta (two keys > one), got %v", delta)
	}
	if result["cheaper"] != "single key" {
		t.Errorf("expected cheaper to be 'single key', got %v", result["cheaper"])
	}
}

func TestMCP_InspectPolicyDetail_NoScan(t *testing.T) {
	resp := mcpRoundtrip(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "inspect_policy_detail",
			"arguments": map[string]interface{}{
				"policy": "Use ARM64",
			},
		},
	})

	if resp["error"] == nil {
		t.Error("expected error when no scan has been run")
	}
}

func TestMCP_UnknownMethod(t *testing.T) {
	resp := mcpRoundtrip(t, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "unknown/method",
	})
	if resp["error"] == nil {
		t.Error("expected error for unknown method")
	}
}

func TestMCP_RegionParam(t *testing.T) {
	// Price same resource in us-east-1 and sa-east-1; sa should be more expensive.
	iac := `resource "aws_kms_key" "k" {}`

	priceIn := func(region string) float64 {
		resp := mcpRoundtrip(t, map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]interface{}{
				"name": "price",
				"arguments": map[string]interface{}{
					"iac":    iac,
					"region": region,
				},
			},
		})
		result, _ := resp["result"].(map[string]interface{})
		summary, _ := result["summary"].(map[string]interface{})
		cost, _ := summary["monthly_cost"].(float64)
		return cost
	}

	base := priceIn("us-east-1")
	sa := priceIn("sa-east-1")
	if sa <= base {
		t.Errorf("sa-east-1 (%v) should be more expensive than us-east-1 (%v)", sa, base)
	}
}
