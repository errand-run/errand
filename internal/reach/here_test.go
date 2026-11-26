package reach

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestANameThatLeadsBackHere(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh here")
	}
	cfg := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(cfg, []byte("Host selfie\n  HostName localhost\nHost loop\n  HostName 127.0.0.1\nHost away\n  HostName example.invalid\nHost through\n  HostName localhost\n  ProxyJump away\n"), 0o600)
	for host, want := range map[string]bool{"selfie": true, "loop": true, "away": false, "through": false} {
		if got := (HostSpec{Host: host, Options: []string{"-F", cfg}}).IsHere(); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
}
