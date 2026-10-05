package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// A database's data disk: growing it, and snapshots of it.

func newDBStorageCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Grow a database's data disk",
	}
	var storageGB int
	var noWait bool
	resize := &cobra.Command{
		Use:   "resize <database> --storage-gb N",
		Short: "Grow a database's data disk, online",
		Long: `Grow a database's data disk to --storage-gb GB, without a restart. Storage
only grows: the size must be larger than the current one, a multiple of the
zone's step (10 GB) and at most its maximum (2000 GB). The database must be
running, and one resize runs at a time.

No upfront charge: storage is billed hourly, at the new size once the resize
has completed. Until then "pantech db get" shows the size it is growing to.`,
		Example: `  pantech db storage resize app --storage-gb 40`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if storageGB <= 0 {
				return &usageError{fmt.Errorf("give the new size with --storage-gb"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			var d api.Database
			if _, err := api.Get(ctx(cmd), c, "/databases/"+url.PathEscape(id), &d); err != nil {
				return err
			}
			if err := checkStorageGrowth(&d, args[0], storageGB); err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Grow the storage of %s from %d GB to %d GB? It is billed at the new size once grown, and can never shrink.", args[0], d.DataVolumeSizeGB, storageGB)); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/databases/" + url.PathEscape(id) + "/resize-storage", Body: map[string]int{"storage_gb": storageGB}}
			if err := a.runWrite(cmd, c, req, noWait, verb{"Growing the storage of", "Grew the storage of"}, args[0]); err != nil {
				return err
			}
			if noWait && !a.out.JSON {
				a.out.Note("%s is growing to %d GB; %s shows it until it is done.", args[0], storageGB, "pantech db get "+args[0])
			}
			return nil
		},
	}
	resize.Flags().IntVar(&storageGB, "storage-gb", 0, "the new data disk size in GB, larger than now (required)")
	resize.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	cmd.AddCommand(resize)
	return cmd
}

// checkStorageGrowth refuses, before anything is sent, a size the platform
// would refuse: storage only grows, one resize at a time.
func checkStorageGrowth(d *api.Database, ref string, storageGB int) error {
	if d.PendingDataVolumeSizeGB != nil {
		return fmt.Errorf("the storage of %s is already growing to %d GB: wait for that to finish\nSee: pantech db get %s", ref, *d.PendingDataVolumeSizeGB, ref)
	}
	if storageGB <= d.DataVolumeSizeGB {
		return fmt.Errorf("storage only grows: %s has %d GB, so --storage-gb must be more than that", ref, d.DataVolumeSizeGB)
	}
	return nil
}

// dbSnapshotKind is the snapshots of one database.
func dbSnapshotKind(dbID, dbRef string) kind {
	return kind{prefix: "snap_", path: "/databases/" + url.PathEscape(dbID) + "/snapshots", noun: "snapshot", listCmd: "pantech db snapshots list " + dbRef}
}

// resolveDBSnapshot takes a database and one of its snapshots, each by id or
// name, and returns their ids.
func resolveDBSnapshot(cmd *cobra.Command, c *api.Client, dbRef, snapRef string) (string, string, error) {
	dbID, err := resolveDatabase(cmd, c, dbRef)
	if err != nil {
		return "", "", err
	}
	snapID, err := resolve(cmd, c, dbSnapshotKind(dbID, dbRef), snapRef, func(s api.Snapshot) (string, string) { return s.ID, s.Name })
	return dbID, snapID, err
}

func newDBSnapshotsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "snapshots",
		Aliases: []string{"snapshot", "snap"},
		Short:   "Snapshot a database's data disk",
	}
	cmd.AddCommand(newDBSnapshotsListCmd(a), newDBSnapshotsGetCmd(a), newDBSnapshotsCreateCmd(a), newDBSnapshotsDeleteCmd(a))
	return cmd
}

func newDBSnapshotsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list <database>",
		Aliases: []string{"ls"},
		Short:   "List a database's snapshots",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			snaps, raw, err := listItems[api.Snapshot](cmd, c, dbSnapshotKind(id, args[0]).path, nil)
			if err != nil {
				return err
			}
			if printList(a, snaps, raw, func(s api.Snapshot) string { return s.ID }, "No snapshots of "+args[0]+" yet.", "pantech db snapshots create "+args[0]+" --name before-upgrade") {
				return nil
			}
			rows := make([][]string, len(snaps))
			for i, s := range snaps {
				rows[i] = []string{s.Name, a.out.State(s.ObservedState), s.Trigger, size(s.SizeBytes), output.Ago(s.CreatedAt), a.out.Dim(s.ID)}
			}
			a.out.Table([]string{"name", "state", "trigger", "size", "taken", "id"}, rows)
			a.out.Summary(count(len(snaps), "snapshot"))
			return nil
		},
	}
}

func newDBSnapshotsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <database> <snapshot>",
		Aliases: []string{"show"},
		Short:   "Show a database's snapshot, by id or name",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			dbID, snapID, err := resolveDBSnapshot(cmd, c, args[0], args[1])
			if err != nil {
				return err
			}
			var s api.Snapshot
			if ok, err := getOne(a, cmd, c, dbSnapshotKind(dbID, args[0]).path+"/"+url.PathEscape(snapID), &s); !ok {
				return err
			}
			a.out.Print(output.Detail{
				Title:    s.Name,
				State:    state(a.out, s.ObservedState, s.DesiredState),
				Subtitle: s.ID,
				Sections: [][]output.Pair{
					{{"Of", snapshotSource(&s)}, {"Size", size(s.SizeBytes)}, {"Trigger", s.Trigger}, {"Region", output.Or(s.Region)}},
					{{"Taken", output.When(s.CreatedAt)}},
				},
			})
			return nil
		},
	}
}

func newDBSnapshotsCreateCmd(a *app) *cobra.Command {
	var name string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "create <database> --name NAME",
		Short: "Snapshot a database's data disk",
		Long: `Snapshot a database's data disk with the engine running. The snapshot is
crash-consistent only: what the disk held at that instant, as after a power
cut, which the engine recovers from on start. It is not an engine-level
backup; stop the database first for a quiesced copy. The database must be
running or stopped.

A snapshot is billed for its size every hour until you delete it, and it
outlives its database. Restoring a database from a snapshot is not available
yet.`,
		Example: `  pantech db snapshots create app --name before-upgrade`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Take snapshot %s of %s? It is billed for its size until deleted.", name, args[0])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: dbSnapshotKind(id, args[0]).path, Body: map[string]any{"name": name}}
			return a.runCreate(cmd, c, req, noWait, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "its name (required)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newDBSnapshotsDeleteCmd(a *app) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:     "delete <database> <snapshot>",
		Aliases: []string{"rm"},
		Short:   "Delete a database's snapshot, by id or name",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			dbID, snapID, err := resolveDBSnapshot(cmd, c, args[0], args[1])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete snapshot %s (%s) of %s? It cannot be recovered.", args[1], snapID, args[0])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodDelete, Path: dbSnapshotKind(dbID, args[0]).path + "/" + url.PathEscape(snapID)}
			return a.runWrite(cmd, c, req, noWait, verb{"Deleting", "Deleted"}, args[1])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}
