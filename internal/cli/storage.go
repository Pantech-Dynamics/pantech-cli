package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

var (
	volumeKind   = kind{prefix: "vol_", path: "/volumes", noun: "volume", listCmd: "pantech volumes list"}
	snapshotKind = kind{prefix: "snap_", path: "/snapshots", noun: "snapshot", listCmd: "pantech snapshots list"}
)

func resolveVolume(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	return resolve(cmd, c, volumeKind, ref, func(v api.Volume) (string, string) { return v.ID, v.Name })
}

func resolveSnapshot(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	return resolve(cmd, c, snapshotKind, ref, func(s api.Snapshot) (string, string) { return s.ID, s.Name })
}

func newVolumesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volumes",
		Aliases: []string{"volume", "vol"},
		Short:   "Create, attach and delete data volumes",
	}
	cmd.AddCommand(
		newVolumesListCmd(a),
		newVolumesGetCmd(a),
		newVolumesCreateCmd(a),
		newVolumesAttachCmd(a),
		newVolumesDetachCmd(a),
		deleteCmd(a, volumeKind, "Its data is erased and cannot be recovered.", resolveVolume),
	)
	return cmd
}

func newVolumesListCmd(a *app) *cobra.Command {
	var search string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List volumes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if search != "" {
				q.Set("q", search)
			}
			vols, raw, err := listItems[api.Volume](cmd, c, "/volumes", q)
			if err != nil {
				return err
			}
			if printList(a, vols, raw, func(v api.Volume) string { return v.ID }, "No volumes yet.", "pantech volumes create --name data --offering small-5gb") {
				return nil
			}
			rows := make([][]string, len(vols))
			for i, v := range vols {
				rows[i] = []string{v.Name, a.out.State(v.ObservedState), fmt.Sprintf("%d GB", v.SizeGB), v.DiskOfferingSlug, output.Or(v.AttachedInstanceName), a.out.Dim(v.ID)}
			}
			a.out.Table([]string{"name", "state", "size", "offering", "attached to", "id"}, rows)
			a.out.Summary(count(len(vols), "volume"))
			return nil
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "only volumes whose name contains this")
	return cmd
}

func newVolumesGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <volume>",
		Aliases: []string{"show"},
		Short:   "Show a volume, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVolume(cmd, c, args[0])
			if err != nil {
				return err
			}
			var v api.Volume
			if ok, err := getOne(a, cmd, c, "/volumes/"+url.PathEscape(id), &v); !ok {
				return err
			}
			a.out.Print(output.Detail{
				Title:    v.Name,
				State:    state(a.out, v.ObservedState, v.DesiredState),
				Subtitle: v.ID,
				Sections: [][]output.Pair{
					{{"Size", fmt.Sprintf("%d GB", v.SizeGB)}, {"Offering", v.DiskOfferingSlug}, {"Storage", output.Or(v.StorageType)}, {"Location", output.Or(v.Zone)}},
					{{"Attached to", output.Or(v.AttachedInstanceName)}, {"Mount point", output.Or(v.MountPoint)}},
					{{"Created", output.When(v.CreatedAt)}},
				},
			})
			return nil
		},
	}
}

func newVolumesCreateCmd(a *app) *cobra.Command {
	var name, offering, region, vm, mountPoint string
	var size int
	var noWait bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a volume",
		Long: `Create a volume. Find offerings with "pantech api GET /disk-offerings"; a
custom-size offering needs --size.`,
		Example: `  pantech volumes create --name data --offering small-5gb --vm web-1 --mount-point /data`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "disk_offering_slug": offering}
			if size > 0 {
				body["size_gb"] = size
			}
			if region != "" {
				body["region"] = region
			}
			if vm != "" {
				id, err := resolveVM(cmd, c, vm)
				if err != nil {
					return err
				}
				body["instance_id"] = id
			}
			if mountPoint != "" {
				body["mount_point"] = mountPoint
			}
			if err := a.confirm(fmt.Sprintf("Create volume %s (%s)? It is billed until deleted.", name, offering)); err != nil {
				return err
			}
			return a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: "/volumes", Body: body}, noWait, name)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "its name (required)")
	f.StringVar(&offering, "offering", "", "disk offering slug (required)")
	f.IntVar(&size, "size", 0, "size in GB, for a custom-size offering")
	f.StringVar(&region, "region", "", "region code (default: the platform's region)")
	f.StringVar(&vm, "vm", "", "attach it to this running VM, by id or name")
	f.StringVar(&mountPoint, "mount-point", "", "a label for where it is mounted, e.g. /data")
	f.BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("offering")
	return cmd
}

func newVolumesAttachCmd(a *app) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   "attach <volume> <vm>",
		Short: "Attach a volume to a running VM in its zone",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVolume(cmd, c, args[0])
			if err != nil {
				return err
			}
			vmID, err := resolveVM(cmd, c, args[1])
			if err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/volumes/" + url.PathEscape(id) + "/attach", Body: map[string]any{"instance_id": vmID}}
			return a.runWrite(cmd, c, req, noWait, verb{"Attaching", "Attached"}, args[0])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newVolumesDetachCmd(a *app) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   "detach <volume>",
		Short: "Detach a volume from its VM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVolume(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Detach %s? Unmount it inside the VM first.", args[0])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/volumes/" + url.PathEscape(id) + "/detach"}
			return a.runWrite(cmd, c, req, noWait, verb{"Detaching", "Detached"}, args[0])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newSnapshotsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "snapshots",
		Aliases: []string{"snapshot", "snap"},
		Short:   "Snapshot VMs and volumes",
	}
	cmd.AddCommand(
		newSnapshotsListCmd(a),
		newSnapshotsGetCmd(a),
		newSnapshotsCreateCmd(a),
		deleteCmd(a, snapshotKind, "", resolveSnapshot),
	)
	return cmd
}

func newSnapshotsListCmd(a *app) *cobra.Command {
	var search string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List snapshots",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if search != "" {
				q.Set("q", search)
			}
			snaps, raw, err := listItems[api.Snapshot](cmd, c, "/snapshots", q)
			if err != nil {
				return err
			}
			if printList(a, snaps, raw, func(s api.Snapshot) string { return s.ID }, "No snapshots yet.", "pantech snapshots create --vm web-1 --name before-upgrade") {
				return nil
			}
			rows := make([][]string, len(snaps))
			for i, s := range snaps {
				rows[i] = []string{s.Name, a.out.State(s.ObservedState), snapshotSource(&s), size(s.SizeBytes), output.Ago(s.CreatedAt), a.out.Dim(s.ID)}
			}
			a.out.Table([]string{"name", "state", "of", "size", "taken", "id"}, rows)
			a.out.Summary(count(len(snaps), "snapshot"))
			return nil
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "only snapshots whose name contains this")
	return cmd
}

func newSnapshotsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <snapshot>",
		Aliases: []string{"show"},
		Short:   "Show a snapshot, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveSnapshot(cmd, c, args[0])
			if err != nil {
				return err
			}
			var s api.Snapshot
			if ok, err := getOne(a, cmd, c, "/snapshots/"+url.PathEscape(id), &s); !ok {
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

func newSnapshotsCreateCmd(a *app) *cobra.Command {
	var name, vm, volume string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Snapshot a VM's root disk or a volume",
		Example: `  pantech snapshots create --vm web-1 --name before-upgrade
  pantech snapshots create --volume data --name nightly`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (vm == "") == (volume == "") {
				return &usageError{errors.New("give exactly one of --vm or --volume"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var path string
			if vm != "" {
				id, err := resolveVM(cmd, c, vm)
				if err != nil {
					return err
				}
				path = "/instances/" + url.PathEscape(id) + "/snapshots"
			} else {
				id, err := resolveVolume(cmd, c, volume)
				if err != nil {
					return err
				}
				path = "/volumes/" + url.PathEscape(id) + "/snapshots"
			}
			if err := a.confirm(fmt.Sprintf("Take snapshot %s? It is billed for its size until deleted.", name)); err != nil {
				return err
			}
			return a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: path, Body: map[string]any{"name": name}}, noWait, name)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "its name, unique per VM or volume (required)")
	f.StringVar(&vm, "vm", "", "the VM whose root disk to snapshot, by id or name")
	f.StringVar(&volume, "volume", "", "the volume to snapshot, by id or name")
	f.BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func snapshotSource(s *api.Snapshot) string {
	switch {
	case s.InstanceID != nil:
		return "vm " + output.Or(firstSet(s.SourceInstanceName, s.InstanceID))
	case s.VolumeID != nil:
		return "volume " + output.Or(firstSet(s.VolumeName, s.VolumeID))
	case s.DatabaseID != nil:
		return "database " + *s.DatabaseID
	}
	return "—"
}

func firstSet(ss ...*string) *string {
	for _, s := range ss {
		if s != nil && *s != "" {
			return s
		}
	}
	return nil
}

func size(b int64) string {
	switch {
	case b <= 0:
		return "—"
	case b < 1<<20:
		return fmt.Sprintf("%d KB", b>>10)
	case b < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
}
