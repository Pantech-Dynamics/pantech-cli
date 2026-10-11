package cli

import (
	"strings"
	"testing"
)

const imagesBody = `{"data":[
{"id":"img_1","slug":"ubuntu-24-04","name":"Ubuntu","version":"24.04 LTS","zones":["af-abj-1","af-abj-2"]},
{"id":"img_2","slug":"ubuntu-24-04-encrypted","name":"Ubuntu (encrypted)","version":"24.04 LTS","zones":["af-abj-1"],"encrypted":true,"encrypted_variant_of":"ubuntu-24-04"},
{"id":"img_3","slug":"debian-12","name":"Debian","version":"12","zones":[]}
],"next_cursor":null}`

func TestImagesShowTheEncryptedColumn(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /images", 200, imagesBody)
	r := run(t, srv, "", "images")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.stdout, "encrypted") {
		t.Fatalf("no encrypted column:\n%s", r.stdout)
	}
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.HasPrefix(line, "ubuntu-24-04-encrypted") && !strings.Contains(line, "yes") {
			t.Fatalf("encrypted image not marked: %q", line)
		}
		if strings.HasPrefix(line, "debian-12") && strings.Contains(line, "yes") {
			t.Fatalf("plain image marked encrypted: %q", line)
		}
	}
}

func TestVMCreateEncrypted(t *testing.T) {
	cases := []struct {
		name, image, wantSlug, wantErr string
		images                         string
	}{
		{name: "picks the pair", image: "ubuntu-24-04", wantSlug: "ubuntu-24-04-encrypted", images: imagesBody},
		{name: "accepts an encrypted slug", image: "ubuntu-24-04-encrypted", wantSlug: "ubuntu-24-04-encrypted", images: imagesBody},
		{name: "no pair", image: "debian-12", wantErr: "no encrypted variant of debian-12", images: imagesBody},
		{name: "not enabled", image: "ubuntu-24-04", wantErr: "enabled per organization on request",
			images: `{"data":[{"id":"img_1","slug":"ubuntu-24-04","name":"Ubuntu","version":"24.04 LTS","zones":[]}],"next_cursor":null}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /images", 200, c.images).on("GET /plans", 200, plansBody).on("POST /instances", 202, orderAccepted)
			r := run(t, srv, "", "vm", "create", "--name", "web-1", "--plan", "starter", "--image", c.image, "--encrypted", "--yes", "--no-wait")
			if c.wantErr != "" {
				if r.err == nil || !strings.Contains(r.err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", r.err, c.wantErr)
				}
				if len(f.sent("POST", "/instances")) != 0 {
					t.Fatal("ordered anyway")
				}
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("POST", "/instances")["image_slug"]; got != c.wantSlug {
				t.Fatalf("image_slug = %v, want %s", got, c.wantSlug)
			}
		})
	}
}

func TestVMCreateEncryptedSlugDirectly(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /plans", 200, plansBody).on("POST /instances", 202, orderAccepted)
	r := run(t, srv, "", "vm", "create", "--name", "web-1", "--plan", "starter", "--image", "ubuntu-24-04-encrypted", "--yes", "--no-wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/instances")["image_slug"]; got != "ubuntu-24-04-encrypted" {
		t.Fatalf("image_slug = %v", got)
	}
	if len(f.sent("GET", "/images")) != 0 {
		t.Fatal("looked up images without --encrypted")
	}
}

func TestVMGetMarksAnEncryptedVM(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /instances/vm_1", 200, `{"id":"vm_1","name":"web-1","image_slug":"ubuntu-24-04-encrypted","encrypted":true,"desired_state":"running","observed_state":"running","private_network_state":"none"}`)
	r := run(t, srv, "", "vm", "get", "vm_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.stdout, "(encrypted)") {
		t.Fatalf("no encrypted marker:\n%s", r.stdout)
	}
}
