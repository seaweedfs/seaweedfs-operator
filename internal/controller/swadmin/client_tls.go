package swadmin

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ClientTLSDialOption builds mutual-TLS transport credentials for gRPC
// connections to a Seaweed cluster, from in-memory PEM material. The operator
// reads the material from the cluster's server TLS Secret via the Kubernetes
// API rather than mounted files, so it cannot reuse
// weed/security.LoadClientTLS directly; this mirrors its semantics instead:
// the client presents cert/key, the server chain is verified against ca, and
// hostname verification is skipped — SeaweedFS components share one
// certificate that is not minted per dial target.
func ClientTLSDialOption(ca, cert, key []byte) (grpc.DialOption, error) {
	clientCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("no CA certificates parsed from ca.crt")
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		MinVersion:   tls.VersionTLS12,
		// Chain verification happens in VerifyPeerCertificate so the
		// hostname check can be skipped without skipping verification.
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: verifyChainOnly(roots),
	}
	return grpc.WithTransportCredentials(credentials.NewTLS(cfg)), nil
}

// verifyChainOnly returns a VerifyPeerCertificate callback that verifies the
// presented chain against roots without a DNSName constraint.
func verifyChainOnly(roots *x509.CertPool) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("server presented no certificate")
		}
		certs := make([]*x509.Certificate, 0, len(rawCerts))
		for _, raw := range rawCerts {
			c, err := x509.ParseCertificate(raw)
			if err != nil {
				return fmt.Errorf("parse server certificate: %w", err)
			}
			certs = append(certs, c)
		}
		intermediates := x509.NewCertPool()
		for _, c := range certs[1:] {
			intermediates.AddCert(c)
		}
		_, err := certs[0].Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		return err
	}
}
