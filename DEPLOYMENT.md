# Playground Deployment

This document describes how to deploy the **SPMD** playground in the same
shape as the upstream TinyGo playground:

- static frontend on Netlify (public site: `https://gofor-tinygo.netlify.app`;
  the custom domain `spmd.ddlm.me` is optional and not currently configured)
- compiler backend on Google Cloud Run, behind a Cloud Armor-protected load
  balancer at `https://spmd-api.ddlm.me`, reverse-proxied by Netlify via `/api/*`
- ~4 GiB / 2-4 vCPU Cloud Run instance (SPMD TinyGo builds use more RAM than
  upstream because the forked Go GOROOT is loaded into memory)
- local-disk cache by default; GCS optional (see "Backend Runtime Settings")
- optional Firestore for shared snippets and stats

The repository already contains most of the production assumptions. This
document turns them into an explicit runbook.

## SPMD-Specific Notes

This fork ships its own toolchain bundle and tooling versions. The high-level
differences from upstream are:

1. The container bundles the forked Go (full GOROOT) **and** the forked
   TinyGo. Both are SPMD-aware. They are delivered as a single artifact
   `release-spmd.tar.gz` built by `make release-spmd.tar.gz` from this
   directory (which calls `make build` in the parent repo and
   `make USE_SYSTEM_BINARYEN=1 build/release` in the TinyGo submodule).
   Expected size: **~700-900 MB compressed**, ~1.5-2 GB extracted.
2. The container installs **binaryen >= 109** from upstream GitHub releases,
   not the Debian package. Older binaryen builds reject the SIMD opcodes the
   SPMD backend emits (relaxed-simd, modern `i8x16.swizzle` lowerings,
   `vpmaddubsw` patterns) with "invalid code after SIMD prefix" and break
   `/api/wat` for `table-lookup` and `base64-mula-lemire`.
3. The container installs **wabt >= 1.0.36** for the same opcode-coverage
   reason on `wasm2wat`.
4. `binutils` is installed for `objdump --disassemble` (used by `/api/asm`).
5. The runtime image is `debian:bookworm-slim`, not `golang:*`, since we
   bundle the forked Go ourselves.
6. **Known browser limitation**: WAT output may contain relaxed-simd opcodes
   that older browser wasm runtimes do not yet implement. The compile/run path
   produces a valid wasi binary; the WAT view is purely informational.

### Build and Test Locally

```sh
# From the playground directory:
make release-spmd.tar.gz   # ~30 min the first time (builds Go + TinyGo)
make build                 # docker build -t spmd-playground:latest .
bash test_docker.sh        # builds image (no-op if up to date), runs it,
                           # asserts binaryen >= 109, runs endpoint smoke
```

`test_docker.sh` verifies that the regressions blocking
`table-lookup` and `base64-mula-lemire` on older binaryen are gone inside the
container, in addition to the standard endpoint smoke from
`test_endpoints.sh`.

### Resource Requirements

- **Container image size**: ~3-4 GB on disk (full GOROOT + TinyGo lib/pkg).
- **Cloud Run memory**: target 4 GiB. 2 GiB is the upstream default and is
  too tight for the forked toolchain.
- **Cloud Run CPU**: 2-4 vCPU. SPMD compiles are not significantly slower
  than upstream TinyGo at runtime, but the warm-cache `RUN` step in the
  Dockerfile benefits from the extra CPUs.

### Frontend / Backend Wiring

The split is Model B (Netlify static + Cloud Run API):

- Netlify hosts `index.html`, `dashboard.{js,css}`, `highlight-*.js`,
  `resources/`, `worker/`, `examples/`.
- Netlify's `[[redirects]]` rule rewrites `/api/*` to the Cloud Run URL with
  `status=200 force=true`, so the browser sees a same-origin response and
  COOP/COEP work without CORS bookkeeping.
- The Cloud Run service still serves the static files at `/` as a fallback
  (the container's `-dir=/app/frontend` flag), which is useful for
  development and as a safety net if Netlify is misconfigured.

After the first Cloud Run deploy, replace `CLOUDRUN_URL_TBD` in
[netlify.toml](netlify.toml) with the assigned service URL and redeploy
Netlify.

## Public Deployment Warning

Do not treat the backend deployment in this document as production-ready unless
you also put abuse controls in front of the public compile endpoint.

At minimum, a public deployment should have one of these protections from day
one:

- Cloud Armor or another rate-limiting layer in front of `/api/compile`
- a private backend reachable only through a trusted frontend or proxy
- restrictive quotas and spend alerts in a dedicated GCP project

The backend compiles untrusted user input and is intentionally CPU-heavy. A
plain unauthenticated public Cloud Run service without traffic controls is easy
to abuse.

## Deployment Shape

The playground has two deployable pieces:

1. Frontend static assets served from Netlify.
2. Backend compiler API served from Cloud Run.

The frontend is self-configuring: it always calls the relative path `/api`,
and the backend URL lives only in the `netlify.toml` rewrite rule. See
"Frontend API URL" below.

## Required Accounts And Access

You need access to both Netlify and Google Cloud.

### Human Accounts

- A Google account with access to the Google Cloud project that will host the
  backend.
- A Netlify account with permission to deploy the `play.tinygo.org` site, or
  whichever Netlify site you use for your deployment.
- Git access to the repository so you can update the frontend API URL if the
  Cloud Run hostname changes.

### Local Tooling

- Docker
- `make`
- `gcloud` CLI
- the forked Go + forked TinyGo built in the parent repo (see the project
  `Makefile` `build` target). `make release-spmd.tar.gz` from this directory
  drives those builds, then bundles them as
  `release-spmd.tar.gz`.

The image build depends on `release-spmd.tar.gz` because the Dockerfile
unpacks it directly into `/app/go` and `/app/tinygo`.

### Google Cloud Roles

At minimum, the operator who builds and deploys should be able to:

- submit builds with Cloud Build
- push images to Artifact Registry
- deploy and update Cloud Run services
- view Cloud Run logs
- create or inspect GCS buckets
- optionally create or inspect Firestore resources

Prefer least-privilege role grants instead of broad project administration.
The exact role split depends on how your project is organized, but typical
permissions map roughly to:

- Cloud Run developer or deployer access for the service
- service account user access for the runtime identity used by Cloud Run
- Cloud Build permissions to submit builds
- Artifact Registry write permissions for the target repository
- Storage administration on the specific cache bucket if using GCS cache
- Firestore access only if you intend to enable sharing and stats

If a single operator account must do all setup tasks, that account may need more
than one role, but avoid handing out broad owner-style access just to get the
first deployment working.

### Cloud Run Service Account

The Cloud Run runtime identity should have:

- read/write access to the compile cache bucket if using `-cache-type=gcs`
- Firestore access if `/api/share` and `/api/stats` should work

The runtime identity does not need interactive human access. Prefer attaching a
dedicated service account to the Cloud Run service rather than reusing a broad
default identity.

If Firestore is not configured, the compile API still works, but shared
snippets and stats endpoints return `Firestore not configured`.

## Google Cloud Resources

### Required

- one Google Cloud project dedicated to the playground backend
- Artifact Registry repository for the container image
- one Cloud Run service

Using a dedicated project is strongly recommended so billing alerts, quotas, or
 emergency kill switches affect only the playground.

### Recommended

- one GCS bucket for compile cache, for example `tinygo-cache`

The default container command in [Dockerfile](Dockerfile) starts the backend as:

```text
./main -dir=/app/frontend -cache-type=local
```

The shipped default is `local` so the image works out of the box with no GCP
configuration. For production multi-instance deploys, switch to
`-cache-type=gcs -bucket-name=tinygo-cache` (or your bucket name) so cache hits
are shared across Cloud Run instances. The `local` default trades some
cold-start compile cost for zero setup; flip it when you scale beyond one
warm instance.

### Optional

- Firestore database for shared snippets and compile stats

The backend uses these collections:

- `shared`
- `track`

If you do not need those features yet, you can deploy without Firestore.

## Netlify Requirements

The frontend requires the headers from [netlify.toml](netlify.toml):

- `Cross-Origin-Opener-Policy: same-origin`
- `Cross-Origin-Embedder-Policy: require-corp`

Those headers are required for browser features used by the playground,
including `SharedArrayBuffer` support. They also reflect the browser isolation
requirements around that feature, so treat them as functional requirements, not
optional polish.

The repository README states that merges to `main` deploy the web UI to Netlify.
If you deploy under a different Netlify site, preserve the same headers.

## Backend Runtime Settings

Recommended initial Cloud Run settings for this workload:

- region: `us-central1`
- CPU: `2`
- memory: `2Gi`
- min instances: `0`
- max instances: `2` to start
- concurrency: `1`
- timeout: `60s`
- port: `8080`
- unauthenticated: enabled if the public frontend must call the API directly

These are the values for the **first** deploy only. The running service may
have been tuned afterwards (notably `concurrency`, which is deliberately low
here but is a common thing to raise once real traffic behavior is known).
Treat this list as the initial recommendation, not a record of live state — see
"Updating an existing service" for how to read and preserve the actual running
configuration before redeploying.

If you do enable unauthenticated public access, pair it with rate limiting and
monitoring. Otherwise, keep the service private until those controls exist.

Why these settings:

- the backend performs real compiler work and is CPU-heavy
- the code serializes compile jobs through a single compiler goroutine per
  instance
- `min instances = 0` avoids idle spend
- a low `max instances` limits surprise spend during traffic spikes

Cold starts are expected with `min instances = 0`. If first-request latency is
more important than idle cost, consider `min instances = 1` and accept the fixed
monthly baseline cost.

## Frontend API URL

The frontend always calls the relative path `/api`. There is **no** hardcoded
backend URL in `dashboard.js`, and there is no `stats/stats.js` file (an older
revision of this document described both — that is no longer accurate).

The only place the backend URL lives is the `[[redirects]]` rule in
[netlify.toml](netlify.toml), which rewrites `/api/*` to the Cloud Armor-
protected load balancer:

```toml
[[redirects]]
  from = "/api/*"
  to = "https://spmd-api.ddlm.me/api/:splat"
  status = 200
  force = true
```

To repoint the backend, change that single `to =` line and redeploy Netlify.
Because it is a `status=200 force=true` rewrite, the browser sees a same-origin
response, so the site behaves identically whether served on
`gofor-tinygo.netlify.app` or a custom domain.

## Build And Deploy Backend

### 1. Prepare The Repository

From the repository root:

```bash
cd /path/to/playground
ls release-spmd.tar.gz || make release-spmd.tar.gz
```

If `release-spmd.tar.gz` is missing, build it. The Makefile target drives
`make build` in the parent repo (so the forked Go and TinyGo are built),
then runs the TinyGo `build/release` target to assemble a redistributable
TinyGo, then tars both trees. Expect this to take ~30 min the first time.

Record the parent-repo commit hash as part of the deployment - the bundled
forked Go and TinyGo are pinned by submodule pointer at the time the tarball
is produced. If you rotate compiler versions, assume the cache may contain
stale artifacts from the previous version.

### 2. Authenticate To Google Cloud

```bash
gcloud init
gcloud auth application-default login
gcloud config set project YOUR_PROJECT_ID
```

### 3. Build The Container Locally (optional, not required for Cloud Run)

```bash
make build
```

This produces a local Docker image tagged `spmd-playground:latest`.

`make build` is **only** needed if you want to test the image locally
(`make run`, `make test-docker`) or push it to Docker Hub (`make push-docker`).
It is **not** a prerequisite for a Cloud Run deploy. `make push-gcloud`
(step 4) runs its own independent build remotely in Cloud Build from the
`Dockerfile` and never consumes the local image produced here. The only thing
the local and remote builds share is the `release-spmd.tar.gz` prerequisite
(declared on both the `build` and `push-gcloud` targets), so if you only intend
to deploy to Cloud Run you can skip straight to step 4 and avoid one redundant
local build.

### 4. Submit The Image To Artifact Registry

The checked-in shortcut is:

```bash
make push-gcloud
```

That runs:

```bash
gcloud builds submit --tag us-central1-docker.pkg.dev/$(GCP_PROJECT)/cloud-run-source-deploy/spmd-playground
```

`GCP_PROJECT` must be set when invoking `make push-gcloud` (the Makefile errors
out otherwise). The image name is `spmd-playground`; change the repository path
in the Makefile if you are deploying under a different Artifact Registry
layout.

`make push-docker` and `make push-gcloud` are not interchangeable:

- `make push-docker` pushes the local image to Docker Hub under
  `spmd-playground:latest`
- `make push-gcloud` submits the source tree to Cloud Build and publishes to the
  Artifact Registry path used by the current GCP deployment flow

Use the GCP path for Cloud Run deployments unless you intentionally deploy from
another registry.

`make push-gcloud` uploads the build context (the `playground/` tree, including
the ~300 MB `release-spmd.tar.gz`) to Cloud Build, which runs `docker build`
**server-side** and publishes the result to Artifact Registry. It does not
reuse any local image. Seeing a "second" Docker build start during
`make push-gcloud` even though you already ran `make build` is expected: the
two builds are independent (local vs remote), they just share the
`release-spmd.tar.gz` prerequisite, which is skipped when the tarball already
exists. If the forked Go/TinyGo submodules have not changed, the existing
tarball is reused and the slow ~30 min toolchain rebuild does not happen.

### 5. Deploy To Cloud Run

There are two distinct cases. Use the right one — they are **not**
interchangeable.

#### 5a. First deploy (new service)

When the service does not exist yet, specify the full shape explicitly. The
values below are the recommended starting point (see "Backend Runtime
Settings"); adjust before first deploy if your workload differs.

```bash
gcloud run deploy playground \
  --image us-central1-docker.pkg.dev/YOUR_PROJECT_ID/cloud-run-source-deploy/spmd-playground:latest \
  --region us-central1 \
  --platform managed \
  --port 8080 \
  --cpu 2 \
  --memory 2Gi \
  --min-instances 0 \
  --max-instances 2 \
  --concurrency 1 \
  --timeout 60 \
  --allow-unauthenticated
```

#### 5b. Updating an existing service (image swap only)

`gcloud run deploy` builds the new revision **from the existing service
configuration** and only overrides the flags you explicitly pass. Any tuning
flag you omit is carried forward from the current revision unchanged.

This has a sharp edge: re-pasting the full 5a command on an update **forces
every listed value back to the literal in the command**. If the live service
was tuned after the last documented change (for example concurrency raised
beyond the `1` recommended here), passing `--concurrency 1` again silently
reverts that production tuning. The same applies to `--cpu`, `--memory`,
`--max-instances`, and `--timeout`.

For a routine update (new image, same tuning) deploy with only the image and
region so live settings are preserved:

```bash
gcloud run deploy playground \
  --image us-central1-docker.pkg.dev/$GCP_PROJECT/cloud-run-source-deploy/spmd-playground:latest \
  --region us-central1
```

Before deploying — and any time the live shape is in doubt — read the current
configuration instead of trusting this document (the values in "Backend Runtime
Settings" are the *initial recommendation*, not a guaranteed reflection of the
running service):

```bash
gcloud run services describe playground --region us-central1 \
  --format='value(
    spec.template.spec.containerConcurrency,
    spec.template.spec.timeoutSeconds,
    spec.template.spec.containers[0].resources.limits)'
```

Only re-pass a tuning flag when you intend to change that specific value.

After deploy, note the generated service URL. You may need it for the frontend
update.

Cloud Run queues excess requests when there is no immediate capacity, subject to
platform limits and request timeout. With `concurrency=1` and a CPU-heavy
service, requests that pile up under load are more likely to wait and then time
out. Keep the timeout aligned with realistic compile latency.

`max-instances` is an important cost guardrail, but it is not a perfect hard cap
for all spike conditions. Use billing alerts and traffic controls in addition to
that limit.

## Secrets And Credentials

The default production shape does not require application-level API keys.

Prefer this model:

- use Cloud Run service account credentials through Application Default
  Credentials in production
- do not check JSON credentials into the repository
- only use `-firebase-credentials=/path/to/file.json` for local development or
  controlled non-GCP environments

If you later introduce actual secrets, store them in Secret Manager or your
deployment platform's secret store rather than in source control.

## Optional Runtime Variants

### Variant A: Minimal Backend, No GCS, No Firestore

Useful for private demos or early bring-up.

Start the binary with local cache only:

```text
./main -dir=/app/frontend -cache-type=local
```

In this mode:

- compile API works
- cache is per-instance and ephemeral
- `/api/share` and `/api/stats` do not work unless Firestore is configured

### Variant B: Production-Like Backend

Use:

- `-cache-type=gcs`
- `-bucket-name=YOUR_BUCKET`
- Firestore enabled through the Cloud Run service account

## Deploy Frontend To Netlify

### 1. Update API URL If Needed

If the Cloud Run hostname changed, update:

- [dashboard.js](dashboard.js)
- [stats/stats.js](stats/stats.js)

### 2. Preserve Netlify Headers

Do not remove the headers in [netlify.toml](netlify.toml). They are part of the
runtime requirements, not just an optimization.

### 3. Publish

If your Netlify site is already wired to this repository, deploy using the same
workflow the project currently uses for `main`.

If you are creating a fresh Netlify site:

- connect the repository
- use the repository root as the publish directory so Netlify serves
  `index.html`, `dashboard.js`, `stats/`, `resources/`, `parts/`, and the other
  checked-in static assets directly
- no separate frontend build step is required for the static site itself unless
  you are regenerating bundled assets
- make sure `netlify.toml` is honored by the site

## Rollback

### Backend Rollback

Keep the previous working image tag or revision name.

Example rollback flow:

```bash
gcloud run revisions list --service playground --region us-central1
gcloud run services update-traffic playground \
  --region us-central1 \
  --to-revisions REVISION_NAME=100
```

If you deploy from tagged images instead of only `latest`, you can also redeploy
the prior image tag directly.

### Frontend Rollback

Use Netlify's deploy history to restore the previous working deploy. If the
rollback is caused by an API URL mismatch, restore the last known-good frontend
deploy or push a correction to [dashboard.js](dashboard.js) and
[stats/stats.js](stats/stats.js).

## Verification Checklist

After deployment, verify all of the following.

### Backend

- `GET /` serves the frontend files when accessed directly on the Cloud Run URL
- `POST /api/compile` compiles a simple `fmt.Println` example
- Cloud Run logs show normal startup and successful compiles

### Frontend

- the browser can load the editor and simulator
- compile from the UI succeeds
- no cross-origin policy errors appear in browser dev tools

### Optional Services

- share links work if Firestore is enabled
- stats page loads if Firestore is enabled
- GCS cache bucket receives build artifacts if using `gcs` cache mode

## Monitoring And Alerts

Set up at least the following before exposing the service broadly:

- Cloud Billing budget alerts on the playground project
- Cloud Run alerting for elevated error rate
- Cloud Run alerting for high request latency
- Cloud Run alerting for sustained instance count near `max-instances`
- log inspection or dashboards for compile failures

If you run with `min instances = 0`, monitor cold-start-heavy periods separately
from steady-state latency so you do not misread the service's normal behavior.

## Cloud Armor

Cloud Armor does not attach directly to a public Cloud Run URL.

For this playground, the Cloud Armor integration shape is:

1. Cloud Run service in `us-central1`
2. serverless NEG pointing at that Cloud Run service
3. global external Application Load Balancer using that NEG as the backend
4. Cloud Armor security policy attached to the backend service
5. Cloud Run ingress restricted so requests cannot bypass the load balancer

This matters because the default `run.app` URL can bypass Cloud Armor unless you
lock Cloud Run down after the load balancer is in place.

### Required IAM For Cloud Armor And Load Balancer Work

At minimum, this part of the setup normally requires:

- permission to create and update Cloud Armor security policies
- permission to attach a security policy to a backend service
- permission to create load balancer resources and serverless NEGs
- permission to update Cloud Run ingress settings

In Google Cloud terms, this is usually split across Compute security-policy,
network, and load-balancing administration capabilities.

### Recommended Initial Policy Shape

For the playground, start conservatively:

- throttle only `/api/compile` aggressively
- return `429` when requests exceed the threshold
- enforce the limit per client IP or X-Forwarded-For IP
- keep Cloud Run `max-instances` low even after Cloud Armor is added

A compiler endpoint is much more expensive than a normal JSON API, so the first
threshold should be tighter than a typical web application rate limit.

For the recommended backend shape in this document:

- `2` Cloud Run instances maximum
- `1` request concurrency per instance
- compile requests that may take several seconds each

a reasonable first pass for a public service is:

- `/api/compile`: `4 requests / 60 seconds / IP`
- optional broad catch-all throttle later, for example `120 requests / 60
  seconds / IP`, only if logs show abuse on non-compile paths

Use throttle first. Only move to rate-based bans if logs show repeated abusive
clients and the throttle rule is not enough.

If you expect many legitimate users behind the same NAT or office egress IP,
start with `6 requests / 60 seconds / IP` instead and watch logs before raising
it further. Do not start with a very high threshold unless you already have real
traffic data.

### Suggested Rollout Plan

Roll Cloud Armor out in stages instead of enabling the harshest policy on day
one.

#### Stage 1: Baseline Logging

Before enforcing stricter policy changes, confirm you have enough visibility to
see whether a rule is harming legitimate users.

Watch at least these signals:

- Cloud Run request count and latency on `/api/compile`
- Cloud Run `429` responses once throttling is enabled
- load balancer request logs for repeated high-rate client IPs
- Cloud Armor rule hit counts by rule priority

If you cannot observe these signals, do not tighten the policy yet.

#### Stage 2: Enable Compile Throttle Only

Start with only the dedicated `/api/compile` throttle rule.

Recommended first production setting:

- `/api/compile`: `4 requests / 60 seconds / IP`

Run with that for an initial observation window, for example a few days of real
traffic.

Things to look for:

- whether legitimate users frequently hit `429`
- whether many blocked requests come from a small number of IPs
- whether latency improves because abusive bursts are suppressed
- whether a specific shared NAT, CI runner, or office IP is hitting limits

If false positives are common, raise the limit slightly to `6 requests / 60
seconds / IP` before adding more rules.

#### Stage 3: Add Broad Protection If Needed

Only add the catch-all throttle rule if logs show that abuse is not limited to
`/api/compile`.

Example use cases:

- repeated scraping or hammering of multiple paths
- bursts against `/api/share` or `/api/stats`
- load balancer logs dominated by a small number of noisy clients

If the non-compile paths are quiet, leave the broader rule disabled.

#### Stage 4: Move From Throttle To Rate-Based Ban Only For Persistent Abuse

Use a rate-based ban when throttling is clearly not enough and the same clients
keep exceeding the threshold over time.

Good indicators that a ban is justified:

- the same IPs trigger the compile throttle repeatedly over long windows
- those clients continue immediately after the throttle interval resets
- the pattern is clearly automated rather than normal human usage
- the blocked traffic is large enough to affect availability or cost

Poor reasons to jump to a ban:

- a single short-lived traffic spike after being linked from Reddit or Slack
- a school, coworking space, or office NAT causing several real users to share
  one IP
- initial uncertainty about what normal usage looks like

If you do move to a ban, keep it short at first. A reasonable starting point is
to continue using the throttle as the first line and add a rate-based ban only
after a client exceeds a much larger threshold over a longer interval.

Example progression:

- throttle at `4 requests / 60 seconds / IP`
- ban only if a client keeps bursting far above that threshold over several
  minutes
- start with a short ban window such as `300` seconds

#### Stage 5: Revisit Thresholds After Real Traffic

After the service has real production usage, revisit the rules using actual
traffic patterns rather than guesses.

Adjust based on:

- percentage of users receiving `429`
- ratio of blocked requests to successful compile requests
- compile queueing and latency under normal peaks
- whether users are concentrated behind shared IPs

The goal is not to eliminate all bursts. The goal is to prevent a small number
of clients from consuming a disproportionate amount of compiler capacity.

### Create The Load Balancer And Cloud Armor Policy

The commands below assume:

- project: `YOUR_PROJECT_ID`
- region: `us-central1`
- Cloud Run service: `playground`
- domain: `play.example.com`
- global static IP resource name: `playground-ip`
- serverless NEG name: `playground-neg`
- backend service name: `playground-backend`
- URL map name: `playground-urlmap`
- HTTPS proxy name: `playground-https-proxy`
- forwarding rule name: `playground-https-rule`
- SSL certificate name: `playground-cert`
- security policy name: `playground-armor`

Set the project first:

```bash
gcloud config set project YOUR_PROJECT_ID
```

Reserve a global static IP for the load balancer:

```bash
gcloud compute addresses create playground-ip \
  --ip-version=IPV4 \
  --global
```

Create a Google-managed certificate for your public hostname:

```bash
gcloud compute ssl-certificates create playground-cert \
  --domains=play.example.com \
  --global
```

Create the serverless NEG pointing at the Cloud Run service:

```bash
gcloud compute network-endpoint-groups create playground-neg \
  --region=us-central1 \
  --network-endpoint-type=serverless \
  --cloud-run-service=playground
```

Create the backend service and attach the NEG:

```bash
gcloud compute backend-services create playground-backend \
  --global \
  --load-balancing-scheme=EXTERNAL_MANAGED

gcloud compute backend-services add-backend playground-backend \
  --global \
  --network-endpoint-group=playground-neg \
  --network-endpoint-group-region=us-central1
```

Create the URL map, HTTPS proxy, and forwarding rule:

```bash
gcloud compute url-maps create playground-urlmap \
  --default-service=playground-backend

gcloud compute target-https-proxies create playground-https-proxy \
  --ssl-certificates=playground-cert \
  --url-map=playground-urlmap

gcloud compute forwarding-rules create playground-https-rule \
  --global \
  --load-balancing-scheme=EXTERNAL_MANAGED \
  --network-tier=PREMIUM \
  --target-https-proxy=playground-https-proxy \
  --ports=443 \
  --address=playground-ip
```

Point DNS for `play.example.com` at the reserved global IP address after the
forwarding rule is created.

### Create And Attach A Starter Cloud Armor Policy

Create the security policy:

```bash
gcloud compute security-policies create playground-armor \
  --description="Rate limiting for TinyGo playground compile API"
```

Create a throttle rule for the compile endpoint:

```bash
gcloud compute security-policies rules create 1000 \
  --security-policy=playground-armor \
  --expression="request.path.matches('^/api/compile$')" \
  --action=throttle \
  --rate-limit-threshold-count=4 \
  --rate-limit-threshold-interval-sec=60 \
  --conform-action=allow \
  --exceed-action=deny-429 \
  --enforce-on-key=IP
```

Optional later: add a coarse catch-all throttle for obviously abusive clients
that hammer the rest of the service as well.

```bash
gcloud compute security-policies rules create 1100 \
  --security-policy=playground-armor \
  --expression="true" \
  --action=throttle \
  --rate-limit-threshold-count=120 \
  --rate-limit-threshold-interval-sec=60 \
  --conform-action=allow \
  --exceed-action=deny-429 \
  --enforce-on-key=IP
```

Attach the policy to the backend service:

```bash
gcloud compute backend-services update playground-backend \
  --global \
  --security-policy=playground-armor
```

### Lock Cloud Run Behind The Load Balancer

After the load balancer is working, restrict Cloud Run ingress so clients cannot
hit the default Cloud Run URL directly:

```bash
gcloud run services update playground \
  --region=us-central1 \
  --ingress=internal-and-cloud-load-balancing
```

If your workflow allows it, also disable the default Cloud Run URL in the Cloud
Run service settings so the public entry point is only the load balancer.

### Add HTTP To HTTPS Redirect

If you want all plain HTTP requests to redirect to HTTPS on the same global IP,
create a partial HTTP load balancer that points at a redirect URL map.

Create a redirect URL map definition:

```yaml
kind: compute#urlMap
name: playground-http-redirect
defaultUrlRedirect:
  httpsRedirect: true
  redirectResponseCode: MOVED_PERMANENTLY_DEFAULT
tests:
- description: Redirect root to HTTPS
  host: play.example.com
  path: /
  expectedOutputUrl: https://play.example.com/
  expectedRedirectResponseCode: 301
```

Save that YAML as `/tmp/playground-http-redirect.yaml`, then validate and import
it:

```bash
gcloud compute url-maps validate \
  --source=/tmp/playground-http-redirect.yaml

gcloud compute url-maps import playground-http-redirect \
  --source=/tmp/playground-http-redirect.yaml \
  --global
```

Create an HTTP proxy for the redirect map and a forwarding rule on port `80`
using the same reserved global IP as the HTTPS frontend:

```bash
gcloud compute target-http-proxies create playground-http-proxy \
  --url-map=playground-http-redirect \
  --global

gcloud compute forwarding-rules create playground-http-rule \
  --global \
  --load-balancing-scheme=EXTERNAL_MANAGED \
  --network-tier=PREMIUM \
  --target-http-proxy=playground-http-proxy \
  --ports=80 \
  --address=playground-ip
```

This redirect path must use the same global IP as the HTTPS forwarding rule or
the browser-visible redirect flow will not behave as expected.

### Verify Cloud Armor Is Really In Path

Verify these items before considering the service protected:

- the public DNS name resolves to the load balancer IP, not directly to Cloud Run
- the Cloud Armor policy is attached to `playground-backend`
- Cloud Run ingress is set to internal-and-load-balancer-only traffic
- direct access to the default `run.app` URL from the public internet no longer
  works as a bypass path
- repeated calls to `/api/compile` eventually return `429`
- `http://play.example.com` returns a `301` redirect to the HTTPS URL if you
  created the redirect rule

### Expected Extra Cost

Cloud Armor Standard adds request-based charges plus small per-policy and per-rule
charges. The external Application Load Balancer also adds its own cost.

That overhead is usually acceptable for a public playground, but it is
materially more complex and more expensive than exposing Cloud Run directly. For
private, demo, or internal deployments, you may choose to keep the service
private instead.

## Cost And Safety Notes

For the recommended `2 vCPU / 2 GiB` Cloud Run service in `us-central1`, the
main cost driver is sustained compiler activity, not the static frontend.

Recommended first safeguards:

- `min instances = 0`
- low `max instances`
- project-level billing budget alerts
- separate playground project
- rate limiting in front of the public compile endpoint

Cloud Run `max instances` is the primary practical spend guardrail, but it is
not a mathematically perfect hard cap during very short spikes.

If you need a stronger emergency brake, add billing-budget-triggered automation
or quota-based controls at the project level.

## Accounts Summary

You need:

- a Google account with deployment rights in the target GCP project
- a Netlify account with access to the target site
- a Cloud Run runtime identity with GCS access if using cached builds
- a Cloud Run runtime identity with Firestore access if enabling sharing and
  stats

You do not need Firestore to get the compiler backend running.

## Future Work

The next hardening task is to define a Cloud Armor policy for the public compile
endpoint so legitimate traffic spikes can be throttled without leaving the
backend completely exposed.
