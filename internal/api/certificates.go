package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// The certificate manager. Only what the CLI shows is here; --json prints
// the API's own body.

// CertificateValidation is how one name is proved for the CA: method
// pantech_dns (automatic) or cname (add CNAMEName → CNAMETarget once).
type CertificateValidation struct {
	Name        string  `json:"name"`
	Method      string  `json:"method"`
	ZoneID      *string `json:"zone_id"`
	CNAMEName   *string `json:"cname_name"`
	CNAMETarget *string `json:"cname_target"`
	Status      string  `json:"status"`
}

// CertificateUsage is a Pantech resource using a certificate.
type CertificateUsage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Certificate is a TLS certificate, issued by Pantech or imported.
type Certificate struct {
	ID                   string                  `json:"id"`
	Kind                 string                  `json:"kind"`
	Names                []string                `json:"names"`
	Description          string                  `json:"description"`
	KeyType              string                  `json:"key_type"`
	Exportable           bool                    `json:"exportable"`
	Status               string                  `json:"status"`
	PendingReason        *string                 `json:"pending_reason"`
	FailureCode          *string                 `json:"failure_code"`
	Validations          []CertificateValidation `json:"validations"`
	ValidationDeadlineAt *string                 `json:"validation_deadline_at"`
	Issuer               *string                 `json:"issuer"`
	SerialNumber         *string                 `json:"serial_number"`
	FingerprintSHA256    *string                 `json:"fingerprint_sha256"`
	NotAfter             *string                 `json:"not_after"`
	IssuedAt             *string                 `json:"issued_at"`
	RenewAfter           *string                 `json:"renew_after"`
	RenewalStatus        *string                 `json:"renewal_status"`
	InUseBy              []CertificateUsage      `json:"in_use_by"`
	RevocationPending    bool                    `json:"revocation_pending"`
	RevokedAt            *string                 `json:"revoked_at"`
	CreatedAt            *string                 `json:"created_at"`
}

// ExportedCertificate holds a private key: it is written to files with
// owner-only permissions and printed only when asked for.
type ExportedCertificate struct {
	CertificatePEM string `json:"certificate_pem"`
	ChainPEM       string `json:"chain_pem"`
	PrivateKeyPEM  string `json:"private_key_pem"`
	NotAfter       string `json:"not_after"`
}

// String hides the private key, so a stray print cannot leak it.
func (e ExportedCertificate) String() string { return "ExportedCertificate{…}" }

// CertificateFailed is a certificate that failed, expired or was revoked
// while being waited for.
type CertificateFailed struct{ Certificate *Certificate }

func (e *CertificateFailed) Error() string {
	code := e.Certificate.Status
	if e.Certificate.FailureCode != nil && *e.Certificate.FailureCode != "" {
		code = *e.Certificate.FailureCode
	}
	why, ok := certificateFailureText[code]
	if !ok {
		why = "it was not issued"
	}
	return fmt.Sprintf("certificate %s is %s: %s\n(%s)", e.Certificate.ID, e.Certificate.Status, why, code)
}

var certificateFailureText = map[string]string{
	"validation_timeout":  "the validation CNAMEs were not in place within 72 hours. Add them and request it again",
	"validation_failed":   "the CA could not validate a name",
	"caa_forbidden":       "a CAA record at the domain does not allow the CA to issue",
	"rejected_identifier": "the CA will not issue for one of the names",
	"rate_limited":        "the CA kept refusing new certificates for these names; try again later",
	"ca_unavailable":      "no CA could be reached for a week; request it again",
	"dns_record_limit":    "the DNS zone is full",
	"ca_rejected":         "the CA refused the request",
	"expired":             "it has expired",
	"revoked":             "it was revoked",
}

// WaitCertificate polls a certificate until it is issued, or failed,
// expired or revoked (a *CertificateFailed). onUpdate sees each change of
// status or pending reason.
func WaitCertificate(ctx context.Context, c *Client, id string, onUpdate func(*Certificate), raw *[]byte) (*Certificate, error) {
	last := ""
	for {
		var cert Certificate
		res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/certificates/" + url.PathEscape(id)}, &cert)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			*raw = res.Body
		}
		state := cert.Status
		if cert.PendingReason != nil {
			state += "/" + *cert.PendingReason
		}
		if state != last && onUpdate != nil {
			onUpdate(&cert)
		}
		last = state
		switch cert.Status {
		case "issued":
			return &cert, nil
		case "failed", "expired", "revoked":
			return &cert, &CertificateFailed{Certificate: &cert}
		}
		select {
		case <-ctx.Done():
			return &cert, ctx.Err()
		case <-time.After(PollInterval):
		}
	}
}
