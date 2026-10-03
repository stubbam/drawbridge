package adguard_test

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard"
	"github.com/stuffam/drawbridge/internal/adguard/adguardtest"
)

// TestContract checks the client against an AdGuard Home's API. It always runs against the fake
// (adguardtest), and it runs against a real AdGuard Home too when these are set, so the fake
// can't drift from what AdGuard Home does:
//
//	DRAWBRIDGE_ADGUARD_URL=http://127.0.0.1:3000/control \
//	DRAWBRIDGE_ADGUARD_USER=… DRAWBRIDGE_ADGUARD_PASSWORD=… go test ./internal/adguard -run Contract
//
// It makes and deletes clients named drawbridge-contract-… on addresses in the documentation
// ranges, and touches nothing else. It sends no wrong password: AdGuard Home blocks a caller
// after five.
func TestContract(t *testing.T) {
	t.Run("fake", func(t *testing.T) {
		fake := adguardtest.New(t, "drawbridge", "secret")
		runContract(t, mustClient(t, fake.URL(), "drawbridge", "secret"))
	})
	if url := os.Getenv("DRAWBRIDGE_ADGUARD_URL"); url != "" {
		t.Run("real", func(t *testing.T) {
			runContract(t, mustClient(t, url, os.Getenv("DRAWBRIDGE_ADGUARD_USER"), os.Getenv("DRAWBRIDGE_ADGUARD_PASSWORD")))
		})
	}
}

func mustClient(t *testing.T, url, user, password string) *adguard.Client {
	t.Helper()
	c, err := adguard.New(url, user, password)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// find returns the client with the exact name.
func find(t *testing.T, c *adguard.Client, name string) (adguard.Persistent, bool) {
	t.Helper()
	all, err := c.Clients(t.Context())
	if err != nil {
		t.Fatalf("listing clients: %v", err)
	}
	for _, p := range all {
		if p.Name == name {
			return p, true
		}
	}
	return adguard.Persistent{}, false
}

func runContract(t *testing.T, c *adguard.Client) {
	ctx := t.Context()
	prefix := fmt.Sprintf("drawbridge-contract-%d", time.Now().UnixNano())
	name, other, renamed := prefix+"-a", prefix+"-b", prefix+"-renamed"
	t.Cleanup(func() {
		for _, n := range []string{name, other, renamed} {
			_ = c.DeleteClient(ctx, n)
		}
	})

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Running || st.Version == "" {
		t.Errorf("Status = %+v, want a running AdGuard Home with a version", st)
	}
	if _, err := c.QueryLogConfig(ctx); err != nil {
		t.Errorf("QueryLogConfig: %v", err)
	}

	// A new client has both of its addresses, and uses AdGuard Home's global settings, which
	// are what block ads. (Added with only a name and addresses, AdGuard Home turns them off.)
	ids := []string{"192.0.2.10", "2001:DB8::10"}
	if err := c.AddClient(ctx, adguard.NewPersistent(name, ids)); err != nil {
		t.Fatalf("AddClient: %v", err)
	}
	got, ok := find(t, c, name)
	if !ok {
		t.Fatal("the added client isn't listed")
	}
	if !adguard.SameIDs(got.IDs, ids) {
		t.Errorf("IDs = %v, want %v in any spelling", got.IDs, ids)
	}
	if !got.UsesGlobalSettings() {
		t.Error("the added client doesn't use the global settings, so AdGuard Home wouldn't block ads for it")
	}

	// Every refusal is a 400 with a message, and none of them means the account was refused.
	for what, p := range map[string]adguard.Persistent{
		"the same name":    adguard.NewPersistent(name, []string{"192.0.2.99"}),
		"the same address": adguard.NewPersistent(other, []string{"192.0.2.10"}),
		"no address":       adguard.NewPersistent(other, nil),
	} {
		var ae *adguard.Error
		err := c.AddClient(ctx, p)
		if !errors.As(err, &ae) || ae.Status != 400 || ae.Message == "" || errors.Is(err, adguard.ErrUnauthorized) {
			t.Errorf("adding a client with %s: err = %v, want a 400 with a message", what, err)
		}
	}
	if _, ok := find(t, c, other); ok {
		t.Error("a refused client was added anyway")
	}

	// An update writes the whole client, so a rename starts from the client as listed. The
	// tags and the other setting stay, and so does the use of the global settings.
	tagged, err := got.WithField("tags", []string{"device_phone"})
	if err != nil {
		t.Fatal(err)
	}
	if tagged, err = tagged.WithField("ignore_statistics", true); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateClient(ctx, name, tagged); err != nil {
		t.Fatalf("UpdateClient (settings): %v", err)
	}
	cur, _ := find(t, c, name)
	cur.Name = renamed
	cur.IDs = append(cur.IDs, "192.0.2.11")
	if err := c.UpdateClient(ctx, name, cur); err != nil {
		t.Fatalf("UpdateClient (rename): %v", err)
	}
	if _, ok := find(t, c, name); ok {
		t.Error("the old name is still listed after a rename")
	}
	moved, ok := find(t, c, renamed)
	if !ok {
		t.Fatal("the renamed client isn't listed")
	}
	if !adguard.SameIDs(moved.IDs, append(slices.Clone(ids), "192.0.2.11")) {
		t.Errorf("IDs after the rename = %v", moved.IDs)
	}
	if v, _ := moved.Field("tags"); string(v) != `["device_phone"]` {
		t.Errorf("tags after the rename = %s, want them kept", v)
	}
	if v, _ := moved.Field("ignore_statistics"); string(v) != "true" {
		t.Errorf("ignore_statistics after the rename = %s, want it kept", v)
	}
	if !moved.UsesGlobalSettings() {
		t.Error("the renamed client lost its use of the global settings")
	}

	// Updating or deleting a client that isn't there is refused, with a message.
	var ae *adguard.Error
	if err := c.UpdateClient(ctx, name, moved); !errors.As(err, &ae) || ae.Status != 400 {
		t.Errorf("updating a missing client: err = %v, want a 400", err)
	}
	if err := c.DeleteClient(ctx, renamed); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}
	if _, ok := find(t, c, renamed); ok {
		t.Error("a deleted client is still listed")
	}
	if err := c.DeleteClient(ctx, renamed); !errors.As(err, &ae) || ae.Status != 400 {
		t.Errorf("deleting a missing client: err = %v, want a 400", err)
	}
}
