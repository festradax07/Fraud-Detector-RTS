# La red: una VPC con una subred por rol, como en el diagrama (ADR-0023 en
# AWS). Todas son públicas (ruta al Internet Gateway) para no pagar un NAT
# Gateway (~$0,045 por hora más el tráfico): las máquinas necesitan salir a
# internet para bajar Docker, el repo y las imágenes de GHCR. Lo que entra
# lo deciden los security groups (seguridad.tf): desde internet, SOLO el
# puerto de la demo en la máquina del borde. En producción, las subredes de
# aplicación y datos serían privadas, con NAT o endpoints de VPC (ADR-0031).

# El ALB exige subredes en DOS zonas de disponibilidad, aunque los motores
# estén en una. Se eligen dos zonas que ofrezcan el tipo de instancia.
data "aws_ec2_instance_type_offerings" "zonas" {
  location_type = "availability-zone"
  filter {
    name   = "instance-type"
    values = [var.tipo_caliente]
  }
}

locals {
  zonas = slice(sort(data.aws_ec2_instance_type_offerings.zonas.locations), 0, 2)

  # Todas las máquinas en la zona A: una sola zona, como en la notebook, para
  # no sumarle a la medición la latencia entre zonas (es otro experimento).
  # La subred "aplicacion-b" existe solo porque el ALB la exige.
  subredes = {
    borde          = { cidr = "10.30.1.0/24", zona = local.zonas[0] }
    aplicacion     = { cidr = "10.30.2.0/24", zona = local.zonas[0] }
    aplicacion-b   = { cidr = "10.30.12.0/24", zona = local.zonas[1] }
    datos          = { cidr = "10.30.3.0/24", zona = local.zonas[0] }
    observabilidad = { cidr = "10.30.4.0/24", zona = local.zonas[0] }
  }
}

# 10.30 y no el 10.20 de la Fase 1: las dos pueden existir a la vez.
resource "aws_vpc" "drts" {
  cidr_block           = "10.30.0.0/16"
  enable_dns_support   = true # sin esto no resuelve la zona privada drts.internal
  enable_dns_hostnames = true
  tags                 = { Name = "drts-fase2" }
}

resource "aws_internet_gateway" "drts" {
  vpc_id = aws_vpc.drts.id
  tags   = { Name = "drts-fase2" }
}

resource "aws_subnet" "rol" {
  for_each = local.subredes

  vpc_id                  = aws_vpc.drts.id
  cidr_block              = each.value.cidr
  availability_zone       = each.value.zona
  map_public_ip_on_launch = true
  tags                    = { Name = "drts-fase2-${each.key}" }
}

resource "aws_route_table" "publica" {
  vpc_id = aws_vpc.drts.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.drts.id
  }
  tags = { Name = "drts-fase2-publica" }
}

resource "aws_route_table_association" "rol" {
  for_each = aws_subnet.rol

  subnet_id      = each.value.id
  route_table_id = aws_route_table.publica.id
}
