package internalapi

import "time"

// CertificateIssuance contains public identity metadata, never key material or PEM.
type CertificateIssuance struct {
	Namespace   string    `json:"namespace"`
	Session     string    `json:"session"`
	Server      string    `json:"server"`
	HumanID     string    `json:"human_id"`
	Serial      string    `json:"serial"`
	Fingerprint string    `json:"fingerprint_sha256"`
	SPIFFEID    string    `json:"spiffe_id"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	IssuedAt    time.Time `json:"issued_at"`
}

// CertificateActivity counts recorded issuance events within an explicit window.
// Older/unrecorded certificates cannot be inferred from session counts.
type CertificateActivity struct {
	Recorded uint64                `json:"recorded"`
	Since    time.Time             `json:"since"`
	Until    time.Time             `json:"until"`
	Recent   []CertificateIssuance `json:"recent"`
}
