package cli

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

// The catalogue: what you can create. Slugs from here are what vm create takes.

func newPlansCmd(a *app) *cobra.Command {
	var placement string
	cmd := &cobra.Command{
		Use:   "plans",
		Short: "List VM plans and their prices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if placement != "" {
				q.Set("placement", placement)
			}
			plans, err := api.ListAll[api.Plan](ctx(cmd), c, "/plans", q)
			if err != nil {
				return err
			}
			if printSimple(a, plans, func(p api.Plan) string { return p.Slug }) {
				return nil
			}
			rows := make([][]string, len(plans))
			for i, p := range plans {
				price := "—"
				if p.Price != nil {
					price = money(p.Price.MonthlyEstimateMinor, p.Price.Currency) + "/mo"
				} else if p.UnpricedReason != nil {
					price = a.out.Dim(*p.UnpricedReason)
				}
				rows[i] = []string{p.Slug, p.Name, fmt.Sprint(p.VCPU), memory(p.MemoryMB), fmt.Sprintf("%d GB", p.DiskGB), price}
			}
			a.out.Table([]string{"slug", "name", "vcpu", "memory", "disk", "price"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&placement, "placement", "", "standard or vpc")
	return cmd
}

func newImagesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "images",
		Short: "List the operating system images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			images, err := api.ListAll[api.Image](ctx(cmd), c, "/images", nil)
			if err != nil {
				return err
			}
			if printSimple(a, images, func(i api.Image) string { return i.Slug }) {
				return nil
			}
			rows := make([][]string, len(images))
			for i, img := range images {
				rows[i] = []string{img.Slug, img.Name, img.Version, strings.Join(img.Zones, ", ")}
			}
			a.out.Table([]string{"slug", "name", "version", "zones"}, rows)
			return nil
		},
	}
}

func newRegionsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "regions",
		Short: "List regions and where in them you can place a VM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			regions, err := api.ListAll[api.Region](ctx(cmd), c, "/regions", nil)
			if err != nil {
				return err
			}
			if printSimple(a, regions, func(r api.Region) string { return r.Code }) {
				return nil
			}
			var rows [][]string
			for _, r := range regions {
				for _, p := range r.Placements {
					available := a.out.State("available")
					if !p.Available {
						available = a.out.Dim("unavailable")
						if p.UnavailableReason != nil {
							available += a.out.Dim(": " + *p.UnavailableReason)
						}
					}
					rows = append(rows, []string{r.Code, r.Name, p.Kind, p.Zone, available})
				}
			}
			a.out.Table([]string{"region", "name", "placement", "zone", "status"}, rows)
			return nil
		},
	}
}

// printSimple handles --json and --quiet for a list; false means print a table.
func printSimple[T any](a *app, items []T, id func(T) string) bool {
	switch {
	case a.out.JSON:
		a.out.Value(items)
	case a.out.Quiet:
		for _, it := range items {
			a.out.Line("%s", id(it))
		}
	default:
		return false
	}
	return true
}
