package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newDNSCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Host DNS zones and manage their records on Pantech DNS",
		Long: `Pantech DNS hosts your zones on Pantech's own name servers.

  1. pantech dns zones create example.com
     prints the two name servers to set at your domain's registrar.
  2. Set them at the registrar, then: pantech dns zones verify example.com
     The zone is active once the registrar delegates to exactly those servers;
     a zone left undelegated for 14 days is removed.
  3. pantech dns records create example.com --name www --type A --value 203.0.113.10

In an active zone, changes are live within about a minute: a record is
"pending" until then. Records added while a zone awaits delegation stay
"pending" until the zone is active. Five zones are free; pantech dns zones
quote shows what more cost.
Domains bought through Pantech (pantech domains) get an active zone at once.
Zones are named by id (dnsz_…) or by name. Any member can read; owners and
admins make changes.`,
	}
	cmd.AddCommand(newDNSZonesCmd(a), newDNSRecordsCmd(a), newDNSImportCmd(a), newDNSExportCmd(a))
	return cmd
}

var zoneKind = kind{prefix: "dnsz_", path: "/dns/zones", noun: "zone", listCmd: "pantech dns zones list"}

func zonePath(id string) string { return "/dns/zones/" + url.PathEscape(id) }

func recordPath(zoneID, recordID string) string {
	return zonePath(zoneID) + "/records/" + url.PathEscape(recordID)
}

// domainName is a name as the API stores it: lower case, no final dot.
func domainName(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

// zoneID resolves a zone's id or name. A deleted zone keeps its name for a
// while, so only zones not deleted match a name.
func zoneID(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	if strings.HasPrefix(ref, zoneKind.prefix) {
		return ref, nil
	}
	return resolve(cmd, c, zoneKind, domainName(ref), func(z api.DNSZone) (string, string) {
		if z.Status == "deleted" {
			return z.ID, ""
		}
		return z.ID, z.Name
	})
}

// dnsError adds what to run next to the API's DNS errors.
func dnsError(err error, zone string) error {
	switch {
	case api.IsCode(err, "DNS_UNAVAILABLE"):
		return fmt.Errorf("%w\nPantech DNS is not available on this deployment", err)
	case api.IsCode(err, "DNS_ZONE_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech dns zones list", err)
	case api.IsCode(err, "DNS_RECORD_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech dns records list %s", err, zone)
	case api.IsCode(err, "DNS_RECORD_EXISTS"):
		return fmt.Errorf("%w\nChange it instead: pantech dns records list %s, then pantech dns records update", err, zone)
	case api.IsCode(err, "VERSION_CONFLICT"):
		return fmt.Errorf("%w\nSomeone changed it since you read it: read it again with pantech dns records get", err)
	case api.IsCode(err, "DNS_ZONE_IN_USE_BY_DOMAIN"):
		return fmt.Errorf("%w\nPoint the domain elsewhere first: pantech domains nameservers <domain> --ns …", err)
	case api.IsCode(err, "DNS_ZONE_LIMIT_REACHED"), api.IsCode(err, "DNS_ZONE_CREATE_LIMIT_REACHED"):
		return fmt.Errorf("%w\nSee: pantech dns zones list", err)
	}
	return err
}

var recordFlags = map[string]string{"name": "--name", "type": "--type", "ttl": "--ttl", "values": "--value", "comment": "--comment", "allow_private": "--allow-private", "description": "--description"}

// --- zones ---

func newDNSZonesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "zones",
		Aliases: []string{"zone"},
		Short:   "Hosted zones: create, list, show, verify the delegation, delete",
	}
	cmd.AddCommand(newDNSZonesListCmd(a), newDNSZonesQuoteCmd(a), newDNSZonesCreateCmd(a), newDNSZonesGetCmd(a), newDNSZonesUpdateCmd(a), newDNSZonesDeleteCmd(a), newDNSZonesVerifyCmd(a))
	return cmd
}

func zoneNext(z *api.DNSZone) [][2]string {
	switch z.Status {
	case "awaiting_delegation":
		return [][2]string{
			{"Set the name servers above at your registrar, then", "pantech dns zones verify " + z.ID},
		}
	case "pending":
		return [][2]string{{"Its name servers are assigned in a moment", "pantech dns zones get " + z.ID}}
	case "active":
		return [][2]string{{"Its records", "pantech dns records list " + z.ID}}
	}
	return nil
}

func zoneDetail(out *output.Printer, z *api.DNSZone) output.Detail {
	desc := z.Description
	if desc == "" {
		desc = "—"
	}
	d := output.Detail{
		Title:    z.Name,
		State:    out.State(z.Status),
		Subtitle: z.ID,
		Sections: [][]output.Pair{
			listPairs("Name servers", z.NameServers, "— (assigned in a moment)"),
			{{"Records", strconv.Itoa(z.RecordCount)}, {"Domain", output.Or(z.DomainID)}, {"Description", desc}},
			{{"Last checked", output.When(z.LastCheckedAt)}, {"Active since", output.When(z.ActivatedAt)}, {"Created", output.When(z.CreatedAt)}},
		},
		Next: zoneNext(z),
	}
	if z.DelegationDeadlineAt != nil {
		d.Sections = append(d.Sections, []output.Pair{{"Removed unless delegated by", output.When(z.DelegationDeadlineAt)}})
	}
	if z.FailureCode != nil || z.SuspendedReason != nil {
		d.Sections = append([][]output.Pair{{{"Failure", output.Or(z.FailureCode)}, {"Suspended", output.Or(z.SuspendedReason)}}}, d.Sections...)
	}
	return d
}

// listPairs shows a list one item per line, the label on the first.
func listPairs(label string, items []string, empty string) []output.Pair {
	if len(items) == 0 {
		return []output.Pair{{label, empty}}
	}
	pairs := make([]output.Pair, len(items))
	for i, it := range items {
		pairs[i] = output.Pair{"", it}
	}
	pairs[0][0] = label
	return pairs
}

func (a *app) printZone(z *api.DNSZone, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(z.ID))
	default:
		a.out.Print(zoneDetail(a.out, z))
	}
}

func newDNSZonesListCmd(a *app) *cobra.Command {
	var q, status string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List hosted zones",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			query := url.Values{}
			if q != "" {
				query.Set("q", q)
			}
			if status != "" {
				query.Set("status", status)
			}
			zones, raw, err := listItems[api.DNSZone](cmd, c, "/dns/zones", query)
			if err != nil {
				return dnsError(err, "")
			}
			if printList(a, zones, raw, func(z api.DNSZone) string { return z.ID }, "No DNS zones yet.", "pantech dns zones create example.com") {
				return nil
			}
			rows := make([][]string, len(zones))
			for i := range zones {
				z := &zones[i]
				rows[i] = []string{a.out.Dim(z.ID), z.Name, a.out.State(z.Status), strconv.Itoa(z.RecordCount), output.Or(z.DomainID), output.Ago(z.CreatedAt)}
			}
			a.out.Table([]string{"id", "name", "status", "records", "domain", "created"}, rows)
			a.out.Summary(count(len(zones), "zone"))
			return nil
		},
	}
	cmd.Flags().StringVar(&q, "q", "", "only zones whose name contains this text")
	cmd.Flags().StringVar(&status, "status", "", "only zones with this status: pending, awaiting_delegation, active, suspended, deleting, failed")
	return cmd
}

func newDNSZonesQuoteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "quote",
		Short: "Show what your zones cost: five are free, each further one is billed monthly",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var q api.DNSZoneQuote
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/dns/zones/quote"}, &q)
			if err != nil {
				return dnsError(err, "")
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				a.out.Line("%d", q.MonthlyEstimateMinor)
				return nil
			}
			a.out.Print(output.Detail{
				Title: "DNS zones",
				Sections: [][]output.Pair{{
					{"Free zones", strconv.Itoa(q.FreeZones)},
					{"Live zones", strconv.Itoa(q.LiveZones)},
					{"Billed zones", strconv.Itoa(q.BillableZones)},
					{"Each further zone", money(q.MonthlyPerZoneMinor, q.Currency) + " a month, billed by the hour"},
					{"Your zones now", money(q.MonthlyEstimateMinor, q.Currency) + " a month"},
				}},
			})
			a.out.Note("Prices are before VAT. A zone counts from awaiting_delegation until it is deleted.")
			return nil
		},
	}
}

func newDNSZonesCreateCmd(a *app) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Host a zone; prints the name servers to set at your registrar",
		Long: `Host a zone for a domain you own, or a subdomain of one. Pantech DNS
assigns it two name servers: set exactly those at the domain's registrar,
then run pantech dns zones verify. The zone answers once the delegation is
seen; a zone not delegated within 14 days is removed. Records you add before
then stay pending until it is active.

Five zones are free; each further zone has a monthly price (pantech dns zones
quote). At most 25 zones per organization, and 10 new ones a day.`,
		Example: `  pantech dns zones create example.com --description "Marketing site"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": domainName(args[0])}
			if description != "" {
				body["description"] = description
			}
			var z api.DNSZone
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/dns/zones", Body: body}, &z)
			if err != nil {
				return dnsError(api.AsFlags(err, map[string]string{"name": "<name>", "description": "--description"}), "")
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Created zone %s (%s).", z.Name, z.ID)
			}
			a.printZone(&z, res.Body)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "a note for yourself")
	return cmd
}

func newDNSZonesGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <zone>",
		Aliases: []string{"show"},
		Short:   "Show a zone and its name servers, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var z api.DNSZone
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: zonePath(id)}, &z)
			if err != nil {
				return dnsError(err, args[0])
			}
			a.printZone(&z, res.Body)
			return nil
		},
	}
}

func newDNSZonesUpdateCmd(a *app) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "update <zone> --description TEXT",
		Short: "Change a zone's description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("description") {
				return &usageError{errors.New(`nothing to change: pass --description ("" clears it)`), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var z api.DNSZone
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: zonePath(id), Body: map[string]any{"description": description}}, &z)
			if err != nil {
				return dnsError(api.AsFlags(err, recordFlags), args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Updated zone %s.", z.Name)
			}
			a.printZone(&z, res.Body)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", `a new note ("" clears it)`)
	return cmd
}

func newDNSZonesDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <zone>",
		Aliases: []string{"rm"},
		Short:   "Delete a zone and all its records; it stops answering at once",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete zone %s (%s) and every record in it? It stops answering DNS queries at once.", args[0], id)); err != nil {
				return err
			}
			var z api.DNSZone
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: zonePath(id)}, &z)
			if err != nil {
				return dnsError(err, args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(z.ID))
			default:
				a.out.Success("Deleting zone %s.", z.Name)
			}
			return nil
		},
	}
}

func newDNSZonesVerifyCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "verify <zone>",
		Short: "Check the zone's delegation at the registrar now",
		Long: `Ask Pantech DNS to check now whether the domain's registrar delegates to the
zone's name servers, instead of waiting for the next periodic check. The check
runs within seconds: see the result with pantech dns zones get.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var z api.DNSZone
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: zonePath(id) + "/verify"}, &z)
			if err != nil {
				return dnsError(err, args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(z.ID))
			default:
				a.out.Success("Checking the delegation of %s.", z.Name)
				a.out.Next("See the result in a few seconds", "pantech dns zones get "+z.ID)
			}
			return nil
		},
	}
}

// --- records ---

func newDNSRecordsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "records",
		Aliases: []string{"record", "rr"},
		Short:   "A zone's record sets: list, create, show, change, delete",
		Long: `A record set is one name and type with its TTL and values. Names are
relative to the zone: "@" for the apex, "www", "*.dev", "_dmarc".

Types: A, AAAA, CNAME, MX, TXT, SRV, CAA, NS (subdomains only) and PTR.
Values: A 203.0.113.10; AAAA 2001:db8::1; CNAME, NS, PTR a host name;
MX "10 mail.example.com"; SRV "10 5 5060 sip.example.com";
CAA '0 issue "ca.example.net"'; TXT any text up to 4000 bytes.
The apex SOA and NS are managed by Pantech and cannot be changed.`,
	}
	cmd.AddCommand(newDNSRecordsListCmd(a), newDNSRecordsCreateCmd(a), newDNSRecordsGetCmd(a), newDNSRecordsUpdateCmd(a), newDNSRecordsDeleteCmd(a))
	return cmd
}

func recordDetail(out *output.Printer, r *api.DNSRecord) output.Detail {
	comment := r.Comment
	if comment == "" {
		comment = "—"
	}
	d := output.Detail{
		Title:    r.FQDN + " " + r.Type,
		State:    out.State(r.Status),
		Subtitle: r.ID,
		Sections: [][]output.Pair{
			listPairs("Values", r.Values, "—"),
			{{"Name", r.Name}, {"TTL", strconv.Itoa(r.TTL) + "s"}, {"Version", strconv.Itoa(r.Version)}, {"Comment", comment}},
			{{"Updated", output.When(r.UpdatedAt)}, {"Created", output.When(r.CreatedAt)}},
		},
	}
	if r.Managed {
		d.Sections[1] = append(d.Sections[1], output.Pair{"Managed", "yes: Pantech keeps it, it cannot be changed"})
	}
	if r.AllowPrivate {
		d.Sections[1] = append(d.Sections[1], output.Pair{"Private", "allowed"})
	}
	if r.FailureCode != nil {
		d.Sections = append([][]output.Pair{{{"Failure", *r.FailureCode}}}, d.Sections...)
	}
	return d
}

func (a *app) printRecord(r *api.DNSRecord, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(r.ID))
	default:
		a.out.Print(recordDetail(a.out, r))
	}
}

func (a *app) recordsTable(records []api.DNSRecord) {
	rows := make([][]string, len(records))
	for i := range records {
		r := &records[i]
		name := r.Name
		if r.Managed {
			name += a.out.Dim(" (managed)")
		}
		rows[i] = []string{a.out.Dim(r.ID), name, r.Type, strconv.Itoa(r.TTL), strings.Join(r.Values, " "), a.out.State(r.Status)}
	}
	a.out.Table([]string{"id", "name", "type", "ttl", "values", "status"}, rows)
}

func newDNSRecordsListCmd(a *app) *cobra.Command {
	var name, typ, q string
	cmd := &cobra.Command{
		Use:     "list <zone>",
		Aliases: []string{"ls"},
		Short:   "List a zone's record sets, by name then type",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			query := url.Values{}
			if name != "" {
				query.Set("name", name)
			}
			if typ != "" {
				query.Set("type", strings.ToUpper(typ))
			}
			if q != "" {
				query.Set("q", q)
			}
			records, raw, err := listItems[api.DNSRecord](cmd, c, zonePath(id)+"/records", query)
			if err != nil {
				return dnsError(err, args[0])
			}
			if printList(a, records, raw, func(r api.DNSRecord) string { return r.ID }, "No records match.", "pantech dns records create "+args[0]+" --name www --type A --value 203.0.113.10") {
				return nil
			}
			a.recordsTable(records)
			a.out.Summary(count(len(records), "record set"))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", `only this name, e.g. "www" or "@"`)
	f.StringVar(&typ, "type", "", "only this type, e.g. MX")
	f.StringVar(&q, "q", "", "only names or values containing this text")
	return cmd
}

// waitRecord follows a record change until it is live, with a spinner.
func (a *app) waitRecord(cmd *cobra.Command, c *api.Client, r *api.DNSRecord, raw []byte) error {
	if r.Status == "pending" {
		steps := a.out.Steps()
		steps.Start("Applying " + r.FQDN + " " + r.Type)
		got, err := api.WaitDNSRecord(ctx(cmd), c, r.ZoneID, r.ID, &raw)
		if err != nil {
			steps.Fail("", "")
			return dnsError(err, r.ZoneID)
		}
		steps.Done("Live on Pantech DNS", "")
		r = got
	}
	a.printRecord(r, raw)
	return nil
}

func newDNSRecordsCreateCmd(a *app) *cobra.Command {
	var name, typ, comment string
	var ttl int
	var values []string
	var allowPrivate, wait bool
	cmd := &cobra.Command{
		Use:   "create <zone> --name NAME --type TYPE --value VALUE…",
		Short: "Add a record set",
		Long: `Add a record set: one name and type, a TTL and up to 100 values (repeat
--value). The change is live within about a minute; --wait follows it.
Private and carrier-grade NAT addresses are refused unless --allow-private.`,
		Example: `  pantech dns records create example.com --name www --type A --value 203.0.113.10 --value 203.0.113.11
  pantech dns records create example.com --name @ --type MX --value "10 mail.example.com" --ttl 3600
  pantech dns records create example.com --name _dmarc --type TXT --value "v=DMARC1; p=reject" --wait`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(values) == 0 {
				return &usageError{errors.New("pass at least one --value"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "type": strings.ToUpper(typ), "values": values}
			if cmd.Flags().Changed("ttl") {
				body["ttl"] = ttl
			}
			if allowPrivate {
				body["allow_private"] = true
			}
			if comment != "" {
				body["comment"] = comment
			}
			var r api.DNSRecord
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: zonePath(id) + "/records", Body: body}, &r)
			if err != nil {
				return dnsError(api.AsFlags(err, recordFlags), args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Created %s %s.", r.FQDN, r.Type)
			}
			if wait {
				return a.waitRecord(cmd, c, &r, res.Body)
			}
			a.printRecord(&r, res.Body)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", `the name in the zone: "@", "www", "*.dev" (required)`)
	f.StringVar(&typ, "type", "", "A, AAAA, CNAME, MX, TXT, SRV, CAA, NS or PTR (required)")
	f.IntVar(&ttl, "ttl", 300, "seconds, 60 to 86400")
	f.StringArrayVar(&values, "value", nil, "a value; repeat for more (required)")
	f.BoolVar(&allowPrivate, "allow-private", false, "allow private and carrier-grade NAT addresses")
	f.StringVar(&comment, "comment", "", "a note for yourself")
	f.BoolVar(&wait, "wait", false, "wait until the change is live")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func newDNSRecordsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <zone> <record id>",
		Aliases: []string{"show"},
		Short:   "Show a record set",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var r api.DNSRecord
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: recordPath(id, args[1])}, &r)
			if err != nil {
				return dnsError(err, args[0])
			}
			a.printRecord(&r, res.Body)
			return nil
		},
	}
}

// ifMatch is the If-Match header for --if-match, or nil.
func ifMatch(cmd *cobra.Command, version int) http.Header {
	if !cmd.Flags().Changed("if-match") {
		return nil
	}
	return http.Header{"If-Match": {strconv.Itoa(version)}}
}

func newDNSRecordsUpdateCmd(a *app) *cobra.Command {
	var comment string
	var ttl, version int
	var values []string
	var allowPrivate, wait bool
	cmd := &cobra.Command{
		Use:   "update <zone> <record id>",
		Short: "Change a record set's TTL, values or comment",
		Long: `Change a record set. Only what you pass changes: --value replaces the whole
list of values. --if-match VERSION changes it only if nobody else has since
you read that version.`,
		Example: `  pantech dns records update example.com dnsr_123 --value 203.0.113.20 --ttl 60
  pantech dns records update example.com dnsr_123 --comment "" --if-match 4`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			body := map[string]any{}
			if fl.Changed("ttl") {
				body["ttl"] = ttl
			}
			if fl.Changed("value") {
				body["values"] = values
			}
			if fl.Changed("allow-private") {
				body["allow_private"] = allowPrivate
			}
			if fl.Changed("comment") {
				body["comment"] = comment
			}
			if len(body) == 0 {
				return &usageError{errors.New("nothing to change: pass --ttl, --value, --allow-private or --comment"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var r api.DNSRecord
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: recordPath(id, args[1]), Body: body, Header: ifMatch(cmd, version)}, &r)
			if err != nil {
				return dnsError(api.AsFlags(err, recordFlags), args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Updated %s %s.", r.FQDN, r.Type)
			}
			if wait {
				return a.waitRecord(cmd, c, &r, res.Body)
			}
			a.printRecord(&r, res.Body)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&ttl, "ttl", 300, "seconds, 60 to 86400")
	f.StringArrayVar(&values, "value", nil, "the whole new list of values; repeat for more")
	f.BoolVar(&allowPrivate, "allow-private", false, "allow private and carrier-grade NAT addresses (--allow-private=false to refuse them again)")
	f.StringVar(&comment, "comment", "", `a new note ("" clears it)`)
	f.IntVar(&version, "if-match", 0, "change it only if its version is still this one")
	f.BoolVar(&wait, "wait", false, "wait until the change is live")
	return cmd
}

func newDNSRecordsDeleteCmd(a *app) *cobra.Command {
	var version int
	cmd := &cobra.Command{
		Use:     "delete <zone> <record id>",
		Aliases: []string{"rm"},
		Short:   "Delete a record set",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var cur api.DNSRecord
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: recordPath(id, args[1])}, &cur); err != nil {
				return dnsError(err, args[0])
			}
			if err := a.confirm(fmt.Sprintf("Delete record %s %s (%s)? It stops answering within about a minute.", cur.FQDN, cur.Type, cur.ID)); err != nil {
				return err
			}
			var r api.DNSRecord
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: recordPath(id, args[1]), Header: ifMatch(cmd, version)}, &r)
			if err != nil {
				return dnsError(err, args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(r.ID))
			default:
				a.out.Success("Deleting %s %s.", r.FQDN, r.Type)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&version, "if-match", 0, "delete it only if its version is still this one")
	return cmd
}

// --- import and export ---

// maxZoneFile bounds a zone file read for import. 1,000 changes of the
// longest records fit well within it.
const maxZoneFile = 16 << 20

func readZoneFile(path string) (string, error) {
	var r io.Reader
	if path == "-" {
		r = stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", path)
		}
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxZoneFile+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxZoneFile {
		return "", fmt.Errorf("%s is larger than %d MiB", path, maxZoneFile>>20)
	}
	return string(data), nil
}

func newDNSImportCmd(a *app) *cobra.Command {
	var file string
	var replace bool
	cmd := &cobra.Command{
		Use:   "import <zone> --file ZONEFILE",
		Short: "Add or change records from a BIND zone file",
		Long: `Read a BIND zone file and apply it to the zone, all or nothing: records in
the file are created or changed. With --replace, records in the zone that are
not in the file are deleted too (asks first). The file's SOA and apex NS are
ignored: Pantech manages them. At most 1000 changes per import.`,
		Example: `  pantech dns import example.com --file example.com.zone
  pantech dns export example.com > before.zone && pantech dns import example.com --file new.zone --replace`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := readZoneFile(file)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"zone_file": text}
			if replace {
				if err := a.confirm(fmt.Sprintf("Replace the records of %s with %s? Records not in the file are deleted.", args[0], file)); err != nil {
					return err
				}
				body["replace"] = true
			}
			var out api.DNSRecords
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: zonePath(id) + "/import", Body: body}, &out)
			if err != nil {
				if api.IsCode(err, "TOO_MANY_CHANGES") {
					return fmt.Errorf("%w\nSplit the file into imports of at most 1000 changes", err)
				}
				return dnsError(api.AsFlags(err, map[string]string{"zone_file": "--file"}), args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				for _, r := range out.Data {
					a.out.Line("%s", output.Clean(r.ID))
				}
			case len(out.Data) == 0:
				a.out.Success("Nothing to change: the zone already matches %s.", file)
			default:
				a.out.Success("Imported %s into %s; live within about a minute.", count(len(out.Data), "change"), args[0])
				a.recordsTable(out.Data)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", `the zone file ("-" for stdin) (required)`)
	cmd.Flags().BoolVar(&replace, "replace", false, "also delete records that are not in the file")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newDNSExportCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "export <zone>",
		Short:   "Print the zone as a BIND zone file",
		Example: `  pantech dns export example.com > example.com.zone`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := zoneID(cmd, c, args[0])
			if err != nil {
				return err
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: zonePath(id) + "/export", Header: http.Header{"Accept": {"text/dns"}}}, nil)
			if err != nil {
				return dnsError(err, args[0])
			}
			// The file comes out as sent, so it can be imported again; only
			// control characters a terminal would act on are dropped, and
			// only when a person is reading it.
			body := string(res.Body)
			if f, ok := a.out.Out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
				lines := strings.Split(body, "\n")
				for i, l := range lines {
					lines[i] = output.Clean(strings.TrimSuffix(l, "\r"))
				}
				body = strings.Join(lines, "\n")
			}
			_, err = io.WriteString(a.out.Out, body)
			return err
		},
	}
}
