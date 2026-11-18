terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

provider "aws" {
  region = "eu-central-1"
}

variable "env" {
  type    = string
  default = "dev"
}

locals {
  prefix = "orders-${var.env}"
  tags   = { team = "orders", env = var.env }
}

data "aws_caller_identity" "me" {}

resource "aws_dynamodb_table" "orders" {
  name         = "${local.prefix}-table"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }
  attribute {
    name = "status"
    type = "S"
  }

  global_secondary_index {
    name            = "by-status"
    hash_key        = "status"
    projection_type = "ALL"
  }

  local_secondary_index {
    name            = "by-pk-status"
    range_key       = "status"
    projection_type = "KEYS_ONLY"
  }

  stream_enabled   = true
  stream_view_type = "NEW_AND_OLD_IMAGES"

  ttl {
    attribute_name = "expires"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }

  tags = local.tags
}

module "events" {
  source = "./modules/queue"
  name   = "${local.prefix}-events"
}

resource "aws_sns_topic" "orders" {
  name = "${local.prefix}-topic"
}

resource "aws_sns_topic_subscription" "events" {
  topic_arn            = aws_sns_topic.orders.arn
  protocol             = "sqs"
  endpoint             = module.events.arn
  raw_message_delivery = true
}

resource "aws_sns_topic_subscription" "mail" {
  topic_arn = aws_sns_topic.orders.arn
  protocol  = "email"
  endpoint  = "ops@example.com"
}

resource "aws_s3_bucket" "uploads" {
  bucket = "${local.prefix}-uploads"
  tags   = local.tags
}

resource "aws_s3_bucket_versioning" "uploads" {
  bucket = aws_s3_bucket.uploads.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_notification" "uploads" {
  bucket = aws_s3_bucket.uploads.id

  queue {
    queue_arn     = module.events.arn
    events        = ["s3:ObjectCreated:*"]
    filter_prefix = "uploads/"
  }

  lambda_function {
    lambda_function_arn = aws_lambda_function.worker.arn
    events              = ["s3:ObjectRemoved:*"]
  }
}

resource "aws_iam_role" "worker" {
  name               = "worker"
  assume_role_policy = "{}"
}

resource "aws_lambda_function" "worker" {
  function_name = "worker"
  role          = aws_iam_role.worker.arn
  environment {
    variables = {
      TABLE     = aws_dynamodb_table.orders.name
      QUEUE_URL = module.events.url
      BUCKET    = aws_s3_bucket.uploads.bucket
      ACCOUNT   = data.aws_caller_identity.me.account_id
      STAGE     = upper(var.env)
    }
  }
}

output "table_name" {
  value = aws_dynamodb_table.orders.name
}

output "stream_arn" {
  value = aws_dynamodb_table.orders.stream_arn
}

output "queue_url" {
  value = module.events.url
}

output "account" {
  value = data.aws_caller_identity.me.account_id
}
