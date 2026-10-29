variable "region" {
  description = "Región de AWS. us-east-1: la más barata, y como el loadgen también corre en AWS, la distancia desde Argentina no influye en lo que se mide."
  type        = string
  default     = "us-east-1"
}

variable "presupuesto_usd" {
  description = "Tope mensual de gasto (USD) para la alarma de presupuesto. Avisa al 50% y al 80%, y si el pronóstico del mes lo supera."
  type        = string
  default     = "10"
}

variable "email_alertas" {
  description = "Mail que recibe las alertas de presupuesto. Sin valor por defecto: se pasa al aplicar (o en un terraform.tfvars, que no se versiona)."
  type        = string
}
