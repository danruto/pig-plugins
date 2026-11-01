// Command tls writes a disposable certificate authority and a chatgpt.com
// server certificate into one directory, so the loopback fixture proxy can
// terminate the host's Codex model requests without any public trust anchor.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: tls <output directory>")
		os.Exit(2)
	}
	if err := write(os.Args[1], time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func write(directory string, now time.Time) error {
	authorityKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	authority := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "usage fixture loopback authority"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	authorityDER, err := x509.CreateCertificate(rand.Reader, authority, authority, &authorityKey.PublicKey, authorityKey)
	if err != nil {
		return err
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	server := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "chatgpt.com"},
		DNSNames:     []string{"chatgpt.com"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, server, authority, &serverKey.PublicKey, authorityKey)
	if err != nil {
		return err
	}
	serverPKCS8, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return err
	}
	for name, block := range map[string]*pem.Block{
		"authority.pem": {Type: "CERTIFICATE", Bytes: authorityDER},
		"server.pem":    {Type: "CERTIFICATE", Bytes: serverDER},
		"server.key":    {Type: "PRIVATE KEY", Bytes: serverPKCS8},
	} {
		if err := os.WriteFile(filepath.Join(directory, name), pem.EncodeToMemory(block), 0o600); err != nil {
			return err
		}
	}
	return nil
}
