package swadmin

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
	"strconv"
	"testing"
	"time"

	"github.com/seaweedfs/seaweedfs/weed/pb/iam_pb"
	"github.com/seaweedfs/seaweedfs/weed/security"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// testPKI is an in-memory CA plus one leaf certificate signed by it, shaped
// like the cert-manager Secret the operator reads: the same leaf serves as
// server and client certificate, and its SAN list does NOT cover the
// 127.0.0.1 address tests dial — mirroring production, where the shared
// SeaweedFS certificate is not minted per dial target and hostname
// verification must be skipped.
type testPKI struct {
	caPEM, certPEM, keyPEM []byte
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test SeaweedFS CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test.seaweedfs"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"seaweed-test-filer"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	return testPKI{
		caPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

// serverCreds builds gRPC server credentials that require and verify a client
// certificate against the test CA — the same posture a SeaweedFS filer takes
// when security.toml configures [grpc] ca.
func (p testPKI) serverCreds(t *testing.T) credentials.TransportCredentials {
	t.Helper()
	serverCert, err := tls.X509KeyPair(p.certPEM, p.keyPEM)
	if err != nil {
		t.Fatalf("server key pair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.caPEM) {
		t.Fatal("append CA to pool")
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	})
}

// startMTLSIAMServer runs an in-process IAM gRPC server behind mTLS and
// returns the filer-style HTTP host:port IAMClient expects (gRPC port minus
// the seaweedfs port delta).
func startMTLSIAMServer(t *testing.T, pki testPKI, key []byte) string {
	t.Helper()
	srv := grpc.NewServer(grpc.Creds(pki.serverCreds(t)))
	t.Cleanup(srv.Stop)
	iam_pb.RegisterSeaweedIdentityAccessManagementServer(srv, &authRecordingIAM{adminSigningKey: security.SigningKey(key)})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	grpcPort := lis.Addr().(*net.TCPAddr).Port
	if grpcPort < 10001 {
		t.Skipf("ephemeral gRPC port %d too low to map to a valid HTTP port", grpcPort)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(grpcPort-10000))
}

// TestIAMClient_MTLS_EndToEnd pins the fix for IAM reconciliation against a
// cluster with [grpc] mTLS: an IAMClient built with ClientTLSDialOption must
// complete the handshake (client cert presented, server chain verified, no
// hostname check — the server cert's SANs deliberately exclude 127.0.0.1)
// and still stamp the admin Bearer token. Before the fix the client dialed
// with insecure credentials and every call failed once the filer required
// TLS.
func TestIAMClient_MTLS_EndToEnd(t *testing.T) {
	pki := newTestPKI(t)
	key := []byte("test-jwt-signing-key")
	filer := startMTLSIAMServer(t, pki, key)

	dialOption, err := ClientTLSDialOption(pki.caPEM, pki.certPEM, pki.keyPEM)
	if err != nil {
		t.Fatalf("ClientTLSDialOption: %v", err)
	}
	c := NewIAMClient(filer, key, dialOption)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u, err := c.GetUser(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUser over mTLS: %v", err)
	}
	if u == nil || u.Name != "alice" {
		t.Fatalf("GetUser = %+v, want alice", u)
	}
}

// TestIAMClient_MTLS_RejectsUnknownCA proves the dial option still verifies
// the server chain: a server presenting a certificate from a different CA
// must fail the handshake rather than being silently trusted.
func TestIAMClient_MTLS_RejectsUnknownCA(t *testing.T) {
	serverPKI := newTestPKI(t)
	clientPKI := newTestPKI(t) // distinct CA
	key := []byte("test-jwt-signing-key")
	filer := startMTLSIAMServer(t, serverPKI, key)

	dialOption, err := ClientTLSDialOption(clientPKI.caPEM, clientPKI.certPEM, clientPKI.keyPEM)
	if err != nil {
		t.Fatalf("ClientTLSDialOption: %v", err)
	}
	c := NewIAMClient(filer, key, dialOption)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.GetUser(ctx, "alice"); err == nil {
		t.Fatal("GetUser succeeded against a server signed by an unknown CA")
	}
}

// TestClientTLSDialOption_BadMaterial pins the error paths so a malformed
// Secret surfaces as a build error instead of a confusing dial failure.
func TestClientTLSDialOption_BadMaterial(t *testing.T) {
	pki := newTestPKI(t)
	if _, err := ClientTLSDialOption([]byte("not pem"), pki.certPEM, pki.keyPEM); err == nil {
		t.Fatal("expected error for unparseable CA")
	}
	if _, err := ClientTLSDialOption(pki.caPEM, []byte("not pem"), pki.keyPEM); err == nil {
		t.Fatal("expected error for unparseable cert")
	}
}
