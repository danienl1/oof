# oof

Cloud cost intelligence for AppFolio infrastructure teams

<img width="1500" alt="Jimmy O Yang" src="jimmy.jpeg" />

## Usage

### Scan Terraform Infrastructure
Quickly analyze cost implications of your Terraform configuration:
```bash
$ oof scan ./terraform
+---------------+------------+------------+
| Resource      | Monthly    | Annual     |
+---------------+------------+------------+
| Lambda        | $42.50     | $510       |
| ECS Fargate   | $183.75    | $2,205     |
| DynamoDB      | $24.99     | $299.88    |
| ALB           | $32.50     | $390       |
+---------------+------------+------------+
Total Estimated Monthly Cost: $283.74
```

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-blue)](https://golang.org/dl/)
[![License](https://img.shields.io/badge/License-Apache%202.0-green)](https://opensource.org/licenses/Apache-2.0)

## Install

### Go Install
```bash
go install github.com/appfolio/oof@latest
```

### Homebrew
```bash
brew tap appfolio/oof
brew install oof
```

### Price Specific Terraform File
Calculate precise costs for a single Terraform file:
```bash
$ oof price main.tf
Total Estimated Cost: $1,245.50/month
```

### Start MCP Server
Launch the Machine Coordination Protocol (MCP) server for AI integration:
```bash
$ oof mcp
MCP Server started on localhost:8080
```

## MCP Integration

oof provides a robust MCP (Machine Coordination Protocol) server designed specifically for Claude Code and other AI agents. It integrates seamlessly with the agent-skills plugin located at `infrastructure-security-skills/skills/oof`, enabling intelligent cost analysis and infrastructure optimization across AppFolio's cloud environments.

## Supported Resources

| Resource Type         | Supports Pricing | Free Base Cost |
|----------------------|-----------------|---------------|
| Lambda               | Yes             | No            |
| ECS Fargate          | Yes             | No            |
| CloudWatch Alarms    | Yes             | Yes           |
| CloudWatch Composite | Yes             | Yes           |
| CloudWatch Dashboard | Yes             | Yes           |
| KMS                  | Yes             | Yes           |
| SNS                  | Yes             | Yes           |
| VPC Endpoints        | Yes             | No            |
| DynamoDB             | Yes             | No            |
| Application Load Balancer | Yes        | No            |
| Route53 Zones        | Yes             | Yes           |
| CloudFront           | Yes             | No            |
| ECR                  | Yes             | Yes           |
| Secrets Manager      | Yes             | No            |

## Repository Structure

```
oof/
├── cmd/oof/main.go          # CLI entry point
├── internal/{hcl,pricing,schema,mcp}/  # core packages
├── .github/workflows/
│   ├── ci.yml                      # test + lint on PR / push to main
│   └── release.yml                 # goreleaser on v* tag push
├── .goreleaser.yaml                # linux/darwin/windows, amd64+arm64
├── Makefile                        # build/test/lint/install/cover
├── README.md, CHANGELOG.md, LICENSE
└── .gitignore
```

## Development

### Build
```bash
make build
```

### Run
```bash
# Scan a Terraform directory (shows top 20 costed resources by default)
./bin/oof scan ./path/to/terraform

# Show more or all resources
./bin/oof scan ./path/to/terraform --top 50
./bin/oof scan ./path/to/terraform --top 0   # all
```

## CI Usage

Add a cost diff comment to every pull request:

```yaml
# .github/workflows/cost.yml
name: Cost Diff

on:
  pull_request:

permissions:
  contents: read
  pull-requests: write

jobs:
  cost:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go install github.com/appfolio/oof@latest
      - name: Post cost diff
        run: oof comment ./terraform --repo ${{ github.repository }} --pr ${{ github.event.pull_request.number }}
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

This posts a table like:

```
| Resource | Base | Head | Delta |
|---|---|---|---|
| `aws_lambda_function.worker` | $300.02 | $750.02 | +$450.00 ⬆️ |

**Total: $1,200.00/mo → $1,650.00/mo (+$450.00/mo) ⬆️**
```

To fail the CI check if costs increase beyond a threshold:

```yaml
- run: oof comment ./terraform --repo ${{ github.repository }} --pr ${{ github.event.pull_request.number }} --fail-on-increase 100
```

## Shipping a Release

```bash
git init && git remote add origin git@github.com:appfolio/oof
git tag v0.1.0 && git push origin v0.1.0
# GitHub Actions runs GoReleaser → publishes binaries + checksums to the release
```

## License
Apache 2.0
