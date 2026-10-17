// csrgen prints a base64-encoded PEM CSR shaped like the ones the MCP Runtime
// runtime API submits: ECDSA P-256, no subject, optional URI and DNS SANs.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
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
	extraSAN := flag.String("extra-san", "", "negative-test SAN: directoryName or otherName")
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
	if *extraSAN != "" {
		if len(tmpl.DNSNames) != 0 {
			fmt.Fprintln(os.Stderr, "extra-san fixtures cannot include DNS names")
			os.Exit(1)
		}
		var names []asn1.RawValue
		for _, uri := range tmpl.URIs {
			names = append(names, asn1.RawValue{Class: 2, Tag: 6, Bytes: []byte(uri.String())})
		}
		var hidden asn1.RawValue
		switch *extraSAN {
		case "directoryName":
			der, err := asn1.Marshal(pkix.Name{CommonName: "forged-session"}.ToRDNSequence())
			if err != nil {
				panic(err)
			}
			hidden = asn1.RawValue{Class: 2, Tag: 4, IsCompound: true, Bytes: der}
		case "otherName":
			der, err := asn1.Marshal(struct {
				Type  asn1.ObjectIdentifier
				Value asn1.RawValue
			}{asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 20, 2, 3}, asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: []byte{12, 4, 'e', 'v', 'i', 'l'}}})
			if err != nil {
				panic(err)
			}
			var sequence asn1.RawValue
			if _, err := asn1.Unmarshal(der, &sequence); err != nil {
				panic(err)
			}
			hidden = asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: sequence.Bytes}
		default:
			fmt.Fprintln(os.Stderr, "unsupported extra-san fixture")
			os.Exit(1)
		}
		der, err := asn1.Marshal(append(names, hidden))
		if err != nil {
			panic(err)
		}
		tmpl.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Value: der}}
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
