package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// More of the public API's resources, as much of each as the CLI shows.

// SSHAccessGrant is port 22 opened to one caller's address for a while.
type SSHAccessGrant struct {
	ID         string  `json:"id"`
	InstanceID string  `json:"instance_id"`
	Status     string  `json:"status"`
	Host       *string `json:"host"`
	Port       *int    `json:"port"`
	ExpiresAt  *string `json:"expires_at"`
}

// SecurityGroupRule is one firewall rule of a security group.
type SecurityGroupRule struct {
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	PortRange string `json:"port_range"`
	CIDR      string `json:"cidr"`
}

// SecurityGroup is a reusable firewall for standard VMs.
type SecurityGroup struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Rules         []SecurityGroupRule `json:"rules"`
	DesiredState  string              `json:"desired_state"`
	ObservedState string              `json:"observed_state"`
	CreatedAt     *string             `json:"created_at"`
}

// Volume is a data disk.
type Volume struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	SizeGB               int     `json:"size_gb"`
	DiskOfferingSlug     string  `json:"disk_offering_slug"`
	StorageType          *string `json:"storage_type"`
	Region               *string `json:"region"`
	Zone                 *string `json:"zone"`
	AttachedInstanceID   *string `json:"attached_instance_id"`
	AttachedInstanceName *string `json:"attached_instance_name"`
	MountPoint           *string `json:"mount_point"`
	DesiredState         string  `json:"desired_state"`
	ObservedState        string  `json:"observed_state"`
	CreatedAt            *string `json:"created_at"`
}

// Snapshot is a point-in-time copy of a VM's root disk or a volume.
type Snapshot struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	InstanceID         *string `json:"instance_id"`
	SourceInstanceName *string `json:"source_instance_name"`
	VolumeID           *string `json:"volume_id"`
	VolumeName         *string `json:"volume_name"`
	DatabaseID         *string `json:"database_id"`
	Region             *string `json:"region"`
	Trigger            string  `json:"trigger"`
	SizeBytes          int64   `json:"size_bytes"`
	DesiredState       string  `json:"desired_state"`
	ObservedState      string  `json:"observed_state"`
	CreatedAt          *string `json:"created_at"`
}

// Network is a VPC.
type Network struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	CIDR          string  `json:"cidr"`
	Region        *string `json:"region"`
	Zone          *string `json:"zone"`
	InstanceCount *int    `json:"instance_count"`
	DesiredState  string  `json:"desired_state"`
	ObservedState string  `json:"observed_state"`
	CreatedAt     *string `json:"created_at"`
}

// Subnet is a range inside a VPC that VMs are placed in.
type Subnet struct {
	ID            string  `json:"id"`
	NetworkID     string  `json:"network_id"`
	Name          string  `json:"name"`
	CIDR          string  `json:"cidr"`
	Zone          *string `json:"zone"`
	ObservedState string  `json:"observed_state"`
}

// PublicIP is a public address in a VPC.
type PublicIP struct {
	ID            string  `json:"id"`
	NetworkID     string  `json:"network_id"`
	NetworkName   *string `json:"network_name"`
	Purpose       string  `json:"purpose"`
	Address       *string `json:"address"`
	InstanceID    *string `json:"instance_id"`
	InstanceName  *string `json:"instance_name"`
	Region        *string `json:"region"`
	Zone          *string `json:"zone"`
	DesiredState  string  `json:"desired_state"`
	ObservedState string  `json:"observed_state"`
	CreatedAt     *string `json:"created_at"`
}

// DatabaseEngine is an engine and the version lines offered for new databases.
type DatabaseEngine struct {
	Engine      string `json:"engine"`
	DisplayName string `json:"display_name"`
	Port        int    `json:"port"`
	Versions    []struct {
		Version string   `json:"version"`
		EOLDate *string  `json:"eol_date"`
		Zones   []string `json:"zones"`
	} `json:"versions"`
	// Storage is the data disk sizes a new database may have, one entry per
	// zone where the engine can be created and storage is offered.
	Storage []DatabaseStorageOption `json:"storage"`
}

// DatabaseStorageOption is one zone's allowed data disk sizes for an engine.
// MinGB 0 means the plan's disk_gb is the minimum; otherwise the minimum is
// the larger of the two. Above the minimum a size is a multiple of StepGB.
type DatabaseStorageOption struct {
	ZoneID               string  `json:"zone_id"`
	MinGB                int     `json:"min_gb"`
	MinIsPlanDisk        bool    `json:"min_is_plan_disk"`
	MaxGB                int     `json:"max_gb"`
	StepGB               int     `json:"step_gb"`
	PricePerGBMonthMinor *int64  `json:"price_per_gb_month_minor"`
	Currency             *string `json:"currency"`
}

// DatabaseAccessRule allows TCP to the engine's port from one CIDR.
type DatabaseAccessRule struct {
	CIDR     string `json:"cidr"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

// DatabaseEffectiveAccessRule is one range the database allows, and where it
// comes from: "access_rule", or "security_group:sg_…".
type DatabaseEffectiveAccessRule struct {
	CIDR   string `json:"cidr"`
	Port   int    `json:"port"`
	Source string `json:"source"`
}

// DatabaseIgnoredSecurityGroupRule is a rule of an attached security group
// that does not apply to the database, and why.
type DatabaseIgnoredSecurityGroupRule struct {
	SecurityGroupID string  `json:"security_group_id"`
	Direction       string  `json:"direction"`
	Protocol        string  `json:"protocol"`
	PortRange       *string `json:"port_range"`
	CIDR            string  `json:"cidr"`
	Reason          string  `json:"reason"`
}

// Database is a managed database. It never carries a password.
type Database struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Engine           string `json:"engine"`
	Version          string `json:"version"`
	Port             int    `json:"port"`
	PlanID           string `json:"plan_id"`
	DataVolumeSizeGB int    `json:"data_volume_size_gb"`
	// PendingDataVolumeSizeGB is the size a storage resize in flight is
	// growing the data disk to; nil when none.
	PendingDataVolumeSizeGB *int                 `json:"pending_data_volume_size_gb"`
	ZoneID                  string               `json:"zone_id"`
	SubnetID                *string              `json:"subnet_id"`
	Hostname                *string              `json:"hostname"`
	PrivateIP               *string              `json:"private_ip"`
	AdminUsername           string               `json:"admin_username"`
	DesiredState            string               `json:"desired_state"`
	ObservedState           string               `json:"observed_state"`
	Generation              int                  `json:"generation"`
	ObservedGeneration      int                  `json:"observed_generation"`
	AccessRules             []DatabaseAccessRule `json:"access_rules"`
	// Security groups only add address ranges to the allow-list.
	SecurityGroupIDs          []string                           `json:"security_group_ids"`
	EffectiveAccessRules      []DatabaseEffectiveAccessRule      `json:"effective_access_rules"`
	IgnoredSecurityGroupRules []DatabaseIgnoredSecurityGroupRule `json:"ignored_security_group_rules"`
	FailureCode               *string                            `json:"failure_code"`
	CreatedAt                 *string                            `json:"created_at"`
}

// DatabaseOrderAccepted is the 202 of creating a database. Password is set
// only when the platform generated it and this request created the order:
// it is shown once and can never be read again.
type DatabaseOrderAccepted struct {
	OrderID          string  `json:"order_id"`
	DatabaseID       string  `json:"database_id"`
	Status           string  `json:"status"`
	OperationID      *string `json:"operation_id"`
	AmountMinor      int64   `json:"amount_minor"`
	Currency         string  `json:"currency"`
	AdminUsername    string  `json:"admin_username"`
	PasswordReturned bool    `json:"password_returned"`
	Password         *string `json:"password"`
}

// DatabasePasswordAccepted is the 202 of resetting a database's password,
// carrying the generated password once.
type DatabasePasswordAccepted struct {
	Accepted
	PasswordReturned bool    `json:"password_returned"`
	Password         *string `json:"password"`
}

// ListAllRaw follows next_cursor to the end like ListAll, keeping each item
// as the API sent it, for --json.
func ListAllRaw(ctx context.Context, c *Client, path string, query url.Values) ([]json.RawMessage, error) {
	return ListAll[json.RawMessage](ctx, c, path, query)
}

// Get reads one resource into out, returning the raw body for --json.
func Get(ctx context.Context, c *Client, path string, out any) ([]byte, error) {
	res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path}, out)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}
