package pricing

import "testing"

func TestRegionMultiplier(t *testing.T) {
	cases := []struct {
		region string
		want   float64
	}{
		{"us-east-1", 1.0},
		{"us-east-2", 1.0},
		{"us-west-2", 1.02},
		{"eu-west-1", 1.07},
		{"sa-east-1", 1.50},
		{"unknown-region", 1.0},
		{"", 1.0},
	}
	for _, tc := range cases {
		got := RegionMultiplier(tc.region)
		if got != tc.want {
			t.Errorf("RegionMultiplier(%q) = %v, want %v", tc.region, got, tc.want)
		}
	}
}
