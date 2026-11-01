variable "region" {
  description = "Región de AWS."
  type        = string
  default     = "us-east-1"
}

variable "tipo_instancia" {
  description = "Tipo de EC2. x86 (la misma imagen que en la notebook) y sin créditos de CPU (nunca t2/t3/t4g para medir). c7i.xlarge: 4 vCPU, 8 GB."
  type        = string
  default     = "c7i.xlarge"
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

variable "git_ref" {
  description = "Rama o tag del repo que se despliega."
  type        = string
  default     = "main"
}

variable "repo_url" {
  description = "Repositorio (público) que clona la EC2."
  type        = string
  default     = "https://github.com/festradax07/Fraud-Detector-RTS.git"
}

variable "ruta_datos" {
  description = "Carpeta local con el dataset (fraudTrain.csv y fraudTest.csv), relativa a esta carpeta."
  type        = string
  default     = "../../../data"
}

variable "horas_maximas" {
  description = "Red de seguridad de costo: la instancia se apaga y se borra sola después de estas horas, por si se olvida el destroy. 0 = desactivado."
  type        = number
  default     = 4
}
