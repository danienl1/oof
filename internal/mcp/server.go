// Package mcp implements a Model Context Protocol server over stdio.
// Protocol: JSON-RPC 2.0, newline-delimited. Tools: scan, price,
// inspect_top_savings, inspect_resources, inspect_diagnostics,
// inspect_policy_detail, price_compare.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danienl1/oof/internal/hcl"
	"github.com/danienl1/oof/internal/schema"
)

const cacheDir = ".cache/oof"

const maxIACBytes = 512 * 1024 // 512 KB per IaC payload

// Serve runs the MCP stdio loop until stdin closes.
func Serve(allowedRoot string) error {
	srv := &server{
		cache:       make(map[string]*cachedScan),
		allowedRoot: allowedRoot,
	}
	// Restore any cached scans from disk.
	srv.loadDiskCache()
	return srv.loop(os.Stdin, os.Stdout)
}

type cachedScan struct {
	Project  *schema.Project
	Warnings []string
}

type server struct {
	cache       map[string]*cachedScan
	lastPath    string
	allowedRoot string
	enc         *json.Encoder // set during loop; used by progress notifications
}

// jsonrpc types
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *server) loop(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	s.enc = json.NewEncoder(out)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = s.enc.Encode(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}

		result, rpcErr := s.dispatch(req.Method, req.Params)
		resp := response{JSONRPC: "2.0", ID: req.ID}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
		_ = s.enc.Encode(resp)
	}
	return scanner.Err()
}

func (s *server) dispatch(method string, params json.RawMessage) (interface{}, *rpcError) {
	switch method {
	case "initialize":
		return s.handleInitialize()
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolCall(params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
	}
}

func (s *server) handleInitialize() (interface{}, *rpcError) {
	return map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"serverInfo": map[string]string{
			"name":    "oof",
			"version": "0.2.0",
		},
		"capabilities": map[string]interface{}{
			"tools": map[string]bool{"listChanged": false},
		},
	}, nil
}

func (s *server) handleToolsList() (interface{}, *rpcError) {
	tools := []map[string]interface{}{
		{
			"name":        "scan",
			"description": "Scan an IaC directory for cloud cost estimates and savings opportunities.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":          map[string]string{"type": "string", "description": "Absolute path to the IaC directory"},
					"currency":      map[string]string{"type": "string", "description": "ISO 4217 currency code (default: USD)"},
					"region":        map[string]string{"type": "string", "description": "AWS region for price multipliers (default: us-east-1)"},
					"discount_rate": map[string]interface{}{"type": "number", "description": "Fractional discount rate 0–1 for Savings Plans / Reserved Instances"},
				},
				"required": []string{"path"},
			},
		},
		{
			"name":        "price",
			"description": "Price a Terraform snippet without writing files to disk.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"iac":           map[string]string{"type": "string", "description": "Terraform HCL content to price"},
					"currency":      map[string]string{"type": "string", "description": "ISO 4217 currency code (default: USD)"},
					"region":        map[string]string{"type": "string", "description": "AWS region for price multipliers (default: us-east-1)"},
					"discount_rate": map[string]interface{}{"type": "number", "description": "Fractional discount rate 0–1"},
				},
				"required": []string{"iac"},
			},
		},
		{
			"name":        "price_compare",
			"description": "Compare costs of two HCL snippets side-by-side.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"iac_a":    map[string]string{"type": "string", "description": "First Terraform HCL snippet"},
					"iac_b":    map[string]string{"type": "string", "description": "Second Terraform HCL snippet"},
					"label_a":  map[string]string{"type": "string", "description": "Label for snippet A (default: 'Option A')"},
					"label_b":  map[string]string{"type": "string", "description": "Label for snippet B (default: 'Option B')"},
					"region":   map[string]string{"type": "string", "description": "AWS region (default: us-east-1)"},
				},
				"required": []string{"iac_a", "iac_b"},
			},
		},
		{
			"name":        "inspect_top_savings",
			"description": "Return the top N savings opportunities from the last scan.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"n":    map[string]interface{}{"type": "integer", "description": "Number of results (default: 10)"},
					"path": map[string]string{"type": "string", "description": "Target a specific previously-scanned path"},
				},
			},
		},
		{
			"name":        "inspect_resources",
			"description": "List or group resources from the last scan.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":     map[string]string{"type": "string"},
					"min_cost": map[string]interface{}{"type": "number"},
					"max_cost": map[string]interface{}{"type": "number"},
					"group_by": map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
				},
			},
		},
		{
			"name":        "inspect_diagnostics",
			"description": "Return parse warnings and diagnostics from the last scan.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":          map[string]string{"type": "string"},
					"critical_only": map[string]interface{}{"type": "boolean"},
				},
			},
		},
		{
			"name":        "inspect_policy_detail",
			"description": "Return resources that trigger a specific cost check, with file:line locations.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"policy":   map[string]string{"type": "string", "description": "Cost check name or slug (e.g. 'Use ARM64 architecture')"},
					"resource": map[string]string{"type": "string", "description": "Narrow to a specific resource address"},
					"path":     map[string]string{"type": "string", "description": "Target a specific previously-scanned path"},
				},
				"required": []string{"policy"},
			},
		},
	}
	return map[string]interface{}{"tools": tools}, nil
}

// toolCallParams is the shape of params for tools/call
type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func (s *server) handleToolCall(raw json.RawMessage) (interface{}, *rpcError) {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}

	switch p.Name {
	case "scan":
		return s.toolScan(p.Arguments)
	case "price":
		return s.toolPrice(p.Arguments)
	case "price_compare":
		return s.toolPriceCompare(p.Arguments)
	case "inspect_top_savings":
		return s.toolInspectTopSavings(p.Arguments)
	case "inspect_resources":
		return s.toolInspectResources(p.Arguments)
	case "inspect_diagnostics":
		return s.toolInspectDiagnostics(p.Arguments)
	case "inspect_policy_detail":
		return s.toolInspectPolicyDetail(p.Arguments)
	default:
		return nil, &rpcError{Code: -32601, Message: "unknown tool: " + p.Name}
	}
}

func (s *server) toolScan(args map[string]interface{}) (interface{}, *rpcError) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, &rpcError{Code: -32602, Message: "path is required"}
	}
	path = filepath.Clean(path)
	if s.allowedRoot != "" {
		root := filepath.Clean(s.allowedRoot)
		if !strings.HasPrefix(path+string(filepath.Separator), root+string(filepath.Separator)) {
			return nil, &rpcError{Code: -32602, Message: "path is outside the allowed workspace"}
		}
	}

	opts := hcl.Options{
		Region: stringArg(args, "region", "us-east-1"),
	}
	if dr, ok := args["discount_rate"].(float64); ok {
		opts.DiscountRate = dr
	}

	// Emit a progress notification before the scan (large repos benefit from this).
	s.sendProgress("scanning", fmt.Sprintf("Scanning %s…", path))

	proj, warnings, err := hcl.ParseDirWithOptions(path, opts)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: fmt.Sprintf("scan failed: %v", err)}
	}

	s.cache[path] = &cachedScan{Project: proj, Warnings: warnings}
	s.lastPath = path
	s.saveDiskCache(path, proj, warnings)

	s.sendProgress("done", fmt.Sprintf("Scanned %d resources, $%.2f/mo", len(proj.Resources), proj.MonthlyCost()))

	return buildScanResult(proj, warnings), nil
}

func (s *server) toolPrice(args map[string]interface{}) (interface{}, *rpcError) {
	iac, ok := args["iac"].(string)
	if !ok || iac == "" {
		return nil, &rpcError{Code: -32602, Message: "iac is required"}
	}
	if len(iac) > maxIACBytes {
		return nil, &rpcError{Code: -32602, Message: "iac payload exceeds 512 KB limit"}
	}

	tmpDir, err := os.MkdirTemp("", "oof-price-*")
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "could not create temp dir"}
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(tmpDir+"/main.tf", []byte(iac), 0600); err != nil {
		return nil, &rpcError{Code: -32000, Message: "could not write temp file"}
	}

	opts := hcl.Options{Region: stringArg(args, "region", "us-east-1")}
	if dr, ok := args["discount_rate"].(float64); ok {
		opts.DiscountRate = dr
	}

	proj, warnings, err := hcl.ParseDirWithOptions(tmpDir, opts)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: fmt.Sprintf("price failed: %v", err)}
	}

	s.cache["__price__"] = &cachedScan{Project: proj, Warnings: warnings}
	s.lastPath = "__price__"

	result := buildScanResult(proj, warnings)
	result["resources"] = buildResourceList(proj)
	return result, nil
}

func (s *server) toolPriceCompare(args map[string]interface{}) (interface{}, *rpcError) {
	iacA, okA := args["iac_a"].(string)
	iacB, okB := args["iac_b"].(string)
	if !okA || !okB || iacA == "" || iacB == "" {
		return nil, &rpcError{Code: -32602, Message: "iac_a and iac_b are required"}
	}

	labelA := stringArg(args, "label_a", "Option A")
	labelB := stringArg(args, "label_b", "Option B")
	region := stringArg(args, "region", "us-east-1")
	opts := hcl.Options{Region: region}

	projA, err := priceSnippet(iacA, opts)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: fmt.Sprintf("price option A: %v", err)}
	}
	projB, err := priceSnippet(iacB, opts)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: fmt.Sprintf("price option B: %v", err)}
	}

	costA := projA.MonthlyCost()
	costB := projB.MonthlyCost()
	delta := costB - costA

	return map[string]interface{}{
		"region": region,
		"comparison": []map[string]interface{}{
			{"label": labelA, "monthly_cost": costA, "resources": buildResourceList(projA)},
			{"label": labelB, "monthly_cost": costB, "resources": buildResourceList(projB)},
		},
		"delta":   delta,
		"cheaper": func() string {
			if costA <= costB {
				return labelA
			}
			return labelB
		}(),
		"savings_per_month": abs(delta),
	}, nil
}

func priceSnippet(iac string, opts hcl.Options) (*schema.Project, error) {
	if len(iac) > maxIACBytes {
		return nil, fmt.Errorf("iac payload exceeds 512 KB limit")
	}
	tmpDir, err := os.MkdirTemp("", "oof-cmp-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	if err := os.WriteFile(tmpDir+"/main.tf", []byte(iac), 0600); err != nil {
		return nil, err
	}
	proj, _, err := hcl.ParseDirWithOptions(tmpDir, opts)
	return proj, err
}

func (s *server) resolveCache(args map[string]interface{}) *cachedScan {
	if p, ok := args["path"].(string); ok && p != "" {
		if c, found := s.cache[p]; found {
			return c
		}
	}
	if s.lastPath != "" {
		return s.cache[s.lastPath]
	}
	return nil
}

func (s *server) toolInspectTopSavings(args map[string]interface{}) (interface{}, *rpcError) {
	c := s.resolveCache(args)
	if c == nil {
		return nil, &rpcError{Code: -32000, Message: "no scan results available; run scan first"}
	}

	n := 10
	if nf, ok := args["n"].(float64); ok {
		n = int(nf)
	}

	type saving struct {
		Resource       string  `json:"resource"`
		Check          string  `json:"check"`
		Description    string  `json:"description"`
		MonthlySavings float64 `json:"monthly_savings"`
	}

	var savings []saving
	for _, r := range c.Project.Resources {
		if !r.IsSupported || r.IsFree {
			continue
		}
		for _, check := range costsChecks(r) {
			savings = append(savings, saving{
				Resource:       r.Name,
				Check:          check.name,
				Description:    check.description,
				MonthlySavings: check.savings,
			})
		}
	}

	sort.Slice(savings, func(i, j int) bool {
		return savings[i].MonthlySavings > savings[j].MonthlySavings
	})
	if len(savings) > n {
		savings = savings[:n]
	}

	total := 0.0
	for _, sv := range savings {
		total += sv.MonthlySavings
	}

	return map[string]interface{}{
		"savings":       savings,
		"total_savings": total,
	}, nil
}

func (s *server) toolInspectResources(args map[string]interface{}) (interface{}, *rpcError) {
	c := s.resolveCache(args)
	if c == nil {
		return nil, &rpcError{Code: -32000, Message: "no scan results available; run scan first"}
	}

	minCost, hasMin := args["min_cost"].(float64)
	maxCost, hasMax := args["max_cost"].(float64)

	var resources []map[string]interface{}
	for _, r := range c.Project.Resources {
		cost := r.MonthlyCost()
		if hasMin && cost < minCost {
			continue
		}
		if hasMax && cost > maxCost {
			continue
		}
		resources = append(resources, map[string]interface{}{
			"name":         r.Name,
			"type":         r.ResourceType,
			"file":         r.FilePath,
			"start_line":   r.StartLine,
			"monthly_cost": cost,
			"is_free":      r.IsFree,
			"is_supported": r.IsSupported,
		})
	}

	sort.Slice(resources, func(i, j int) bool {
		ci, _ := resources[i]["monthly_cost"].(float64)
		cj, _ := resources[j]["monthly_cost"].(float64)
		return ci > cj
	})

	if groupByRaw, ok := args["group_by"].([]interface{}); ok && len(groupByRaw) > 0 {
		groupBy := make([]string, len(groupByRaw))
		for i, g := range groupByRaw {
			groupBy[i], _ = g.(string)
		}
		return map[string]interface{}{"groups": groupResources(resources, groupBy)}, nil
	}

	return map[string]interface{}{"resources": resources}, nil
}

func (s *server) toolInspectDiagnostics(args map[string]interface{}) (interface{}, *rpcError) {
	c := s.resolveCache(args)
	warnings := []string{}
	if c != nil {
		warnings = c.Warnings
	}

	critOnly, _ := args["critical_only"].(bool)

	type diag struct {
		Severity string `json:"severity"`
		Message  string `json:"message"`
	}

	var diags []diag
	for _, w := range warnings {
		sev := "warning"
		if strings.Contains(strings.ToLower(w), "error") || strings.Contains(strings.ToLower(w), "unresolved") {
			sev = "warning"
		}
		if critOnly && sev != "error" {
			continue
		}
		diags = append(diags, diag{Severity: sev, Message: w})
	}

	return map[string]interface{}{"diagnostics": diags}, nil
}

func (s *server) toolInspectPolicyDetail(args map[string]interface{}) (interface{}, *rpcError) {
	c := s.resolveCache(args)
	if c == nil {
		return nil, &rpcError{Code: -32000, Message: "no scan results available; run scan first"}
	}

	policy, _ := args["policy"].(string)
	if policy == "" {
		return nil, &rpcError{Code: -32602, Message: "policy is required"}
	}
	filterResource, _ := args["resource"].(string)

	policyLower := strings.ToLower(policy)

	type finding struct {
		Resource    string  `json:"resource"`
		Type        string  `json:"type"`
		File        string  `json:"file"`
		StartLine   int     `json:"start_line"`
		Check       string  `json:"check"`
		Description string  `json:"description"`
		Savings     float64 `json:"monthly_savings"`
	}

	var findings []finding
	for _, r := range c.Project.Resources {
		if filterResource != "" && r.Name != filterResource {
			continue
		}
		for _, check := range costsChecks(r) {
			if strings.Contains(strings.ToLower(check.name), policyLower) ||
				strings.Contains(strings.ToLower(check.description), policyLower) {
				findings = append(findings, finding{
					Resource:    r.Name,
					Type:        r.ResourceType,
					File:        r.FilePath,
					StartLine:   r.StartLine,
					Check:       check.name,
					Description: check.description,
					Savings:     check.savings,
				})
			}
		}
	}

	totalSavings := 0.0
	for _, f := range findings {
		totalSavings += f.Savings
	}

	return map[string]interface{}{
		"policy":         policy,
		"findings":       findings,
		"total_savings":  totalSavings,
		"resource_count": len(findings),
	}, nil
}

// --- disk cache ---

func cachePath(scanPath string) string {
	home, _ := os.UserHomeDir()
	sanitized := strings.ReplaceAll(scanPath, "/", "_")
	if len(sanitized) > 100 {
		sanitized = sanitized[len(sanitized)-100:]
	}
	return filepath.Join(home, cacheDir, sanitized+".json")
}

type diskEntry struct {
	Project  *schema.Project `json:"project"`
	Warnings []string        `json:"warnings"`
}

func (s *server) saveDiskCache(path string, proj *schema.Project, warnings []string) {
	p := cachePath(path)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return
	}
	data, err := json.Marshal(diskEntry{Project: proj, Warnings: warnings})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0600)
}

func (s *server) loadDiskCache() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, cacheDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var entry diskEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}
		// Reconstruct the original path from the filename (best-effort).
		key := strings.TrimSuffix(e.Name(), ".json")
		s.cache[key] = &cachedScan{Project: entry.Project, Warnings: entry.Warnings}
	}
}

// sendProgress emits an MCP notifications/progress message (fire-and-forget).
func (s *server) sendProgress(state, message string) {
	if s.enc == nil {
		return
	}
	_ = s.enc.Encode(response{
		JSONRPC: "2.0",
		Method:  "notifications/progress",
		Params: map[string]interface{}{
			"state":   state,
			"message": message,
		},
	})
}

// --- helpers ---

func buildScanResult(proj *schema.Project, warnings []string) map[string]interface{} {
	monthly := proj.MonthlyCost()

	var resources []map[string]interface{}
	costed := 0
	for _, r := range proj.Resources {
		if r.IsFree || !r.IsSupported {
			continue
		}
		costed++
		resources = append(resources, map[string]interface{}{
			"name":         r.Name,
			"type":         r.ResourceType,
			"monthly_cost": r.MonthlyCost(),
		})
	}

	totalSavings := 0.0
	for _, r := range proj.Resources {
		for _, c := range costsChecks(r) {
			totalSavings += c.savings
		}
	}

	return map[string]interface{}{
		"currency": "USD",
		"summary": map[string]interface{}{
			"monthly_cost":          monthly,
			"total_monthly_savings": totalSavings,
			"costed_resources":      costed,
			"critical_diagnostics":  0,
			"warning_diagnostics":   len(warnings),
		},
		"project_details": []map[string]interface{}{
			{
				"name":         proj.Name,
				"path":         proj.Path,
				"monthly_cost": monthly,
				"has_errors":   len(warnings) > 0,
			},
		},
	}
}

func buildResourceList(proj *schema.Project) []map[string]interface{} {
	var out []map[string]interface{}
	for _, r := range proj.Resources {
		if r.IsFree || !r.IsSupported {
			continue
		}
		var components []map[string]interface{}
		for _, c := range r.CostComponents {
			components = append(components, map[string]interface{}{
				"name":             c.Name,
				"unit":             c.Unit,
				"monthly_quantity": c.MonthlyQuantity,
				"price_per_unit":   c.PricePerUnit,
				"monthly_cost":     c.MonthlyCost(),
			})
		}
		out = append(out, map[string]interface{}{
			"name":               r.Name,
			"type":               r.ResourceType,
			"total_monthly_cost": r.MonthlyCost(),
			"cost_components":    components,
			"file":               r.FilePath,
			"start_line":         r.StartLine,
		})
	}
	return out
}

func groupResources(resources []map[string]interface{}, groupBy []string) []map[string]interface{} {
	totals := map[string]float64{}
	counts := map[string]int{}
	for _, r := range resources {
		key := ""
		for _, g := range groupBy {
			if v, ok := r[g]; ok {
				key += fmt.Sprintf("%v:", v)
			}
		}
		cost, _ := r["monthly_cost"].(float64)
		totals[key] += cost
		counts[key]++
	}

	var groups []map[string]interface{}
	for k, cost := range totals {
		groups = append(groups, map[string]interface{}{
			"key":          k,
			"monthly_cost": cost,
			"count":        counts[k],
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		ci, _ := groups[i]["monthly_cost"].(float64)
		cj, _ := groups[j]["monthly_cost"].(float64)
		return ci > cj
	})
	return groups
}

type costCheck struct {
	name        string
	description string
	savings     float64
}

func costsChecks(r *schema.Resource) []costCheck {
	var checks []costCheck

	switch r.ResourceType {
	case "aws_lambda_function":
		for _, c := range r.CostComponents {
			if c.Name == "Duration" {
				armSavings := c.MonthlyCost() * 0.20
				if armSavings > 0.01 {
					checks = append(checks, costCheck{
						name:        "Use ARM64 architecture",
						description: "Lambda ARM64 (Graviton2) costs ~20% less than x86_64 for compute.",
						savings:     armSavings,
					})
				}
			}
		}

	case "aws_vpc_endpoint":
		checks = append(checks, costCheck{
			name:        "Review interface endpoint necessity",
			description: "Interface VPC endpoints cost $7.30/mo per AZ. Verify this endpoint is actively used. af_terraform shows 16 interface endpoints ($116/mo) — audit for unused or consolidatable endpoints.",
			savings:     0,
		})
	}

	return checks
}

func stringArg(args map[string]interface{}, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
