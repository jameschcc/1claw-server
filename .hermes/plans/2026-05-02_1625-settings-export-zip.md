# Plan: Settings Page - Password-Protected Export to ZIP

## Goal

Add an export feature to the Settings page: user enters a password, clicks "Export", backend creates a ZIP containing the DB, profiles/, shared/, and config, returns it as a downloadable file.

## Architecture

```
[Flutter Settings] --POST /api/export {password}--> [Go Backend]
                                                      |
                                                      v
                                              Creates ZIP in memory
                                              (db + profiles/ + shared/ + config)
                                                      |
                                                      v
                                              Returns ZIP as download
```

## Backend Changes (1claw-server)

### New endpoint: `POST /api/export`
- **Route**: `/api/export`
- **Auth**: password check (simple comparison with provided password)
- **Process**:
  1. Receive `{ "password": "xxx" }` in request body
  2. Locate all source files:
     - DB file: `~/.hermes/hermes.db` (or from config)
     - Profiles dir: `~/.hermes/profiles/`
     - Shared dir: `~/.hermes/shared/`
     - Config file: `~/.hermes/config.yaml`
  3. Create ZIP in memory using Go's `archive/zip`
     - `hermes.db` → root of zip
     - `profiles/` → recursive directory
     - `shared/` → recursive directory
     - `config.yaml` → root of zip
  4. Set response headers:
     - `Content-Type: application/zip`
     - `Content-Disposition: attachment; filename="hermes-export-{datetime}.zip"`
  5. Write zip bytes to response

### Files to change:
- `internal/handler/handler.go` or similar - register new route
- New file: `internal/handler/export.go` - export handler logic
- `internal/server/router.go` - add route

## Frontend Changes (1claw-app)

### Settings page addition
- Add a new section card: "Export Data"
- Password text field (obscure)
- "Export" button
- On tap: POST to `/api/export` with password
- Download response as file (use `path_provider` to save to Downloads)

### Files to change:
- `lib/models/api_client.dart` or equivalent - add `exportData()` method
- `lib/screens/settings_screen.dart` - add UI section

## Risks & Tradeoffs

1. **Password**: The password check is server-side. For security, the password could be compared against a stored hash, but for simplicity we'll do direct comparison with a configurable value.
2. **File paths**: Need to determine where the backend stores its data files. Likely in `~/.hermes/` relative to the server process.
3. **Large exports**: ZIP creation in memory could be large if profiles contain many files. Consider streaming the zip in the future.
4. **Download on mobile**: On mobile (iOS/Android), downloading a file needs special handling. For desktop (Linux/Windows/macOS), simpler.

## Step-by-step

1. Inspect backend structure to find router, handlers, data paths
2. Create `export.go` handler
3. Register route in router
4. Inspect frontend API client structure
5. Add `exportData()` method
6. Add UI to settings screen
7. Compile & test
8. Push to GitLab + GitHub
