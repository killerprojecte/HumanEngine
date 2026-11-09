package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
)

type Owners struct {
	VectorVersion    string
	Main             *sql.DB
	Authority        *recovery.Authority
	Assertions       *assertions.Store
	Sources          *sourceevents.Store
	RuntimeAuthority *sourceevents.RuntimeAuthority
	Memes            *memeservice.Service
	Habits           *habitservice.Service
	Credentials      *credentials.Store
	Budgets          *providertransport.BudgetService
	Codec            *imagecodec.Compressor
	Dates            *jsdate.Parser
	Compare          func(string, string) (int, error)
}

type Options struct {
	// Per-open private constructor injection. Nil preserves the concrete owner path.
	relationExposurePort                                              func(*Application, Owners) (*memoryservice.ExposurePort, error)
	Installation                                                      installation.Options
	ImageLibrary, Locale, MemeDirectory, PluginRoot, Node, AdminToken string
	// Entries are installed local V1 modules, relative to PluginRoot. Registration
	// never enables an unconfigured plugin or replays durable source events.
	Entries     []string
	Builtins    []string
	Diagnostics io.Writer
	Host        plugins.V1HostOptions
	HTTP        *http.Client
	Counter     providertransport.CounterRegistry
	LookupEnv   func(string) (string, bool)
	Logs        observability.LogOwner
	// LogRegistry is an optional shared registry for embedding and tests. Nil
	// uses the process-wide source owners; it is not an opt-in logging mode.
	LogRegistry *logdelivery.Registry
	ownsLogs    bool
}

// Open owns genuine installation enrollment, database and native application
// lifetimes. No fixture schema, fake provider, synthetic authority or media fetch
// implementation is selected here. The production command remains preview-only.
func Open(ctx context.Context, o Options) (_ *Application, err error) {
	if o.Node == "" || o.PluginRoot == "" {
		return nil, errors.New("application_dependencies_required")
	}
	var installationOwner *installation.Store
	if o.Installation.VectorExtension == "" {
		installationOwner, err = installation.OpenBundled(ctx, o.Installation)
	} else {
		installationOwner, err = installation.Open(ctx, o.Installation)
	}
	if err != nil {
		return nil, err
	}
	var collator *topiccollation.Collator
	transferred := false
	// No producer exists until assemble succeeds. Its failure cleanup joins every
	// created async owner before this deferred database release can run.
	defer func() {
		if err != nil && !transferred {
			if collator != nil {
				err = errors.Join(err, collator.Close())
			}
			err = errors.Join(err, installationOwner.Close())
		}
	}()
	collator, err = topiccollation.New(o.Locale)
	if err != nil {
		return nil, err
	}
	dates, err := jsdate.New(o.Installation.SourceTimezone)
	if err != nil {
		return nil, err
	}
	codec, err := imagecodec.New(o.ImageLibrary)
	if err != nil {
		return nil, err
	}
	db, a := installationOwner.DB, installationOwner.Authority
	st, err := assertions.New(db, a, assertions.Options{DateParser: dates})
	if err != nil {
		return nil, err
	}
	if err = st.Initialize(ctx); err != nil {
		return nil, err
	}
	sources, err := sourceevents.New(db, a, sourceevents.Options{})
	if err != nil {
		return nil, err
	}
	runtimeAuthority, err := sourceevents.NewRuntimeAuthority(sources)
	if err != nil {
		return nil, err
	}
	memesDir := o.MemeDirectory
	if memesDir == "" {
		memesDir = filepath.Join(filepath.Dir(o.Installation.Database), "memes")
	}
	memes, err := memeservice.New(ctx, db, a, st, assertionauthority.Options{DateParser: dates, Compare: collator.Compare}, memesDir)
	if err != nil {
		return nil, err
	}
	habits, err := habitservice.New(ctx, db, a, habitservice.Options{})
	if err != nil {
		return nil, err
	}
	var entropy [16]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	ledger, err := providertransport.NewSQLLedger(ctx, db, hex.EncodeToString(entropy[:]), nil)
	if err != nil {
		return nil, err
	}
	owners := Owners{installationOwner.VectorVersion, db, a, st, sources, runtimeAuthority, memes, habits, installationOwner.Secrets, &providertransport.BudgetService{DB: db, Ledger: ledger}, codec, dates, collator.Compare}
	if o.Logs == nil {
		o.Logs = observability.NewStoreLogger(observability.StoreLoggerOptions{Database: o.Installation.Database, Registry: o.LogRegistry, LookupEnv: o.LookupEnv})
		o.ownsLogs = true
	}
	transferred = true
	return assemble(ctx, owners, o, collator, func() error { return errors.Join(collator.Close(), installationOwner.Close()) })
}
