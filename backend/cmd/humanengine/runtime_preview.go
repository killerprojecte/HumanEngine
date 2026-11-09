package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/killerprojecte/HumanEngine/backend/internal/app"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
	"github.com/killerprojecte/HumanEngine/backend/internal/vectorasset"
	"github.com/mattn/go-sqlite3"
)

type runtimeConfig struct {
	authority, vector, images, node, pluginRoot, timezone, locale string
}

func runtimeFlags(flags *flag.FlagSet) *runtimeConfig {
	c := &runtimeConfig{}
	flags.StringVar(&c.authority, "authority", "", "independent authority directory for runtime preview")
	flags.StringVar(&c.vector, "vector-extension", "", "absolute pinned sqlite-vec path; empty uses bundled_sqlite_vec")
	flags.StringVar(&c.images, "image-library", "", "absolute pinned libvips library path for runtime preview")
	flags.StringVar(&c.node, "node", "node", "Node executable for lazy V1 plugin workers")
	flags.StringVar(&c.pluginRoot, "plugin-root", "", "existing local plugin directory for runtime preview")
	flags.StringVar(&c.timezone, "source-timezone", "UTC", "source installation IANA timezone")
	flags.StringVar(&c.locale, "locale", "en-US", "native source collation locale")
	return c
}

// runRuntimePreview only owns launch resources. The application owns every
// producer, request admission barrier, durable store and their drain ordering.
func runRuntimePreview(ctx context.Context, database, address string, config runtimeConfig, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reserve the socket before any writable installation enrollment.
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("runtime_preview_listen_failed")
	}
	defer listener.Close()
	options, err := config.preflight(ctx, database)
	if err != nil {
		return err
	}
	logs := observability.New(stdout, stderr)
	options.Logs = logs
	// Raw plugin stderr is deliberately not forwarded: it may contain credentials.
	options.Diagnostics = io.Discard
	options.AdminToken = os.Getenv("HUMANENGINE_ADMIN_TOKEN")
	application, err := app.Open(ctx, options)
	if err != nil {
		// Open may return a partial owner after unsuccessful physical drain. Never
		// substitute a database Close, and never use the cancelled startup context.
		if application != nil {
			return errors.Join(errors.New("runtime_preview_open_failed"), application.ShutdownAndFlush(context.Background()))
		}
		return errors.Join(errors.New("runtime_preview_open_failed"), logs.Close(context.Background()))
	}
	server := &http.Server{Handler: application}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	_ = logs.Emit(observability.Event{Level: "info", Module: "server", Code: "server.listening", Message: "Go writable runtime preview ready; migration is incomplete", Fields: map[string]any{"address": listener.Addr().String()}})
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-done:
	}
	// HTTP shutdown and the application's producer/request drain must begin
	// together. The signal context must not cancel accepted request draining.
	// No deadline or forced DB close turns an unfinished drain into success.
	httpDone := make(chan error, 1)
	go func() { httpDone <- server.Shutdown(context.Background()) }()
	drainErr := application.ShutdownAndFlush(context.Background())
	httpErr := <-httpDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if serveErr != nil {
		serveErr = errors.New("runtime_preview_serve_failed")
	}
	return errors.Join(serveErr, httpErr, drainErr)
}

// Validate native dependencies without touching the installation. The actual
// app.Open repeats owner validation at enrollment, not trusting this preflight
// as permission to bypass recovery gates or its own checks.
func (c runtimeConfig) preflight(ctx context.Context, database string) (app.Options, error) {
	fail := func(code string) (app.Options, error) { return app.Options{}, errors.New(code) }
	if database == "" || database == ":memory:" {
		return fail("runtime_preview_file_database_required")
	}
	db, err := filepath.Abs(database)
	if err != nil {
		return fail("runtime_preview_database_invalid")
	}
	authority, err := recovery.AuthorityPath(db, c.authority)
	if err != nil {
		return fail("runtime_preview_authority_invalid")
	}
	if err = storage.RequireFTS5(); err != nil {
		return fail("runtime_preview_fts5_required")
	}
	if _, err = jsdate.New(c.timezone); err != nil {
		return fail("runtime_preview_timezone_invalid")
	}
	collator, err := topiccollation.New(c.locale)
	if err != nil {
		return fail("runtime_preview_native_icu_required")
	}
	if err = collator.Close(); err != nil {
		return fail("runtime_preview_native_icu_close_failed")
	}
	if !filepath.IsAbs(c.images) {
		return fail("runtime_preview_image_library_required")
	}
	if _, err = imagecodec.New(c.images); err != nil {
		return fail("runtime_preview_image_library_invalid")
	}
	node, err := exec.LookPath(c.node)
	if err != nil {
		return fail("runtime_preview_node_required")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		return fail("runtime_preview_node_invalid")
	}
	// Do not spawn Node to probe it. With no enabled V1 plugin there must be no
	// Node process. The concrete V1 owner validates the worker when enabled.
	if c.pluginRoot == "" {
		return fail("runtime_preview_plugin_root_required")
	}
	root, err := filepath.Abs(c.pluginRoot)
	if err != nil {
		return fail("runtime_preview_plugin_root_invalid")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fail("runtime_preview_plugin_root_invalid")
	}
	if err = checkRuntimeVector(ctx, c.vector); err != nil {
		return fail("runtime_preview_sqlite_vec_invalid")
	}
	// Verify shipped modules without enabling any extra builtin or owner.
	for _, id := range []string{"typing-delay", "web-read", "web-search", "snowluma"} {
		if artifact, err := plugins.BuiltinArtifact(id); err != nil || len(artifact) == 0 {
			return fail("runtime_preview_builtin_artifact_required")
		}
	}
	return app.Options{Installation: installation.Options{Database: db, AuthorityDirectory: authority, VectorExtension: c.vector, SourceTimezone: c.timezone}, ImageLibrary: c.images, Locale: c.locale, Node: node, PluginRoot: root}, nil
}

func checkRuntimeVector(ctx context.Context, path string) error {
	if path == "" {
		asset, err := vectorasset.Extract()
		if err != nil {
			return err
		}
		return errors.Join(asset.Use(func(p string) error { return checkRuntimeVector(ctx, p) }), asset.Close())
	}
	if !filepath.IsAbs(path) {
		return errors.New("absolute_vector_path_required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) != vectorasset.BinarySize || fmt.Sprintf("%x", sha256.Sum256(data)) != vectorasset.SHA256 {
		return errors.New("vector_integrity_failed")
	}
	// A private in-memory connection verifies the real native extension ABI.
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return err
	}
	defer db.Close()
	connection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	err = connection.Raw(func(raw any) error {
		conn, ok := raw.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("sqlite_driver_invalid")
		}
		return conn.LoadExtension(path, "sqlite3_vec_init")
	})
	if err != nil {
		return err
	}
	var version string
	if err = connection.QueryRowContext(ctx, "SELECT vec_version()").Scan(&version); err != nil {
		return err
	}
	if version != "v"+vectorasset.Version && version != vectorasset.Version {
		return errors.New("vector_version_invalid")
	}
	return nil
}
