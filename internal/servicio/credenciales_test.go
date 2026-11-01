package servicio

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// autoridad arma una autoridad certificante propia y un certificado firmado
// por ella para "localhost" (lo mismo que hace Terraform para el ALB, con
// motor.drts.internal). Devuelve la ruta del PEM de la autoridad y el
// certificado del servidor.
func autoridad(t *testing.T) (string, tls.Certificate) {
	t.Helper()
	claveCA, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	plantillaCA := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "drts prueba CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	derCA, err := x509.CreateCertificate(rand.Reader, plantillaCA, plantillaCA, &claveCA.PublicKey, claveCA)
	if err != nil {
		t.Fatalf("autoridad: %v", err)
	}
	ca, _ := x509.ParseCertificate(derCA)

	clave, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	plantilla := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, plantilla, ca, &clave.PublicKey, claveCA)
	if err != nil {
		t.Fatalf("certificado: %v", err)
	}

	ruta := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ruta, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derCA}), 0o600); err != nil {
		t.Fatalf("escribir ca.pem: %v", err)
	}
	return ruta, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: clave}
}

// servirTLS levanta un servidor gRPC con TLS (el papel del ALB) y devuelve
// su puerto.
func servirTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer(grpc.Creds(credentials.NewServerTLSFromCert(&cert)))
	healthpb.RegisterHealthServer(g, health.NewServer())
	go g.Serve(lis)
	t.Cleanup(g.Stop)
	_, puerto, _ := net.SplitHostPort(lis.Addr().String())
	return puerto
}

func llamar(t *testing.T, destino, rutaCA string) error {
	t.Helper()
	cred, err := CredencialesCliente(rutaCA)
	if err != nil {
		t.Fatalf("credenciales: %v", err)
	}
	conn, err := grpc.NewClient(destino, cred)
	if err != nil {
		t.Fatalf("cliente: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

// Con la autoridad propia, el cliente se conecta por TLS a un servidor cuyo
// certificado firmó esa autoridad.
func TestCredencialesClienteConAutoridadPropia(t *testing.T) {
	rutaCA, cert := autoridad(t)
	puerto := servirTLS(t, cert)
	if err := llamar(t, "localhost:"+puerto, rutaCA); err != nil {
		t.Fatalf("con la autoridad propia: %v", err)
	}
}

// TLS de verdad: si el nombre del destino no está en el certificado, el
// cliente se niega (no es "TLS sin verificar").
func TestCredencialesClienteRechazaOtroNombre(t *testing.T) {
	rutaCA, cert := autoridad(t)
	puerto := servirTLS(t, cert)
	if err := llamar(t, "127.0.0.1:"+puerto, rutaCA); err == nil {
		t.Fatal("se conectó a un nombre que no está en el certificado")
	}
}

// Sin autoridad (local), el cliente no usa TLS: contra un servidor TLS falla.
func TestCredencialesClienteSinAutoridadEsSinTLS(t *testing.T) {
	_, cert := autoridad(t)
	puerto := servirTLS(t, cert)
	if err := llamar(t, "localhost:"+puerto, ""); err == nil {
		t.Fatal("sin autoridad se conectó a un servidor TLS")
	}
}

func TestCredencialesClienteArchivoInvalido(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ruta, []byte("no es un certificado"), 0o600)
	if _, err := CredencialesCliente(ruta); err == nil {
		t.Fatal("aceptó un archivo sin certificados")
	}
}
