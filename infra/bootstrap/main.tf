# Bootstrap: lo que tiene que existir ANTES que cualquier entorno, y que se
# aplica una sola vez, desde la notebook (ADR-0030).
#
#   1. La alarma de presupuesto: lo primero, antes de crear nada que cueste.
#   2. El bucket del state de Terraform: privado, versionado y cifrado.
#   3. El bucket de datos: el dataset (~500MB, no está en git) para la EC2.
#
# El huevo y la gallina: este state arranca local (no hay bucket todavía) y
# después se migra al bucket que crea (ver README.md de esta carpeta).

terraform {
  required_version = ">= 1.10" # use_lockfile: el lock del state en S3, sin DynamoDB

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }

  # Después de la primera aplicación, descomentar y correr
  # `terraform init -migrate-state -backend-config=backend.hcl`.
  # El bucket y la key van en backend.hcl (no se versiona: el nombre del
  # bucket lleva el número de cuenta, y el repo es público). Ver README.md.
  # backend "s3" {}
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { proyecto = "drts", parte = "bootstrap", gestionado_por = "terraform" }
  }
}

# La cuenta: los nombres de bucket son globales en todo S3, así que llevan el
# número de cuenta para no chocar con los de nadie más.
data "aws_caller_identity" "actual" {}

locals {
  cuenta = data.aws_caller_identity.actual.account_id
}

# ------------------------------------------------------------ presupuesto

resource "aws_budgets_budget" "mensual" {
  name         = "drts-mensual"
  budget_type  = "COST"
  limit_amount = var.presupuesto_usd
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  # Gasto real: avisa al 50% y al 80% del tope.
  dynamic "notification" {
    for_each = [50, 80]
    content {
      comparison_operator        = "GREATER_THAN"
      threshold                  = notification.value
      threshold_type             = "PERCENTAGE"
      notification_type          = "ACTUAL"
      subscriber_email_addresses = [var.email_alertas]
    }
  }

  # Pronóstico: avisa si al ritmo actual el mes va a pasar el tope (sirve
  # para darse cuenta de algo que quedó prendido).
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.email_alertas]
  }
}

# ----------------------------------------------------------------- buckets

resource "aws_s3_bucket" "state" {
  bucket = "drts-tfstate-${local.cuenta}"
  # Sin este bucket Terraform no sabe qué creó ni qué destruir.
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket" "datos" {
  bucket = "drts-datos-${local.cuenta}"
}

# Versionado del state: si un apply lo deja mal, se recupera la versión
# anterior.
resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

# Los dos buckets: privados (sin acceso público de ninguna forma) y
# cifrados en reposo.
resource "aws_s3_bucket_public_access_block" "privado" {
  for_each = { state = aws_s3_bucket.state.id, datos = aws_s3_bucket.datos.id }

  bucket                  = each.value
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "cifrado" {
  for_each = { state = aws_s3_bucket.state.id, datos = aws_s3_bucket.datos.id }

  bucket = each.value
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Las versiones viejas del state no tienen que quedar para siempre.
resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    id     = "versiones-viejas"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = 90
    }
  }
}
