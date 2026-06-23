# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2025-06-05

### Added
- Initial release
- HCL parser for Terraform .tf files
- Static AWS price tables (no API key required)
- scan subcommand: estimate costs for an IaC directory
- price subcommand: price a single Terraform file
- mcp subcommand: MCP stdio server for AI agent integration
- Supported resource types: 
  * aws_lambda_function
  * aws_ecs_task_definition
  * aws_cloudwatch_metric_alarm
  * aws_cloudwatch_composite_alarm
  * aws_cloudwatch_dashboard
  * aws_kms_key
  * aws_sns_topic
  * aws_vpc_endpoint
  * aws_dynamodb_table
  * aws_lb
  * aws_route53_zone
  * aws_cloudfront_distribution
  * aws_ecr_repository
  * aws_secretsmanager_secret
  * aws_s3_bucket