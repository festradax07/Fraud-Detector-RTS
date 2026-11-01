# Fase 2: el sistema DISTRIBUIDO. Cada rol en su máquina (borde, dos motores,
# datos, frío), los motores detrás de un ALB interno (ADR-0030) y los
# servicios encontrándose por DNS privado. Efímero, como la Fase 1: se crea
# para la sesión y se destruye al terminar. State local (una sola persona
# maneja Terraform).
#
# Diferencias con la Fase 1: ninguna máquina construye imágenes (las baja de
# GHCR, publicadas por GitHub Actions) y cada una levanta solo su parte
# (compose/<rol>.yml).

terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
    # Genera la autoridad certificante propia y el certificado del ALB, sin
    # dominio ni autoridad pública (ADR-0030).
    tls = {
      source  = "hashicorp/tls"
      version = ">= 4.0"
    }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { proyecto = "drts", entorno = "fase2", gestionado_por = "terraform" }
  }
}
