variable "suffix" {
  default = "x"
}

data "aws_region" "current" {}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
}

module "remote_queue" {
  source = "git::https://example.com/queue.git"
}

# The name refers to a data source: identity, so the table is skipped.
resource "aws_dynamodb_table" "regional" {
  name     = "t-${data.aws_region.current.name}"
  hash_key = "id"
  attribute {
    name = "id"
    type = "S"
  }
}

# The hash key comes from a function outside the table: skipped.
resource "aws_dynamodb_table" "stamped" {
  name     = "stamped"
  hash_key = uuid()
  attribute {
    name = "id"
    type = "S"
  }
}

# A property that cannot be resolved is left out; the queue stays.
resource "aws_sqs_queue" "main" {
  name                       = "main-${var.suffix}"
  visibility_timeout_seconds = timestamp()
  policy                     = jsonencode({ Statement = [{ Resource = aws_iam_role.r.arn }] })
}

# A name built from another queue's name resolves.
resource "aws_sqs_queue" "dlq" {
  name = "${aws_sqs_queue.main.name}-dlq"
}

# A FIFO queue with a generated name, and a name taken from a remote module.
resource "aws_sqs_queue" "jobs" {
  name_prefix = "jobs-"
  fifo_queue  = true
}

resource "aws_sqs_queue" "remote_dlq" {
  name = "${module.remote_queue.name}-dlq"
}

resource "aws_iam_role" "r" {
  name = "r"
}

# Provider v3 inline versioning and policy, attached by literal name.
resource "aws_s3_bucket" "legacy" {
  bucket = "legacy-bucket"
  versioning {
    enabled = true
  }
}

resource "aws_s3_bucket_policy" "legacy" {
  bucket = "legacy-bucket"
  policy = jsonencode({ Statement = [{ Effect = "Deny", Resource = "${aws_s3_bucket.legacy.arn}/*" }] })
}

resource "aws_s3_bucket_policy" "elsewhere" {
  bucket = "not-in-this-stack"
  policy = "{}"
}

resource "aws_s3_bucket_cors_configuration" "legacy" {
  bucket = aws_s3_bucket.legacy.id
}

resource "aws_sns_topic" "t" {
  name = "events-${var.suffix}"
  # Wrong on purpose: try falls back past the data source.
  display_name = try(data.aws_region.current.name, "fallback")
}

resource "google_storage_bucket" "other" {
  name = "other-cloud"
}

output "vpc_id" {
  value = module.vpc.vpc_id
}

output "dlq_url" {
  value = aws_sqs_queue.dlq.url
}
