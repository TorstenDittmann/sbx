package core

import (
	"net/netip"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
)

// The vector is from the Standard Webhooks reference libraries, so any of
// their verifiers accepts sbx deliveries.
func TestSignWebhookStandardVector(t *testing.T) {
	sig, err := SignWebhook("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "msg_p5jXN8AQM9LWM0D4loKWxJek", 1614265330, []byte(`{"test": 2432232314}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; sig != want {
		t.Fatalf("got %s, want %s", sig, want)
	}
	if _, err := SignWebhook("whsec_not base64!", "m", 1, nil); err == nil {
		t.Fatal("accepted a bad secret")
	}
}

func TestWebhookBlocked(t *testing.T) {
	cfg := config.Default()
	cfg.Network.PrivateAllow = []string{"10.1.0.0/16"}
	m := &Manager{cfg: cfg}
	for addr, want := range map[string]bool{
		"93.184.215.14":   false,
		"127.0.0.1":       true,
		"::1":             true,
		"10.0.0.5":        true,
		"10.1.2.3":        false,
		"169.254.169.254": true,
		"::ffff:10.0.0.5": true,
		"0.0.0.0":         true,
		"2606:4700::1111": false,
	} {
		if got := m.webhookBlocked(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s blocked=%v, want %v", addr, got, want)
		}
	}
	m.cfg.Webhooks.AllowPrivate = true
	for addr, want := range map[string]bool{"127.0.0.1": false, "::1": false, "10.0.0.5": false, "169.254.169.254": true} {
		if got := m.webhookBlocked(netip.MustParseAddr(addr)); got != want {
			t.Errorf("allow_private: %s blocked=%v, want %v", addr, got, want)
		}
	}
}
