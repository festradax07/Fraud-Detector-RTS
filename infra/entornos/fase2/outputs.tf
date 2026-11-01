output "url_presentador" {
  description = "Abrir en el proyector: el QR y el gráfico en vivo. Lleva el token: terraform output -raw url_presentador"
  value       = "http://${aws_instance.rol["borde"].public_ip}${var.demo_puerto == 80 ? "" : ":${var.demo_puerto}"}/presentador?t=${var.demo_token}"
  sensitive   = true
}

output "instancias" {
  description = "Id de cada máquina (para SSM)."
  value       = { for k, m in aws_instance.rol : k => m.id }
}

output "ips_privadas" {
  description = "IP privada de cada máquina (las que resuelve drts.internal)."
  value       = { for k, m in aws_instance.rol : k => m.private_ip }
}

output "entrar" {
  description = "Abrir una consola en una máquina, sin SSH: eval \"$(terraform output -json entrar | jq -r .borde)\""
  value       = { for k, m in aws_instance.rol : k => "aws ssm start-session --target ${m.id}" }
}

output "ver_arranque" {
  description = "Seguir el arranque de una máquina (termina cuando dice LISTO)."
  value = { for k, m in aws_instance.rol : k =>
    "aws ssm start-session --target ${m.id} --document-name AWS-StartInteractiveCommand --parameters command='sudo tail -f /var/log/drts-arranque.log'"
  }
}

output "tunel_grafana" {
  description = "Grafana (máquina frio) en http://localhost:3000 desde la notebook, por un túnel de SSM."
  value       = "aws ssm start-session --target ${aws_instance.rol["frio"].id} --document-name AWS-StartPortForwardingSession --parameters '{\"portNumber\":[\"3000\"],\"localPortNumber\":[\"3000\"]}'"
}

output "salud_motores" {
  description = "Estado de los motores según el health check del ALB."
  value       = "aws elbv2 describe-target-health --target-group-arn ${aws_lb_target_group.motor.arn} --query 'TargetHealthDescriptions[].[Target.Id,TargetHealth.State]' --output table"
}
