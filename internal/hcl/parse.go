// Package hcl parses Terraform HCL files and maps resource blocks to schema.Resource.
package hcl

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/danienl1/oof/internal/pricing"
	"github.com/danienl1/oof/internal/schema"
	"github.com/danienl1/oof/internal/usage"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// Options controls scanning behaviour.
type Options struct {
	Region       string      // AWS region for price multipliers (default: us-east-1)
	DiscountRate float64     // Fractional discount applied to all costs (0–1)
	Usage        *usage.File // Usage overrides; nil means zero usage for usage-based resources
}

// ParseDir walks dir recursively, parses every *.tf file in parallel, and
// returns a Project. One goroutine per file, bounded by runtime.NumCPU().
func ParseDir(dir string) (*schema.Project, []string, error) {
	return ParseDirWithOptions(dir, Options{})
}

// ParseDirWithOptions is ParseDir with region, discount, and usage overrides.
func ParseDirWithOptions(dir string, opts Options) (*schema.Project, []string, error) {
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	regionMult := pricing.RegionMultiplier(opts.Region)

	// Resolve variable defaults from variables.tf and tfvars files in the root.
	varDefaults := resolveVarDefaults(dir)

	// Collect .tf file paths.
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".tf") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// Worker pool bounded by CPU count.
	type result struct {
		resources []*schema.Resource
		warnings  []string
	}

	numWorkers := runtime.NumCPU()
	if numWorkers < 1 {
		numWorkers = 1
	}
	jobs := make(chan string, len(paths))
	results := make(chan result, len(paths))

	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				res, warns := parseFile(path, varDefaults, opts, regionMult)
				results <- result{resources: res, warnings: warns}
			}
		}()
	}

	for _, p := range paths {
		jobs <- p
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	proj := &schema.Project{
		Name: filepath.Base(dir),
		Path: dir,
	}
	var warnings []string
	for r := range results {
		proj.Resources = append(proj.Resources, r.resources...)
		warnings = append(warnings, r.warnings...)
	}

	return proj, warnings, nil
}

func parseFile(path string, varDefaults map[string]cty.Value, opts Options, regionMult float64) ([]*schema.Resource, []string) {
	var warnings []string

	src, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("could not read %s: %v", path, err)}
	}

	file, diags := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return nil, []string{fmt.Sprintf("parse error in %s: %s", path, diags.Error())}
	}

	resources, warns := extractResources(file, path, varDefaults, opts, regionMult)
	warnings = append(warnings, warns...)
	return resources, warnings
}

func extractResources(file *hcl.File, path string, varDefaults map[string]cty.Value, opts Options, regionMult float64) ([]*schema.Resource, []string) {
	var out []*schema.Resource
	var warnings []string

	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, nil
	}

	evalCtx := buildEvalCtx(varDefaults)

	for _, block := range body.Blocks {
		if block.Type != "resource" || len(block.Labels) < 2 {
			continue
		}
		rType := block.Labels[0]
		rName := block.Labels[1]
		address := rType + "." + rName

		r := &schema.Resource{
			Name:         address,
			ResourceType: rType,
			FilePath:     path,
			StartLine:    block.OpenBraceRange.Start.Line,
			IsSupported:  true,
		}

		unresolvedVars := mapResource(r, block, evalCtx, opts)
		for _, v := range unresolvedVars {
			warnings = append(warnings, fmt.Sprintf("unresolved variable %q in %s.%s — using default", v, rType, rName))
		}

		// Apply region multiplier to all cost components.
		if regionMult != 1.0 {
			for _, c := range r.CostComponents {
				c.PricePerUnit *= regionMult
			}
		}

		// Apply discount rate.
		if opts.DiscountRate > 0 && opts.DiscountRate < 1 {
			for _, c := range r.CostComponents {
				c.PricePerUnit *= (1 - opts.DiscountRate)
			}
		}

		out = append(out, r)
	}

	return out, warnings
}

// resolveVarDefaults reads variable default blocks from all *.tf files in dir
// and merges in values from terraform.tfvars and *.auto.tfvars.
func resolveVarDefaults(dir string) map[string]cty.Value {
	defaults := map[string]cty.Value{}

	// First pass: parse variable blocks from .tf files.
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tf") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		file, diags := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			return nil
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			return nil
		}
		for _, block := range body.Blocks {
			if block.Type != "variable" || len(block.Labels) < 1 {
				continue
			}
			varName := block.Labels[0]
			if defaultAttr, ok := block.Body.Attributes["default"]; ok {
				val, diags := defaultAttr.Expr.Value(nil)
				if !diags.HasErrors() {
					defaults[varName] = val
				}
			}
		}
		return nil
	})

	// Second pass: merge tfvars files (override defaults).
	tfvarsFiles := []string{
		filepath.Join(dir, "terraform.tfvars"),
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".auto.tfvars") {
			tfvarsFiles = append(tfvarsFiles, path)
		}
		return nil
	})

	for _, tfvarsPath := range tfvarsFiles {
		src, err := os.ReadFile(tfvarsPath)
		if err != nil {
			continue
		}
		file, diags := hclsyntax.ParseConfig(src, tfvarsPath, hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			continue
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for name, attr := range body.Attributes {
			val, diags := attr.Expr.Value(nil)
			if !diags.HasErrors() {
				defaults[name] = val
			}
		}
	}

	return defaults
}

func buildEvalCtx(varDefaults map[string]cty.Value) *hcl.EvalContext {
	if len(varDefaults) == 0 {
		return nil
	}
	vars := map[string]cty.Value{}
	for k, v := range varDefaults {
		vars[k] = v
	}
	return &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var": cty.ObjectVal(vars),
		},
	}
}

func mapResource(r *schema.Resource, block *hclsyntax.Block, evalCtx *hcl.EvalContext, opts Options) []string {
	attrs := block.Body.Attributes
	var unresolvedVars []string

	// attrFloatR resolves with evalCtx to catch var.xxx references.
	attrFloatR := func(name string, def float64) float64 {
		v, unresolved := attrFloatCtx(attrs, name, def, evalCtx)
		if unresolved {
			unresolvedVars = append(unresolvedVars, name)
		}
		return v
	}
	attrStringR := func(name string, def string) string {
		v, unresolved := attrStringCtx(attrs, name, def, evalCtx)
		if unresolved {
			unresolvedVars = append(unresolvedVars, name)
		}
		return v
	}

	u := opts.Usage.Get(r.Name)

	switch r.ResourceType {

	case "aws_lambda_function":
		memMB := attrFloatR("memory_size", 128)
		timeoutSec := attrFloatR("timeout", 3)

		invocations := 1_000.0
		if u.MonthlyRequests > 0 {
			invocations = u.MonthlyRequests
		}
		durationSec := timeoutSec * 0.1
		if u.AverageDuration > 0 {
			durationSec = u.AverageDuration / 1000
		}
		gbSec := (memMB / 1024) * durationSec * invocations

		r.CostComponents = []*schema.CostComponent{
			{
				Name:            fmt.Sprintf("Requests (%.0f/mo)", invocations),
				Unit:            "1M requests",
				MonthlyQuantity: invocations / 1_000_000,
				PricePerUnit:    pricing.Lambda.RequestPer1M,
			},
			{
				Name:            "Duration",
				Unit:            "GB-second",
				MonthlyQuantity: gbSec,
				PricePerUnit:    pricing.Lambda.GBSecond,
			},
		}

	case "aws_ecs_task_definition":
		cpu := attrFloatR("cpu", 256) / 1024
		mem := attrFloatR("memory", 512) / 1024

		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "vCPU",
				Unit:            "vCPU-hour",
				MonthlyQuantity: cpu * pricing.HoursPerMonth,
				PricePerUnit:    pricing.ECSFargate.VCPUPerHour,
			},
			{
				Name:            "Memory",
				Unit:            "GB-hour",
				MonthlyQuantity: mem * pricing.HoursPerMonth,
				PricePerUnit:    pricing.ECSFargate.MemGBPerHour,
			},
		}

	case "aws_ecs_service":
		r.IsFree = true

	case "aws_cloudwatch_metric_alarm":
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Alarm",
				Unit:            "alarm",
				MonthlyQuantity: 1,
				PricePerUnit:    pricing.CloudWatchAlarm.StandardPerMonth,
			},
		}

	case "aws_cloudwatch_composite_alarm":
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Composite alarm",
				Unit:            "alarm",
				MonthlyQuantity: 1,
				PricePerUnit:    pricing.CloudWatchAlarm.CompositePerMonth,
			},
		}

	case "aws_cloudwatch_dashboard":
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Dashboard",
				Unit:            "dashboard",
				MonthlyQuantity: 1,
				PricePerUnit:    pricing.CloudWatchDashboard.PerMonth,
			},
		}

	case "aws_sns_topic":
		publishes := u.MonthlyPublishes
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Publishes",
				Unit:            "1M publishes",
				MonthlyQuantity: publishes / 1_000_000,
				PricePerUnit:    pricing.SNS.PublishPer1M,
			},
		}

	case "aws_kms_key":
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Customer managed key",
				Unit:            "key",
				MonthlyQuantity: 1,
				PricePerUnit:    pricing.KMS.KeyPerMonth,
			},
		}

	case "aws_vpc_endpoint":
		endpointType := attrStringR("vpc_endpoint_type", "Interface")
		if strings.EqualFold(endpointType, "Gateway") {
			r.IsFree = true
		} else {
			r.CostComponents = []*schema.CostComponent{
				{
					Name:            "Endpoint (1 AZ assumed)",
					Unit:            "hour",
					MonthlyQuantity: pricing.HoursPerMonth,
					PricePerUnit:    pricing.VPCEndpoint.HourPerAZ,
				},
			}
		}

	case "aws_dynamodb_table":
		billingMode := attrStringR("billing_mode", "PROVISIONED")
		if strings.EqualFold(billingMode, "PAY_PER_REQUEST") {
			reads := u.MonthlyReadUnits
			writes := u.MonthlyWriteUnits
			r.CostComponents = []*schema.CostComponent{
				{
					Name:            "Reads",
					Unit:            "1M RRUs",
					MonthlyQuantity: reads / 1_000_000,
					PricePerUnit:    0.25,
				},
				{
					Name:            "Writes",
					Unit:            "1M WRUs",
					MonthlyQuantity: writes / 1_000_000,
					PricePerUnit:    1.25,
				},
			}
		} else {
			wcu := attrFloatR("write_capacity", 5)
			rcu := attrFloatR("read_capacity", 5)
			r.CostComponents = []*schema.CostComponent{
				{
					Name:            fmt.Sprintf("Write capacity (%g WCU)", wcu),
					Unit:            "WCU",
					MonthlyQuantity: wcu,
					PricePerUnit:    pricing.DynamoDB.ProvisionedWCUPerMonth,
				},
				{
					Name:            fmt.Sprintf("Read capacity (%g RCU)", rcu),
					Unit:            "RCU",
					MonthlyQuantity: rcu,
					PricePerUnit:    pricing.DynamoDB.ProvisionedRCUPerMonth,
				},
			}
		}

	case "aws_lb", "aws_alb":
		lbType := attrStringR("load_balancer_type", "application")
		hourly := pricing.ALB.HourlyRate
		if strings.EqualFold(lbType, "network") {
			hourly = 0.008
		}
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            fmt.Sprintf("%s LB", lbType),
				Unit:            "hour",
				MonthlyQuantity: pricing.HoursPerMonth,
				PricePerUnit:    hourly,
			},
		}

	case "aws_route53_zone":
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Hosted zone",
				Unit:            "zone",
				MonthlyQuantity: 1,
				PricePerUnit:    pricing.Route53Zone.PerMonth,
			},
		}

	case "aws_cloudfront_distribution":
		httpsReqs := u.MonthlyHTTPS
		dataGB := u.DataTransferGB
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "HTTPS requests",
				Unit:            "10M requests",
				MonthlyQuantity: httpsReqs / 10_000_000,
				PricePerUnit:    pricing.CloudFront.Per10MRequests,
			},
			{
				Name:            "Data transfer",
				Unit:            "GB",
				MonthlyQuantity: dataGB,
				PricePerUnit:    pricing.CloudFront.DataTransferGB,
			},
		}

	case "aws_ecr_repository":
		storageGB := u.ImageStorageGB
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Storage",
				Unit:            "GB-month",
				MonthlyQuantity: storageGB,
				PricePerUnit:    pricing.ECRRepository.StoragePerGBMonth,
			},
		}

	case "aws_secretsmanager_secret":
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Secret",
				Unit:            "secret",
				MonthlyQuantity: 1,
				PricePerUnit:    pricing.SecretsManager.PerSecretMonth,
			},
		}

	case "aws_iam_role", "aws_iam_role_policy", "aws_iam_role_policy_attachment",
		"aws_iam_policy", "aws_iam_policy_attachment", "aws_iam_user", "aws_iam_user_policy",
		"aws_iam_user_policy_attachment", "aws_iam_group", "aws_iam_group_policy",
		"aws_iam_group_policy_attachment", "aws_iam_access_key", "aws_iam_account_password_policy",
		"aws_iam_instance_profile", "aws_iam_service_linked_role",
		"aws_iam_openid_connect_provider",
		"aws_security_group", "aws_vpc_security_group_ingress_rule", "aws_vpc_security_group_egress_rule",
		"aws_cloudwatch_log_group", "aws_cloudwatch_metric_filter", "aws_cloudwatch_log_metric_filter",
		"aws_cloudwatch_event_rule", "aws_cloudwatch_event_target",
		"aws_lambda_permission",
		"aws_kms_alias",
		"aws_sns_topic_subscription", "aws_sns_topic_policy",
		"aws_ecs_cluster",
		"aws_s3_bucket_policy", "aws_s3_bucket_server_side_encryption_configuration",
		"aws_s3_bucket_lifecycle_configuration", "aws_s3_bucket_acl", "aws_s3_bucket_object",
		"aws_s3_bucket_versioning", "aws_s3_bucket_public_access_block",
		"aws_s3_bucket_ownership_controls",
		"aws_route53_record", "aws_route53_vpc_association_authorization",
		"aws_acm_certificate", "aws_acm_certificate_validation",
		"aws_shield_subscription", "aws_shield_drt_access_role_arn_association",
		"aws_lb_listener", "aws_lb_target_group", "aws_alb_listener_rule", "aws_alb_target_group",
		"aws_launch_template", "aws_autoscaling_attachment", "aws_autoscaling_group",
		"aws_db_parameter_group",
		"aws_ecr_lifecycle_policy", "aws_ecr_pull_through_cache_rule",
		"aws_ecr_registry_policy", "aws_ecr_repository_policy",
		"aws_ssm_parameter",
		"aws_organizations_policy", "aws_organizations_policy_attachment",
		"aws_organizations_organizational_unit", "aws_organizations_delegated_administrator",
		"aws_ram_resource_share_accepter", "aws_ram_sharing_with_organization",
		"aws_route", "aws_vpc", "aws_vpc_peering_connection_accepter",
		"aws_codeartifact_domain", "aws_codeartifact_domain_permissions_policy",
		"aws_codeartifact_repository",
		"aws_config_organization_managed_rule",
		"aws_budgets_budget", "aws_budgets_budget_action",
		"aws_securityhub_organization_configuration",
		"aws_ses_active_receipt_rule_set", "aws_ses_receipt_rule_set",
		"aws_cloudtrail",
		"github_repository", "github_repository_file", "github_repository_ruleset",
		"github_team_repository",
		"pagerduty_escalation_policy", "pagerduty_schedule", "pagerduty_service",
		"pagerduty_service_integration", "pagerduty_team", "pagerduty_team_membership",
		"pagerduty_user", "pagerduty_user_contact_method":
		r.IsFree = true

	case "aws_s3_bucket":
		storageGB := u.StorageGB
		r.CostComponents = []*schema.CostComponent{
			{
				Name:            "Storage",
				Unit:            "GB-month",
				MonthlyQuantity: storageGB,
				PricePerUnit:    0.023,
			},
		}

	default:
		r.IsSupported = false
	}

	return unresolvedVars
}

// attrFloatCtx resolves an attribute value using evalCtx for variable references.
// Returns (value, wasUnresolved).
func attrFloatCtx(attrs hclsyntax.Attributes, name string, def float64, evalCtx *hcl.EvalContext) (float64, bool) {
	attr, ok := attrs[name]
	if !ok {
		return def, false
	}
	val, diags := attr.Expr.Value(evalCtx)
	if diags.HasErrors() {
		// Try without evalCtx in case it's a literal.
		val, diags = attr.Expr.Value(nil)
		if diags.HasErrors() {
			return def, true // unresolved variable reference
		}
	}
	if val.Type() == cty.Number {
		bf := val.AsBigFloat()
		f, _ := bf.Float64()
		return f, false
	}
	if val.Type() == cty.String {
		s := val.AsString()
		var f float64
		fmt.Sscanf(s, "%f", &f)
		if f > 0 {
			return f, false
		}
	}
	return def, false
}

func attrStringCtx(attrs hclsyntax.Attributes, name string, def string, evalCtx *hcl.EvalContext) (string, bool) {
	attr, ok := attrs[name]
	if !ok {
		return def, false
	}
	val, diags := attr.Expr.Value(evalCtx)
	if diags.HasErrors() {
		val, diags = attr.Expr.Value(nil)
		if diags.HasErrors() {
			return def, true
		}
	}
	if val.Type() == cty.String {
		return val.AsString(), false
	}
	return def, false
}

// Legacy wrappers kept for callers that don't need evalCtx.
func attrFloat(attrs hclsyntax.Attributes, name string, def float64) float64 {
	v, _ := attrFloatCtx(attrs, name, def, nil)
	return v
}

func attrBool(attrs hclsyntax.Attributes, name string, def bool) bool {
	attr, ok := attrs[name]
	if !ok {
		return def
	}
	val, diags := attr.Expr.Value(nil)
	if diags.HasErrors() {
		return def
	}
	if val.Type() == cty.Bool {
		return val.True()
	}
	return def
}

func attrString(attrs hclsyntax.Attributes, name string, def string) string {
	v, _ := attrStringCtx(attrs, name, def, nil)
	return v
}

// suppress unused warnings for legacy helpers used only in tests
var _ = attrFloat
var _ = attrBool
var _ = attrString
