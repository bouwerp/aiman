variable "from_default" {
  default = "default"
}

variable "from_tfvars" {
  default = "default"
}

variable "from_tfvars_json" {
  default = "default"
}

variable "from_auto_a" {
  default = "default"
}

variable "from_auto_b" {
  default = "default"
}

variable "from_var_file" {
  default = "default"
}

variable "from_vars" {
  default = "default"
}

variable "replicas" {
  type    = number
  default = 1
}

variable "secret" {
  type      = string
  sensitive = true
}

locals {
  all = join(" ", [var.from_default, var.from_tfvars, var.from_tfvars_json, var.from_auto_a, var.from_auto_b, var.from_var_file, var.from_vars])
}

resource "aws_sqs_queue" "q" {
  count       = var.replicas
  name_prefix = "q${count.index}-"
}

output "all" {
  value = local.all
}

output "secret" {
  value     = "token-${var.secret}"
  sensitive = true
}

output "leaks" {
  value = var.secret
}

output "math" {
  value = max(var.replicas, 5) + length("abc")
}
