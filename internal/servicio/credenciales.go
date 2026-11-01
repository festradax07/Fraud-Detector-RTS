package servicio

import (
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// CredencialesCliente elige cómo se conecta un cliente al motor:
//
//   - rutaCA vacía: sin TLS. Es el caso local (binarios o Docker Compose),
//     donde el cliente le habla directo al motor.
//   - rutaCA con un certificado PEM: TLS, confiando SOLO en esa autoridad
//     certificante. Es el caso de AWS, donde en el medio hay un balanceador
//     (ALB) que reparte las requests entre las réplicas del motor, y el ALB
//     solo acepta gRPC por HTTPS. La autoridad es nuestra (la genera
//     Terraform): no hace falta un dominio ni una autoridad pública, porque
//     del otro lado no hay navegadores sino nuestros propios servicios.
//
// El balanceo es de la infraestructura (ADR-0030): el cliente abre una sola
// conexión al nombre del balanceador y no sabe cuántas réplicas hay detrás.
func CredencialesCliente(rutaCA string) (grpc.DialOption, error) {
	if rutaCA == "" {
		return grpc.WithTransportCredentials(insecure.NewCredentials()), nil
	}
	pem, err := os.ReadFile(rutaCA)
	if err != nil {
		return nil, fmt.Errorf("leer la autoridad certificante: %w", err)
	}
	confiables := x509.NewCertPool()
	if !confiables.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s no tiene ningún certificado PEM válido", rutaCA)
	}
	// "" = verificar el certificado contra el nombre del destino (por
	// ejemplo motor.drts.internal), como hace un navegador.
	return grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(confiables, "")), nil
}
