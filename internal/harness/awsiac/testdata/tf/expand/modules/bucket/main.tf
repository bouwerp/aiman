variable "name" {
  type = string
}

variable "status" {
  type = string
}

resource "aws_s3_bucket" "this" {
  bucket_prefix = "${var.name}-"
}

resource "aws_s3_bucket_versioning" "this" {
  bucket = aws_s3_bucket.this.id
  versioning_configuration {
    status = var.status
  }
}

output "arn" {
  value = aws_s3_bucket.this.arn
}
