package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/danienl1/oof/internal/gitdiff"
	"github.com/danienl1/oof/internal/hcl"
	"github.com/danienl1/oof/internal/mcp"
	"github.com/danienl1/oof/internal/schema"
	"github.com/danienl1/oof/internal/usage"
	"github.com/danienl1/oof/internal/watch"
	"github.com/spf13/cobra"
)

const version = "0.2.0"

func main() {
	root := &cobra.Command{
		Use:     "oof",
		Short:   "Cloud cost intelligence for AppFolio infrastructure teams",
		Version: version,
	}

	root.AddCommand(
		scanCmd(),
		priceCmd(),
		diffCmd(),
		commentCmd(),
		watchCmd(),
		mcpCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// --- shared flags helper ---

type scanFlags struct {
	outputFormat string
	region       string
	usageFile    string
	discountRate float64
	top          int
}

func addScanFlags(cmd *cobra.Command, f *scanFlags) {
	cmd.Flags().StringVarP(&f.outputFormat, "format", "f", "table", "Output format: table or json")
	cmd.Flags().StringVar(&f.region, "region", "us-east-1", "AWS region for price multipliers (e.g. us-west-2, eu-west-1)")
	cmd.Flags().StringVar(&f.usageFile, "usage-file", "", "Path to usage.yml with expected monthly quantities")
	cmd.Flags().Float64Var(&f.discountRate, "discount-rate", 0, "Fractional discount to apply to all costs (0–1, e.g. 0.30 for 30% SP/RI discount)")
	cmd.Flags().IntVar(&f.top, "top", 20, "Show only the top N most expensive resources (0 = all)")
}

func (f *scanFlags) buildOptions() (hcl.Options, error) {
	uf, err := usage.Load(f.usageFile)
	if err != nil {
		return hcl.Options{}, err
	}
	return hcl.Options{
		Region:       f.region,
		DiscountRate: f.discountRate,
		Usage:        uf,
	}, nil
}

// --- scan ---

func scanCmd() *cobra.Command {
	var flags scanFlags

	cmd := &cobra.Command{
		Use:   "scan [path]",
		Short: "Estimate costs for an IaC directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}

			opts, err := flags.buildOptions()
			if err != nil {
				return err
			}

			proj, warnings, err := hcl.ParseDirWithOptions(path, opts)
			if err != nil {
				return fmt.Errorf("scan failed: %w", err)
			}

			if flags.outputFormat == "json" {
				return printJSON(buildScanOutput(proj, warnings))
			}

			printScanTable(proj, warnings, flags.region, flags.top)
			return nil
		},
	}

	addScanFlags(cmd, &flags)
	return cmd
}

// --- price ---

func priceCmd() *cobra.Command {
	var flags scanFlags

	cmd := &cobra.Command{
		Use:   "price [terraform-file]",
		Short: "Price a Terraform file or snippet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}

			var dir string
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if info.IsDir() {
				dir = path
			} else {
				tmp, err := os.MkdirTemp("", "oof-price-*")
				if err != nil {
					return err
				}
				defer os.RemoveAll(tmp)
				src, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(tmp, "main.tf"), src, 0600); err != nil {
					return err
				}
				dir = tmp
			}

			opts, err := flags.buildOptions()
			if err != nil {
				return err
			}

			proj, warnings, err := hcl.ParseDirWithOptions(dir, opts)
			if err != nil {
				return fmt.Errorf("price failed: %w", err)
			}

			if flags.outputFormat == "json" {
				return printJSON(buildScanOutput(proj, warnings))
			}

			printScanTable(proj, warnings, flags.region, flags.top)
			return nil
		},
	}

	addScanFlags(cmd, &flags)
	return cmd
}

// --- diff ---

func diffCmd() *cobra.Command {
	var (
		flags   scanFlags
		baseRef string
	)

	cmd := &cobra.Command{
		Use:   "diff [path]",
		Short: "Show cost delta between HEAD and a base git ref",
		Long: `Scans the IaC directory at HEAD and at the base ref, then prints a
unified diff-style cost table showing +/- per resource and total delta.

If --base-ref is not specified, the base is auto-detected from the upstream
tracking branch, or falls back to origin/main / origin/master.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}

			opts, err := flags.buildOptions()
			if err != nil {
				return err
			}

			result, err := gitdiff.Run(path, gitdiff.DiffOptions{
				BaseRef:     baseRef,
				ScanOptions: opts,
			})
			if err != nil {
				return fmt.Errorf("diff failed: %w", err)
			}

			if flags.outputFormat == "json" {
				return printJSON(result)
			}

			printDiffTable(result, flags.region)
			return nil
		},
	}

	addScanFlags(cmd, &flags)
	cmd.Flags().StringVar(&baseRef, "base-ref", "", "Git ref to compare against (branch, tag, or commit SHA)")
	return cmd
}

// --- comment ---

func commentCmd() *cobra.Command {
	var (
		flags         scanFlags
		baseRef       string
		prNumber      int
		failOnIncrease float64
		repo          string
	)

	cmd := &cobra.Command{
		Use:   "comment [path]",
		Short: "Post a cost diff as a GitHub PR comment",
		Long: `Computes the cost diff (same as 'diff') and posts it as a markdown
table comment on a GitHub pull request. Reads GITHUB_TOKEN from the environment.

PR number is read from the GITHUB_EVENT_PULL_REQUEST_NUMBER env var if --pr is
not specified. Repository is read from GITHUB_REPOSITORY if --repo is not set.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}

			token := os.Getenv("GITHUB_TOKEN")
			if token == "" {
				return fmt.Errorf("GITHUB_TOKEN environment variable is not set")
			}

			if repo == "" {
				repo = os.Getenv("GITHUB_REPOSITORY")
			}
			if repo == "" {
				return fmt.Errorf("--repo or GITHUB_REPOSITORY is required")
			}

			if prNumber == 0 {
				fmt.Sscanf(os.Getenv("GITHUB_EVENT_PULL_REQUEST_NUMBER"), "%d", &prNumber)
			}
			if prNumber == 0 {
				return fmt.Errorf("--pr or GITHUB_EVENT_PULL_REQUEST_NUMBER is required")
			}

			opts, err := flags.buildOptions()
			if err != nil {
				return err
			}

			result, err := gitdiff.Run(path, gitdiff.DiffOptions{
				BaseRef:     baseRef,
				ScanOptions: opts,
			})
			if err != nil {
				return fmt.Errorf("diff failed: %w", err)
			}

			body := renderDiffMarkdown(result, flags.region)
			if err := postGitHubComment(token, repo, prNumber, body); err != nil {
				return fmt.Errorf("post comment: %w", err)
			}

			fmt.Printf("Posted cost diff to %s#%d\n", repo, prNumber)

			if failOnIncrease > 0 && result.TotalDelta() > failOnIncrease {
				return fmt.Errorf("cost increase of %s exceeds threshold of %s",
					schema.FormatUSD(result.TotalDelta()),
					schema.FormatUSD(failOnIncrease))
			}
			return nil
		},
	}

	addScanFlags(cmd, &flags)
	cmd.Flags().StringVar(&baseRef, "base-ref", "", "Git ref to compare against")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "GitHub PR number (default: GITHUB_EVENT_PULL_REQUEST_NUMBER)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repo in owner/name format (default: GITHUB_REPOSITORY)")
	cmd.Flags().Float64Var(&failOnIncrease, "fail-on-increase", 0, "Exit non-zero if monthly cost increases by more than this amount (USD)")
	return cmd
}

// --- watch ---

func watchCmd() *cobra.Command {
	var flags scanFlags

	cmd := &cobra.Command{
		Use:   "watch [path]",
		Short: "Re-scan on .tf file changes and print cost delta",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}

			opts, err := flags.buildOptions()
			if err != nil {
				return err
			}

			return watch.Run(path, watch.Options{
				ScanOptions: opts,
				Region:      flags.region,
			})
		},
	}

	addScanFlags(cmd, &flags)
	return cmd
}

// --- mcp ---

func mcpCmd() *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start the MCP server (stdio JSON-RPC)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcp.Serve(workspace)
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "Restrict scan paths to this directory (recommended)")
	return cmd
}

// --- output helpers ---

func buildScanOutput(proj *schema.Project, warnings []string) map[string]interface{} {
	costed, free, unsupported := 0, 0, 0
	for _, r := range proj.Resources {
		if r.IsFree {
			free++
		} else if !r.IsSupported {
			unsupported++
		} else {
			costed++
		}
	}

	var resourcesOut []map[string]interface{}
	for _, r := range proj.Resources {
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
		resourcesOut = append(resourcesOut, map[string]interface{}{
			"name":               r.Name,
			"type":               r.ResourceType,
			"total_monthly_cost": r.MonthlyCost(),
			"is_free":            r.IsFree,
			"is_supported":       r.IsSupported,
			"cost_components":    components,
		})
	}

	return map[string]interface{}{
		"currency": "USD",
		"summary": map[string]interface{}{
			"monthly_cost":          proj.MonthlyCost(),
			"resources":             len(proj.Resources),
			"costed_resources":      costed,
			"free_resources":        free,
			"unsupported_resources": unsupported,
			"warning_diagnostics":   len(warnings),
		},
		"resources": resourcesOut,
		"warnings":  warnings,
	}
}

func printScanTable(proj *schema.Project, warnings []string, region string, top int) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	var costed []*schema.Resource
	for _, r := range proj.Resources {
		if !r.IsFree && r.IsSupported {
			costed = append(costed, r)
		}
	}
	sort.Slice(costed, func(i, j int) bool {
		return costed[i].MonthlyCost() > costed[j].MonthlyCost()
	})
	shown := costed
	if top > 0 && len(costed) > top {
		shown = costed[:top]
	}

	regionLabel := region
	if regionLabel == "" {
		regionLabel = "us-east-1"
	}
	fmt.Fprintf(w, "\n  RESOURCE\tTYPE\tMONTHLY COST\n")
	fmt.Fprintf(w, "  %s\t%s\t%s\n", strings.Repeat("─", 40), strings.Repeat("─", 30), strings.Repeat("─", 14))

	for _, r := range shown {
		cost := r.MonthlyCost()
		var costStr string
		if cost == 0 {
			costStr = "Usage-based"
		} else {
			costStr = schema.FormatUSD(cost) + "/mo"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Name, r.ResourceType, costStr)
	}

	fmt.Fprintf(w, "  %s\t%s\t%s\n", strings.Repeat("─", 40), strings.Repeat("─", 30), strings.Repeat("─", 14))
	if top > 0 && len(costed) > top {
		fmt.Fprintf(w, "  (showing %d of %d costed resources)\t\t\n", top, len(costed))
	}
	fmt.Fprintf(w, "  TOTAL (%s)\t\t%s/mo\n\n", regionLabel, schema.FormatUSD(proj.MonthlyCost()))

	if len(warnings) > 0 {
		fmt.Fprintf(w, "  Diagnostics:\n")
		for _, w2 := range warnings {
			fmt.Fprintf(w, "    ⚠  %s\n", w2)
		}
		fmt.Fprintln(w)
	}
}

func printDiffTable(result *gitdiff.Result, region string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	regionLabel := region
	if regionLabel == "" {
		regionLabel = "us-east-1"
	}

	// Sort: cost-increasing changes first, then decreasing, then removals/additions.
	deltas := make([]gitdiff.ResourceDelta, len(result.Deltas))
	copy(deltas, result.Deltas)
	sort.Slice(deltas, func(i, j int) bool {
		return deltas[i].Delta() > deltas[j].Delta()
	})

	fmt.Fprintf(w, "\n  RESOURCE\tBASE\tHEAD\tDELTA\n")
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n",
		strings.Repeat("─", 40), strings.Repeat("─", 12), strings.Repeat("─", 12), strings.Repeat("─", 12))

	for _, d := range deltas {
		prefix := " "
		if d.Delta() > 0 {
			prefix = "+"
		} else if d.Delta() < 0 {
			prefix = "-"
		}
		baseStr := schema.FormatUSD(d.BaseCost)
		headStr := schema.FormatUSD(d.HeadCost)
		if d.IsNew {
			baseStr = "(new)"
		}
		if d.IsGone {
			headStr = "(removed)"
		}
		deltaStr := prefix + schema.FormatUSD(abs(d.Delta()))
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", d.Address, baseStr, headStr, deltaStr)
	}

	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n",
		strings.Repeat("─", 40), strings.Repeat("─", 12), strings.Repeat("─", 12), strings.Repeat("─", 12))

	totalDelta := result.TotalDelta()
	totalPrefix := " "
	if totalDelta > 0 {
		totalPrefix = "+"
	} else if totalDelta < 0 {
		totalPrefix = "-"
	}
	fmt.Fprintf(w, "  TOTAL (%s)\t%s/mo\t%s/mo\t%s%s/mo\n\n",
		regionLabel,
		schema.FormatUSD(result.BaseTotal),
		schema.FormatUSD(result.HeadTotal),
		totalPrefix,
		schema.FormatUSD(abs(totalDelta)),
	)
}

func renderDiffMarkdown(result *gitdiff.Result, region string) string {
	var sb strings.Builder

	regionLabel := region
	if regionLabel == "" {
		regionLabel = "us-east-1"
	}

	sb.WriteString("## 💰 oof Cost Diff\n\n")
	sb.WriteString(fmt.Sprintf("Region: `%s`\n\n", regionLabel))

	deltas := make([]gitdiff.ResourceDelta, len(result.Deltas))
	copy(deltas, result.Deltas)
	sort.Slice(deltas, func(i, j int) bool {
		return deltas[i].Delta() > deltas[j].Delta()
	})

	if len(deltas) == 0 {
		sb.WriteString("No cost changes detected.\n")
	} else {
		sb.WriteString("| Resource | Base | Head | Delta |\n")
		sb.WriteString("|----------|------|------|-------|\n")
		for _, d := range deltas {
			baseStr := schema.FormatUSD(d.BaseCost)
			headStr := schema.FormatUSD(d.HeadCost)
			if d.IsNew {
				baseStr = "—"
			}
			if d.IsGone {
				headStr = "—"
			}
			delta := d.Delta()
			var deltaStr string
			if delta > 0 {
				deltaStr = fmt.Sprintf("+%s ⬆️", schema.FormatUSD(delta))
			} else if delta < 0 {
				deltaStr = fmt.Sprintf("-%s ⬇️", schema.FormatUSD(-delta))
			} else {
				deltaStr = "no change"
			}
			sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s |\n", d.Address, baseStr, headStr, deltaStr))
		}
		sb.WriteString("\n")
	}

	totalDelta := result.TotalDelta()
	var totalLine string
	if totalDelta > 0 {
		totalLine = fmt.Sprintf("**Total: %s/mo → %s/mo (+%s/mo) ⬆️**",
			schema.FormatUSD(result.BaseTotal), schema.FormatUSD(result.HeadTotal), schema.FormatUSD(totalDelta))
	} else if totalDelta < 0 {
		totalLine = fmt.Sprintf("**Total: %s/mo → %s/mo (-%s/mo) ⬇️**",
			schema.FormatUSD(result.BaseTotal), schema.FormatUSD(result.HeadTotal), schema.FormatUSD(-totalDelta))
	} else {
		totalLine = fmt.Sprintf("**Total: %s/mo (no change)**", schema.FormatUSD(result.HeadTotal))
	}
	sb.WriteString(totalLine + "\n\n")
	sb.WriteString("_Generated by [oof](https://github.com/danienl1/oof). Estimates use on-demand pricing; actual costs may differ due to Reserved Instances, Savings Plans, and usage-based charges._\n")

	return sb.String()
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
