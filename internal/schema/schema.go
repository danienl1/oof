package schema

import "fmt"

// CostComponent is a single billable line item within a Resource.
type CostComponent struct {
	Name            string
	Unit            string
	UnitMultiplier  float64 // e.g. 730 to convert hourly price → monthly
	MonthlyQuantity float64
	PricePerUnit    float64 // USD
}

func (c *CostComponent) MonthlyCost() float64 {
	qty := c.MonthlyQuantity
	if c.UnitMultiplier != 0 {
		qty *= c.UnitMultiplier
	}
	return qty * c.PricePerUnit
}

// Resource maps to one Terraform resource block.
type Resource struct {
	Name           string
	ResourceType   string
	FilePath       string
	StartLine      int
	IsSupported    bool
	IsFree         bool
	CostComponents []*CostComponent
}

func (r *Resource) MonthlyCost() float64 {
	total := 0.0
	for _, c := range r.CostComponents {
		total += c.MonthlyCost()
	}
	return total
}

// Project is a scanned IaC directory.
type Project struct {
	Name      string
	Path      string
	Resources []*Resource
}

func (p *Project) MonthlyCost() float64 {
	total := 0.0
	for _, r := range p.Resources {
		total += r.MonthlyCost()
	}
	return total
}

// Summary is the top-level scan output.
type Summary struct {
	Currency          string
	Projects          []*Project
	TotalMonthly      float64
	TotalSavings      float64
	ResourceCount     int
	CostingCount      int
	FreeCount         int
	UnsupportedCount  int
	CostChecks        []*CostCheck
}

// CostCheck represents a best-practice recommendation that may apply to multiple resources.
type CostCheck struct {
	Name        string
	Description string
	Savings     float64
	Resources   []string // resource addresses
}

func FormatUSD(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}
