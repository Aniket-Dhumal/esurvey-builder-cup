package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Where a staleness-tolerant read goes, and what happens when the replica is
// not there.

func routerPools(t *testing.T) (primary, replica *Pool) {
	t.Helper()
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the router tests")
	}
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := p.Ping(context.Background()); err != nil {
		p.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(p.Close)
	// A second pool on the same server stands in for a replica that is fully
	// caught up: not in recovery reads as zero lag.
	r, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return p, r
}

func TestReadsGoToAReplicaThatIsCaughtUp(t *testing.T) {
	primary, replica := routerPools(t)
	r := NewRouter(primary, replica, 10*time.Second, nil)
	r.Check(context.Background())
	if r.Read() != replica {
		t.Fatalf("reading from the primary: %+v", r.Status())
	}
	if st := r.Status(); st.ReadingFrom != "replica" || !st.ReplicaConfigured {
		t.Errorf("status %+v", st)
	}
}

func TestReadsGoToThePrimaryWhenTheReplicaDoesNotAnswer(t *testing.T) {
	primary, _ := routerPools(t)
	cfg, err := pgxpool.ParseConfig("postgres://nobody:nothing@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	dead, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close()

	r := NewRouter(primary, dead, 10*time.Second, nil)
	r.Check(context.Background())
	if r.Read() != primary {
		t.Fatal("reads went to a replica that is not answering")
	}
	if st := r.Status(); st.ReadingFrom != "primary" || st.Reason == "" {
		t.Errorf("status does not say why: %+v", st)
	}
}

func TestNoReplicaMeansThePrimary(t *testing.T) {
	primary, _ := routerPools(t)
	r := NewRouter(primary, nil, 0, nil)
	r.Check(context.Background())
	if r.Read() != primary {
		t.Fatal("no replica configured and reads did not go to the primary")
	}
	var nilRouter *Router
	if nilRouter.Read() != nil {
		t.Error("a nil router must answer nil so callers fall back to their own pool")
	}
}
