# Fase 1: el sistema completo (el mismo Docker Compose de la notebook) en
# UNA EC2, más la demo con QR. Efímero: se crea para la sesión y se destruye
# al terminar (terraform destroy). State local, como el bootstrap (una sola
# persona maneja Terraform).

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
    tags = { proyecto = "drts", entorno = "fase1", gestionado_por = "terraform" }
  }
}
