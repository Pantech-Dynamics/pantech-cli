package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newCertificatesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "certificates",
		Aliases: []string{"certs", "cert"},
		Short:   "TLS certificates: issued free and renewed automatically, or imported",
		Long: `Pantech issues TLS certificates from a publicly trusted CA for free, and
renews them automatically 30 days before they expire.

  pantech certificates request example.com '*.example.com' --wait

A name in one of your active Pantech DNS zones is validated with no work on
your side. For any other name, the request shows a CNAME record to add at
your DNS host, once: it keeps every renewal automatic for as long as it
stays. A certificate bought elsewhere can be imported instead.

Certificates are named by id (cert_…). Any member can read; owners and admins
make changes. Exporting, revoking and deleting need an API key with the
delete scope, and ask first.`,
	}
	cmd.AddCommand(newCertsListCmd(a), newCertsRequestCmd(a), newCertsImportCmd(a), newCertsGetCmd(a), newCertsUpdateCmd(a),
		newCertsVerifyCmd(a), newCertsReimportCmd(a), newCertsExportCmd(a), newCertsRevokeCmd(a), newCertsDeleteCmd(a))
	return cmd
}

func certPath(id string) string { return "/certificates/" + url.PathEscape(id) }

// certError adds what to run next to the API's certificate errors.
func certError(err error, id string) error {
	switch {
	case api.IsCode(err, "CERTIFICATES_UNAVAILABLE"):
		return fmt.Errorf("%w\nCertificates are not available on this deployment", err)
	case api.IsCode(err, "CERTIFICATE_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech certificates list", err)
	case api.IsCode(err, "CERTIFICATE_IN_USE"):
		return fmt.Errorf("%w\nDetach it from what uses it first: pantech certificates get %s", err, id)
	case api.IsCode(err, "CERTIFICATE_NOT_EXPORTABLE"):
		return fmt.Errorf("%w\nOnly a certificate requested or imported with --exportable can be exported; request a new one with --exportable", err)
	case api.IsCode(err, "CERTIFICATE_NOT_ISSUED"):
		return fmt.Errorf("%w\nSee its status: pantech certificates get %s", err, id)
	case api.IsCode(err, "CERTIFICATE_NOT_PENDING"):
		return fmt.Errorf("%w\nIt is not waiting for validation: pantech certificates get %s", err, id)
	case api.IsCode(err, "CERTIFICATE_RATE_LIMITED"):
		return fmt.Errorf("%w\nThese names were requested 5 times in 7 days: reuse an existing certificate (pantech certificates list --q <name>)", err)
	}
	return err
}

var certFlagNames = map[string]string{"names": "<name>", "key_type": "--key-type", "description": "--description", "certificate_pem": "--cert",
	"chain_pem": "--chain", "private_key_pem": "--key", "reason": "--reason", "exportable": "--exportable"}

// cnameValidations are the validations that need a CNAME at the customer's
// DNS host, one per CNAME name (names that share one are listed once).
func cnameValidations(c *api.Certificate) []api.CertificateValidation {
	var out []api.CertificateValidation
	seen := map[string]bool{}
	for _, v := range c.Validations {
		if v.Method != "cname" || v.CNAMEName == nil || v.CNAMETarget == nil || seen[*v.CNAMEName] {
			continue
		}
		seen[*v.CNAMEName] = true
		out = append(out, v)
	}
	return out
}

// showCNAMEs tells the customer which records to add, on stderr: they are
// instructions, not the result.
func (a *app) showCNAMEs(c *api.Certificate) {
	vs := cnameValidations(c)
	if len(vs) == 0 || a.out.JSON || a.out.Quiet {
		return
	}
	pending := 0
	for _, v := range vs {
		if v.Status == "pending" {
			pending++
		}
	}
	if pending == 0 {
		return
	}
	a.out.Note("")
	a.out.Note("Add these CNAME records at the DNS host of these names (not in a Pantech DNS zone), once:")
	for _, v := range vs {
		if v.Status == "pending" {
			a.out.Note("  %s  CNAME  %s", *v.CNAMEName, *v.CNAMETarget)
		}
	}
	deadline := ""
	if c.ValidationDeadlineAt != nil {
		deadline = " by " + output.When(c.ValidationDeadlineAt)
	}
	a.out.Note("They keep renewals automatic while they stay. The request fails if they are not in place%s.", deadline)
	a.out.Next("Check now once added", "pantech certificates verify "+c.ID)
}

var pendingReasonText = map[string]string{
	"awaiting_cname":    "waiting for the validation CNAMEs",
	"awaiting_dns":      "publishing the challenge records",
	"awaiting_ca":       "the CA is validating and issuing",
	"rate_limited":      "the CA asked to slow down; it retries by itself",
	"ca_unavailable":    "the CA could not be reached; it retries by itself",
	"validation_failed": "a renewal's validation failed; it retries",
}

func certDetail(out *output.Printer, c *api.Certificate) output.Detail {
	desc := c.Description
	if desc == "" {
		desc = "—"
	}
	title := c.ID
	if len(c.Names) > 0 {
		title = c.Names[0]
	}
	status := []output.Pair{{"Kind", c.Kind}, {"Key", c.KeyType}, {"Exportable", yesNo(c.Exportable)}, {"Description", desc}}
	if c.PendingReason != nil {
		status = append(status, output.Pair{"Waiting", pendingReasonText[*c.PendingReason] + " (" + *c.PendingReason + ")"})
	}
	if c.FailureCode != nil {
		status = append(status, output.Pair{"Failure", *c.FailureCode})
	}
	if c.RevocationPending {
		status = append(status, output.Pair{"Revocation", "pending at the CA"})
	}
	d := output.Detail{
		Title:    title,
		State:    out.State(c.Status),
		Subtitle: c.ID,
		Sections: [][]output.Pair{listPairs("Names", c.Names, "—"), status},
	}
	if c.IssuedAt != nil || c.NotAfter != nil {
		d.Sections = append(d.Sections, []output.Pair{
			{"Issuer", output.Or(c.Issuer)}, {"Serial", output.Or(c.SerialNumber)}, {"SHA-256", output.Or(c.FingerprintSHA256)},
			{"Issued", output.When(c.IssuedAt)}, {"Expires", output.When(c.NotAfter)}, {"Renews from", output.When(c.RenewAfter)}, {"Renewal", output.Or(c.RenewalStatus)},
		})
	}
	if len(c.Validations) > 0 {
		var pairs []output.Pair
		for i, v := range c.Validations {
			how := "Pantech DNS (automatic)"
			if v.Method == "cname" && v.CNAMEName != nil && v.CNAMETarget != nil {
				how = *v.CNAMEName + " CNAME " + *v.CNAMETarget
			}
			label := ""
			if i == 0 {
				label = "Validation"
			}
			pairs = append(pairs, output.Pair{label, v.Name + ": " + v.Status + ", " + how})
		}
		d.Sections = append(d.Sections, pairs)
	}
	if len(c.InUseBy) > 0 {
		uses := make([]string, len(c.InUseBy))
		for i, u := range c.InUseBy {
			uses[i] = u.Type + " " + u.ID
		}
		d.Sections = append(d.Sections, listPairs("In use by", uses, "—"))
	}
	return d
}

func (a *app) printCert(c *api.Certificate, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(c.ID))
	default:
		a.out.Print(certDetail(a.out, c))
	}
}

func newCertsListCmd(a *app) *cobra.Command {
	var q, status, kind string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List certificates, newest first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			query := url.Values{}
			for k, v := range map[string]string{"q": q, "status": status, "kind": kind} {
				if v != "" {
					query.Set(k, v)
				}
			}
			certs, raw, err := listItems[api.Certificate](cmd, c, "/certificates", query)
			if err != nil {
				return certError(err, "")
			}
			if printList(a, certs, raw, func(x api.Certificate) string { return x.ID }, "No certificates yet.", "pantech certificates request example.com") {
				return nil
			}
			rows := make([][]string, len(certs))
			for i := range certs {
				x := &certs[i]
				names := strings.Join(x.Names, ", ")
				if len(x.Names) > 3 {
					names = strings.Join(x.Names[:3], ", ") + fmt.Sprintf(" +%d", len(x.Names)-3)
				}
				rows[i] = []string{a.out.Dim(x.ID), names, x.Kind, a.out.State(x.Status), output.When(x.NotAfter)}
			}
			a.out.Table([]string{"id", "names", "kind", "status", "expires"}, rows)
			a.out.Summary(count(len(certs), "certificate"))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&q, "q", "", "only certificates with a name containing this text")
	f.StringVar(&status, "status", "", "pending_validation, issuing, issued, renewing, expired, revoked or failed")
	f.StringVar(&kind, "kind", "", "public or imported")
	return cmd
}

// waitCert follows a certificate until it is issued, with a spinner, and
// prints it.
func (a *app) waitCert(cmd *cobra.Command, c *api.Client, cert *api.Certificate, raw []byte) error {
	steps := a.out.Steps()
	steps.Start("Issuing " + cert.Names[0])
	got, err := api.WaitCertificate(ctx(cmd), c, cert.ID, nil, &raw)
	if err != nil {
		steps.Fail("", "")
		if got != nil && a.out.JSON {
			a.out.RawJSON(raw)
		}
		return certError(err, cert.ID)
	}
	steps.Done("Issued", "")
	a.printCert(got, raw)
	return nil
}

func newCertsRequestCmd(a *app) *cobra.Command {
	var keyType, description string
	var exportable, wait bool
	cmd := &cobra.Command{
		Use:   "request <name>...",
		Short: "Request a certificate for up to 100 names; issued free and renewed automatically",
		Long: `Request a certificate for 1 to 100 names, wildcards as '*.example.com'
(quote them so your shell leaves them alone). Names in an active Pantech DNS
zone are validated automatically; for others, it prints the CNAME to add at
your DNS host. --exportable lets the private key be exported later (it cannot
be changed afterwards). --wait follows the request until it is issued.`,
		Example: `  pantech certificates request example.com '*.example.com' --wait
  pantech certificates request api.example.org --key-type rsa-2048 --exportable`,
		Args: cobra.RangeArgs(1, 100),
		RunE: func(cmd *cobra.Command, args []string) error {
			names := make([]string, len(args))
			for i, n := range args {
				names[i] = domainName(n)
			}
			body := map[string]any{"names": names}
			if cmd.Flags().Changed("key-type") {
				body["key_type"] = keyType
			}
			if exportable {
				body["exportable"] = true
			}
			if description != "" {
				body["description"] = description
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var cert api.Certificate
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/certificates", Body: body}, &cert)
			if err != nil {
				return certError(api.AsFlags(err, certFlagNames), "")
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Requested %s (%s).", count(len(cert.Names), "name"), cert.ID)
			}
			a.showCNAMEs(&cert)
			if wait {
				return a.waitCert(cmd, c, &cert, res.Body)
			}
			a.printCert(&cert, res.Body)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&keyType, "key-type", "ecdsa-p256", "ecdsa-p256 or rsa-2048")
	f.BoolVar(&exportable, "exportable", false, "allow exporting the private key later (fixed at creation)")
	f.StringVar(&description, "description", "", "a note for yourself")
	f.BoolVar(&wait, "wait", false, "wait until it is issued")
	return cmd
}

const maxPEMFile = 64 << 10

func readPEM(flag, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", flag, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPEMFile+1))
	if err != nil {
		return "", fmt.Errorf("%s: %w", flag, err)
	}
	if len(data) > maxPEMFile {
		return "", fmt.Errorf("%s: %s is larger than 64 KiB", flag, path)
	}
	if !strings.Contains(string(data), "-----BEGIN ") {
		return "", fmt.Errorf("%s: %s is not a PEM file", flag, path)
	}
	return string(data), nil
}

// pemBody reads --cert, --chain and --key into a request body.
func pemBody(cmd *cobra.Command, certFile, chainFile, keyFile string) (map[string]any, error) {
	if certFile == "" || keyFile == "" {
		return nil, &usageError{errors.New("pass --cert and --key (and --chain for the intermediates)"), cmd}
	}
	body := map[string]any{}
	for _, f := range []struct{ flag, path, field string }{{"--cert", certFile, "certificate_pem"}, {"--chain", chainFile, "chain_pem"}, {"--key", keyFile, "private_key_pem"}} {
		if f.path == "" {
			continue
		}
		pem, err := readPEM(f.flag, f.path)
		if err != nil {
			return nil, err
		}
		body[f.field] = pem
	}
	return body, nil
}

func newCertsImportCmd(a *app) *cobra.Command {
	var certFile, chainFile, keyFile, description string
	var exportable bool
	cmd := &cobra.Command{
		Use:   "import --cert cert.pem --key key.pem [--chain chain.pem]",
		Short: "Import a certificate bought elsewhere, with its private key",
		Long: `Import a certificate and its unencrypted private key (PKCS #1, PKCS #8 or
SEC 1), with its intermediates in --chain. It must be valid now. Imported
certificates are not renewed: replace one in place with reimport.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := pemBody(cmd, certFile, chainFile, keyFile)
			if err != nil {
				return err
			}
			if exportable {
				body["exportable"] = true
			}
			if description != "" {
				body["description"] = description
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var cert api.Certificate
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/certificates/import", Body: body}, &cert)
			if err != nil {
				return certError(api.AsFlags(err, certFlagNames), "")
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Imported %s.", cert.ID)
			}
			a.printCert(&cert, res.Body)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&certFile, "cert", "", "the certificate (leaf) PEM file (required)")
	f.StringVar(&chainFile, "chain", "", "the intermediates PEM file")
	f.StringVar(&keyFile, "key", "", "the private key PEM file (required)")
	f.BoolVar(&exportable, "exportable", false, "allow exporting the private key later")
	f.StringVar(&description, "description", "", "a note for yourself")
	return cmd
}

func newCertsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <certificate id>",
		Aliases: []string{"show"},
		Short:   "Show a certificate, its validations and expiry",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var cert api.Certificate
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: certPath(args[0])}, &cert)
			if err != nil {
				return certError(err, args[0])
			}
			a.printCert(&cert, res.Body)
			a.showCNAMEs(&cert)
			return nil
		},
	}
}

func newCertsUpdateCmd(a *app) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "update <certificate id> --description TEXT",
		Short: "Change a certificate's description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("description") {
				return &usageError{errors.New("nothing to change: pass --description"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var cert api.Certificate
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: certPath(args[0]), Body: map[string]any{"description": description}}, &cert)
			if err != nil {
				return certError(api.AsFlags(err, certFlagNames), args[0])
			}
			a.printCert(&cert, res.Body)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", `a new note ("" clears it)`)
	return cmd
}

func newCertsVerifyCmd(a *app) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "verify <certificate id>",
		Short: "Check the validation CNAMEs now instead of within 5 minutes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var cert api.Certificate
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: certPath(args[0]) + "/verify"}, &cert)
			if err != nil {
				return certError(err, args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Checking the validation records of %s.", cert.ID)
			}
			if wait {
				return a.waitCert(cmd, c, &cert, res.Body)
			}
			a.printCert(&cert, res.Body)
			a.showCNAMEs(&cert)
			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until it is issued")
	return cmd
}

func newCertsReimportCmd(a *app) *cobra.Command {
	var certFile, chainFile, keyFile string
	cmd := &cobra.Command{
		Use:   "reimport <certificate id> --cert cert.pem --key key.pem [--chain chain.pem]",
		Short: "Replace an imported certificate in place, for the same names",
		Long: `Replace an imported certificate with a new one for the same names, for
example its renewal from the CA you bought it from. Everything using it keeps
working and serves the new one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := pemBody(cmd, certFile, chainFile, keyFile)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var cert api.Certificate
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: certPath(args[0]) + "/reimport", Body: body}, &cert)
			if err != nil {
				if api.IsCode(err, "CERTIFICATE_NAMES_MISMATCH") {
					return fmt.Errorf("%w\nA reimport must name exactly the same names; import it as a new certificate instead", err)
				}
				return certError(api.AsFlags(err, certFlagNames), args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Replaced %s; it expires %s.", cert.ID, output.When(cert.NotAfter))
			}
			a.printCert(&cert, res.Body)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&certFile, "cert", "", "the new certificate (leaf) PEM file (required)")
	f.StringVar(&chainFile, "chain", "", "the intermediates PEM file")
	f.StringVar(&keyFile, "key", "", "the new private key PEM file (required)")
	return cmd
}

func newCertsExportCmd(a *app) *cobra.Command {
	var outDir string
	var toStdout, force bool
	cmd := &cobra.Command{
		Use:   "export <certificate id> [--out-dir DIR]",
		Short: "Write the certificate, its chain and its private key to files",
		Long: `Export a certificate created with --exportable: cert.pem (the leaf),
chain.pem (the intermediates), fullchain.pem (both, what most servers take)
and privkey.pem, written to --out-dir (default ./<certificate id>) readable
only by you (0600). An existing file is kept unless --force, and a symbolic
link is never written through. The private key is never printed unless
--stdout, which prints the full chain and then the key. Every export is
recorded and emails every owner and admin, and it asks first.`,
		Example: `  pantech certificates export cert_01JD… --out-dir /etc/ssl/example
  pantech certificates export cert_01JD… --stdout --yes > bundle.pem`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if toStdout && cmd.Flags().Changed("out-dir") {
				return &usageError{errors.New("pass --out-dir or --stdout, not both"), cmd}
			}
			if outDir == "" {
				outDir = args[0]
			}
			var files []pendingFile
			if !toStdout {
				for _, name := range []string{"cert.pem", "chain.pem", "fullchain.pem", "privkey.pem"} {
					path := filepath.Join(outDir, name)
					if err := checkTarget(path, force); err != nil {
						if errors.Is(err, errFileExists) {
							return fmt.Errorf("%w: pass --force to replace it", err)
						}
						return err
					}
					files = append(files, pendingFile{path: path})
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			where := "to " + outDir
			if toStdout {
				where = "to standard output"
			}
			if err := a.confirm(fmt.Sprintf("Export certificate %s with its private key %s? Every owner and admin is emailed about it.", args[0], where)); err != nil {
				return err
			}
			if !toStdout {
				if err := os.MkdirAll(outDir, 0o700); err != nil {
					return err
				}
			}
			var exp api.ExportedCertificate
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: certPath(args[0]) + "/export"}, &exp); err != nil {
				return certError(err, args[0])
			}
			full := withNewline(exp.CertificatePEM) + exp.ChainPEM
			if toStdout {
				_, err := io.WriteString(a.out.Out, withNewline(full)+withNewline(exp.PrivateKeyPEM))
				return err
			}
			for i, data := range []string{exp.CertificatePEM, exp.ChainPEM, full, exp.PrivateKeyPEM} {
				files[i].data = []byte(withNewline(data))
			}
			if err := writeFiles(files, 0o600); err != nil {
				return err
			}
			if a.out.Quiet {
				a.out.Line("%s", output.Clean(outDir))
				return nil
			}
			if !a.out.JSON {
				a.out.Success("Wrote cert.pem, chain.pem, fullchain.pem and privkey.pem to %s (only you can read them). It expires %s.", outDir, output.When(&exp.NotAfter))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&outDir, "out-dir", "", "the directory to write the files to (default ./<certificate id>)")
	f.BoolVar(&toStdout, "stdout", false, "print the full chain and the private key to stdout instead of writing files")
	f.BoolVar(&force, "force", false, "replace files already there")
	return cmd
}

func withNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func newCertsRevokeCmd(a *app) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "revoke <certificate id> [--reason key_compromise]",
		Short: "Ask the CA to revoke a certificate (cannot be undone)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reason != "unspecified" && reason != "key_compromise" {
				return &usageError{errors.New("--reason must be unspecified or key_compromise"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Revoke certificate %s? Browsers stop trusting it, and this cannot be undone.", args[0])); err != nil {
				return err
			}
			var cert api.Certificate
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: certPath(args[0]) + "/revoke", Body: map[string]any{"reason": reason}}, &cert)
			if err != nil {
				return certError(api.AsFlags(err, certFlagNames), args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Revoking %s; the CA confirms within minutes.", cert.ID)
			}
			a.printCert(&cert, res.Body)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "unspecified", "unspecified or key_compromise")
	return cmd
}

func newCertsDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <certificate id>",
		Aliases: []string{"rm"},
		Short:   "Delete a certificate and its private key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete certificate %s and its private key? It is no longer renewed, and this cannot be undone.", args[0])); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: certPath(args[0])}, nil); err != nil {
				return certError(err, args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Deleted %s.", args[0])
			}
			return nil
		},
	}
}
