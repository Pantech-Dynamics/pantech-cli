package api

import (
	"encoding/json"
	"testing"
)

func TestAddress(t *testing.T) {
	s := func(v string) *string { return &v }
	for name, c := range map[string]struct {
		vm   Instance
		want string
	}{
		// What the API returns for a standard VM: its one address in private_ipv4.
		"standard, address in private_ipv4": {Instance{PrivateIPv4: s("102.211.122.76")}, "102.211.122.76"},
		"standard, public_ipv4 set":         {Instance{PublicIPv4: s("102.211.122.80"), PrivateIPv4: s("102.211.122.80")}, "102.211.122.80"},
		"VPC, private only":                 {Instance{SubnetID: s("snet_1"), PrivateIPv4: s("10.0.1.12")}, ""},
		"VPC with a static IP":              {Instance{SubnetID: s("snet_1"), PublicIPv4: s("102.211.122.90"), PrivateIPv4: s("10.0.1.12")}, "102.211.122.90"},
		"nothing yet":                       {Instance{}, ""},
	} {
		if got := c.vm.Address(); got != c.want {
			t.Errorf("%s: Address() = %q, want %q", name, got, c.want)
		}
	}
}

// A real standard VM from the production API (Oct 2026), as returned.
func TestAddressFromProduction(t *testing.T) {
	body := `{"id":"vm_06gfeqpnbxsgb73ww1nf40tkbm","name":"cosmic-cobra","plan_slug":"individual","image_slug":"ubuntu-24-04","region":"af-abj","zone":"af-abj-1","network_id":null,"subnet_id":null,"security_group_id":"sg_06ges3vep9xf38t2swjynwtds0","public_ipv4":null,"private_ipv4":"102.211.122.76","desired_state":"running","observed_state":"running"}`
	var vm Instance
	if err := json.Unmarshal([]byte(body), &vm); err != nil {
		t.Fatal(err)
	}
	if vm.InVPC() || vm.Address() != "102.211.122.76" {
		t.Fatalf("InVPC=%v Address=%q: a standard VM is reached at its one address", vm.InVPC(), vm.Address())
	}
}

func TestOrderFailedExplainsTheCode(t *testing.T) {
	s := func(v string) *string { return &v }
	for status, code := range map[string]string{"payment_failed": "organization_deleted", "failed": "organization_deleted"} {
		err := &OrderFailed{Order: &Order{ID: "ord_1", Status: status, FailureCode: s(code)}}
		if got := err.Error(); got != "order ord_1 did not provision: it was cancelled because its organization was deleted; any payment taken is back in your credit (organization_deleted)" {
			t.Errorf("%s: %q", status, got)
		}
	}
	err := &OrderFailed{Order: &Order{ID: "ord_1", Status: "payment_failed", FailureCode: s("payment_expired")}}
	if got := err.Error(); got != "order ord_1 did not provision: it was not paid within an hour (payment_expired)" {
		t.Errorf("%q", got)
	}
	err = &OrderFailed{Order: &Order{ID: "ord_1", Status: "failed", FailureCode: s("something_new")}}
	if got := err.Error(); got != "order ord_1 did not provision: something_new" {
		t.Errorf("unknown codes are shown as they are: %q", got)
	}
}

func TestOperationFailedNamesTheOperation(t *testing.T) {
	op := &Operation{ID: "op_1", Kind: "create_instance", Failure: &Failure{Code: "PROVISIONING_FAILED", Reason: "We couldn't complete this change. Try again; if it keeps failing, contact support with the operation id."}}
	want := "create_instance failed: We couldn't complete this change. Try again; if it keeps failing, contact support with the operation id. (PROVISIONING_FAILED, operation op_1)"
	if got := (&OperationFailed{Op: op}).Error(); got != want {
		t.Fatalf("%q", got)
	}
}
