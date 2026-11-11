package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Domain registration: search, register, transfer in, renew and configure
// domains, paid from the organization's credit. Only what the CLI shows is
// here; --json prints the API's own body.

// TLDOffer is what a TLD sells for, per year, in minor units.
type TLDOffer struct {
	TLD           string `json:"tld"`
	Currency      string `json:"currency"`
	RegisterMinor int64  `json:"register_minor"`
	RenewMinor    int64  `json:"renew_minor"`
	TransferMinor int64  `json:"transfer_minor"`
	Featured      bool   `json:"featured"`
	// PrivacyAlwaysOn: the registry (.ng) always hides the contact.
	PrivacyAlwaysOn bool `json:"privacy_always_on"`
}

// RegisterPrice is what registering for years costs: the first year at the
// register price, every other at the renew price.
func (o *TLDOffer) RegisterPrice(years int) int64 {
	return o.RegisterMinor + o.RenewMinor*int64(years-1)
}

// TransferPrice is what a transfer for years costs; a transfer adds a year.
func (o *TLDOffer) TransferPrice(years int) int64 {
	return o.TransferMinor + o.RenewMinor*int64(years-1)
}

// RenewPrice is what renewing for years costs.
func (o *TLDOffer) RenewPrice(years int) int64 { return o.RenewMinor * int64(years) }

// DomainSearchResult is one name's availability: available, unavailable or
// unknown (could not be checked; try again).
type DomainSearchResult struct {
	Name   string    `json:"name"`
	TLD    string    `json:"tld"`
	Status string    `json:"status"`
	Price  *TLDOffer `json:"price"`
}

// DomainTransferCheck says whether a name can be transferred to Pantech now.
type DomainTransferCheck struct {
	Name         string    `json:"name"`
	Transferable bool      `json:"transferable"`
	Reason       *string   `json:"reason"`
	Price        *TLDOffer `json:"price"`
}

// Domain is a domain registered or transferred in through Pantech. Status is
// pending, registering, transferring, active, expired, failed,
// transferred_out or suspended.
type Domain struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	TLD             string   `json:"tld"`
	Status          string   `json:"status"`
	FailureCode     *string  `json:"failure_code"`
	AutoRenew       bool     `json:"auto_renew"`
	TransferLock    bool     `json:"transfer_lock"`
	Privacy         bool     `json:"privacy"`
	NameServerMode  string   `json:"name_server_mode"`
	NameServers     []string `json:"name_servers"`
	ZoneID          *string  `json:"zone_id"`
	RegisteredAt    *string  `json:"registered_at"`
	ExpiresAt       *string  `json:"expires_at"`
	ContactVerified bool     `json:"contact_verified"`
	SettingsPending bool     `json:"settings_pending"`
	AuthCodePending bool     `json:"auth_code_pending"`
	SuspendedReason *string  `json:"suspended_reason"`
	CreatedAt       *string  `json:"created_at"`
	UpdatedAt       *string  `json:"updated_at"`
	Version         int      `json:"version"`
}

// DomainOrder is one paid action on a domain: register, renew or transfer.
// Status goes pending, paid, submitted, then succeeded or failed; a failed
// order returns its amount to credit (Refunded).
type DomainOrder struct {
	ID          string  `json:"id"`
	DomainID    string  `json:"domain_id"`
	DomainName  string  `json:"domain_name"`
	Kind        string  `json:"kind"`
	Years       int     `json:"years"`
	AmountMinor int64   `json:"amount_minor"`
	Currency    string  `json:"currency"`
	Status      string  `json:"status"`
	FailureCode *string `json:"failure_code"`
	Refunded    bool    `json:"refunded"`
	// While the order is awaiting_payment (credit falls short), it waits
	// until PaymentExpiresAt: a US-dollar order charges the saved card the
	// difference, and PaymentURL is set only when the bank asks the customer
	// to confirm that payment; a naira order waits for credit to be added.
	PaymentURL       *string `json:"payment_url"`
	PaymentExpiresAt *string `json:"payment_expires_at"`
	CreatedAt        *string `json:"created_at"`
	CompletedAt      *string `json:"completed_at"`
}

func (o *DomainOrder) Done() bool { return o.Status == "succeeded" || o.Status == "failed" }

// DomainOrderAccepted is the answer to a register, transfer or renew.
type DomainOrderAccepted struct {
	Domain Domain      `json:"domain"`
	Order  DomainOrder `json:"order"`
}

// DomainAuthCode is the answer of GET /domains/{id}/auth-code: the code and
// when it stops being shown (200), or status "pending" (202).
type DomainAuthCode struct {
	AuthCode  string  `json:"auth_code"`
	ExpiresAt *string `json:"expires_at"`
	Status    string  `json:"status"`
}

// DomainOrderFailed is a domain order that finished without the change.
type DomainOrderFailed struct{ Order *DomainOrder }

func (e *DomainOrderFailed) Error() string {
	code := "failed"
	if e.Order.FailureCode != nil && *e.Order.FailureCode != "" {
		code = *e.Order.FailureCode
	}
	why, ok := domainOrderFailureText[code]
	if !ok {
		why = "the registrar did not complete it"
	}
	msg := fmt.Sprintf("the %s order %s for %s failed: %s", e.Order.Kind, e.Order.ID, e.Order.DomainName, why)
	if e.Order.Refunded {
		msg += "\nThe amount was returned to your credit."
	}
	return msg + "\n(" + code + ")"
}

var domainOrderFailureText = map[string]string{
	"insufficient_credit": "there was not enough credit and the payment did not arrive within the hour; nothing was charged. Order again",
	"price_changed":       "the price changed before the order was paid; nothing was charged. Order again to see the new price",
	"domain_unavailable":  "the name is no longer available",
	"registrar_rejected":  "the registrar refused it",
	"transfer_rejected":   "the transfer was refused by the current registrar or the owner",
	"invalid_auth_code":   "the auth code is wrong. Get a new one from the current registrar",
	"contact_rejected":    "the registrant contact was refused. Check it and order again",
	"tld_not_sold":        "Pantech does not sell this TLD",
	"billing_unavailable": "billing could not be reached. Nothing was charged; try again in a moment",
	"domain_expired":      "the domain has expired",
}

func domainOrderPath(id string) string { return "/domain-orders/" + url.PathEscape(id) }

// GetDomainOrder reads one domain order.
func GetDomainOrder(ctx context.Context, c *Client, id string) (*DomainOrder, *Response, error) {
	var o DomainOrder
	res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: domainOrderPath(id)}, &o)
	if err != nil {
		return nil, nil, err
	}
	return &o, res, nil
}

// RetryDomainOrderPayment makes an order that is awaiting_payment try its
// payment again now, and wait another hour: a US-dollar order charges the
// saved card once more, a naira one takes credit just added. It never
// charges twice, so a retry after a lost answer is safe. An order that is
// not waiting for a payment, or whose hour has ended, is
// DOMAIN_ORDER_NOT_AWAITING_PAYMENT (409).
func RetryDomainOrderPayment(ctx context.Context, c *Client, id string) (*DomainOrder, *Response, error) {
	var o DomainOrder
	res, err := c.Do(ctx, Request{Method: http.MethodPost, Path: domainOrderPath(id) + "/retry-payment"}, &o)
	if err != nil {
		return nil, nil, err
	}
	return &o, res, nil
}

// WaitDomainOrder polls a domain order until it has succeeded or failed.
// onUpdate, when set, sees each change of status. A failed order is a
// *DomainOrderFailed.
func WaitDomainOrder(ctx context.Context, c *Client, id string, onUpdate func(*DomainOrder), raw *[]byte) (*DomainOrder, error) {
	last := ""
	for {
		o, res, err := GetDomainOrder(ctx, c, id)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			*raw = res.Body
		}
		if o.Status != last && onUpdate != nil {
			onUpdate(o)
		}
		last = o.Status
		if o.Done() {
			if o.Status == "failed" {
				return o, &DomainOrderFailed{Order: o}
			}
			return o, nil
		}
		select {
		case <-ctx.Done():
			return o, ctx.Err()
		case <-time.After(PollInterval):
		}
	}
}

// ErrAuthCodeTimeout is an auth code that was not ready in time.
var ErrAuthCodeTimeout = errors.New("the auth code is still being fetched from the registrar")

// WaitDomainAuthCode polls GET /domains/{id}/auth-code until the code is
// there (200), and returns it with the raw body.
func WaitDomainAuthCode(ctx context.Context, c *Client, domainID string) (*DomainAuthCode, []byte, error) {
	path := "/domains/" + url.PathEscape(domainID) + "/auth-code"
	for {
		var code DomainAuthCode
		res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path}, &code)
		if err != nil {
			return nil, nil, err
		}
		if res.Status == http.StatusOK && code.AuthCode != "" {
			return &code, res.Body, nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, nil, ErrAuthCodeTimeout
			}
			return nil, nil, ctx.Err()
		case <-time.After(PollInterval):
		}
	}
}
