package clientconf

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/stuffam/drawbridge/internal/model"
)

func mustKey(t *testing.T, s string) wgtypes.Key {
	t.Helper()
	k, err := wgtypes.ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func fixture(t *testing.T) (model.Settings, model.Client) {
	t.Helper()
	serverKey := mustKey(t, "YGqmUc6XZ4dPC6hDFxYlZ2JZhTzZoq9M4b/7Szs9pVw=")
	s, err := model.NewSettings(serverKey, netip.MustParsePrefix("fd3a:5c1e:92b0:1::/64"))
	if err != nil {
		t.Fatal(err)
	}
	s.EndpointHost = "vpn.example.com"
	clientKey := mustKey(t, "gIq0lCBAyEeEVJTTaPUBeFgjNYHLnw7ARXdJwqhpzmg=")
	c := model.Client{
		Name:         "Alex's phone",
		IPv4:         netip.MustParseAddr("10.8.0.23"),
		IPv6:         netip.MustParseAddr("fd3a:5c1e:92b0:1::23"),
		PublicKey:    clientKey.PublicKey(),
		PrivateKey:   &clientKey,
		PresharedKey: mustKey(t, "RvD5N2a6c2W5BEo+kLKeL2Hcn3pO4ZfV6SWsZMUuHuo="),
	}
	return s, c
}

func TestRender(t *testing.T) {
	s, c := fixture(t)
	got, err := Render(s, c)
	if err != nil {
		t.Fatal(err)
	}
	want := `[Interface]
PrivateKey = gIq0lCBAyEeEVJTTaPUBeFgjNYHLnw7ARXdJwqhpzmg=
Address = 10.8.0.23/32, fd3a:5c1e:92b0:1::23/128
DNS = 10.8.0.1, fd3a:5c1e:92b0:1::1
MTU = 1420

[Peer]
PublicKey = ` + s.PublicKey().String() + `
PresharedKey = RvD5N2a6c2W5BEo+kLKeL2Hcn3pO4ZfV6SWsZMUuHuo=
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, c.Name) || strings.Contains(got, "phone") {
		t.Fatal("the client's name reached the config file")
	}
}

func TestRenderVariants(t *testing.T) {
	s, c := fixture(t)

	s.EndpointHost, s.EndpointPort = "2001:db8::1", 443
	s.Keepalive = 0
	s.DNS = nil
	c.IPv6 = netip.Addr{}
	c.PresharedKey = wgtypes.Key{}
	got, err := Render(s, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Endpoint = [2001:db8::1]:443\n", "Address = 10.8.0.23/32\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"PersistentKeepalive", "DNS =", "PresharedKey"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, got)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	s, c := fixture(t)
	s.EndpointHost = ""
	if _, err := Render(s, c); !errors.Is(err, model.ErrNoEndpoint) {
		t.Errorf("no endpoint: err %v", err)
	}
	s, c = fixture(t)
	c.PrivateKey = nil
	if _, err := Render(s, c); !errors.Is(err, ErrNoPrivateKey) {
		t.Errorf("no private key: err %v", err)
	}
}

func TestWriteQR(t *testing.T) {
	s, c := fixture(t)
	conf, err := Render(s, c)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteQR(&buf, conf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	width := strings.Count(lines[0], "▀")
	// A QR code is 17 + 4v modules wide, plus the quiet zone on both sides.
	if (width-2*quietZone-17)%4 != 0 || width <= 2*quietZone+17 {
		t.Fatalf("width %d isn't a QR code size plus the quiet zone", width)
	}
	if want := (width + 1) / 2; len(lines) != want {
		t.Fatalf("%d lines, want %d for a %d-module code", len(lines), want, width)
	}
	for i, l := range lines {
		if strings.Count(l, "▀") != width {
			t.Fatalf("line %d has %d cells, want %d", i, strings.Count(l, "▀"), width)
		}
		if !strings.HasSuffix(l, reset) {
			t.Fatalf("line %d doesn't reset the colors", i)
		}
	}
	// The top quiet zone is light on both halves of every cell.
	if strings.Contains(lines[0], fgDark) || strings.Contains(lines[0], bgDark) {
		t.Fatal("the quiet zone contains dark modules")
	}
}
