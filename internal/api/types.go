package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

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
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	PlanSlug            *string `json:"plan_slug"`
	ImageSlug           *string `json:"image_slug"`
	Encrypted           bool    `json:"encrypted"`
	Region              *string `json:"region"`
	Zone                *string `json:"zone"`
	NetworkID           *string `json:"network_id"`
	SubnetID            *string `json:"subnet_id"`
	PublicIPv4          *string `json:"public_ipv4"`
	PrivateIPv4         *string `json:"private_ipv4"`
	SecurityGroupID     *string `json:"security_group_id"`
	PrivateNetworkState string  `json:"private_network_state"`
	PrivateNetworkIP    *string `json:"private_network_ip"`
	DesiredState        string  `json:"desired_state"`
	ObservedState       string  `json:"observed_state"`
	Spec                *struct {
		VCPU     int `json:"vcpu"`
		MemoryMB int `json:"memory_mb"`
		DiskGB   int `json:"disk_gb"`
	} `json:"spec"`
	Failure   *Failure `json:"failure"`
	CreatedAt *string  `json:"created_at"`
}

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
	// Encrypted is an image whose disk is encrypted at rest, each VM with its
	// own key; EncryptedVariantOf is then the slug of the plain image it pairs
	// with. Both are absent from an older API, which reads as plain.
	Encrypted          bool    `json:"encrypted"`
	EncryptedVariantOf *string `json:"encrypted_variant_of"`
}

type Region struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Placements []struct {
		Kind              string  `json:"kind"`
		Zone              string  `json:"zone"`
		Available         bool    `json:"available"`
		UnavailableReason *string `json:"unavailable_reason"`
		// PrivateNetworkCIDR is the zone's private database network range,
		// when its VMs can attach to it; nil otherwise.
		PrivateNetworkCIDR *string `json:"private_network_cidr"`
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

type InstanceOrder = Order

// DatabaseOrder is creating a database: paying, then provisioning. It never
// carries a password.
type DatabaseOrder = Order

func (o *Order) Done() bool {
	return o.Status == "provisioned" || o.Status == "failed" || o.Status == "payment_failed"
}

func (o *Order) ResourceID() string {
	if o.InstanceID != "" {
		return o.InstanceID
	}
	return o.DatabaseID
}

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
	if why, ok := orderFailureText[code]; ok {
		return fmt.Sprintf("order %s did not provision: %s (%s)", e.Order.ID, why, code)
	}
	return fmt.Sprintf("order %s did not provision: %s", e.Order.ID, code)
}

var orderFailureText = map[string]string{
	"payment_expired":      "it was not paid within an hour",
	"organization_deleted": "it was cancelled because its organization was deleted; any payment taken is back in your credit",
	"card_declined":        "the card was declined",
	"insufficient_credit":  "there was not enough credit to pay for it",
	"database_name_taken":  "a database with that name already exists",
	"provisioning_failed":  "provisioning could not start; any payment taken is back in your credit",
}

type OperationFailed struct{ Op *Operation }

func (e *OperationFailed) Error() string {
	if e.Op.Failure != nil {
		return fmt.Sprintf("%s failed: %s (%s, operation %s)", e.Op.Kind, e.Op.Failure.Reason, e.Op.Failure.Code, e.Op.ID)
	}
	return fmt.Sprintf("%s failed (operation %s)", e.Op.Kind, e.Op.ID)
}

// ObjectStorageLimits holds organization-level object storage limits.
type ObjectStorageLimits struct {
	MaxBuckets      int   `json:"max_buckets"`
	MaxStorageBytes int64 `json:"max_storage_bytes"`
	MaxObjects      int64 `json:"max_objects"`
}

// ObjectStorageStatus says whether object storage is on for the
// organization (a closed beta) and where its S3 endpoint is.
type ObjectStorageStatus struct {
	Enabled             bool                 `json:"enabled"`
	Endpoint            string               `json:"endpoint"`
	Beta                bool                 `json:"beta"`
	DirectUploadEnabled bool                 `json:"direct_upload_enabled"`
	Limits              *ObjectStorageLimits `json:"limits"`
}

// Bucket is an object storage bucket. QuotaBytes 0 is no limit.
type Bucket struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Region         string   `json:"region"`
	Endpoint       string   `json:"endpoint"`
	QuotaBytes     int64    `json:"quota_bytes"`
	SizeBytes      int64    `json:"size_bytes"`
	ObjectCount    int64    `json:"object_count"`
	Versioning     bool     `json:"versioning"`
	ObjectLock     bool     `json:"object_lock"`
	PublicPrefixes []string `json:"public_prefixes"`
	State          string   `json:"state"`
	CreatedAt      string   `json:"created_at"`
	OperationID    string   `json:"operation_id,omitempty"`
}

// ObjectStorageKey is an S3 access key. SecretAccessKey and Endpoint are
// set only in the answer to its create.
type ObjectStorageKey struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Region          string   `json:"region"`
	AccessKeyID     string   `json:"access_key_id"`
	ReadOnly        bool     `json:"read_only"`
	Buckets         []string `json:"buckets"`
	CreatedAt       string   `json:"created_at"`
	SecretAccessKey string   `json:"secret_access_key,omitempty"`
	Endpoint        string   `json:"endpoint,omitempty"`
}

// ObjectStorageHourUsage is one hour of object storage transfer and request counts.
type ObjectStorageHourUsage struct {
	Hour          string `json:"hour"`
	BytesSent     int64  `json:"bytes_sent"`
	BytesReceived int64  `json:"bytes_received"`
	ReadOps       int64  `json:"read_ops"`
	WriteOps      int64  `json:"write_ops"`
}

// ObjectStorageUsage summarizes transfer and request counts over a time range.
type ObjectStorageUsage struct {
	Range              string                   `json:"range"`
	TotalBytesSent     int64                    `json:"total_bytes_sent"`
	TotalBytesReceived int64                    `json:"total_bytes_received"`
	TotalReadOps       int64                    `json:"total_read_ops"`
	TotalWriteOps      int64                    `json:"total_write_ops"`
	Hours              []ObjectStorageHourUsage `json:"hours"`
}

// BucketStats holds live stored bytes and object count for a bucket.
type BucketStats struct {
	SizeBytes   int64 `json:"size_bytes"`
	ObjectCount int64 `json:"object_count"`
}

// EmptyBucketResult summarizes emptying a bucket.
type EmptyBucketResult struct {
	DeletedCount     int      `json:"deleted_count"`
	AbortedUploads   int      `json:"aborted_uploads"`
	LockedCount      int      `json:"locked_count"`
	SampleLockedKeys []string `json:"sample_locked_keys"`
}

// StorageTag is a key-value tag on a bucket or object.
type StorageTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// BucketObject summarizes one object in a bucket.
type BucketObject struct {
	Key          string `json:"key"`
	SizeBytes    int64  `json:"size_bytes"`
	ETag         string `json:"etag"`
	ContentType  string `json:"content_type"`
	LastModified string `json:"last_modified"`
	StorageClass string `json:"storage_class"`
	VersionID    string `json:"version_id"`
	IsLatest     bool   `json:"is_latest"`
	Public       bool   `json:"public"`
	ObjectPublic bool   `json:"object_public"`
	PublicSource string `json:"public_source"`
	PublicURL    string `json:"public_url"`
}

// BucketObjectVersion is one version or delete marker of an object.
type BucketObjectVersion struct {
	Key            string `json:"key"`
	VersionID      string `json:"version_id"`
	IsLatest       bool   `json:"is_latest"`
	IsDeleteMarker bool   `json:"is_delete_marker"`
	SizeBytes      int64  `json:"size_bytes"`
	ETag           string `json:"etag"`
	LastModified   string `json:"last_modified"`
	StorageClass   string `json:"storage_class"`
}

// BucketObjectList is a page of folders and objects, or object versions.
type BucketObjectList struct {
	Prefix              string                `json:"prefix"`
	Folders             []string              `json:"folders"`
	Objects             []BucketObject        `json:"objects"`
	NextCursor          string                `json:"next_cursor"`
	Versions            []BucketObjectVersion `json:"versions"`
	NextKeyMarker       string                `json:"next_key_marker"`
	NextVersionIDMarker string                `json:"next_version_id_marker"`
	IsTruncated         bool                  `json:"is_truncated"`
}

// BucketObjectMeta holds full properties, metadata, tags, lock and visibility for one object.
type BucketObjectMeta struct {
	Key                string            `json:"key"`
	SizeBytes          int64             `json:"size_bytes"`
	ETag               string            `json:"etag"`
	ContentType        string            `json:"content_type"`
	CacheControl       string            `json:"cache_control"`
	ContentDisposition string            `json:"content_disposition"`
	LastModified       string            `json:"last_modified"`
	StorageClass       string            `json:"storage_class"`
	VersionID          string            `json:"version_id"`
	Metadata           map[string]string `json:"metadata"`
	Tags               []StorageTag      `json:"tags"`
	RetentionMode      string            `json:"retention_mode"`
	RetainUntil        *string           `json:"retain_until"`
	LegalHold          bool              `json:"legal_hold"`
	Public             bool              `json:"public"`
	ObjectPublic       bool              `json:"object_public"`
	PublicSource       string            `json:"public_source"`
	PublicURL          string            `json:"public_url"`
}

// PresignedURL is a short-lived direct download or upload URL.
type PresignedURL struct {
	Direct           bool   `json:"direct"`
	URL              string `json:"url"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

// ObjectVisibility describes an object's public or private read state.
type ObjectVisibility struct {
	Key          string `json:"key"`
	Public       bool   `json:"public"`
	ObjectPublic bool   `json:"object_public"`
	PublicSource string `json:"public_source"`
	PublicURL    string `json:"public_url"`
}

// DeleteObjectFailure reports one object that could not be deleted.
type DeleteObjectFailure struct {
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	Code      string `json:"code"`
	Locked    bool   `json:"locked"`
}

// DeleteObjectsResult summarizes a batch or prefix object deletion.
type DeleteObjectsResult struct {
	Deleted int                   `json:"deleted"`
	Failed  []DeleteObjectFailure `json:"failed"`
}

// BucketCORSRule is one cross-origin resource sharing rule on a bucket.
type BucketCORSRule struct {
	ID             string   `json:"id"`
	AllowedOrigins []string `json:"allowed_origins"`
	AllowedMethods []string `json:"allowed_methods"`
	AllowedHeaders []string `json:"allowed_headers"`
	ExposeHeaders  []string `json:"expose_headers"`
	MaxAgeSeconds  int      `json:"max_age_seconds"`
}

// BucketLifecycleRule is one object expiration or cleanup rule on a bucket.
type BucketLifecycleRule struct {
	ID                           string `json:"id"`
	Prefix                       string `json:"prefix"`
	Enabled                      bool   `json:"enabled"`
	ExpirationDays               int    `json:"expiration_days"`
	NoncurrentExpirationDays     int    `json:"noncurrent_expiration_days"`
	AbortIncompleteMultipartDays int    `json:"abort_incomplete_multipart_days"`
	ExpiredObjectDeleteMarker    bool   `json:"expired_object_delete_marker"`
}

// BucketObjectLockConfig holds default object-lock retention settings on a bucket.
type BucketObjectLockConfig struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	Days    int    `json:"days"`
	Years   int    `json:"years"`
}

// MultipartStart describes an active multipart upload session.
type MultipartStart struct {
	UploadID      string `json:"upload_id"`
	Mode          string `json:"mode"`
	PartSizeBytes int64  `json:"part_size_bytes"`
}

// MultipartPart identifies one uploaded part of a multipart upload.
type MultipartPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

// DynamicIPEvent is one change in a VM's dynamic IP history.
type DynamicIPEvent struct {
	Kind        string  `json:"kind"`
	FromAddress *string `json:"from_address"`
	ToAddress   *string `json:"to_address"`
	Degraded    bool    `json:"degraded"`
	OccurredAt  string  `json:"occurred_at"`
}

// DynamicIP is a VM's scheduled outbound IPv4 rotation configuration and state.
type DynamicIP struct {
	InstanceID       string           `json:"instance_id"`
	Schedule         string           `json:"schedule"`
	Connections      string           `json:"connections"`
	OnDemand         bool             `json:"on_demand"`
	State            string           `json:"state"`
	CurrentAddress   *string          `json:"current_address"`
	NextRotationAt   *string          `json:"next_rotation_at"`
	LastRotatedAt    *string          `json:"last_rotated_at"`
	SurchargePercent float64          `json:"surcharge_percent"`
	OnDemandUsed     int              `json:"on_demand_used"`
	OnDemandLimit    int              `json:"on_demand_limit"`
	FailureReason    *string          `json:"failure_reason"`
	Events           []DynamicIPEvent `json:"events"`
}

// DynamicIPFailed is returned when enabling dynamic IP finishes in the failed state.
type DynamicIPFailed struct{ DynamicIP *DynamicIP }

func (e *DynamicIPFailed) Error() string {
	if e.DynamicIP != nil && e.DynamicIP.FailureReason != nil && *e.DynamicIP.FailureReason != "" {
		if e.DynamicIP.InstanceID != "" {
			return fmt.Sprintf("dynamic IP on %s failed: %s", e.DynamicIP.InstanceID, *e.DynamicIP.FailureReason)
		}
		return fmt.Sprintf("dynamic IP failed: %s", *e.DynamicIP.FailureReason)
	}
	if e.DynamicIP != nil && e.DynamicIP.InstanceID != "" {
		return fmt.Sprintf("dynamic IP on %s failed", e.DynamicIP.InstanceID)
	}
	return "dynamic IP failed"
}
