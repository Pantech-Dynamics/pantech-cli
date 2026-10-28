package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Pantech Migrate: connect another cloud account read-only, scan it, and plan
// its move to Pantech. Only what the CLI shows is here; --json prints the
// API's own body.

// CloudFailure is why a connection's check or a scan failed. Message says
// what to do about it.
type CloudFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type CloudConnectionSetup struct {
	IssuerURL      string  `json:"issuer_url"`
	Audience       string  `json:"audience"`
	Subject        string  `json:"subject"`
	QuickCreateURL *string `json:"quick_create_url"`
	Template       string  `json:"template"`
}

// CloudConnection is a read-only link to another cloud account. Status is
// awaiting_role, verifying, connected or failed.
type CloudConnection struct {
	ID           string               `json:"id"`
	Provider     string               `json:"provider"`
	Name         string               `json:"name"`
	Status       string               `json:"status"`
	Failure      *CloudFailure        `json:"failure"`
	RoleARN      *string              `json:"role_arn"`
	AWSAccountID *string              `json:"aws_account_id"`
	VerifiedAt   *string              `json:"verified_at"`
	CreatedAt    *string              `json:"created_at"`
	UpdatedAt    *string              `json:"updated_at"`
	Setup        CloudConnectionSetup `json:"setup"`
}

type CloudScanCounts struct {
	Instances      int `json:"instances"`
	Volumes        int `json:"volumes"`
	Networks       int `json:"networks"`
	Subnets        int `json:"subnets"`
	SecurityGroups int `json:"security_groups"`
	PublicIPs      int `json:"public_ips"`
	LoadBalancers  int `json:"load_balancers"`
	Databases      int `json:"databases"`
	Buckets        int `json:"buckets"`
	Users          int `json:"users"`
	SSHKeys        int `json:"ssh_keys"`
}

func (c *CloudScanCounts) Total() int {
	return c.Instances + c.Volumes + c.Networks + c.Subnets + c.SecurityGroups + c.PublicIPs +
		c.LoadBalancers + c.Databases + c.Buckets + c.Users + c.SSHKeys
}

type CloudCostMonth struct {
	Month       string `json:"month"`
	AmountMinor int64  `json:"amount_minor"`
}

// CloudCost is the source account's whole bill, every service.
type CloudCost struct {
	Currency            string           `json:"currency"`
	AverageMonthlyMinor int64            `json:"average_monthly_minor"`
	Months              []CloudCostMonth `json:"months"`
}

// CloudScan is one read of a connected account. Status is queued, running,
// succeeded or failed. Inventory is only there with include=inventory:
// one list of objects per kind.
type CloudScan struct {
	ID           string                                  `json:"id"`
	ConnectionID string                                  `json:"connection_id"`
	Status       string                                  `json:"status"`
	Failure      *CloudFailure                           `json:"failure"`
	CreatedAt    *string                                 `json:"created_at"`
	StartedAt    *string                                 `json:"started_at"`
	FinishedAt   *string                                 `json:"finished_at"`
	AWSAccountID *string                                 `json:"aws_account_id"`
	Regions      []string                                `json:"regions"`
	Counts       *CloudScanCounts                        `json:"counts"`
	Cost         *CloudCost                              `json:"cost"`
	Warnings     []string                                `json:"warnings"`
	Truncated    []string                                `json:"truncated"`
	Inventory    map[string][]map[string]json.RawMessage `json:"inventory"`
}

func (s *CloudScan) Done() bool { return s.Status == "succeeded" || s.Status == "failed" }

type MigrationFlag struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

type MigrationRule struct {
	Direction string  `json:"direction"`
	Protocol  string  `json:"protocol"`
	PortRange *string `json:"port_range"`
	CIDR      string  `json:"cidr"`
}

type MigrationListener struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

// MigrationItemTarget is what an item becomes on Pantech; fields that do
// not apply to its kind are null or empty.
type MigrationItemTarget struct {
	PlanSlug      *string             `json:"plan_slug"`
	DiskGB        *int                `json:"disk_gb"`
	CIDR          *string             `json:"cidr"`
	NetworkKey    *string             `json:"network_key"`
	SubnetKey     *string             `json:"subnet_key"`
	InstanceKey   *string             `json:"instance_key"`
	Rules         []MigrationRule     `json:"rules"`
	Engine        *string             `json:"engine"`
	EngineVersion *string             `json:"engine_version"`
	StorageGB     *int                `json:"storage_gb"`
	Role          *string             `json:"role"`
	Email         *string             `json:"email"`
	PublicKey     *string             `json:"public_key"`
	Listeners     []MigrationListener `json:"listeners"`
}

type MigrationItemSource struct {
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	Region    *string `json:"region"`
	Summary   string  `json:"summary"`
	VCPU      *int    `json:"vcpu"`
	MemoryMiB *int    `json:"memory_mib"`
	DiskGB    *int    `json:"disk_gb"`
}

// MigrationItem is one thing in the source account and its Pantech
// equivalent. Status is planned, excluded or unsupported.
type MigrationItem struct {
	Key    string              `json:"key"`
	Kind   string              `json:"kind"`
	Source MigrationItemSource `json:"source"`
	Status string              `json:"status"`
	Flags  []MigrationFlag     `json:"flags"`
	Target MigrationItemTarget `json:"target"`
}

// MigrationEstimate is Pantech's monthly price for the plan next to the
// source account's monthly bill, computed when read.
type MigrationEstimate struct {
	Currency            string   `json:"currency"`
	PantechPriced       bool     `json:"pantech_priced"`
	PantechMonthlyMinor *int64   `json:"pantech_monthly_minor"`
	Covers              []string `json:"covers"`
	NotCovered          []string `json:"not_covered"`
	SourceKnown         bool     `json:"source_known"`
	SourceMonthlyMinor  *int64   `json:"source_monthly_minor"`
	SourceCurrency      *string  `json:"source_currency"`
	SourceMonths        int      `json:"source_months"`
	SourceScope         string   `json:"source_scope"`
}

type MigrationSummary struct {
	Planned     int            `json:"planned"`
	Excluded    int            `json:"excluded"`
	Unsupported int            `json:"unsupported"`
	ByKind      map[string]int `json:"by_kind"`
}

// Migration is a plan to move a scanned account to Pantech. Items and
// Estimate are null in a list.
type Migration struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Region       string             `json:"region"`
	ConnectionID string             `json:"connection_id"`
	ScanID       string             `json:"scan_id"`
	Stage        string             `json:"stage"`
	Version      int                `json:"version"`
	Summary      MigrationSummary   `json:"summary"`
	Estimate     *MigrationEstimate `json:"estimate"`
	Items        []MigrationItem    `json:"items"`
	CreatedAt    *string            `json:"created_at"`
	UpdatedAt    *string            `json:"updated_at"`
}

type MigrationTerraformFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type MigrationTerraform struct {
	Files []MigrationTerraformFile `json:"files"`
}

// MigratePollInterval is how often a connection check or a scan is asked
// after. Tests shorten it.
var MigratePollInterval = 3 * time.Second

// MigrateFailed is a connection check or a scan that finished, but failed.
type MigrateFailed struct {
	What    string // "scan", "connection check"
	ID      string
	Failure *CloudFailure
}

func (e *MigrateFailed) Error() string {
	if e.Failure == nil {
		return fmt.Sprintf("the %s of %s failed", e.What, e.ID)
	}
	msg := e.Failure.Message
	if msg == "" {
		msg = e.Failure.Code
	}
	return fmt.Sprintf("the %s of %s failed: %s\n(%s)", e.What, e.ID, msg, e.Failure.Code)
}

// WaitCloudConnection polls a connection while it is being verified, and
// returns it once it is not. A failed check is a *MigrateFailed.
func WaitCloudConnection(ctx context.Context, c *Client, id string, raw *[]byte) (*CloudConnection, error) {
	for {
		var conn CloudConnection
		res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/cloud-connections/" + url.PathEscape(id)}, &conn)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			*raw = res.Body
		}
		if conn.Status != "verifying" {
			if conn.Status == "failed" {
				return &conn, &MigrateFailed{What: "connection check", ID: conn.ID, Failure: conn.Failure}
			}
			return &conn, nil
		}
		select {
		case <-ctx.Done():
			return &conn, ctx.Err()
		case <-time.After(MigratePollInterval):
		}
	}
}

// WaitCloudScan polls a scan until it has succeeded or failed. onUpdate,
// when set, sees each change of status. A failed scan is a *MigrateFailed.
func WaitCloudScan(ctx context.Context, c *Client, id string, onUpdate func(*CloudScan), raw *[]byte) (*CloudScan, error) {
	last := ""
	for {
		var s CloudScan
		res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/cloud-scans/" + url.PathEscape(id)}, &s)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			*raw = res.Body
		}
		if s.Status != last && onUpdate != nil {
			onUpdate(&s)
		}
		last = s.Status
		if s.Done() {
			if s.Status == "failed" {
				return &s, &MigrateFailed{What: "scan", ID: s.ID, Failure: s.Failure}
			}
			return &s, nil
		}
		select {
		case <-ctx.Done():
			return &s, ctx.Err()
		case <-time.After(MigratePollInterval):
		}
	}
}
