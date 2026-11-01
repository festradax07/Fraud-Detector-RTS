variable "region" {
  description = "Región de AWS."
  type        = string
  default     = "us-east-1"
}

variable "drts_tag" {
  description = "Etiqueta de las imágenes de GHCR que se despliegan: sha-<commit> (la publica GitHub Actions). Fija: así se sabe exactamente qué código corre."
  type        = string

  validation {
    condition     = can(regex("^sha-[0-9a-f]{7,}$", var.drts_tag))
    error_message = "Tiene que ser una etiqueta fija, sha-<commit> (por ejemplo sha-caf2c7e), no una rama: una rama se mueve."
  }
}

variable "git_ref" {
  description = "Rama o tag del repo que clonan las máquinas (los compose por rol y la configuración de Grafana). El mismo commit que drts_tag."
  type        = string
  default     = "main"
}

variable "tipo_caliente" {
  description = "EC2 del camino de decisión (borde, motores, datos). x86 y sin créditos de CPU: nunca t2/t3/t4g para medir. c7i.large: 2 vCPU, 4 GB."
  type        = string
  default     = "c7i.large"
}

variable "tipo_frio" {
  description = "EC2 del cold path (Kafka, consumidores, Prometheus, Grafana). Fuera del camino de decisión: puede ser burstable. t3.large: 2 vCPU, 8 GB."
  type        = string
  default     = "t3.large"
}

variable "demo_token" {
  description = "Token de la demo: va en el QR y lo exige el gateway. Privado: en terraform.tfvars, que no se versiona."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.demo_token) >= 12 && can(regex("^[A-Za-z0-9-]+$", var.demo_token))
    error_message = "El token tiene que tener al menos 12 caracteres (letras, números o guiones): va en una URL pública."
  }
}

variable "demo_puerto" {
  description = "Puerto público de la demo (HTTP)."
  type        = number
  default     = 80
}

variable "cidr_demo" {
  description = "Desde dónde se puede entrar a la demo. Por defecto, cualquiera (los celulares de la clase); el token y los límites del gateway la protegen."
  type        = string
  default     = "0.0.0.0/0"
}

variable "repo_url" {
  description = "Repositorio (público) que clonan las máquinas."
  type        = string
  default     = "https://github.com/festradax07/Fraud-Detector-RTS.git"
}

variable "ruta_datos" {
  description = "Carpeta local con el dataset (fraudTrain.csv y fraudTest.csv), relativa a esta carpeta."
  type        = string
  default     = "../../../data"
}

variable "horas_maximas" {
  description = "Red de seguridad de costo: cada máquina se apaga y se borra sola después de estas horas, por si se olvida el destroy. 0 = desactivado."
  type        = number
  default     = 4
}
