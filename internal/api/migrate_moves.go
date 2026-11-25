package api

import (
	"context"
	"net/http"
	"time"
)

// Moving what a migration plan lists: bucket imports, server copies,
// database imports and Kubernetes moves. Only what the CLI shows is here;
// --json prints the API's own body.

// CopyAccess is a connection's opt-in access for copying chosen buckets,
// through a copy role separate from its read-only one. Status is
// not_requested, awaiting_role, verifying, verified or failed.
type CopyAccess struct {
	ConnectionID string        `json:"connection_id"`
	Status       string        `json:"status"`
	Failure      *CloudFailure `json:"failure"`
	RoleARN      *string       `json:"role_arn"`
	Buckets      []struct {
		Name       string        `json:"name"`
		Region     *string       `json:"region"`
		Versioning *string       `json:"versioning"`
		Ready      bool          `json:"ready"`
		Failure    *CloudFailure `json:"failure"`
	} `json:"buckets"`
	VerifiedAt *string `json:"verified_at"`
	Setup      struct {
		Subject  string  `json:"subject"`
		Template *string `json:"template"`
	} `json:"setup"`
}

// BucketImport is one source bucket copied into a Pantech bucket. Status is
// queued, running, succeeded, failed or cancelled.
type BucketImport struct {
	ID           string  `json:"id"`
	ConnectionID string  `json:"connection_id"`
	MigrationID  *string `json:"migration_id"`
	Source       struct {
		Provider string  `json:"provider"`
		Bucket   string  `json:"bucket"`
		Region   *string `json:"region"`
	} `json:"source"`
	Destination struct {
		BucketID   string `json:"bucket_id"`
		BucketName string `json:"bucket_name"`
		PublicRead bool   `json:"public_read"`
	} `json:"destination"`
	Status          string        `json:"status"`
	Failure         *CloudFailure `json:"failure"`
	CancelRequested bool          `json:"cancel_requested"`
	Pass            int           `json:"pass"`
	Progress        struct {
		ObjectsCopied  int64 `json:"objects_copied"`
		ObjectsSkipped int64 `json:"objects_skipped"`
		ObjectsFailed  int64 `json:"objects_failed"`
		BytesCopied    int64 `json:"bytes_copied"`
		BytesSkipped   int64 `json:"bytes_skipped"`
	} `json:"progress"`
	TotalBytesCopied int64   `json:"total_bytes_copied"`
	MirrorDeletes    bool    `json:"mirror_deletes"`
	ObjectsDeleted   int64   `json:"objects_deleted"`
	CreatedAt        *string `json:"created_at"`
	FinishedAt       *string `json:"finished_at"`
	Failures         []struct {
		Key     string `json:"key"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"failures"`
}

// ServerCopy is one planned server's disks copied into a new Pantech
// instance. Status is waiting, copying, synced, importing,
// creating_instance, completed, failed or cancelled.
type ServerCopy struct {
	ID          string        `json:"id"`
	MigrationID string        `json:"migration_id"`
	ItemKey     string        `json:"item_key"`
	Source      string        `json:"source"`
	Status      string        `json:"status"`
	Failure     *CloudFailure `json:"failure"`
	OSFamily    string        `json:"os_family"`
	Instance    struct {
		Name     string  `json:"name"`
		PlanSlug string  `json:"plan_slug"`
		Region   string  `json:"region"`
		ID       *string `json:"id"`
	} `json:"instance"`
	Disks []struct {
		Index     int    `json:"index"`
		Role      string `json:"role"`
		Source    string `json:"source"`
		SizeBytes int64  `json:"size_bytes"`
	} `json:"disks"`
	Flags []MigrationFlag `json:"flags"`
	Pass  *struct {
		Number int    `json:"number"`
		Kind   string `json:"kind"`
	} `json:"pass"`
	PendingPass *string `json:"pending_pass"`
	Progress    struct {
		BytesTotal    int64  `json:"bytes_total"`
		BytesUploaded int64  `json:"bytes_uploaded"`
		ETASeconds    *int64 `json:"eta_seconds"`
	} `json:"progress"`
	SyncedAt  *string `json:"synced_at"`
	ExpiresAt *string `json:"expires_at"`
	CreatedAt *string `json:"created_at"`
}

// ServerCopyTemplate is the AWS copy role's CloudFormation template for one
// planned server.
type ServerCopyTemplate struct {
	Template    string            `json:"template"`
	SnapshotTag map[string]string `json:"snapshot_tag"`
}

// DatabaseImport is one of a plan's databases moved into a Pantech database
// through the Migrate agent.
type DatabaseImport struct {
	ID                    string        `json:"id"`
	MigrationID           string        `json:"migration_id"`
	ItemKey               string        `json:"item_key"`
	DestinationDatabaseID string        `json:"destination_database_id"`
	TargetDatabase        string        `json:"target_database"`
	Mode                  string        `json:"mode"`
	SourceRef             string        `json:"source_ref"`
	Engine                string        `json:"engine"`
	Status                string        `json:"status"`
	Failure               *CloudFailure `json:"failure"`
	AgentConnected        bool          `json:"agent_connected"`
	LagBytes              *int64        `json:"lag_bytes"`
	Readiness             *struct {
		Blocked bool `json:"blocked"`
		Issues  []struct {
			Code     string  `json:"code"`
			Blocking bool    `json:"blocking"`
			Message  string  `json:"message"`
			Fix      *string `json:"fix"`
			Subject  *string `json:"subject"`
		} `json:"issues"`
	} `json:"readiness"`
	Tables []struct {
		Schema       *string `json:"schema"`
		Name         string  `json:"name"`
		Status       string  `json:"status"`
		RowsCopied   int64   `json:"rows_copied"`
		Verification *struct {
			Passed bool `json:"passed"`
		} `json:"verification"`
	} `json:"tables"`
	CreatedAt  *string `json:"created_at"`
	FinishedAt *string `json:"finished_at"`
}

// KubernetesMove is a plan's Kubernetes cluster moved into a Pantech
// cluster. Resources, Volumes and Holds are only in a single move's answer.
type KubernetesMove struct {
	ID          string        `json:"id"`
	MigrationID string        `json:"migration_id"`
	ItemKey     string        `json:"item_key"`
	ClusterID   string        `json:"cluster_id"`
	SourceName  string        `json:"source_name"`
	Status      string        `json:"status"`
	Failure     *CloudFailure `json:"failure"`
	Options     struct {
		IncludeSecrets       bool     `json:"include_secrets"`
		CopyVolumes          bool     `json:"copy_volumes"`
		PublishLoadBalancers bool     `json:"publish_load_balancers"`
		Namespaces           []string `json:"namespaces"`
	} `json:"options"`
	Summary struct {
		Objects int `json:"objects"`
		Refused int `json:"refused"`
		Skipped int `json:"skipped"`
		Flagged int `json:"flagged"`
		Volumes int `json:"volumes"`
	} `json:"summary"`
	PreviewOK bool    `json:"preview_ok"`
	CreatedAt *string `json:"created_at"`
	Resources []struct {
		Group         string          `json:"group"`
		Kind          string          `json:"kind"`
		Namespace     string          `json:"namespace"`
		Name          string          `json:"name"`
		Action        string          `json:"action"`
		PolicyRefused bool            `json:"policy_refused"`
		Flags         []MigrationFlag `json:"flags"`
	} `json:"resources"`
	Volumes []struct {
		Namespace string `json:"namespace"`
		Claim     string `json:"claim"`
		Status    string `json:"status"`
		SizeBytes int64  `json:"size_bytes"`
		BytesDone int64  `json:"bytes_done"`
	} `json:"volumes"`
	Holds []struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Replicas  int    `json:"replicas"`
	} `json:"holds"`
}

// KubernetesMoveSourceAccess is the manifests to apply to the source cluster.
type KubernetesMoveSourceAccess struct {
	Reader        string `json:"reader"`
	SecretsReader string `json:"secrets_reader"`
	Copier        string `json:"copier"`
}

// WaitMove polls a move at path while busy says its status is still moving,
// and returns it once it is not. A move that ended failed is a *MigrateFailed
// named what. onUpdate, when set, sees each change of status.
func WaitMove[T any](ctx context.Context, c *Client, path, what string, state func(*T) (id, status string, failure *CloudFailure),
	busy func(status string) bool, onUpdate func(status string), raw *[]byte) (*T, error) {
	last := ""
	for {
		var v T
		res, err := c.Do(ctx, Request{Method: http.MethodGet, Path: path}, &v)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			*raw = res.Body
		}
		id, status, failure := state(&v)
		if status != last && onUpdate != nil {
			onUpdate(status)
		}
		last = status
		if !busy(status) {
			if status == "failed" {
				return &v, &MigrateFailed{What: what, ID: id, Failure: failure}
			}
			return &v, nil
		}
		select {
		case <-ctx.Done():
			return &v, ctx.Err()
		case <-time.After(MigratePollInterval):
		}
	}
}
