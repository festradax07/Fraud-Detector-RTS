# La red: una VPC con una subred pública. Es la Fase 1 (una sola máquina);
# las cuatro subredes del diagrama (borde, aplicación, datos,
# observabilidad) son de la Fase 2.

# No todas las zonas ofrecen todos los tipos de instancia (c7i, por
# ejemplo, no está en todas): se elige una que lo tenga.
data "aws_ec2_instance_type_offerings" "zonas" {
  location_type = "availability-zone"
  filter {
    name   = "instance-type"
    values = [var.tipo_instancia]
  }
}

locals {
  zona = sort(data.aws_ec2_instance_type_offerings.zonas.locations)[0]
}

resource "aws_vpc" "drts" {
  cidr_block           = "10.20.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = "drts-fase1" }
}

# La puerta a internet: sin esto, nada entra ni sale de la VPC.
resource "aws_internet_gateway" "drts" {
  vpc_id = aws_vpc.drts.id
  tags   = { Name = "drts-fase1" }
}

# Subred pública: las instancias reciben una IP pública (la del QR). Sin NAT
# Gateway (~$33/mes): la salida a internet (Docker, el repo) va directo por
# el Internet Gateway.
resource "aws_subnet" "publica" {
  vpc_id                  = aws_vpc.drts.id
  cidr_block              = "10.20.1.0/24"
  availability_zone       = local.zona
  map_public_ip_on_launch = true
  tags                    = { Name = "drts-fase1-publica" }
}

# La tabla de rutas: todo lo que no es de la VPC sale por el Internet Gateway.
resource "aws_route_table" "publica" {
  vpc_id = aws_vpc.drts.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.drts.id
  }
  tags = { Name = "drts-fase1-publica" }
}

resource "aws_route_table_association" "publica" {
  subnet_id      = aws_subnet.publica.id
  route_table_id = aws_route_table.publica.id
}
