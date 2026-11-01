# Quién puede entrar y qué puede hacer la máquina.

# Security group: el firewall de la instancia. Entra SOLO la demo; no hay
# SSH (se entra por SSM). Grafana y Prometheus no se abren: escuchan en
# localhost y se ven con un túnel de SSM (ver README.md).
resource "aws_security_group" "demo" {
  name        = "drts-fase1-demo"
  description = "Solo la demo (HTTP). Sin SSH: acceso por SSM."
  vpc_id      = aws_vpc.drts.id
}

resource "aws_vpc_security_group_ingress_rule" "demo" {
  security_group_id = aws_security_group.demo.id
  description       = "Demo con QR (gateway)"
  ip_protocol       = "tcp"
  from_port         = var.demo_puerto
  to_port           = var.demo_puerto
  cidr_ipv4         = var.cidr_demo
}

# Salida libre: la instancia baja paquetes, el repo, las imágenes base y el
# dataset (S3), y habla con SSM.
resource "aws_vpc_security_group_egress_rule" "todo" {
  security_group_id = aws_security_group.demo.id
  description       = "Salida a internet"
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# Rol de IAM de la instancia: credenciales temporales que AWS le da a la
# máquina (por IMDSv2). Ninguna clave guardada en la instancia.
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
  name               = "drts-fase1-instancia"
  assume_role_policy = data.aws_iam_policy_document.asumir_ec2.json
}

# SSM: entrar a la máquina y abrir túneles sin SSH ni puertos abiertos.
resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.instancia.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# Mínimo privilegio: solo LEER el bucket del dataset de esta sesión.
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
  name = "drts-fase1-instancia"
  role = aws_iam_role.instancia.name
}
