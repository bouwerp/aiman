variable "name" {
  type = string
}

variable "max_receive_count" {
  type    = number
  default = 3

  validation {
    condition     = var.max_receive_count > 0
    error_message = "must be positive"
  }
}

resource "aws_sqs_queue" "dlq" {
  name = "${var.name}-dlq"
}

resource "aws_sqs_queue" "main" {
  name                       = var.name
  visibility_timeout_seconds = 60
  kms_master_key_id          = "alias/aws/sqs"
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = var.max_receive_count
  })
}

resource "aws_sqs_queue_policy" "main" {
  queue_url = aws_sqs_queue.main.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = "*"
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.main.arn
    }]
  })
}

output "arn" {
  value = aws_sqs_queue.main.arn
}

output "url" {
  value = aws_sqs_queue.main.url
}
