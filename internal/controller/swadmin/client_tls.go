package swadmin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"

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
	return ClientTLSDialOptionVerifyPeer(ca, cert, key, nil)
}

// ClientTLSDialOptionVerifyPeer is ClientTLSDialOption plus a callback the
// verified leaf must still pass — used to bind the dial to an expected
// server identity on shared-CA deployments, where the chain alone does not
// single out the intended peer.
func ClientTLSDialOptionVerifyPeer(ca, cert, key []byte, verifyPeer func(*x509.Certificate) error) (grpc.DialOption, error) {
	return ClientTLSDialOptionVerifyPeerFor(ca, cert, key, func(_ string, leaf *x509.Certificate) error {
		if verifyPeer == nil {
			return nil
		}
		return verifyPeer(leaf)
	})
}

// ClientTLSDialOptionVerifyPeerFor is ClientTLSDialOptionVerifyPeer whose
// callback additionally receives the handshake authority (host:port), so the
// expected server identity can differ per endpoint — one credential can then
// serve master, filer and volume dials while each still pins its own target.
func ClientTLSDialOptionVerifyPeerFor(ca, cert, key []byte, verifyPeer func(authority string, leaf *x509.Certificate) error) (grpc.DialOption, error) {
	creds, err := newAuthorityPeerCredentials(ca, cert, key, verifyPeer)
	if err != nil {
		return nil, err
	}
	return grpc.WithTransportCredentials(creds), nil
}

func newAuthorityPeerCredentials(ca, cert, key []byte, verifyPeer func(authority string, leaf *x509.Certificate) error) (credentials.TransportCredentials, error) {
	clientCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("no CA certificates parsed from ca.crt")
	}
	return &authorityPeerCredentials{
		roots:      roots,
		verifyPeer: verifyPeer,
		config: &tls.Config{
			Certificates:       []tls.Certificate{clientCert},
			MinVersion:         tls.VersionTLS12,
			NextProtos:         []string{"h2"},
			InsecureSkipVerify: true,
		},
	}, nil
}

// authorityPeerCredentials binds VerifyPeerCertificate per handshake: the
// stock tls credentials hand every handshake the same callback, which cannot
// see the dial target, so per-endpoint peer checks need this thin wrapper.
type authorityPeerCredentials struct {
	config     *tls.Config
	roots      *x509.CertPool
	verifyPeer func(authority string, leaf *x509.Certificate) error
}

func (c *authorityPeerCredentials) ClientHandshake(ctx context.Context, authority string, rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	cfg := c.config.Clone()
	host, _, err := net.SplitHostPort(authority)
	if err != nil {
		host = authority
	}
	cfg.ServerName = host // SNI only — verification is the callback below
	verifyPeer := c.verifyPeer
	cfg.VerifyPeerCertificate = verifyChainOnly(c.roots, func(leaf *x509.Certificate) error {
		if verifyPeer == nil {
			return nil
		}
		return verifyPeer(authority, leaf)
	})
	conn := tls.Client(rawConn, cfg)
	if err := conn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, nil, fmt.Errorf("TLS handshake to %s: %w", authority, err)
	}
	return conn, credentials.TLSInfo{
		State:          conn.ConnectionState(),
		CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.PrivacyAndIntegrity},
	}, nil
}

func (c *authorityPeerCredentials) ServerHandshake(net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, fmt.Errorf("authorityPeerCredentials is client-only")
}

func (c *authorityPeerCredentials) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "tls"}
}

func (c *authorityPeerCredentials) Clone() credentials.TransportCredentials {
	return &authorityPeerCredentials{config: c.config.Clone(), roots: c.roots, verifyPeer: c.verifyPeer}
}

func (c *authorityPeerCredentials) OverrideServerName(serverNameOverride string) error {
	c.config.ServerName = serverNameOverride
	return nil
}

// verifyChainOnly returns a VerifyPeerCertificate callback that verifies the
// presented chain against roots without a DNSName constraint, then hands the
// leaf to verifyPeer when one is given.
func verifyChainOnly(roots *x509.CertPool, verifyPeer func(*x509.Certificate) error) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
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
		if _, err := certs[0].Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return err
		}
		if verifyPeer != nil {
			return verifyPeer(certs[0])
		}
		return nil
	}
}
