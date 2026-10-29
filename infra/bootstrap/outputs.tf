output "bucket_state" {
  description = "Bucket del state de Terraform (para el backend de cada entorno y para migrar el state del bootstrap)."
  value       = aws_s3_bucket.state.bucket
}

output "bucket_datos" {
  description = "Bucket de datos: ahí va el dataset para que la EC2 lo baje al arrancar."
  value       = aws_s3_bucket.datos.bucket
}
