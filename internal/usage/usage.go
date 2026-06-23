// Package usage loads a usage.yml file specifying expected monthly quantities
// for usage-based resources (Lambda invocations, S3 GB, CloudFront requests, etc.).
package usage

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// File holds per-resource usage overrides keyed by resource address
// (e.g. "aws_lambda_function.my_fn").
type File struct {
	Resources map[string]ResourceUsage `yaml:"resources"`
}

// ResourceUsage holds all possible usage fields. Fields not set default to zero.
type ResourceUsage struct {
	// Lambda
	MonthlyRequests  float64 `yaml:"monthly_requests"`   // total invocations per month
	AverageDuration  float64 `yaml:"average_duration_ms"` // ms; converted to seconds internally

	// S3
	StorageGB        float64 `yaml:"storage_gb"`
	MonthlyGET       float64 `yaml:"monthly_get_requests"`
	MonthlyPUT       float64 `yaml:"monthly_put_requests"`

	// CloudFront
	MonthlyHTTPS     float64 `yaml:"monthly_https_requests"`
	DataTransferGB   float64 `yaml:"data_transfer_gb"`

	// SNS
	MonthlyPublishes float64 `yaml:"monthly_publishes"`

	// ECR
	ImageStorageGB   float64 `yaml:"image_storage_gb"`

	// DynamoDB on-demand
	MonthlyReadUnits  float64 `yaml:"monthly_read_units"`
	MonthlyWriteUnits float64 `yaml:"monthly_write_units"`
}

// Load reads a usage YAML file. Returns an empty File (not an error) if path is "".
func Load(path string) (*File, error) {
	if path == "" {
		return &File{Resources: map[string]ResourceUsage{}}, nil
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("usage file: %w", err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("usage file: %w", err)
	}

	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("usage file: %w", err)
	}

	if f.Resources == nil {
		f.Resources = map[string]ResourceUsage{}
	}
	return &f, nil
}

// Get returns the usage entry for a resource address, or zero value if absent.
func (f *File) Get(address string) ResourceUsage {
	if f == nil {
		return ResourceUsage{}
	}
	return f.Resources[address]
}
