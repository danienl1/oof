// Package pricing provides static AWS on-demand price tables (us-east-1 unless
// noted). Values are USD per unit per month unless the comment says otherwise.
// Sources: AWS pricing pages, last refreshed 2025-06.
package pricing

// Lambda is priced per request + per GB-second of duration.
var Lambda = struct {
	RequestPer1M  float64 // per 1M requests/month
	GBSecond      float64 // per GB-second
}{
	RequestPer1M: 0.20,
	GBSecond:     0.0000166667,
}

// ECSFargate prices per vCPU-hour and per GB-hour.
var ECSFargate = struct {
	VCPUPerHour   float64
	MemGBPerHour  float64
}{
	VCPUPerHour:  0.04048,
	MemGBPerHour: 0.004445,
}

// CloudWatchAlarm is per alarm per month.
var CloudWatchAlarm = struct {
	StandardPerMonth    float64
	CompositePerMonth   float64
	HighResPerMonth     float64
}{
	StandardPerMonth:  0.10,
	CompositePerMonth: 0.50,
	HighResPerMonth:   0.30,
}

// CloudWatchDashboard is per dashboard per month (first 3 free).
var CloudWatchDashboard = struct {
	PerMonth float64
}{
	PerMonth: 3.00,
}

// SNS pricing per million publishes and per notification type.
var SNS = struct {
	PublishPer1M     float64
	HTTPPer1M        float64
	EmailPer1M       float64
	LambdaPer1M      float64
}{
	PublishPer1M: 0.50,
	HTTPPer1M:    0.60,
	EmailPer1M:   2.00,
	LambdaPer1M:  0.20,
}

// KMS per key per month and per 10k API calls.
var KMS = struct {
	KeyPerMonth     float64
	Per10kAPIcalls  float64
}{
	KeyPerMonth:    1.00,
	Per10kAPIcalls: 0.03,
}

// VPCEndpoint is per interface endpoint per AZ per hour.
var VPCEndpoint = struct {
	HourPerAZ   float64
	DataGBIn    float64
	DataGBOut   float64
}{
	HourPerAZ: 0.01,
	DataGBIn:  0.01,
	DataGBOut: 0.01,
}

// DynamoDB pricing per WCU/RCU provisioned per month, and on-demand per million requests.
var DynamoDB = struct {
	ProvisionedWCUPerMonth float64
	ProvisionedRCUPerMonth float64
	StoragePerGBMonth      float64
}{
	ProvisionedWCUPerMonth: 0.00065,
	ProvisionedRCUPerMonth: 0.00013,
	StoragePerGBMonth:      0.25,
}

// ALB pricing per hour and per LCU.
var ALB = struct {
	HourlyRate    float64
	LCUPerHour    float64
}{
	HourlyRate: 0.008,
	LCUPerHour: 0.008,
}

// Route53Zone per hosted zone per month (first 25 zones $0.50, then $0.10).
var Route53Zone = struct {
	PerMonth float64
}{
	PerMonth: 0.50,
}

// CloudFront per 10M HTTPS requests and per GB data transfer (simplified).
var CloudFront = struct {
	Per10MRequests float64
	DataTransferGB float64
}{
	Per10MRequests: 0.0100,
	DataTransferGB: 0.0085,
}

// ECRRepository storage per GB per month.
var ECRRepository = struct {
	StoragePerGBMonth float64
}{
	StoragePerGBMonth: 0.10,
}

// SecretsManager per secret per month.
var SecretsManager = struct {
	PerSecretMonth float64
}{
	PerSecretMonth: 0.40,
}

// HoursPerMonth is the standard billing assumption.
const HoursPerMonth = 730.0
