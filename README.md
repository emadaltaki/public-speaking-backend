# Speak Up Fearless API

Go backend for the Speak Up Fearless site (Flutter web on Vercel at
https://public-speaking-lemon.vercel.app). It stores user profiles, practice
game history and booking requests.

- **Sign-in** stays with Firebase Auth (project `speakup-ca500`, Google and
  email/password). The Flutter app sends the user's Firebase ID token as
  `Authorization: Bearer <token>` and the API verifies it against Google's
  public keys. No service account is needed.
- **Database:** Postgres, hosted on [Neon](https://neon.tech) (free tier, no card).
  Tables are created automatically on startup (`internal/store/migrations`).
- **Hosting:** Google Cloud Run in the same Google Cloud project as Firebase.

## Why these choices

- **Cloud Run** lives in the Google account that already owns the Firebase
  project, scales to zero, and its free tier (2 million requests a month)
  covers a site this size. Render's free tier sleeps and takes ~50 s to wake;
  Fly.io now needs a card for anything.
- **Neon Postgres** is free without a card, doesn't pause data, and plain SQL
  fits users, history and bookings. Because it's ordinary Postgres, the API
  can move to any host or database provider later without code changes.

## API

All responses are JSON. Errors look like `{"error": "message"}`.

| Method | Path | Auth | What it does |
| --- | --- | --- | --- |
| GET | `/healthz` | none | `{"status":"ok"}`, or 503 if the database is down |
| GET | `/v1/me` | user | Current user's profile (created on first call) |
| PATCH | `/v1/me` | user | Update `displayName` and/or `language` (`"en"` or `"ar"`) |
| POST | `/v1/games/sessions` | user | Record a finished round: `gameId`, `durationSeconds`, optional `score`, optional `details` object |
| GET | `/v1/games/sessions?gameId=&limit=` | user | Recent rounds, newest first (limit 1–200, default 50) |
| GET | `/v1/games/progress` | user | Per game: plays, total seconds, best score, last played |
| POST | `/v1/bookings` | optional | Booking request: `name`, `contact`, `preferredTime`, `reason` (why they want the call), optional `description`. Linked to the user when signed in. Limited to 5 per IP per 10 minutes |
| GET | `/v1/bookings` | user | The signed-in user's booking requests |
| GET | `/v1/admin/bookings?limit=` | admin | All booking requests |
| PATCH | `/v1/admin/bookings/{id}` | admin | Set `status` to `new`, `contacted`, `scheduled` or `closed` |

Game IDs match the Flutter catalog: `triple-step`, `conductor`, `podcast`,
`autocomplete`. Admins are the emails in `ADMIN_EMAILS`, and their Firebase
email must be verified (Google sign-in always is).

Calling it from Flutter:

```dart
final token = await FirebaseAuth.instance.currentUser!.getIdToken();
await http.post(
  Uri.parse('$apiBase/v1/games/sessions'),
  headers: {'Authorization': 'Bearer $token', 'Content-Type': 'application/json'},
  body: jsonEncode({'gameId': 'triple-step', 'durationSeconds': 90, 'score': 4}),
);
```

## Configuration

| Variable | Default | Notes |
| --- | --- | --- |
| `PORT` | `8080` | Cloud Run sets this |
| `FIREBASE_PROJECT_ID` | `speakup-ca500` | Tokens from other projects are rejected |
| `DATABASE_URL` | empty | Neon connection string. Empty means an in-memory store (data lost on restart) |
| `ALLOWED_ORIGINS` | `https://public-speaking-lemon.vercel.app` | Comma-separated exact origins for CORS |
| `ALLOW_LOCALHOST` | `true` | Also allow `http://localhost:<port>` for local Flutter runs |
| `ADMIN_EMAILS` | empty | Comma-separated |

See `.env.example`.

## Run locally

Needs Go 1.25+. Without `DATABASE_URL` it runs on an in-memory store, which is
enough to try the API.

```powershell
go run ./cmd/api
```

```bash
curl http://localhost:8080/healthz
```

With a real database (PowerShell):

```powershell
$env:DATABASE_URL = "postgresql://user:pass@ep-xxx.neon.tech/neondb?sslmode=require"
go run ./cmd/api
```

Tests:

```bash
go test ./...
```

The store tests also run against Postgres when `TEST_DATABASE_URL` points at a
throwaway database (a Neon branch works well).

## Deploy to Vercel

Vercel runs this repo as a Go server with no extra config. In the Vercel
project, open Settings > Environment Variables and set:

- `DATABASE_URL`: the Neon connection string (required; without it every
  serverless instance keeps its own throwaway in-memory data)
- `ADMIN_EMAILS`: emails that can manage bookings
- `ALLOW_LOCALHOST`: `false` for Production

Redeploy after changing variables, then open `/healthz`. It should say
`"database": "postgres"`. A request log line for `/favicon.ico` is just the
browser asking for an icon and can be ignored.

## Deploy to Cloud Run

One-time setup, done by the project owner:

1. **Turn on billing for `speakup-ca500`.** In the Firebase console, upgrade the
   project to the Blaze (pay-as-you-go) plan, or link a billing account in the
   Google Cloud console. Cloud Run requires it even when usage stays inside the
   free tier. Add a budget alert (for example $5) under Billing > Budgets.
2. **Create the database.** Sign up at neon.tech, create a project (pick a
   region near `us-central1`, such as AWS us-east-1), and copy the connection
   string.
3. **Install the gcloud CLI** and sign in:

   ```bash
   gcloud auth login
   gcloud config set project speakup-ca500
   gcloud services enable run.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com secretmanager.googleapis.com
   ```

4. **Store the database URL as a secret** and let Cloud Run read it:

   ```bash
   printf '%s' 'postgresql://...neon.tech/neondb?sslmode=require' | gcloud secrets create database-url --data-file=-
   ```

   ```bash
   gcloud secrets add-iam-policy-binding database-url --member="serviceAccount:$(gcloud projects describe speakup-ca500 --format='value(projectNumber)')-compute@developer.gserviceaccount.com" --role=roles/secretmanager.secretAccessor
   ```

Deploy (and redeploy after changes) from this folder:

```bash
gcloud run deploy speakup-api --source . --region us-central1 --allow-unauthenticated --set-secrets DATABASE_URL=database-url:latest --set-env-vars "FIREBASE_PROJECT_ID=speakup-ca500,ALLOWED_ORIGINS=https://public-speaking-lemon.vercel.app,ALLOW_LOCALHOST=false,ADMIN_EMAILS=you@example.com" --min-instances 0 --max-instances 3
```

`--allow-unauthenticated` lets browsers reach the service; the API does its own
Firebase sign-in checks per route. The command prints the service URL
(`https://speakup-api-....run.app`); check it with `/healthz`, then give that
URL to the Flutter app as its API base.

If you add a custom domain or Vercel preview URLs, add them to
`ALLOWED_ORIGINS`.

## Layout

```
cmd/api/              entry point, config from env
internal/auth/        Firebase ID token verification
internal/api/         routes, CORS, auth middleware, handlers
internal/store/       Store interface, Postgres and in-memory implementations, SQL migrations
```
