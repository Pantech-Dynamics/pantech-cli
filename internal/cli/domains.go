package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newDomainsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "domains",
		Aliases: []string{"domain"},
		Short:   "Search, register, transfer, renew and configure domains",
		Long: `Buy domains through Pantech, paid from your organization's credit, with their
DNS hosted on Pantech DNS.

  pantech domains search example          what is free, and what it costs
  pantech domains register example.com --contact-file contact.json --accept-terms --wait
  pantech domains transfer example.com --auth-code-stdin --contact-file contact.json --accept-terms

A registration, transfer or renewal is an order: its price is taken from your
credit before the registrar is asked, and returned if the order fails. Every
command that costs money shows the price and asks first (--yes to skip).
Privacy protection and the transfer lock are on by default; Pantech renews
domains with auto-renew on 30 days before they expire. An order your credit
does not cover waits an hour for the rest: in US dollars your saved card is
charged the difference; in naira, add credit (then: pantech domains
retry-payment <order>).

Domains are named by id (dom_…) or by name. Any member can read; owners and
admins buy and make changes.`,
	}
	cmd.AddCommand(
		newDomainsTLDsCmd(a),
		newDomainsSearchCmd(a),
		newDomainsTransferCheckCmd(a),
		newDomainsListCmd(a),
		newDomainsGetCmd(a),
		newDomainsRegisterCmd(a),
		newDomainsTransferCmd(a),
		newDomainsRenewCmd(a),
		newDomainsSetCmd(a),
		newDomainsNameServersCmd(a),
		newDomainsContactCmd(a),
		newDomainsAuthCodeCmd(a),
		newDomainsOrdersCmd(a),
		newDomainsRetryPaymentCmd(a),
	)
	return cmd
}

var domainKind = kind{prefix: "dom_", path: "/domains", noun: "domain", listCmd: "pantech domains list"}

func domainPath(id string) string { return "/domains/" + url.PathEscape(id) }

// domainID resolves a domain's id or name. A failed order or a domain that
// left Pantech keeps its row, so only the others match a name.
func domainID(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	if strings.HasPrefix(ref, domainKind.prefix) {
		return ref, nil
	}
	return resolve(cmd, c, domainKind, domainName(ref), func(d api.Domain) (string, string) {
		if d.Status == "failed" || d.Status == "transferred_out" {
			return d.ID, ""
		}
		return d.ID, d.Name
	})
}

// domainError adds what to run next to the API's domain errors.
func domainError(err error, ref string) error {
	switch {
	case api.IsCode(err, "DOMAINS_UNAVAILABLE"):
		return fmt.Errorf("%w\nDomains are not sold on this deployment", err)
	case api.IsCode(err, "DOMAIN_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech domains list", err)
	case api.IsCode(err, "DOMAIN_ORDER_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech domains orders %s", err, ref)
	case api.IsCode(err, "TLD_NOT_SOLD"):
		return fmt.Errorf("%w\nSee: pantech domains tlds", err)
	case api.IsCode(err, "DOMAIN_NOT_AVAILABLE"):
		return fmt.Errorf("%w\nFind another: pantech domains search %s", err, ref)
	case api.IsCode(err, "DOMAIN_ALREADY_HELD"):
		return fmt.Errorf("%w\nSee: pantech domains list", err)
	case api.IsCode(err, "DOMAIN_ORDER_IN_FLIGHT"):
		return fmt.Errorf("%w\nSee: pantech domains orders %s", err, ref)
	case api.IsCode(err, "DOMAIN_LOCKED"):
		return fmt.Errorf("%w\nTurn the lock off first: pantech domains set %s --lock=off", err, ref)
	case api.IsCode(err, "DOMAIN_NOT_CONFIGURABLE"):
		return fmt.Errorf("%w\nSee: pantech domains get %s", err, ref)
	case api.IsCode(err, "DOMAIN_LOOKUP_UNAVAILABLE"):
		return fmt.Errorf("%w\nTry again in a moment", err)
	case api.HasFieldCode(err, "privacy", "PRIVACY_ALWAYS_ON"), api.HasFieldCode(err, "--privacy", "PRIVACY_ALWAYS_ON"), api.HasFieldCode(err, "--no-privacy", "PRIVACY_ALWAYS_ON"):
		return fmt.Errorf("%w\nThe .ng registry hides every registrant's contact details, so privacy stays on for these domains", err)
	case api.IsCode(err, "DOMAIN_ORDER_LIMIT_REACHED"):
		return fmt.Errorf("%w\nSee: pantech domains list", err)
	}
	return err
}

var domainFlags = map[string]string{"name": "<name>", "years": "--years", "accept_terms": "--accept-terms", "auth_code": "--auth-code", "name_servers": "--ns", "zone_id": "--zone", "auto_renew": "--auto-renew", "privacy": "--privacy", "transfer_lock": "--lock"}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func years(n int) string {
	if n == 1 {
		return "1 year"
	}
	return fmt.Sprintf("%d years", n)
}

// --- catalogue and search ---

func listTLDs(cmd *cobra.Command, c *api.Client) ([]api.TLDOffer, []byte, error) {
	var out struct {
		Data []api.TLDOffer `json:"data"`
	}
	res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/domains/tlds"}, &out)
	if err != nil {
		return nil, nil, domainError(err, "")
	}
	return out.Data, res.Body, nil
}

// offerFor is the offer of the longest TLD that name ends in.
func offerFor(offers []api.TLDOffer, name string) *api.TLDOffer {
	var best *api.TLDOffer
	for i := range offers {
		o := &offers[i]
		if strings.HasSuffix(name, "."+o.TLD) && (best == nil || len(o.TLD) > len(best.TLD)) {
			best = o
		}
	}
	return best
}

func newDomainsTLDsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tlds",
		Short: "List the TLDs Pantech sells, with their yearly prices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			offers, raw, err := listTLDs(cmd, c)
			if err != nil {
				return err
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(raw)
				return nil
			case a.out.Quiet:
				for _, o := range offers {
					a.out.Line("%s", output.Clean(o.TLD))
				}
				return nil
			case len(offers) == 0:
				a.out.Note("No TLDs are on sale.")
				return nil
			}
			rows := make([][]string, len(offers))
			for i, o := range offers {
				featured := ""
				if o.Featured {
					featured = "★"
				}
				privacy := "optional"
				if o.PrivacyAlwaysOn {
					privacy = "always on"
				}
				rows[i] = []string{"." + o.TLD, money(o.RegisterMinor, o.Currency), money(o.RenewMinor, o.Currency), money(o.TransferMinor, o.Currency), privacy, featured}
			}
			a.out.Table([]string{"tld", "register/yr", "renew/yr", "transfer", "privacy", "featured"}, rows)
			a.out.Summary(count(len(offers), "TLD"))
			a.out.Note("Prices are before VAT.")
			return nil
		},
	}
}

func newDomainsSearchCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "search <query>",
		Short:   "See which names are available, and their prices",
		Example: "  pantech domains search example\n  pantech domains search example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var out struct {
				Data []api.DomainSearchResult `json:"data"`
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/domains/search", Query: url.Values{"query": {args[0]}}}, &out)
			if err != nil {
				return domainError(api.AsFlags(err, map[string]string{"query": "<query>"}), args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				for _, r := range out.Data {
					if r.Status == "available" {
						a.out.Line("%s", output.Clean(r.Name))
					}
				}
				return nil
			case len(out.Data) == 0:
				a.out.Note("Nothing found for %s.", args[0])
				return nil
			}
			rows := make([][]string, len(out.Data))
			first := ""
			for i, r := range out.Data {
				register, renew := "—", "—"
				if r.Price != nil && r.Status == "available" {
					register, renew = money(r.Price.RegisterMinor, r.Price.Currency), money(r.Price.RenewMinor, r.Price.Currency)
				}
				if first == "" && r.Status == "available" {
					first = r.Name
				}
				rows[i] = []string{r.Name, a.out.State(r.Status), register, renew}
			}
			a.out.Table([]string{"name", "status", "first year", "renew/yr"}, rows)
			if first != "" {
				a.out.Next("Register it", "pantech domains register "+first+" --contact-file contact.json --accept-terms")
			}
			return nil
		},
	}
}

func newDomainsTransferCheckCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "transfer-check <name>",
		Short: "See whether a domain can be transferred to Pantech now, and the price",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			name := domainName(args[0])
			var out api.DomainTransferCheck
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/domains/transfer-check", Query: url.Values{"name": {name}}}, &out)
			if err != nil {
				return domainError(api.AsFlags(err, map[string]string{"name": "<name>"}), name)
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			if a.out.Quiet {
				a.out.Line("%t", out.Transferable)
				return nil
			}
			state := "unavailable"
			if out.Transferable {
				state = "available"
			}
			d := output.Detail{Title: out.Name, State: a.out.State(state), Sections: [][]output.Pair{{{"Transferable", yesNo(out.Transferable)}, {"Reason", output.Or(out.Reason)}}}}
			if out.Price != nil {
				d.Sections = append(d.Sections, []output.Pair{{"Price", money(out.Price.TransferPrice(1), out.Price.Currency) + " (adds a year)"}})
			}
			if out.Transferable {
				d.Next = [][2]string{{"Transfer it", "pantech domains transfer " + out.Name + " --auth-code-stdin --contact-file contact.json --accept-terms"}}
			}
			a.out.Print(d)
			return nil
		},
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// --- domains ---

func domainDetail(out *output.Printer, d *api.Domain) output.Detail {
	ns := "Pantech DNS"
	if d.NameServerMode == "custom" {
		ns = "custom"
	}
	pending := []string{}
	if d.SettingsPending {
		pending = append(pending, "settings being applied at the registrar")
	}
	if d.AuthCodePending {
		pending = append(pending, "auth code being fetched")
	}
	det := output.Detail{
		Title:    d.Name,
		State:    out.State(d.Status),
		Subtitle: d.ID,
		Sections: [][]output.Pair{
			{{"Expires", output.When(d.ExpiresAt)}, {"Auto-renew", onOff(d.AutoRenew)}, {"Transfer lock", onOff(d.TransferLock)}, {"Privacy", onOff(d.Privacy)}},
			append([]output.Pair{{"DNS", ns}, {"Zone", output.Or(d.ZoneID)}}, listPairs("Name servers", d.NameServers, "—")...),
			{{"Registered", output.When(d.RegisteredAt)}, {"Created", output.When(d.CreatedAt)}},
		},
	}
	if len(pending) > 0 {
		det.Sections[0] = append(det.Sections[0], output.Pair{"Pending", strings.Join(pending, "; ")})
	}
	if d.FailureCode != nil || d.SuspendedReason != nil {
		det.Sections = append([][]output.Pair{{{"Failure", output.Or(d.FailureCode)}, {"Suspended", output.Or(d.SuspendedReason)}}}, det.Sections...)
	}
	switch {
	case d.Status == "active" && !d.ContactVerified:
		det.Sections[0] = append(det.Sections[0], output.Pair{"Contact", "not verified: click the link in the registry's email, or the domain is suspended 15 days after registration"})
	case d.Status == "registering" || d.Status == "transferring" || d.Status == "pending":
		det.Next = [][2]string{{"Follow its order", "pantech domains orders " + d.ID}}
	}
	return det
}

func (a *app) printDomain(d *api.Domain, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(d.ID))
	default:
		a.out.Print(domainDetail(a.out, d))
	}
}

func newDomainsListCmd(a *app) *cobra.Command {
	var q, status string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List your domains",
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
			domains, raw, err := listItems[api.Domain](cmd, c, "/domains", query)
			if err != nil {
				return domainError(err, "")
			}
			if printList(a, domains, raw, func(d api.Domain) string { return d.ID }, "No domains yet.", "pantech domains search <name>") {
				return nil
			}
			rows := make([][]string, len(domains))
			for i := range domains {
				d := &domains[i]
				rows[i] = []string{a.out.Dim(d.ID), d.Name, a.out.State(d.Status), output.Or(d.ExpiresAt), onOff(d.AutoRenew), onOff(d.TransferLock)}
			}
			a.out.Table([]string{"id", "name", "status", "expires", "auto-renew", "lock"}, rows)
			a.out.Summary(count(len(domains), "domain"))
			return nil
		},
	}
	cmd.Flags().StringVar(&q, "q", "", "only domains whose name contains this text")
	cmd.Flags().StringVar(&status, "status", "", "only domains with this status, e.g. active or expired")
	return cmd
}

func newDomainsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <domain>",
		Aliases: []string{"show"},
		Short:   "Show a domain, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := domainID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var d api.Domain
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: domainPath(id)}, &d)
			if err != nil {
				return domainError(err, args[0])
			}
			a.printDomain(&d, res.Body)
			return nil
		},
	}
}

// --- orders that cost money ---

// maxContactFile bounds a contact file: a contact is a few hundred bytes.
const maxContactFile = 64 << 10

// readContact reads a registrant contact from a JSON file. Its content is
// personal data: it is sent to the API and never printed.
func readContact(path string) (json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxContactFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxContactFile {
		return nil, fmt.Errorf("%s is too large for a contact", path)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object with the contact's fields (first_name, last_name, email, phone, address_line1, city, country_code, …)", path)
	}
	return json.RawMessage(bytes.TrimSpace(data)), nil
}

type orderFlags struct {
	years                int
	contactFile          string
	autoRenew, noPrivacy bool
	acceptTerms, wait    bool
	authCode             string
	authCodeStdin        bool
}

const termsText = "the registrant agreement and the registrant rights and responsibilities"

func (o *orderFlags) check(cmd *cobra.Command) error {
	if o.years < 1 || o.years > 10 {
		return &usageError{errors.New("--years must be 1 to 10"), cmd}
	}
	if !o.acceptTerms {
		return &usageError{fmt.Errorf("pass --accept-terms to accept %s: the domain is registered in the name of the contact in --contact-file, who is its legal owner, and the registry may email them to verify it", termsText), cmd}
	}
	return nil
}

func (o *orderFlags) body(cmd *cobra.Command, name string) (map[string]any, error) {
	contact, err := readContact(o.contactFile)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": name, "years": o.years, "contact": contact, "accept_terms": true}
	if cmd.Flags().Changed("auto-renew") {
		body["auto_renew"] = o.autoRenew
	}
	if o.noPrivacy {
		body["privacy"] = false
	}
	return body, nil
}

func addOrderFlags(cmd *cobra.Command, o *orderFlags) {
	f := cmd.Flags()
	f.IntVar(&o.years, "years", 1, "years, 1 to 10")
	f.StringVar(&o.contactFile, "contact-file", "", "a JSON file with the registrant contact (required)")
	f.BoolVar(&o.autoRenew, "auto-renew", true, "renew a year at a time from credit, 30 days before expiry (--auto-renew=false to turn off)")
	f.BoolVar(&o.noPrivacy, "no-privacy", false, "publish the contact in the public registry instead of hiding it")
	f.BoolVar(&o.acceptTerms, "accept-terms", false, "accept "+termsText+" (required)")
	f.BoolVar(&o.wait, "wait", false, "wait until the order has succeeded or failed")
	_ = cmd.MarkFlagRequired("contact-file")
}

// orderSettlePolls bounds how many times placeOrder checks a new order
// while its credit is being reserved (a few seconds), to learn whether it
// waits for a payment.
var orderSettlePolls = 15

// settleOrder follows a new order while it is pending, until its credit is
// reserved or it needs a payment. An error reading it leaves the order as
// placed: it went through, and the next command shows it.
func settleOrder(cmd *cobra.Command, c *api.Client, o *api.DomainOrder) *api.DomainOrder {
	for i := 0; i < orderSettlePolls && o.Status == "pending"; i++ {
		select {
		case <-ctx(cmd).Done():
			return o
		case <-time.After(api.PollInterval):
		}
		next, _, err := api.GetDomainOrder(ctx(cmd), c, o.ID)
		if err != nil {
			return o
		}
		o = next
	}
	return o
}

// showPayment says what to do about an order that is awaiting_payment: its
// credit falls short, and it waits an hour for the rest. In US dollars the
// saved card is charged the difference, and a payment link appears only when
// the bank asks the customer to confirm that payment; in naira the customer
// adds credit. A link is the one thing they must act on, so it is printed
// even with --quiet.
func (a *app) showPayment(o *api.DomainOrder) {
	if o.Status != "awaiting_payment" || a.out.JSON {
		return
	}
	link := o.PaymentURL != nil && *o.PaymentURL != ""
	if a.out.Quiet {
		if link {
			a.out.Note("%s", *o.PaymentURL)
		}
		return
	}
	retry := "pantech domains retry-payment " + output.Clean(o.ID)
	switch {
	case link:
		a.out.Warn("Your credit does not cover order %s. Your bank asks you to confirm the card payment of the difference:", output.Clean(o.ID))
		a.out.Note("  %s", *o.PaymentURL)
	case o.Currency == "USD":
		a.out.Warn("Your credit does not cover order %s: your saved card is being charged the difference.", output.Clean(o.ID))
		a.out.Note("If the card is declined, save another card in the Pantech console, then: %s", retry)
	default:
		a.out.Warn("Your credit does not cover order %s. Add credit, then retry: %s", output.Clean(o.ID), retry)
	}
	if o.PaymentExpiresAt != nil {
		a.out.Note("It waits until %s; unpaid by then, the order fails and nothing is charged.", output.When(o.PaymentExpiresAt))
	}
}

// placeOrder sends a register, transfer or renew, and prints (or follows)
// the order it creates. An order whose credit falls short waits for its
// payment: showPayment says what to do, and --wait follows the order until
// the payment lands and the order finishes.
func (a *app) placeOrder(cmd *cobra.Command, c *api.Client, req api.Request, wait bool, ref string) error {
	var out api.DomainOrderAccepted
	res, err := c.Do(ctx(cmd), req, &out)
	if err != nil {
		return domainError(api.AsFlags(err, domainFlags), ref)
	}
	o := &out.Order
	if !wait {
		if a.out.JSON {
			a.out.RawJSON(res.Body)
			return nil
		}
		o = settleOrder(cmd, c, o)
		if o.Status == "failed" {
			return &api.DomainOrderFailed{Order: o}
		}
		if a.out.Quiet {
			a.out.Line("%s", output.Clean(out.Domain.ID))
		} else {
			a.out.Success("Ordered: %s %s for %s, %s plus VAT (order %s).", o.Kind, o.DomainName, years(o.Years), money(o.AmountMinor, o.Currency), o.ID)
		}
		a.showPayment(o)
		if !a.out.Quiet {
			a.out.Next("Follow it", "pantech domains orders "+out.Domain.ID)
		}
		return nil
	}
	steps := a.out.Steps()
	steps.Mark("Ordered "+o.DomainName, money(o.AmountMinor, o.Currency)+" plus VAT")
	return a.followOrder(cmd, c, steps, o, out.Domain.ID)
}

// followOrder waits for a domain order to finish, through its payment, and
// prints the domain it changed.
func (a *app) followOrder(cmd *cobra.Command, c *api.Client, steps *output.Steps, o *api.DomainOrder, domainID string) error {
	label := strings.ToUpper(o.Kind[:1]) + o.Kind[1:] + " in progress"
	steps.Start(label)
	var raw []byte
	shown := false
	done, err := api.WaitDomainOrder(ctx(cmd), c, o.ID, func(o *api.DomainOrder) {
		switch {
		case o.Status == "awaiting_payment" && !shown:
			shown = true
			steps.Done("Credit is short", "")
			a.showPayment(o)
			steps.Start("Waiting for the payment")
		case shown && o.Status != "awaiting_payment" && o.Status != "failed":
			shown = false
			steps.Done("Payment received", "")
			steps.Start(label)
		}
	}, &raw)
	if err != nil {
		steps.Fail("", "")
		if done != nil && a.out.JSON {
			a.out.RawJSON(raw)
		}
		return err
	}
	steps.Done("Done", "")
	var d api.Domain
	dres, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: domainPath(domainID)}, &d)
	if err != nil {
		return domainError(err, domainID)
	}
	a.printDomain(&d, dres.Body)
	return nil
}

// domainOffer is the offer for name's TLD, priced in the account's currency.
func domainOffer(cmd *cobra.Command, c *api.Client, name string) (*api.TLDOffer, error) {
	offers, _, err := listTLDs(cmd, c)
	if err != nil {
		return nil, err
	}
	o := offerFor(offers, name)
	if o == nil {
		return nil, fmt.Errorf("Pantech does not sell domains like %s\nSee: pantech domains tlds", name)
	}
	return o, nil
}

func domainPrice(cmd *cobra.Command, c *api.Client, name string, price func(*api.TLDOffer) int64) (string, error) {
	o, err := domainOffer(cmd, c, name)
	if err != nil {
		return "", err
	}
	return money(price(o), o.Currency), nil
}

// privacyAlwaysOnText says why privacy cannot be turned off for a TLD.
func privacyAlwaysOnText(tld string) string {
	root := tld[strings.LastIndex(tld, ".")+1:]
	return fmt.Sprintf("The .%s registry hides every registrant's contact details, so privacy is always on for .%s domains", root, tld)
}

// checkPrivacy refuses --no-privacy for a TLD whose registry always hides
// the contact, before anything is ordered.
func (o *orderFlags) checkPrivacy(cmd *cobra.Command, offer *api.TLDOffer) error {
	if o.noPrivacy && offer.PrivacyAlwaysOn {
		return &usageError{errors.New(privacyAlwaysOnText(offer.TLD) + ": leave out --no-privacy"), cmd}
	}
	return nil
}

func newDomainsRegisterCmd(a *app) *cobra.Command {
	var o orderFlags
	cmd := &cobra.Command{
		Use:   "register <name> --contact-file FILE --accept-terms",
		Short: "Buy a domain, paid from credit; its DNS is hosted on Pantech",
		Long: `Register a domain for --years (1 to 10). The first year is at the register
price and every other at the renew price (pantech domains tlds); it is taken
from your organization's credit when the order is placed, and returned if the
registration fails. It asks first, showing the price (--yes to skip).

--contact-file is a JSON file with the registrant, the domain's legal owner:
  {"type": "person", "first_name": "Ada", "last_name": "Obi",
   "email": "ada@example.com", "phone": "+234.8012345678",
   "address_line1": "1 Marina", "city": "Lagos", "state": "Lagos",
   "country_code": "NG", "postal_code": "101001"}
("type": "company" needs "organization_name"; .ng domains need "state" and
"postal_code", and name.ng is for people only, "type": "person"). The registry emails the
registrant to verify the address; an unverified domain is suspended after 15
days. Privacy protection hides the contact from the public registry
(--no-privacy to publish it; the .ng registry hides every registrant's
contact details, so .ng domains always have it). Prices are before VAT. When
your credit falls short, the order waits an hour for the rest: in US dollars
your saved card is charged the difference (a link is printed if your bank asks
you to confirm the payment); in naira, add credit, then pantech domains
retry-payment <order>. --wait follows the order until the payment lands and
the domain is registered. The domain gets a Pantech DNS zone, or adopts
yours of the same name.`,
		Example: `  pantech domains register example.com --contact-file contact.json --accept-terms --wait
  pantech domains register example.org --years 2 --auto-renew=false --contact-file contact.json --accept-terms --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.check(cmd); err != nil {
				return err
			}
			name := domainName(args[0])
			body, err := o.body(cmd, name)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			offer, err := domainOffer(cmd, c, name)
			if err != nil {
				return err
			}
			if err := o.checkPrivacy(cmd, offer); err != nil {
				return err
			}
			price := money(offer.RegisterPrice(o.years), offer.Currency)
			if err := a.confirm(fmt.Sprintf("Register %s for %s for %s plus VAT? It is charged to your organization's credit now (any shortfall waits an hour for payment), and returned only if the registration fails.", name, years(o.years), price)); err != nil {
				return err
			}
			return a.placeOrder(cmd, c, api.Request{Method: http.MethodPost, Path: "/domains", Body: body}, o.wait, name)
		},
	}
	addOrderFlags(cmd, &o)
	return cmd
}

func newDomainsTransferCmd(a *app) *cobra.Command {
	var o orderFlags
	cmd := &cobra.Command{
		Use:   "transfer <name> --auth-code-stdin --contact-file FILE --accept-terms",
		Short: "Move a domain to Pantech from another registrar, paid from credit",
		Long: `Transfer a domain in from its current registrar. Unlock it there and get its
auth code (EPP code) first; pantech domains transfer-check says whether it can
move now. A transfer adds a year (--years more renews further) and is taken
from your organization's credit when the order is placed, returned if the
transfer fails. It asks first, showing the price (--yes to skip).

Pass the auth code on stdin with --auth-code-stdin, so it stays out of your
shell history; --auth-code works too. --contact-file is as for register.`,
		Example: `  pantech domains transfer example.com --auth-code-stdin --contact-file contact.json --accept-terms < code.txt`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.check(cmd); err != nil {
				return err
			}
			code := o.authCode
			switch {
			case o.authCodeStdin && code != "":
				return &usageError{errors.New("pass --auth-code or --auth-code-stdin, not both"), cmd}
			case o.authCodeStdin:
				line, err := bufio.NewReader(stdin).ReadString('\n')
				if err != nil && line == "" {
					return errors.New("no auth code on stdin")
				}
				code = strings.TrimRight(line, "\r\n")
			}
			if strings.TrimSpace(code) == "" {
				return &usageError{errors.New("pass the domain's auth code from its current registrar: --auth-code-stdin, or --auth-code"), cmd}
			}
			name := domainName(args[0])
			body, err := o.body(cmd, name)
			if err != nil {
				return err
			}
			body["auth_code"] = code
			c, err := a.client()
			if err != nil {
				return err
			}
			offer, err := domainOffer(cmd, c, name)
			if err != nil {
				return err
			}
			if err := o.checkPrivacy(cmd, offer); err != nil {
				return err
			}
			price := money(offer.TransferPrice(o.years), offer.Currency)
			if err := a.confirm(fmt.Sprintf("Transfer %s to Pantech, adding %s, for %s plus VAT? It is charged to your organization's credit now (any shortfall waits an hour for payment), and returned if the transfer fails.", name, years(o.years), price)); err != nil {
				return err
			}
			return a.placeOrder(cmd, c, api.Request{Method: http.MethodPost, Path: "/domains/transfers", Body: body}, o.wait, name)
		},
	}
	addOrderFlags(cmd, &o)
	cmd.Flags().StringVar(&o.authCode, "auth-code", "", "the auth code from the current registrar (prefer --auth-code-stdin)")
	cmd.Flags().BoolVar(&o.authCodeStdin, "auth-code-stdin", false, "read the auth code from stdin")
	return cmd
}

func newDomainsRenewCmd(a *app) *cobra.Command {
	var n int
	var wait bool
	cmd := &cobra.Command{
		Use:   "renew <domain> --years N",
		Short: "Renew a domain now, paid from credit",
		Long: `Renew a domain for --years more, at the renew price per year, taken from your
organization's credit when the order is placed and returned if the renewal
fails. It asks first, showing the price (--yes to skip). With auto-renew on,
Pantech renews a year at a time 30 days before expiry.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if n < 1 || n > 10 {
				return &usageError{errors.New("--years must be 1 to 10"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := domainID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var d api.Domain
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: domainPath(id)}, &d); err != nil {
				return domainError(err, args[0])
			}
			price, err := domainPrice(cmd, c, d.Name, func(t *api.TLDOffer) int64 { return t.RenewPrice(n) })
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Renew %s for %s for %s plus VAT? It is charged to your organization's credit now (any shortfall waits an hour for payment), and returned if the renewal fails.", d.Name, years(n), price)); err != nil {
				return err
			}
			return a.placeOrder(cmd, c, api.Request{Method: http.MethodPost, Path: domainPath(id) + "/renew", Body: map[string]any{"years": n}}, wait, id)
		},
	}
	cmd.Flags().IntVar(&n, "years", 1, "years, 1 to 10")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the order has succeeded or failed")
	return cmd
}

// --- settings ---

func parseOnOff(flag, v string) (bool, error) {
	switch strings.ToLower(v) {
	case "on", "true", "yes":
		return true, nil
	case "off", "false", "no":
		return false, nil
	}
	return false, fmt.Errorf("--%s must be on or off", flag)
}

func newDomainsSetCmd(a *app) *cobra.Command {
	var autoRenew, lock, privacy string
	cmd := &cobra.Command{
		Use:   "set <domain> [--auto-renew=on|off] [--lock=on|off] [--privacy=on|off]",
		Short: "Turn auto-renew, the transfer lock or privacy protection on or off",
		Long: `Change a domain's settings. Only what you pass changes. The lock and privacy
are applied at the registrar within minutes ("pending" in pantech domains get).
Turn the transfer lock off only to move the domain to another registrar.`,
		Example: `  pantech domains set example.com --auto-renew=off
  pantech domains set example.com --lock=off`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			for _, s := range []struct{ flag, field, value string }{{"auto-renew", "auto_renew", autoRenew}, {"lock", "transfer_lock", lock}, {"privacy", "privacy", privacy}} {
				if !cmd.Flags().Changed(s.flag) {
					continue
				}
				v, err := parseOnOff(s.flag, s.value)
				if err != nil {
					return &usageError{err, cmd}
				}
				body[s.field] = v
			}
			if len(body) == 0 {
				return &usageError{errors.New("nothing to change: pass --auto-renew, --lock or --privacy"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := domainID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if v, ok := body["privacy"]; ok && v == false {
				// Refuse before asking the registrar for what its registry never allows.
				var cur api.Domain
				if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: domainPath(id)}, &cur); err != nil {
					return domainError(err, args[0])
				}
				if offer, err := domainOffer(cmd, c, cur.Name); err == nil && offer.PrivacyAlwaysOn {
					return &usageError{errors.New(privacyAlwaysOnText(offer.TLD) + ": it cannot be turned off"), cmd}
				}
			}
			var d api.Domain
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: domainPath(id), Body: body}, &d)
			if err != nil {
				return domainError(api.AsFlags(err, domainFlags), args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Updated %s.", d.Name)
				if v, ok := body["transfer_lock"]; ok && v == false {
					a.out.Warn("The transfer lock is off: with its auth code, the domain can be moved to another registrar.")
				}
			}
			a.printDomain(&d, res.Body)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&autoRenew, "auto-renew", "", "on or off")
	f.StringVar(&lock, "lock", "", "the transfer lock: on or off")
	f.StringVar(&privacy, "privacy", "", "privacy protection: on or off")
	return cmd
}

func newDomainsNameServersCmd(a *app) *cobra.Command {
	var pantech bool
	var ns []string
	var zone string
	cmd := &cobra.Command{
		Use:     "nameservers <domain> --pantech | --ns HOST…",
		Aliases: []string{"ns", "name-servers"},
		Short:   "Point a domain at Pantech DNS or at other name servers",
		Long: `Choose who answers DNS for a domain. --pantech points it at its Pantech DNS
zone (--zone to name another zone of the same name); --ns sets 2 to 6 other
name servers (repeat it). Applied at the registrar within minutes.`,
		Example: `  pantech domains nameservers example.com --pantech
  pantech domains nameservers example.com --ns ns1.example.net --ns ns2.example.net`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case pantech == (len(ns) > 0):
				return &usageError{errors.New("pass --pantech, or --ns for each name server"), cmd}
			case zone != "" && !pantech:
				return &usageError{errors.New("--zone goes with --pantech"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := domainID(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"use_pantech_dns": pantech}
			if pantech && zone != "" {
				zid, err := zoneID(cmd, c, zone)
				if err != nil {
					return err
				}
				body["zone_id"] = zid
			}
			if !pantech {
				body["name_servers"] = ns
			}
			var d api.Domain
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPut, Path: domainPath(id) + "/name-servers", Body: body}, &d)
			if err != nil {
				if api.IsCode(err, "DOMAIN_ZONE_MISMATCH") {
					return fmt.Errorf("%w\nThe zone must have the domain's name: pantech dns zones list", err)
				}
				return domainError(api.AsFlags(err, domainFlags), args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Changing the name servers of %s; applied at the registrar within minutes.", d.Name)
			}
			a.printDomain(&d, res.Body)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&pantech, "pantech", false, "use Pantech DNS")
	f.StringSliceVar(&ns, "ns", nil, "a name server host; repeat for each")
	f.StringVar(&zone, "zone", "", "with --pantech, the zone to use (id or name)")
	return cmd
}

func newDomainsContactCmd(a *app) *cobra.Command {
	var set string
	cmd := &cobra.Command{
		Use:   "contact <domain> [--set FILE]",
		Short: "Show or change a domain's registrant contact (owners and admins)",
		Long: `Show the registrant contact of a domain, or replace it with --set and a JSON
file shaped as for register. The contact is personal data: only owners and
admins can read it. A change is applied at the registrar within minutes, and a
new email address may need verifying again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var contact json.RawMessage
			if set != "" {
				var err error
				if contact, err = readContact(set); err != nil {
					return err
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := domainID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if set != "" {
				var d api.Domain
				res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPut, Path: domainPath(id) + "/contact", Body: map[string]any{"contact": contact}}, &d)
				if err != nil {
					return domainError(err, args[0])
				}
				if !a.out.JSON && !a.out.Quiet {
					a.out.Success("Changing the contact of %s; applied at the registrar within minutes.", d.Name)
				}
				a.printDomain(&d, res.Body)
				return nil
			}
			var ct map[string]any
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: domainPath(id) + "/contact"}, &ct)
			if err != nil {
				return domainError(err, args[0])
			}
			if a.out.JSON || a.out.Quiet {
				a.out.RawJSON(res.Body)
				return nil
			}
			field := func(k string) string {
				if s, ok := ct[k].(string); ok && s != "" {
					return s
				}
				return "—"
			}
			name := strings.TrimSpace(field("first_name") + " " + field("last_name"))
			address := []string{}
			for _, k := range []string{"address_line1", "address_line2", "city", "state", "postal_code", "country_code"} {
				if v := field(k); v != "—" {
					address = append(address, v)
				}
			}
			a.out.Print(output.Detail{
				Title: name,
				Sections: [][]output.Pair{
					{{"Type", field("type")}, {"Organization", field("organization_name")}},
					{{"Email", field("email")}, {"Phone", field("phone")}, {"Address", strings.Join(address, ", ")}},
				},
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "replace the contact with this JSON file")
	return cmd
}

// authCodeWait bounds how long auth-code waits for the registrar.
var authCodeWait = 5 * time.Minute

func newDomainsAuthCodeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "auth-code <domain>",
		Short: "Get the code that moves a domain to another registrar (shown once)",
		Long: `Fetch a domain's auth code (EPP code), the key another registrar asks for to
take the domain away from Pantech. It is printed once, alone on stdout, and
then only for an hour. Turn the transfer lock off first (pantech domains set
<domain> --lock=off). It asks first (--yes to skip). Owners and admins only;
an API key needs the delete scope.`,
		Example: `  pantech domains auth-code example.com > code.txt`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := domainID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Get the auth code of %s (%s)? Anyone with it can move the domain away from Pantech.", args[0], id)); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: domainPath(id) + "/auth-code"}, nil); err != nil {
				return domainError(err, args[0])
			}
			steps := a.out.Steps()
			steps.Start("Fetching the auth code from the registrar")
			wctx, cancel := context.WithTimeout(ctx(cmd), authCodeWait)
			defer cancel()
			code, raw, err := api.WaitDomainAuthCode(wctx, c, id)
			if err != nil {
				steps.Fail("", "")
				if errors.Is(err, api.ErrAuthCodeTimeout) {
					return fmt.Errorf("%w\nTry again in a few minutes: pantech domains auth-code %s", err, id)
				}
				return domainError(err, args[0])
			}
			steps.Done("Fetched", "")
			if a.out.JSON {
				a.out.RawJSON(raw)
				return nil
			}
			if !a.out.Quiet {
				a.out.Warn("This code lets the domain leave Pantech: give it only to the registrar you are moving to. Shown once; it expires %s.", output.When(code.ExpiresAt))
			}
			a.out.Line("%s", output.Clean(code.AuthCode))
			return nil
		},
	}
}

func newDomainsOrdersCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "orders <domain>",
		Short: "List a domain's orders: registration, renewals, transfer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := domainID(cmd, c, args[0])
			if err != nil {
				return err
			}
			orders, raw, err := listItems[api.DomainOrder](cmd, c, domainPath(id)+"/orders", nil)
			if err != nil {
				return domainError(err, args[0])
			}
			if printList(a, orders, raw, func(o api.DomainOrder) string { return o.ID }, "No orders.", "") {
				return nil
			}
			rows := make([][]string, len(orders))
			for i := range orders {
				rows[i] = a.orderRow(&orders[i])
			}
			a.out.Table(orderColumns, rows)
			a.out.Summary(count(len(orders), "order"))
			for i := range orders {
				a.showPayment(&orders[i])
			}
			return nil
		},
	}
}

var orderColumns = []string{"order", "kind", "years", "amount", "status", "failure", "created"}

func (a *app) orderRow(o *api.DomainOrder) []string {
	failure := output.Or(o.FailureCode)
	if o.Refunded {
		failure += " (refunded)"
	}
	return []string{a.out.Dim(o.ID), o.Kind, strconv.Itoa(o.Years), money(o.AmountMinor, o.Currency), a.out.State(o.Status), failure, output.Ago(o.CreatedAt)}
}

// domainOrderError adds what to run next to a retry's errors.
func domainOrderError(err error, o *api.DomainOrder) error {
	domain := "<domain>"
	if o != nil && o.DomainID != "" {
		domain = output.Clean(o.DomainID)
	}
	switch {
	case api.IsCode(err, "DOMAIN_ORDER_NOT_FOUND"), api.IsCode(err, "RESOURCE_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech domains orders %s", err, domain)
	case api.IsCode(err, "DOMAIN_ORDER_NOT_AWAITING_PAYMENT"):
		return fmt.Errorf("%w\nIt is not waiting for a payment, or its hour has ended: place the order again. See: pantech domains orders %s", err, domain)
	}
	return domainError(err, domain)
}

func newDomainsRetryPaymentCmd(a *app) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "retry-payment <order>",
		Short: "Try a domain order's payment again now",
		Long: `An order your credit does not cover waits for its payment (awaiting_payment)
for an hour. In US dollars your saved card is charged the difference (a link is
printed only if your bank asks you to confirm the payment); in naira, add
credit and the order is paid from it.

retry-payment makes the order try again now and wait another hour: it charges
your saved card once more (after a decline, or once you have saved a new card),
or takes credit you have just added. The order is never charged twice. It asks
first (--yes to skip). Order ids (dor_…) are in pantech domains orders.`,
		Example: `  pantech domains retry-payment dor_… --wait`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			o, _, err := api.GetDomainOrder(ctx(cmd), c, args[0])
			if err != nil {
				return domainOrderError(err, nil)
			}
			if o.Status != "awaiting_payment" {
				return fmt.Errorf("order %s is %s, not waiting for a payment: there is nothing to retry\nSee: pantech domains orders %s", output.Clean(o.ID), output.Clean(o.Status), output.Clean(o.DomainID))
			}
			question := fmt.Sprintf("Retry the payment of the %s order %s for %s (%s plus VAT)?", o.Kind, o.ID, o.DomainName, money(o.AmountMinor, o.Currency))
			if o.Currency == "USD" {
				question += " Your saved card is charged what your credit does not cover; the order is never charged twice."
			} else {
				question += " It is paid from your credit, including credit you have just added."
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			next, res, err := api.RetryDomainOrderPayment(ctx(cmd), c, o.ID)
			if err != nil {
				return domainOrderError(err, o)
			}
			o = next
			if wait {
				steps := a.out.Steps()
				steps.Mark("Payment retried", money(o.AmountMinor, o.Currency)+" plus VAT")
				return a.followOrder(cmd, c, steps, o, o.DomainID)
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			if a.out.Quiet {
				a.out.Line("%s", output.Clean(o.ID))
				a.showPayment(o)
				return nil
			}
			a.out.Success("Payment retried: %s %s (order %s).", o.Kind, o.DomainName, o.ID)
			a.out.Table(orderColumns, [][]string{a.orderRow(o)})
			a.showPayment(o)
			a.out.Next("Follow it", "pantech domains orders "+o.DomainID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the order has succeeded or failed")
	return cmd
}
