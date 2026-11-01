# La zona privada drts.internal: los nombres con los que los servicios se
# encuentran (los mismos que usan los compose por rol). Solo se resuelve
# desde adentro de la VPC. Cuesta $0,50 por mes, pero si se borra dentro de
# las 12 horas de creada no se cobra.

resource "aws_route53_zone" "drts" {
  name = "drts.internal"
  vpc {
    vpc_id = aws_vpc.drts.id
  }
  force_destroy = true
}

# motor → el ALB (registro alias: sigue al ALB aunque cambien sus IPs).
resource "aws_route53_record" "motor" {
  zone_id = aws_route53_zone.drts.zone_id
  name    = "motor.drts.internal"
  type    = "A"
  alias {
    name                   = aws_lb.motor.dns_name
    zone_id                = aws_lb.motor.zone_id
    evaluate_target_health = false
  }
}

# Un nombre por máquina (motor-1, motor-2, datos, frio, borde) y uno por
# servicio (redis, settlement, kafka). Los de servicio permiten mover un
# servicio de máquina sin tocar los compose.
locals {
  nombres = merge(
    { for k, m in aws_instance.rol : k => m.private_ip },
    {
      redis      = aws_instance.rol["datos"].private_ip
      settlement = aws_instance.rol["datos"].private_ip
      kafka      = aws_instance.rol["frio"].private_ip
    },
  )
}

resource "aws_route53_record" "maquina" {
  for_each = local.nombres

  zone_id = aws_route53_zone.drts.zone_id
  name    = "${each.key}.drts.internal"
  type    = "A"
  ttl     = 10
  records = [each.value]
}
