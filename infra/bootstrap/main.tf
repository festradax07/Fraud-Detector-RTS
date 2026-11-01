# Bootstrap: lo que tiene que existir antes que cualquier entorno, y que
# queda siempre. Se aplica una sola vez, desde la notebook.
#
# Es solo la alarma de presupuesto (gratis): lo primero, antes de crear nada
# que cueste. Todo lo demás (la red, la EC2, el bucket con el dataset) es
# efímero y vive en infra/entornos/fase1: se crea para la sesión y se
# destruye al terminar.
#
# State LOCAL, sin backend: Terraform lo maneja una sola persona, así que no
# hace falta compartir ni bloquear el state (lo que resuelve un bucket). El
# terraform.tfstate queda en esta carpeta, fuera de git (.gitignore), y
# conviene tener un backup.

terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { proyecto = "drts", parte = "bootstrap", gestionado_por = "terraform" }
  }
}

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
