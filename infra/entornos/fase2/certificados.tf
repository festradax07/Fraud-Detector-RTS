# TLS del ALB con una autoridad certificante PROPIA (ADR-0030): el ALB solo
# acepta gRPC por HTTPS, pero quienes le hablan son nuestros servicios
# (gateway y loadgen), no navegadores. No hace falta un dominio ni una
# autoridad pública: los clientes confían solo en esta autoridad y verifican
# el nombre motor.drts.internal.
#
# Las claves privadas quedan en el state local de Terraform (que no se
# versiona). Todo es efímero: se genera en el apply y desaparece en el
# destroy.

# 1. La autoridad: una clave y un certificado autofirmado que puede firmar
#    otros certificados.
resource "tls_private_key" "ca" {
  algorithm   = "ECDSA"
  ecdsa_curve = "P256"
}

resource "tls_self_signed_cert" "ca" {
  private_key_pem       = tls_private_key.ca.private_key_pem
  is_ca_certificate     = true
  validity_period_hours = 24 * 30
  allowed_uses          = ["cert_signing", "crl_signing"]
  subject {
    common_name  = "D-RTS CA (fase2)"
    organization = "D-RTS"
  }
}

# 2. El certificado del ALB, para motor.drts.internal, firmado por la
#    autoridad.
resource "tls_private_key" "motor" {
  algorithm   = "ECDSA"
  ecdsa_curve = "P256"
}

resource "tls_cert_request" "motor" {
  private_key_pem = tls_private_key.motor.private_key_pem
  dns_names       = ["motor.drts.internal"]
  subject {
    common_name = "motor.drts.internal"
  }
}

resource "tls_locally_signed_cert" "motor" {
  cert_request_pem      = tls_cert_request.motor.cert_request_pem
  ca_private_key_pem    = tls_private_key.ca.private_key_pem
  ca_cert_pem           = tls_self_signed_cert.ca.cert_pem
  validity_period_hours = 24 * 30
  # Si quedan menos de 7 días, el próximo apply lo renueva.
  early_renewal_hours = 24 * 7
  allowed_uses        = ["digital_signature", "key_encipherment", "server_auth"]
}

# 3. Importado en ACM (gratis para certificados importados): el listener
#    HTTPS del ALB lo usa.
resource "aws_acm_certificate" "motor" {
  private_key       = tls_private_key.motor.private_key_pem
  certificate_body  = tls_locally_signed_cert.motor.cert_pem
  certificate_chain = tls_self_signed_cert.ca.cert_pem
  tags              = { Name = "drts-fase2-motor" }

  lifecycle {
    create_before_destroy = true # al renovarlo, el listener no queda sin certificado
  }
}
