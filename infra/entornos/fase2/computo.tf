# Las máquinas: una por rol, on-demand (si AWS reclamara una spot en medio de
# la presentación, no habría demo). Todas con la misma base (Ubuntu, Docker)
# y el mismo script de arranque; lo que cambia es qué compose levantan.

locals {
  maquinas = {
    borde   = { rol = "borde", tipo = var.tipo_caliente, subred = "borde", disco = 20 }
    motor-1 = { rol = "motor", tipo = var.tipo_caliente, subred = "aplicacion", disco = 20 }
    motor-2 = { rol = "motor", tipo = var.tipo_caliente, subred = "aplicacion", disco = 20 }
    # Escalado manual en vivo (demo): descomentar y terraform apply suma una
    # réplica detrás del ALB en ~5 minutos.
    motor-3 = { rol = "motor", tipo = var.tipo_caliente, subred = "aplicacion", disco = 20 }
    datos   = { rol = "datos", tipo = var.tipo_caliente, subred = "datos", disco = 20 }
    frio    = { rol = "frio", tipo = var.tipo_frio, subred = "observabilidad", disco = 30 }
  }
}

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

resource "aws_instance" "rol" {
  for_each = local.maquinas

  ami                    = data.aws_ami.ubuntu.id
  instance_type          = each.value.tipo
  subnet_id              = aws_subnet.rol[each.value.subred].id
  vpc_security_group_ids = [aws_security_group.rol[each.value.rol].id]
  iam_instance_profile   = aws_iam_instance_profile.instancia.name

  instance_initiated_shutdown_behavior = var.horas_maximas > 0 ? "terminate" : "stop"

  user_data = templatefile("${path.module}/arranque.sh.tftpl", {
    rol           = each.value.rol
    nombre        = each.key
    repo_url      = var.repo_url
    git_ref       = var.git_ref
    drts_tag      = var.drts_tag
    bucket        = aws_s3_bucket.datos.id
    horas_maximas = var.horas_maximas
    demo_puerto   = var.demo_puerto
    # Solo el borde recibe el token de la demo y la autoridad del ALB (la
    # autoridad es un certificado PÚBLICO: no es secreto, es en quién
    # confiar).
    demo_token = each.value.rol == "borde" ? var.demo_token : ""
    ca_pem     = each.value.rol == "borde" ? tls_self_signed_cert.ca.cert_pem : ""
  })
  user_data_replace_on_change = true

  # IMDSv2 obligatorio. Ojo: con red host, los contenedores comparten la red
  # de la máquina y SÍ llegan a las credenciales del rol (el hop limit no los
  # frena). El rol solo permite SSM y leer el dataset de esta sesión.
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = each.value.disco
    encrypted   = true
  }

  depends_on = [aws_s3_object.dataset, aws_iam_role_policy.leer_datos]

  tags = { Name = "drts-fase2-${each.key}", rol = each.value.rol }
}
