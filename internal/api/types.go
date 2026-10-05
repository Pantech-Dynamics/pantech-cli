package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// The public API's resources, as much of each as the CLI shows. --json
// prints the API's own body, so fields left out here are never lost.

type Me struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	APIKey         struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		ExpiresAt *string  `json:"expires_at"`
	} `json:"api_key"`
}

type Failure struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

type Instance struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	PlanSlug      *string `json:"plan_slug"`
	ImageSlug     *string `json:"image_slug"`
	Region        *string `json:"region"`
	Zone          *string `json:"zone"`
	NetworkID     *string `json:"network_id"`
	SubnetID      *string `json:"subnet_id"`
	PublicIPv4    *string `json:"public_ipv4"`
	PrivateIPv4   *string `json:"private_ipv4"`
	// SecurityGroupID is the group of a standard VM.
	SecurityGroupID *string `json:"security_group_id"`
	// PrivateNetworkState is the VM's interface on its zone's private
	// database network: none, attaching, attached or detaching.
	PrivateNetworkState string `json:"private_network_state"`
	// PrivateNetworkIP is that interface's address once attached.
	PrivateNetworkIP *string `json:"private_network_ip"`
	DesiredState  string  `json:"desired_state"`
	ObservedState string  `json:"observed_state"`
	Spec          *struct {
		VCPU     int `json:"vcpu"`
		MemoryMB int `json:"memory_mb"`
		DiskGB   int `json:"disk_gb"`
	} `json:"spec"`
	Failure   *Failure `json:"failure"`
	CreatedAt *string  `json:"created_at"`
}

// InVPC reports whether the VM sits in a VPC subnet. One that does not is a
// standard VM, behind a security group.
func (v *Instance) InVPC() bool { return v.SubnetID != nil && *v.SubnetID != "" }

// Address is the VM's address reachable from outside, or "" if it has none:
// its public IPv4 if set, else, for a standard VM, its one address. The API
// returns a standard VM's address in private_ipv4 with public_ipv4 null; the
// console shows it as the VM's "static IP" the same way. A VPC VM's private
// address is not reachable, so without a public IP it has none.
func (v *Instance) Address() string {
	if v.PublicIPv4 != nil && *v.PublicIPv4 != "" {
		return *v.PublicIPv4
	}
	if !v.InVPC() && v.PrivateIPv4 != nil {
		return *v.PrivateIPv4
	}
	return ""
}

type Plan struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	VCPU     int    `json:"vcpu"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
	Price    *struct {
		Currency             string `json:"currency"`
		MonthlyEstimateMinor int64  `json:"monthly_estimate_minor"`
	} `json:"price"`
	UnpricedReason *string `json:"unpriced_reason"`
}

type Image struct {
	ID      string   `json:"id"`
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Zones   []string `json:"zones"`
}

type Region struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Placements []struct {
		Kind              string  `json:"kind"`
		Zone              string  `json:"zone"`
		Available         bool    `json:"available"`
		UnavailableReason *string `json:"unavailable_reason"`
	} `json:"placements"`
}

type SSHKey struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Fingerprint string  `json:"fingerprint"`
	PublicKey   string  `json:"public_key"`
	CreatedAt   *string `json:"created_at"`
	// Only when the API generated the pair: shown once, never again.
	PrivateKey string `json:"private_key,omitempty"`
}

// Accepted is a write's 202: the operation to follow.
type Accepted struct {
	OperationID string `json:"operation_id"`
	ResourceID  string `json:"resource_id"`
	Status      string `json:"status"`
}

type Operation struct {
	ID           string   `json:"id"`
	ResourceType string   `json:"resource_type"`
	ResourceID   string   `json:"resource_id"`
	Kind         string   `json:"kind"`
	Status       string   `json:"status"`
	Failure      *Failure `json:"failure"`
}

// Done reports whether the operation has finished, either way.
func (o *Operation) Done() bool { return o.Status == "succeeded" || o.Status == "failed" }

// Order is paying for and then provisioning a new instance or database.
// The two kinds share their statuses; each sets only its own resource id.
type Order struct {
	ID            string  `json:"id"`
	InstanceID    string  `json:"instance_id,omitempty"`
	DatabaseID    string  `json:"database_id,omitempty"`
	Status        string  `json:"status"`
	OperationID   *string `json:"operation_id"`
	FailureCode   *string `json:"failure_code"`
	AmountMinor   int64   `json:"amount_minor"`
	Currency      string  `json:"currency"`
	AdminUsername string  `json:"admin_username,omitempty"`
	CreatedAt     *string `json:"created_at"`
}

// InstanceOrder is creating an instance: paying, then provisioning.
type InstanceOrder = Order

// DatabaseOrder is creating a database: paying, then provisioning. It never
// carries a password.
type DatabaseOrder = Order

// Done reports whether the order has finished, either way.
func (o *Order) Done() bool {
	return o.Status == "provisioned" || o.Status == "failed" || o.Status == "payment_failed"
}

// ResourceID is the instance or database the order creates.
func (o *Order) ResourceID() string {
	if o.InstanceID != "" {
		return o.InstanceID
	}
	return o.DatabaseID
}

// Page is a list response.
type Page[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

// ListAll follows next_cursor to the end, as the API asks before deciding
// something does not exist.
func ListAll[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	var all []T
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	for {
		var page Page[T]
		if _, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: q}, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return all, nil
		}
		q.Set("cursor", *page.NextCursor)
	}
}

// PollInterval is how often Wait* asks. The API suggests 2–5 seconds.
var PollInterval = 2 * time.Second

// WaitOperation polls an operation until it finishes, calling onUpdate on
// every change of status. A failed operation is returned with an error.
func WaitOperation(ctx context.Context, c *Client, id string, onUpdate func(*Operation)) (*Operation, error) {
	last := ""
	for {
		var op Operation
		if _, err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/operations/" + url.PathEscape(id)}, &op); err != nil {
			return nil, err
		}
		if op.Status != last && onUpdate != nil {
			onUpdate(&op)
		}
		last = op.Status
		if op.Done() {
			if op.Status == "failed" {
				return &op, &OperationFailed{Op: &op}
			}
			return &op, nil
		}
		select {
		case <-ctx.Done():
			return &op, ctx.Err()
		case <-time.After(PollInterval):
		}
	}
}

// WaitOrder polls an instance order until it is provisioned or fails. An
// order that fails is returned with an *OrderFailed.
func WaitOrder(ctx context.Context, c *Client, id string, onUpdate func(*InstanceOrder)) (*InstanceOrder, error) {
	return waitOrder(ctx, c, "/instance-orders/", id, onUpdate)
}

// WaitDatabaseOrder polls a database order until it is provisioned (handed
// to provisioning, whose operation is then its operation_id) or fails.
func WaitDatabaseOrder(ctx context.Context, c *Client, id string, onUpdate func(*DatabaseOrder)) (*DatabaseOrder, error) {
	return waitOrder(ctx, c, "/database-orders/", id, onUpdate)
}

func waitOrder(ctx context.Context, c *Client, base, id string, onUpdate func(*Order)) (*Order, error) {
	last := ""
	for {
		var o Order
		if _, err := c.Do(ctx, Request{Method: http.MethodGet, Path: base + url.PathEscape(id)}, &o); err != nil {
			return nil, err
		}
		if o.Status != last && onUpdate != nil {
			onUpdate(&o)
		}
		last = o.Status
		if o.Done() {
			if o.Status != "provisioned" {
				return &o, &OrderFailed{Order: &o}
			}
			return &o, nil
		}
		select {
		case <-ctx.Done():
			return &o, ctx.Err()
		case <-time.After(PollInterval):
		}
	}
}

// OrderFailed is an order that finished without provisioning: its payment
// failed or provisioning could not start. Any payment taken is returned to
// credit by the platform.
type OrderFailed struct{ Order *Order }

func (e *OrderFailed) Error() string {
	code := e.Order.Status
	if e.Order.FailureCode != nil && *e.Order.FailureCode != "" {
		code = *e.Order.FailureCode
	}
	return fmt.Sprintf("order %s did not provision: %s", e.Order.ID, code)
}

// OperationFailed is an operation that finished with status failed.
type OperationFailed struct{ Op *Operation }

func (e *OperationFailed) Error() string {
	if e.Op.Failure != nil {
		return fmt.Sprintf("%s failed: %s (%s)", e.Op.Kind, e.Op.Failure.Reason, e.Op.Failure.Code)
	}
	return fmt.Sprintf("%s failed (operation %s)", e.Op.Kind, e.Op.ID)
}
