# Quién le puede hablar a quién: un security group por rol, y una regla por
# flujo permitido. Es la matriz de redes del Compose (ADR-0023) traducida a
# AWS: en una sola máquina la imponían las redes de Docker; acá, como cada
# contenedor usa la red de su máquina, la imponen los security groups.
#
#   origen            → destino   puerto   para qué
#   internet          → borde     demo     la demo con QR (el gateway)
#   borde             → alb       443      gRPC con TLS hacia los motores
#   alb               → motor     50051    requests y health check
#   motor             → datos     6379     estado de detección (Redis)
#   motor             → datos     50052    liquidar (Settlement)
#   frio              → datos     6379     alertas y estadísticas en sombra
#   motor, datos      → frio      9092     publicar eventos (Kafka)
#   frio (Prometheus) → todos     211x     /metrics
#
# Sin SSH en ninguna: se entra por SSM.

locals {
  roles = toset(["borde", "alb", "motor", "datos", "frio"])

  # Una regla por flujo. "desde" es otro security group; "cidr", una red.
  reglas = {
    demo                = { sg = "borde", puerto = var.demo_puerto, cidr = var.cidr_demo, desc = "Demo con QR (gateway)" }
    borde-alb           = { sg = "alb", puerto = 443, desde = "borde", desc = "gRPC con TLS hacia los motores" }
    alb-motor           = { sg = "motor", puerto = 50051, desde = "alb", desc = "Requests y health check del ALB" }
    motor-redis         = { sg = "datos", puerto = 6379, desde = "motor", desc = "Redis: estado de deteccion" }
    motor-settlement    = { sg = "datos", puerto = 50052, desde = "motor", desc = "Settlement: liquidar" }
    frio-redis          = { sg = "datos", puerto = 6379, desde = "frio", desc = "Redis: alertas y estadisticas" }
    motor-kafka         = { sg = "frio", puerto = 9092, desde = "motor", desc = "Kafka: eventos del motor" }
    datos-kafka         = { sg = "frio", puerto = 9092, desde = "datos", desc = "Kafka: eventos de Settlement" }
    metricas-motor      = { sg = "motor", puerto = 2112, desde = "frio", desc = "Prometheus: motor" }
    metricas-settlement = { sg = "datos", puerto = 2113, desde = "frio", desc = "Prometheus: Settlement" }
    metricas-loadgen    = { sg = "borde", puerto = 2114, desde = "frio", desc = "Prometheus: loadgen" }
    metricas-gateway    = { sg = "borde", puerto = 2116, desde = "frio", desc = "Prometheus: gateway" }
  }
}

resource "aws_security_group" "rol" {
  for_each = local.roles

  name        = "drts-fase2-${each.key}"
  description = "Rol ${each.key} de la Fase 2. Sin SSH: acceso por SSM."
  vpc_id      = aws_vpc.drts.id
  tags        = { Name = "drts-fase2-${each.key}" }
}

resource "aws_vpc_security_group_ingress_rule" "flujo" {
  for_each = local.reglas

  security_group_id            = aws_security_group.rol[each.value.sg].id
  description                  = each.value.desc
  ip_protocol                  = "tcp"
  from_port                    = each.value.puerto
  to_port                      = each.value.puerto
  cidr_ipv4                    = lookup(each.value, "cidr", null)
  referenced_security_group_id = can(each.value.desde) ? aws_security_group.rol[each.value.desde].id : null
}

# Salida libre: las máquinas bajan paquetes, el repo y las imágenes, y
# hablan con SSM y S3. (Lo que importa es quién ENTRA.)
resource "aws_vpc_security_group_egress_rule" "todo" {
  for_each = local.roles

  security_group_id = aws_security_group.rol[each.key].id
  description       = "Salida"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# Rol de IAM de las máquinas: el mismo para todas (SSM y leer el dataset).
data "aws_iam_policy_document" "asumir_ec2" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "instancia" {
  name               = "drts-fase2-instancia"
  assume_role_policy = data.aws_iam_policy_document.asumir_ec2.json
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.instancia.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

data "aws_iam_policy_document" "leer_datos" {
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.datos.arn]
  }
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.datos.arn}/*"]
  }
}

resource "aws_iam_role_policy" "leer_datos" {
  name   = "leer-dataset"
  role   = aws_iam_role.instancia.id
  policy = data.aws_iam_policy_document.leer_datos.json
}

resource "aws_iam_instance_profile" "instancia" {
  name = "drts-fase2-instancia"
  role = aws_iam_role.instancia.name
}
