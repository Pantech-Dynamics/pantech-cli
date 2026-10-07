package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

type SSHAccessGrant struct {
	ID         string  `json:"id"`
	InstanceID string  `json:"instance_id"`
	Status     string  `json:"status"`
	Host       *string `json:"host"`
	Port       *int    `json:"port"`
	ExpiresAt  *string `json:"expires_at"`
}

type SecurityGroupRule struct {
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	PortRange string `json:"port_range"`
	CIDR      string `json:"cidr"`
}

type SecurityGroup struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Rules         []SecurityGroupRule `json:"rules"`
	DesiredState  string              `json:"desired_state"`
	ObservedState string              `json:"observed_state"`
	CreatedAt     *string             `json:"created_at"`
}

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

type Subnet struct {
	ID            string  `json:"id"`
	NetworkID     string  `json:"network_id"`
	Name          string  `json:"name"`
	CIDR          string  `json:"cidr"`
	Zone          *string `json:"zone"`
	ObservedState string  `json:"observed_state"`
}

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
	// InSync is false while an attach or detach has not reached the address.
	InSync    *bool   `json:"in_sync"`
	CreatedAt *string `json:"created_at"`
}

// Applying reports whether a change has not reached the resource yet:
// in_sync is false. A missing in_sync is taken as in sync.
func Applying(inSync *bool) bool { return inSync != nil && !*inSync }

// Detached reports whether a static_nat address is held without a VM.
func (p *PublicIP) Detached() bool {
	return p.Purpose == "static_nat" && firstNonEmpty(p.InstanceID, p.InstanceName) == ""
}

func firstNonEmpty(ss ...*string) string {
	for _, s := range ss {
		if s != nil && *s != "" {
			return *s
		}
	}
	return ""
}

type LoadBalancerMember struct {
	InstanceID    string  `json:"instance_id"`
	InstanceName  *string `json:"instance_name"`
	DesiredState  string  `json:"desired_state"`
	ObservedState string  `json:"observed_state"`
}

type LoadBalancer struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	PublicIPID      string               `json:"public_ip_id"`
	PublicIPAddress *string              `json:"public_ip_address"`
	NetworkID       *string              `json:"network_id"`
	SubnetID        string               `json:"subnet_id"`
	Protocol        string               `json:"protocol"`
	Algorithm       string               `json:"algorithm"`
	PublicPort      int                  `json:"public_port"`
	PrivatePort     int                  `json:"private_port"`
	CIDRList        []string             `json:"cidr_list"`
	Members         []LoadBalancerMember `json:"members"`
	DesiredState    string               `json:"desired_state"`
	ObservedState   string               `json:"observed_state"`
	InSync          *bool                `json:"in_sync"`
	CreatedAt       *string              `json:"created_at"`
}

type KubernetesVersion struct {
	ID          string `json:"id"`
	ZoneID      string `json:"zone_id"`
	Version     string `json:"version"`
	Status      string `json:"status"`
	MinCPU      int    `json:"min_cpu"`
	MinMemoryMB int    `json:"min_memory_mb"`
}

// KubernetesVersionList is the versions list: not paged, with the zones
// that offer 3 control nodes.
type KubernetesVersionList struct {
	Data      []KubernetesVersion `json:"data"`
	HAZoneIDs []string            `json:"ha_zone_ids"`
}

type KubernetesAutoscaling struct {
	Enabled    bool `json:"enabled"`
	MinWorkers int  `json:"min_workers"`
	MaxWorkers int  `json:"max_workers"`
}

type KubernetesCluster struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	ZoneID              string  `json:"zone_id"`
	KubernetesVersionID string  `json:"kubernetes_version_id"`
	KubernetesVersion   string  `json:"kubernetes_version"`
	NetworkID           *string `json:"network_id"`
	SubnetID            *string `json:"subnet_id"`
	NodePlanID          string  `json:"node_plan_id"`
	Node                struct {
		VCPU     int `json:"vcpu"`
		MemoryMB int `json:"memory_mb"`
		DiskGB   int `json:"disk_gb"`
	} `json:"node"`
	ControlNodes      int                   `json:"control_nodes"`
	Workers           int                   `json:"workers"`
	Nodes             int                   `json:"nodes"`
	DesiredState      string                `json:"desired_state"`
	ObservedState     string                `json:"observed_state"`
	InSync            *bool                 `json:"in_sync"`
	FailureCode       *string               `json:"failure_code"`
	FailureReason     *string               `json:"failure_reason"`
	AvailableUpgrades []KubernetesVersion   `json:"available_upgrades"`
	Autoscaling       KubernetesAutoscaling `json:"autoscaling"`
	APIAllowedCIDRs   []string              `json:"api_allowed_cidrs"`
	Endpoint          *string               `json:"endpoint"`
	VolumeStorageGB   int                   `json:"volume_storage_gb"`
	CreatedAt         *string               `json:"created_at"`
}

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

type DatabaseAccessRule struct {
	CIDR     string `json:"cidr"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

type DatabaseEffectiveAccessRule struct {
	CIDR   string `json:"cidr"`
	Port   int    `json:"port"`
	Source string `json:"source"`
}

type DatabaseIgnoredSecurityGroupRule struct {
	SecurityGroupID string  `json:"security_group_id"`
	Direction       string  `json:"direction"`
	Protocol        string  `json:"protocol"`
	PortRange       *string `json:"port_range"`
	CIDR            string  `json:"cidr"`
	Reason          string  `json:"reason"`
}

type Database struct {
	ID                        string                             `json:"id"`
	Name                      string                             `json:"name"`
	Engine                    string                             `json:"engine"`
	Version                   string                             `json:"version"`
	Port                      int                                `json:"port"`
	PlanID                    string                             `json:"plan_id"`
	DataVolumeSizeGB          int                                `json:"data_volume_size_gb"`
	PendingDataVolumeSizeGB   *int                               `json:"pending_data_volume_size_gb"`
	ZoneID                    string                             `json:"zone_id"`
	SubnetID                  *string                            `json:"subnet_id"`
	Hostname                  *string                            `json:"hostname"`
	PrivateIP                 *string                            `json:"private_ip"`
	AdminUsername             string                             `json:"admin_username"`
	DesiredState              string                             `json:"desired_state"`
	ObservedState             string                             `json:"observed_state"`
	Generation                int                                `json:"generation"`
	ObservedGeneration        int                                `json:"observed_generation"`
	AccessRules               []DatabaseAccessRule               `json:"access_rules"`
	SecurityGroupIDs          []string                           `json:"security_group_ids"`
	EffectiveAccessRules      []DatabaseEffectiveAccessRule      `json:"effective_access_rules"`
	IgnoredSecurityGroupRules []DatabaseIgnoredSecurityGroupRule `json:"ignored_security_group_rules"`
	FailureCode               *string                            `json:"failure_code"`
	CreatedAt                 *string                            `json:"created_at"`
}

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

type DatabasePasswordAccepted struct {
	Accepted
	PasswordReturned bool    `json:"password_returned"`
	Password         *string `json:"password"`
}

func ListAllRaw(ctx context.Context, c *Client, path string, query url.Values) ([]json.RawMessage, error) {
	return ListAll[json.RawMessage](ctx, c, path, query)
}

func Get(ctx context.Context, c *Client, path string, out any) ([]byte, error) {
	res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path}, out)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}
