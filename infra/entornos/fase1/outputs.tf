output "ip_publica" {
  description = "IP pública de la máquina (la del QR)."
  value       = aws_instance.drts.public_ip
}

output "url_presentador" {
  description = "Abrir en el proyector: el QR y el gráfico en vivo. Lleva el token: terraform output -raw url_presentador"
  value       = "http://${aws_instance.drts.public_ip}${var.demo_puerto == 80 ? "" : ":${var.demo_puerto}"}/presentador?t=${var.demo_token}"
  sensitive   = true
}

output "instancia" {
  description = "Id de la EC2 (para SSM)."
  value       = aws_instance.drts.id
}

output "entrar" {
  description = "Abrir una consola en la máquina, sin SSH."
  value       = "aws ssm start-session --target ${aws_instance.drts.id}"
}

output "ver_arranque" {
  description = "Seguir el arranque (instalar, construir, levantar: ~10 min). Termina cuando dice LISTO."
  value       = "aws ssm start-session --target ${aws_instance.drts.id} --document-name AWS-StartInteractiveCommand --parameters command='sudo tail -f /var/log/drts-arranque.log'"
}

output "tunel_grafana" {
  description = "Grafana en http://localhost:3000 desde la notebook, por un túnel de SSM (el puerto no se abre a internet)."
  value       = "aws ssm start-session --target ${aws_instance.drts.id} --document-name AWS-StartPortForwardingSession --parameters '{\"portNumber\":[\"3000\"],\"localPortNumber\":[\"3000\"]}'"
}

output "bucket_datos" {
  description = "Bucket efímero con el dataset (se borra en el destroy)."
  value       = aws_s3_bucket.datos.id
}
