package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Empty(t *testing.T) {
	f, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("expected non-nil file")
	}
	u := f.Get("aws_lambda_function.fn")
	if u.MonthlyRequests != 0 {
		t.Errorf("expected 0 monthly requests, got %v", u.MonthlyRequests)
	}
}

func TestLoad_ParsesYAML(t *testing.T) {
	dir := t.TempDir()
	content := `
resources:
  aws_lambda_function.my_fn:
    monthly_requests: 5000000
    average_duration_ms: 200
  aws_s3_bucket.data:
    storage_gb: 500
  aws_cloudfront_distribution.cdn:
    monthly_https_requests: 100000000
    data_transfer_gb: 1000
`
	p := filepath.Join(dir, "usage.yml")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}

	fn := f.Get("aws_lambda_function.my_fn")
	if fn.MonthlyRequests != 5_000_000 {
		t.Errorf("lambda monthly_requests: got %v, want 5000000", fn.MonthlyRequests)
	}
	if fn.AverageDuration != 200 {
		t.Errorf("lambda average_duration_ms: got %v, want 200", fn.AverageDuration)
	}

	s3 := f.Get("aws_s3_bucket.data")
	if s3.StorageGB != 500 {
		t.Errorf("s3 storage_gb: got %v, want 500", s3.StorageGB)
	}

	cdn := f.Get("aws_cloudfront_distribution.cdn")
	if cdn.MonthlyHTTPS != 100_000_000 {
		t.Errorf("cloudfront monthly_https: got %v, want 100000000", cdn.MonthlyHTTPS)
	}

	// Non-existent resource returns zero value.
	zero := f.Get("aws_lambda_function.other")
	if zero.MonthlyRequests != 0 {
		t.Errorf("expected zero value for absent resource")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/usage.yml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
