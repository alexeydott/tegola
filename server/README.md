# Server

The server package is responsible for handling webserver requests for map tiles and various JSON endpoints describing the configured server. Example config:

```toml
[webserver]
port = ":9090"              # set something different than default ":8080"
ssl_cert = "fullchain.pem"  # ssl cert for serving by https
ssl_key = "privkey.pem"     # ssl key for serving by https

[webserver.tile_operations]
enabled = true                 # default false
token = "change-me-secret"     # required when enabled
rate_per_minute = 60           # default 60
max_concurrent = 4             # default 4

[webserver.headers]
Access-Control-Allow-Origin = "*"
```

### Config properties

- `port` (string): [Optional] Port and bind string. For example ":9090" or "127.0.0.1:9090". Defaults to ":8080"
- `hostname` (string): [Optional] The hostname to use in the various JSON endpoints. This is useful if tegola is behind a proxy and can't read the API consumer's request host directly.
- `uri_prefix` (string): [Optional] A prefix to add to all API routes. This is useful when tegola is behind a proxy (i.e. example.com/tegola). The prexfix will be added to all URLs included in the capabilities endpoint responses.
- `ssl_cert` (string): [Optional, unless ssl_key provided] Path to a certificate file for serving through HTTPS
- `ssl_key` (string): [Optional, unless ssl_cert provided] Path to a private key file for serving through HTTPS

### Tile operations (`[webserver.tile_operations]`)

The tile endpoints accept maintenance parameters: `?tile=update` and `?tile=getupdated` (cache regeneration), `?tile=status` (cache state) and `?dirty=true` (force regeneration, bypassing the cache). These can trigger up to 64 tile renders and cache writes per request, so they are **disabled by default** and, when enabled, require an authentication token.

- `enabled` (bool): [Optional] Enables the tile maintenance parameters above. Defaults to `false`. When disabled, any request carrying `?tile=...` or `?dirty` is rejected with `403 Forbidden` — the parameters are not silently ignored — and no tile is rendered or written to the cache. Ordinary tile serving is unaffected.
- `token` (string): [Required when `enabled` is true] Shared secret clients must send in the `X-Tegola-Tile-Operations-Token` header. A missing or wrong token yields `403 Forbidden`; if `enabled` is true and no token is configured, all tile operations fail closed with `403`.
- `rate_per_minute` (int): [Optional] Maximum number of tile-operation requests accepted per minute. Defaults to `60`. Requests over the limit receive `429 Too Many Requests`.
- `max_concurrent` (int): [Optional] Maximum number of tile operations executing at the same time. Defaults to `4`. Additional mutating requests (`?tile=update`, `?tile=getupdated`, `?dirty=true`) receive `503 Service Unavailable`; `?tile=status` is exempt from the concurrency slot as it never renders.

Note: `max_concurrent` limits tile *operations*; requests over the limit fail fast rather than queue, so a burst of cache-maintenance traffic cannot exhaust the server.

## Local development of the embedded viewer

Tegola's built in viewer code is stored in the `ui/` directory. To build the ui `npm` must be installed. Once `npm` is installed the following command can be run from the repository root to generate a .go file for inclusion in the tegola binary:

```
go generate ./server
```

## Disabling the viewer

The viewer can be excluded during building by using the build flag `noViewer`. For example, building tegola from the `cmd/tegola` directory:

```bash
go build -tags "noViewer"
```
