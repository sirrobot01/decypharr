// Package reacquire replaces media a Decypharr entry can no longer serve.
//
// It binds each managed file to the Sonarr or Radarr library symlink that points
// at its entry folder and filename. A symlink that points into the mount but
// carries a stale entry folder is bound by filename and size instead, so a
// folder rename does not drop the file. A confirmed content failure can then be
// turned into an Arr action on exact database IDs.
//
// Indexer keeps the bindings current: a full reconciliation per Arr on start
// and on demand, and a targeted pass for one entry after a download completes.
// Service persists them, and turns a Reacquire request into an idempotent job
// that deletes the Arr file, fails the exact grab history so the Arr
// blocklists the release, searches for a replacement, and reports ready once
// the replacement is imported. Every Arr mutation is recorded as an intent
// before it is sent and confirmed against the Arr afterwards, so a retry after
// a crash or an unclear response never blocklists or searches twice.
//
// Only Sonarr and Radarr are supported; other Arr kinds have no equivalent
// file, history, and search APIs, and are left alone.
package reacquire
