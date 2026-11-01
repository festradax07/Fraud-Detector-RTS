# El balanceador interno delante de los motores (ADR-0030): reparte CADA
# request gRPC entre las réplicas sanas. Los clientes le hablan a un solo
# nombre (motor.drts.internal) y no saben cuántas réplicas hay.

resource "aws_lb" "motor" {
  name               = "drts-fase2-motor"
  internal           = true # solo desde la VPC: lo usan el gateway y el loadgen
  load_balancer_type = "application"
  security_groups    = [aws_security_group.rol["alb"].id]
  # Dos zonas: requisito de AWS para un ALB.
  subnets = [aws_subnet.rol["aplicacion"].id, aws_subnet.rol["aplicacion-b"].id]
}

# Los motores, del lado del ALB. HTTP + GRPC: el ALB les habla gRPC sin TLS
# (h2c), así que el motor no cambia.
resource "aws_lb_target_group" "motor" {
  name             = "drts-fase2-motor"
  vpc_id           = aws_vpc.drts.id
  target_type      = "instance"
  port             = 50051
  protocol         = "HTTP"
  protocol_version = "GRPC"

  # El chequeo estándar de gRPC que el motor ya expone (ADR-0020). Código 0 =
  # OK; el motor contesta NOT_SERVING (error) mientras se apaga.
  health_check {
    path                = "/grpc.health.v1.Health/Check"
    matcher             = "0"
    interval            = 5
    timeout             = 2
    healthy_threshold   = 2
    unhealthy_threshold = 2
  }

  # Al sacar una réplica, el ALB espera esto a que terminen las requests en
  # vuelo. Una transacción dura como mucho 200ms; el margen sobra.
  deregistration_delay = 10
}

resource "aws_lb_target_group_attachment" "motor" {
  for_each = { for k, m in aws_instance.rol : k => m if local.maquinas[k].rol == "motor" }

  target_group_arn = aws_lb_target_group.motor.arn
  target_id        = each.value.id
  port             = 50051
}

# El listener: HTTPS en el 443 (lo exige AWS para gRPC), con el certificado
# de la autoridad propia.
resource "aws_lb_listener" "motor" {
  load_balancer_arn = aws_lb.motor.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = aws_acm_certificate.motor.arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.motor.arn
  }
}
