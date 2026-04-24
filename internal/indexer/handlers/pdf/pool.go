package pdf

import (
	"errors"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// This is the package's one deliberate deviation from the stateless-handler
// convention (see internal/indexer/handlers/office/handler.go for the
// pattern). Per-Parse WebAssembly init would allocate and reclaim ~5 MB on
// every call, so the pool is shared across the indexing run. cmd/index.go
// defers Shutdown to release it when indexing completes.
//
// PDFium serialises its own API calls against a single instance, so the
// shared handle is safe across goroutines. Shutdown coordinates with
// in-flight callers via inFlight — without it, closing the WASM runtime
// while a Parse is mid-call would use-after-free the embedded runtime.
var (
	poolMu   sync.Mutex
	pool     pdfium.Pool
	instance pdfium.Pdfium
	inFlight sync.WaitGroup
	closing  bool
)

// errShuttingDown surfaces when a Parse races a deferred Shutdown.
var errShuttingDown = errors.New("pdf pool shutting down")

// getInstance lazy-initialises the WebAssembly pool on first use and
// returns a release function the caller MUST invoke exactly once when
// done (a sync.Once guard prevents a double-call from panicking the
// WaitGroup). After Shutdown the next call re-initialises — tests
// exercise this.
func getInstance() (pdfium.Pdfium, func(), error) {
	poolMu.Lock()
	defer poolMu.Unlock()
	if closing {
		return nil, func() {}, errShuttingDown
	}
	if instance == nil {
		p, err := webassembly.Init(webassembly.Config{
			MinIdle:  1,
			MaxIdle:  1,
			MaxTotal: 1,
		})
		if err != nil {
			return nil, func() {}, err
		}
		inst, err := p.GetInstance(30 * time.Second)
		if err != nil {
			p.Close()
			return nil, func() {}, err
		}
		pool, instance = p, inst
	}
	inFlight.Add(1)
	var once sync.Once
	return instance, func() { once.Do(inFlight.Done) }, nil
}

// Shutdown releases the WebAssembly pool and instance. cmd/index.go
// defers this so the ~5 MB WASM runtime is returned to the OS promptly.
// Safe to call multiple times. Blocks until any in-flight Parse calls
// return so the runtime isn't torn down mid-operation.
func Shutdown() error {
	poolMu.Lock()
	closing = true
	poolMu.Unlock()

	inFlight.Wait()

	poolMu.Lock()
	defer poolMu.Unlock()
	if instance != nil {
		instance.Close()
		instance = nil
	}
	var err error
	if pool != nil {
		err = pool.Close()
		pool = nil
	}
	closing = false
	return err
}
