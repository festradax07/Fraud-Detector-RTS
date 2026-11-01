# La máquina: una EC2 on-demand con el sistema completo en Docker Compose.
# On-demand y no spot: si AWS reclamara una instancia spot en medio de la
# presentación, no habría demo.

# Ubuntu 24.04 LTS, la imagen oficial de Canonical más reciente (trae el
# agente de SSM instalado).
data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

resource "aws_instance" "drts" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.tipo_instancia
  subnet_id              = aws_subnet.publica.id
  vpc_security_group_ids = [aws_security_group.demo.id]
  iam_instance_profile   = aws_iam_instance_profile.instancia.name

  # Red de seguridad de costo: el script de arranque programa un apagado a
  # las horas_maximas, y apagarse = borrarse (deja de cobrar).
  instance_initiated_shutdown_behavior = var.horas_maximas > 0 ? "terminate" : "stop"

  # Lo que corre la instancia al arrancar (una sola vez): Docker, el repo,
  # el dataset y la demo. Si cambia, la instancia se reemplaza.
  user_data = templatefile("${path.module}/arranque.sh.tftpl", {
    repo_url      = var.repo_url
    git_ref       = var.git_ref
    bucket        = aws_s3_bucket.datos.id
    demo_token    = var.demo_token
    demo_puerto   = var.demo_puerto
    horas_maximas = var.horas_maximas
  })
  user_data_replace_on_change = true

  # IMDSv2 obligatorio: las credenciales temporales del rol solo se piden con
  # un token de sesión (frena ataques que engañan a la máquina para leerlas).
  # hop limit 1: los contenedores no llegan a esas credenciales.
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = 30 # imágenes de Docker, Kafka, el dataset (~500MB)
    encrypted   = true
  }

  # El script baja el dataset y usa el rol al arrancar: tienen que existir
  # antes. (El bucket ya es una dependencia implícita; los archivos y el
  # permiso de lectura, no.)
  depends_on = [aws_s3_object.dataset, aws_iam_role_policy.leer_datos]

  tags = { Name = "drts-fase1" }
}
