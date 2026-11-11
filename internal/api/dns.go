package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Pantech DNS: hosted zones and their record sets. Only what the CLI shows
// is here; --json prints the API's own body.

// DNSZone is a hosted zone. Status is pending, awaiting_delegation, active,
// suspended, deleting, deleted or failed (with FailureCode).
type DNSZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	FailureCode *string  `json:"failure_code"`
	NameServers []string `json:"name_servers"`
	DomainID    *string  `json:"domain_id"`
	RecordCount int      `json:"record_count"`
	// DelegationDeadlineAt is when a zone still awaiting delegation is removed.
	DelegationDeadlineAt *string `json:"delegation_deadline_at"`
	LastCheckedAt        *string `json:"last_checked_at"`
	ActivatedAt          *string `json:"activated_at"`
	SuspendedReason      *string `json:"suspended_reason"`
	CreatedAt            *string `json:"created_at"`
	UpdatedAt            *string `json:"updated_at"`
	Version              int     `json:"version"`
}

// DNSRecord is one record set: a name and a type, its TTL and values.
// Status is pending, active, deleting or failed (with FailureCode).
// Managed records (the apex SOA and NS) cannot be changed.
type DNSRecord struct {
	ID           string   `json:"id"`
	ZoneID       string   `json:"zone_id"`
	Name         string   `json:"name"`
	FQDN         string   `json:"fqdn"`
	Type         string   `json:"type"`
	TTL          int      `json:"ttl"`
	Values       []string `json:"values"`
	AllowPrivate bool     `json:"allow_private"`
	Comment      string   `json:"comment"`
	Managed      bool     `json:"managed"`
	Status       string   `json:"status"`
	FailureCode  *string  `json:"failure_code"`
	Version      int      `json:"version"`
	CreatedAt    *string  `json:"created_at"`
	UpdatedAt    *string  `json:"updated_at"`
}

// DNSRecords is the answer of a batch or an import: the records changed.
type DNSRecords struct {
	Data []DNSRecord `json:"data"`
}

// DNSRecordFailed is a record change the DNS network refused.
type DNSRecordFailed struct{ Record *DNSRecord }

func (e *DNSRecordFailed) Error() string {
	code := "failed"
	if e.Record.FailureCode != nil && *e.Record.FailureCode != "" {
		code = *e.Record.FailureCode
	}
	return fmt.Sprintf("the change to record %s %s (%s) was not applied: the record keeps its last good values\n(%s)", e.Record.Name, e.Record.Type, e.Record.ID, code)
}

// WaitDNSRecord polls a record until its change is live (active) or failed,
// and returns it. A failed change is a *DNSRecordFailed.
func WaitDNSRecord(ctx context.Context, c *Client, zoneID, recordID string, raw *[]byte) (*DNSRecord, error) {
	path := "/dns/zones/" + url.PathEscape(zoneID) + "/records/" + url.PathEscape(recordID)
	for {
		var r DNSRecord
		res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path}, &r)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			*raw = res.Body
		}
		switch r.Status {
		case "active":
			return &r, nil
		case "failed":
			return &r, &DNSRecordFailed{Record: &r}
		}
		select {
		case <-ctx.Done():
			return &r, ctx.Err()
		case <-time.After(PollInterval):
		}
	}
}

// DNSZoneQuote is what hosted zones cost: FreeZones are free, each further
// live zone costs MonthlyPerZoneMinor a month.
type DNSZoneQuote struct {
	FreeZones            int    `json:"free_zones"`
	LiveZones            int    `json:"live_zones"`
	BillableZones        int    `json:"billable_zones"`
	Currency             string `json:"currency"`
	MonthlyPerZoneMinor  int64  `json:"monthly_per_zone_minor"`
	MonthlyEstimateMinor int64  `json:"monthly_estimate_minor"`
}
