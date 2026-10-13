// csrgen prints a base64-encoded PEM CSR shaped like the ones the MCP Runtime
// runtime API submits: ECDSA P-256, no subject, optional URI and DNS SANs.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

func main() {
	uri := flag.String("uri", "", "comma-separated URI SANs")
	dns := flag.String("dns", "", "comma-separated DNS SANs")
	flag.Parse()

	tmpl := &x509.CertificateRequest{}
	for _, u := range strings.Split(*uri, ",") {
		if u = strings.TrimSpace(u); u != "" {
			parsed, err := url.Parse(u)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			tmpl.URIs = append(tmpl.URIs, parsed)
		}
	}
	for _, d := range strings.Split(*dns, ",") {
		if d = strings.TrimSpace(d); d != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, d)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	fmt.Print(base64.StdEncoding.EncodeToString(pemBytes))
}
