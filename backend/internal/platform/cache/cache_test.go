package cache

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/config"
)

// There is one thing worth asserting about this package, and it is
// operational rather than logical: when Redis is unreachable, the failure
// says where it tried. The address comes from the environment, so the
// difference between "Redis is down" and "the address has a typo in it" is
// the only thing an operator is trying to establish at that moment.

func TestAnUnreachableRedisReportsWhereItTried(t *testing.T) {
	// Port 1 refuses immediately on every platform, so this neither waits for
	// a timeout nor depends on nothing listening on a plausible port.
	const addr = "127.0.0.1:1"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := Open(ctx, config.Redis{Addr: addr, PoolSize: 2})
	if err == nil {
		_ = c.Close()
		t.Fatal("Open succeeded against a port with nothing on it")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error = %v, want it to name %s", err, addr)
	}
}

func TestOpenRespectsACancelledContext(t *testing.T) {
	// Startup passes its own context. A cancelled one must not leave the
	// process dialling for the full timeout before it can report why it is
	// not starting.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	c, err := Open(ctx, config.Redis{Addr: "127.0.0.1:1", PoolSize: 2})
	if err == nil {
		_ = c.Close()
		t.Fatal("Open succeeded with a cancelled context")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("Open took %s on a cancelled context", elapsed)
	}
}
