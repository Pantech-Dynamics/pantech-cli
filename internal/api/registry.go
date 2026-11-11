package api

// The private container registry: the organization's namespace, its
// repositories and tags, and registry tokens. Only what the CLI shows is
// here; --json prints the API's own body.

// DefaultRegistryHost is where images are pushed and pulled.
const DefaultRegistryHost = "registry.pantechdynamics.com"

// RegistryNamespace is the organization's one namespace.
type RegistryNamespace struct {
	Name              string  `json:"name"`
	RegistryHost      string  `json:"registry_host"`
	CreateOnPush      bool    `json:"create_on_push"`
	RepositoryCount   int     `json:"repository_count"`
	StorageBytes      int64   `json:"storage_bytes"`
	StorageLimitBytes int64   `json:"storage_limit_bytes"`
	RepositoryLimit   int     `json:"repository_limit"`
	CreatedAt         *string `json:"created_at"`
}

// RegistryRetention is a repository's retention policy; a nil field is no
// rule. A PATCH replaces the whole policy.
type RegistryRetention struct {
	KeepLast          *int    `json:"keep_last"`
	UntaggedAfterDays *int    `json:"untagged_after_days"`
	KeepPattern       *string `json:"keep_pattern"`
}

// RegistryRepository is one repository, <namespace>/<name>. Status is
// active, deleting or suspended.
type RegistryRepository struct {
	ID              string             `json:"id"`
	Namespace       string             `json:"namespace"`
	Name            string             `json:"name"`
	FullName        string             `json:"full_name"`
	Description     string             `json:"description"`
	Visibility      string             `json:"visibility"`
	ImmutableTags   bool               `json:"immutable_tags"`
	Retention       *RegistryRetention `json:"retention"`
	TagCount        int                `json:"tag_count"`
	StorageBytes    int64              `json:"storage_bytes"`
	LastPushedAt    *string            `json:"last_pushed_at"`
	Status          string             `json:"status"`
	SuspendedReason *string            `json:"suspended_reason"`
	CreatedAt       *string            `json:"created_at"`
	Version         int                `json:"version"`
}

// RegistryTag is a tag and the image it points at.
type RegistryTag struct {
	Name         string   `json:"name"`
	Digest       string   `json:"digest"`
	MediaType    string   `json:"media_type"`
	SizeBytes    int64    `json:"size_bytes"`
	Platforms    []string `json:"platforms"`
	PushedAt     *string  `json:"pushed_at"`
	LastPulledAt *string  `json:"last_pulled_at"`
	Immutable    bool     `json:"immutable"`
}

// RegistryToken is a credential for the registry only. Secret is set in the
// answer to a create alone.
type RegistryToken struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Permission   string   `json:"permission"`
	Repositories []string `json:"repositories"`
	ExpiresAt    *string  `json:"expires_at"`
	LastUsedAt   *string  `json:"last_used_at"`
	CreatedAt    *string  `json:"created_at"`
	RevokedAt    *string  `json:"revoked_at"`
	Secret       string   `json:"secret,omitempty"`
}
