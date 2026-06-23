package pricing

// RegionMultiplier returns the price multiplier for a given AWS region
// relative to us-east-1 (baseline = 1.0). Values sourced from AWS pricing
// pages; us-west-2 and eu-west-1 are the primary AppFolio regions.
func RegionMultiplier(region string) float64 {
	switch region {
	case "us-east-1", "us-east-2":
		return 1.0
	case "us-west-1":
		return 1.10
	case "us-west-2":
		return 1.02
	case "eu-west-1":
		return 1.07
	case "eu-west-2":
		return 1.13
	case "eu-west-3":
		return 1.14
	case "eu-central-1":
		return 1.12
	case "ap-northeast-1":
		return 1.14
	case "ap-northeast-2":
		return 1.12
	case "ap-southeast-1":
		return 1.12
	case "ap-southeast-2":
		return 1.14
	case "ap-south-1":
		return 1.08
	case "sa-east-1":
		return 1.50
	case "ca-central-1":
		return 1.09
	default:
		return 1.0
	}
}
