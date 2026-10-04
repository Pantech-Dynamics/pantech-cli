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

// InstanceOrder is creating an instance: paying, then provisioning.
type InstanceOrder struct {
	ID          string  `json:"id"`
	InstanceID  string  `json:"instance_id"`
	Status      string  `json:"status"`
	OperationID *string `json:"operation_id"`
	FailureCode *string `json:"failure_code"`
	AmountMinor int64   `json:"amount_minor"`
	Currency    string  `json:"currency"`
}

// Done reports whether the order has finished, either way.
func (o *InstanceOrder) Done() bool {
	return o.Status == "provisioned" || o.Status == "failed" || o.Status == "payment_failed"
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

// WaitOrder polls an instance order until it is provisioned or fails.
func WaitOrder(ctx context.Context, c *Client, id string, onUpdate func(*InstanceOrder)) (*InstanceOrder, error) {
	last := ""
	for {
		var o InstanceOrder
		if _, err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/instance-orders/" + url.PathEscape(id)}, &o); err != nil {
			return nil, err
		}
		if o.Status != last && onUpdate != nil {
			onUpdate(&o)
		}
		last = o.Status
		if o.Done() {
			if o.Status != "provisioned" {
				code := o.Status
				if o.FailureCode != nil {
					code = *o.FailureCode
				}
				return &o, fmt.Errorf("order %s did not provision: %s", o.ID, code)
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

// OperationFailed is an operation that finished with status failed.
type OperationFailed struct{ Op *Operation }

func (e *OperationFailed) Error() string {
	if e.Op.Failure != nil {
		return fmt.Sprintf("%s failed: %s (%s)", e.Op.Kind, e.Op.Failure.Reason, e.Op.Failure.Code)
	}
	return fmt.Sprintf("%s failed (operation %s)", e.Op.Kind, e.Op.ID)
}
