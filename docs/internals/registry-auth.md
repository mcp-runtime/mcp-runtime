# Registry authentication

## Native backend authentication

`mcp-runtime registry enable-auth --realm https://api.example.com/api/v1/registry/token`
activates Distribution token authentication on port 5000 as well as the public
route. Install the updated platform-api and runtime-api images first, and log in
as a platform administrator. The platform-api image must come from an external
or public registry: the token broker cannot depend on the registry it protects.
Use `--dry-run` to inspect the prerequisites without writing cluster resources.

Activation preserves the workload and TLS certificate authorities. It creates
a separate registry signing Secret in `mcp-platform`, copies only its public
certificate into `registry`, rolls the broker, checks its public realm, and
provisions read-only pull credentials for all existing managed namespaces.
Runtime-api then uses the broker for new namespace credentials. Distribution
checks signed repository scopes for every backend request; public forward-auth
also understands these tokens. The registry Service becomes ClusterIP.

Node credentials have the `mcpp_` prefix, are bound to their namespace username,
and expire after 90 days. Their database records contain a hash and explicit
repository/prefix scope; they can mint only five-minute pull tokens. Shared
gateway images and images already used by the namespace are included explicitly.
Run `registry enable-auth --pull-namespace mcp-team-example --realm ...` before
expiry to replace a namespace's pull Secret. Old credentials remain valid until
expiry or administrative revocation with
`DELETE /api/v1/registry/pull-credentials?id=rp_...`; already issued bearer tokens
remain valid for at most five minutes. New credentials are returned only once
by the admin-only POST endpoint, with `Cache-Control: no-store`.

Activation also rotates service admin keys found in legacy namespace pull
Secrets across their owner Secrets and compatibility mirrors, and rolls their
consumers. A private rotation journal permits retry after a partial failure;
rerun activation using an administrator login, not the copied service key.
Do not delete the journal during recovery. Failed activation never turns native
backend authentication off. A completed activation removes the journal.

Publication helpers run only in the trusted `registry` namespace and mount
`mcp-registry-publisher` as a Docker authentication file. Its service publication
credential is never copied into team pull Secrets or placed in helper commands.
Changes to signing roots are refused by this command; certificate renewal/root
rotation require a separately reviewed procedure before the one-year registry
signing certificate expires. Setup registry rendering and compatibility overlays preserve the native registry
authentication environment, public trust mount, and ClusterIP-only Service.
Keep the platform-api bootstrap image external/public during later upgrades.

CI's native registry acceptance job creates and removes its own Distribution
container. It tests anonymous rejection, cross-team rejection, node upload
rejection, complete OCI publication, and read-only image retrieval. It does not
use a Kubernetes context or deploy to production.
