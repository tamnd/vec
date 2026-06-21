package auth

import (
	"crypto/x509"
	"errors"
)

// MTLSConfig maps a verified client certificate to a principal (spec 23 section
// 6.4). The TLS listener does the certificate chain verification; this config
// only decides which certificate field names the principal.
type MTLSConfig struct {
	PrincipalFromCN  bool   // use the certificate common name
	PrincipalFromSAN string // "dns", "email", or "uri"; used when CN mapping is off
}

// PrincipalFromCert extracts the principal from a verified client certificate
// (spec 23 section 6.4). The caller passes the leaf certificate after the TLS
// stack has validated it against the configured CA.
func (c MTLSConfig) PrincipalFromCert(cert *x509.Certificate) (Principal, error) {
	if cert == nil {
		return Principal{}, errors.New("vec/auth: no client certificate")
	}
	if c.PrincipalFromCN {
		if cert.Subject.CommonName == "" {
			return Principal{}, errors.New("vec/auth: client certificate has no common name")
		}
		return Principal{ID: cert.Subject.CommonName, Kind: "mtls"}, nil
	}
	switch c.PrincipalFromSAN {
	case "dns":
		if len(cert.DNSNames) == 0 {
			return Principal{}, errors.New("vec/auth: client certificate has no DNS SAN")
		}
		return Principal{ID: cert.DNSNames[0], Kind: "mtls"}, nil
	case "email":
		if len(cert.EmailAddresses) == 0 {
			return Principal{}, errors.New("vec/auth: client certificate has no email SAN")
		}
		return Principal{ID: cert.EmailAddresses[0], Kind: "mtls"}, nil
	case "uri":
		if len(cert.URIs) == 0 {
			return Principal{}, errors.New("vec/auth: client certificate has no URI SAN")
		}
		return Principal{ID: cert.URIs[0].String(), Kind: "mtls"}, nil
	default:
		return Principal{}, errors.New("vec/auth: no principal mapping configured")
	}
}
