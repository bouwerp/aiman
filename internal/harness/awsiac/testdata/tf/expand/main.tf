variable "workers" {
  type    = list(string)
  default = ["a", "b"]
}

variable "tables" {
  type = map(object({
    hash_key = string
    sort_key = optional(string)
    indexes  = optional(list(string), [])
  }))
  default = {
    users = { hash_key = "id" }
    events = {
      hash_key = "stream"
      sort_key = "at"
      indexes  = ["kind", "owner"]
    }
  }
}

locals {
  attrs = {
    for name, t in var.tables : name => distinct(compact(concat([t.hash_key], t.sort_key == null ? [] : [t.sort_key], t.indexes)))
  }
}

data "aws_availability_zones" "all" {}

resource "aws_sqs_queue" "work" {
  count      = length(var.workers)
  name       = "work-${var.workers[count.index]}"
  fifo_queue = false
}

resource "aws_sqs_queue" "per_zone" {
  for_each = toset(data.aws_availability_zones.all.names)
  name     = "zone-${each.key}"
}

resource "aws_dynamodb_table" "t" {
  for_each     = var.tables
  name         = each.key
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = each.value.hash_key
  range_key    = each.value.sort_key

  dynamic "attribute" {
    for_each = concat([each.value.hash_key], each.value.sort_key == null ? [] : [each.value.sort_key], each.value.indexes)
    content {
      name = attribute.value
      type = "S"
    }
  }

  dynamic "global_secondary_index" {
    for_each = each.value.indexes
    iterator = ix
    content {
      name            = "by-${ix.value}"
      hash_key        = ix.value
      projection_type = "ALL"
    }
  }
}

resource "aws_sns_topic" "fanout" {
  name       = "fanout"
  depends_on = [aws_sqs_queue.work]
}

resource "aws_sns_topic_subscription" "work" {
  count     = length(aws_sqs_queue.work)
  topic_arn = aws_sns_topic.fanout.arn
  protocol  = "sqs"
  endpoint  = aws_sqs_queue.work[count.index].arn
}

module "buckets" {
  source   = "./modules/bucket"
  for_each = { logs = "Suspended", assets = "Enabled" }
  name     = each.key
  status   = each.value
}

output "work_urls" {
  value = join(",", aws_sqs_queue.work[*].url)
}

output "bucket_arns" {
  value = { for k, m in module.buckets : k => m.arn }
}

output "table_names" {
  value = [for t in aws_dynamodb_table.t : t.name]
}
