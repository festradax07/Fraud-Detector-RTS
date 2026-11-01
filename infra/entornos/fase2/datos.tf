# El dataset en un bucket EFÍMERO: Terraform lo crea y le sube los archivos
# en el apply, y lo borra entero en el destroy. Nada queda cobrando.

resource "aws_s3_bucket" "datos" {
  # bucket_prefix: AWS completa el nombre con algo único (los nombres de
  # bucket son globales en todo S3) sin poner el número de cuenta.
  bucket_prefix = "drts-fase2-datos-"
  # Sin esto, AWS no deja borrar un bucket con archivos y el destroy falla.
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "datos" {
  bucket                  = aws_s3_bucket.datos.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Un objeto por archivo (for_each). etag = el MD5 del archivo local: si el
# archivo cambia, Terraform lo vuelve a subir; si no, no.
resource "aws_s3_object" "dataset" {
  for_each = toset(["fraudTrain.csv", "fraudTest.csv"])

  bucket = aws_s3_bucket.datos.id
  key    = "data/${each.key}"
  source = "${path.module}/${var.ruta_datos}/${each.key}"
  etag   = filemd5("${path.module}/${var.ruta_datos}/${each.key}")
}
